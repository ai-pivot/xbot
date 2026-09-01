package agent

import (
	"fmt"
	"os"
	"sync/atomic"

	"xbot/channel"
	"xbot/protocol"
	"xbot/storage/sqlite"
)

// maxIncrementalIterations caps how many iteration-history entries
// GetActiveProgress transfers for an incremental pull (from_iter >= 0).
// Beyond this, the gap is too large for delta transfer — the client is
// signalled to reload from DB instead (ResyncRequired).
const maxIncrementalIterations = 30

// SetCWD sets the current working directory for a session.
// It refreshes plugin workDir with the correct tenantID.
func (a *Agent) SetCWD(ch, chatID, dir string) error {
	if a.sandboxMode != "none" {
		return fmt.Errorf("CWD sync not supported in %s sandbox mode", a.sandboxMode)
	}
	if a.MultiSession() == nil {
		return ErrNoSessionManager
	}
	sess, err := a.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		return err
	}
	// Set CWD — but only for brand new sessions with no persisted CWD.
	// On restart, loadPersistedCWD restores the user's last CWD (which may differ
	// from the terminal dir if the user used the Cd tool). We must not overwrite it.
	// Also handles the edge case where the persisted directory no longer exists
	// (e.g. deleted between runs) by falling back to the terminal CWD.
	existingCWD := sess.GetCurrentDir()
	if existingCWD == "" {
		sess.SetCurrentDir(dir)
	} else if _, err := os.Stat(existingCWD); os.IsNotExist(err) {
		// Persisted CWD is stale (directory removed), fall back to terminal CWD
		sess.SetCurrentDir(dir)
	}
	// Always refresh plugin contexts so script plugins see the correct workDir
	if a.pluginMgr != nil {
		cwd := sess.GetCurrentDir()
		a.pluginMgr.RefreshWorkDir(cwd, ch, chatID, sess.TenantID())
		a.pluginMgr.RefreshTenantID(sess.TenantID())
	}
	return nil
}

// IsProcessingByChannel returns true if there is an active Run for the given channel:chatID.
func (a *Agent) IsProcessingByChannel(ch, chatID string) bool {
	key := ch + ":" + chatID
	if _, found := a.chatCancelCh.Load(key); found {
		return true
	}
	// Agent (sub-agent) sessions: one-shot/interactive sub-agents run directly
	// via Run() and do NOT register chatCancelCh — check the interactive session
	// running state instead so the web sidebar shows running sub-agents reliably.
	if ch == "agent" {
		if value, ok := a.interactiveSubAgents.Load(chatID); ok {
			if ia, ok := value.(*interactiveAgent); ok && ia != nil {
				ia.mu.Lock()
				running := ia.running
				ia.mu.Unlock()
				return running
			}
		}
	}
	return false
}

// HasPendingAskUserFast reports whether the session has a pending AskUser
// prompt, checking ONLY the in-memory waitingUserSessions registry (no DB
// Replay fallback — loadPendingAskUserEntry's Replay is far too expensive to
// call per session-tree row). Used by the session tree to mark waiting_input
// rows so the sidebar and the panel agree during a WaitingUser pause:
// chatCancelCh is already deregistered there, so IsProcessingByChannel reports
// false and the sidebar showed idle while the panel showed busy.
func (a *Agent) HasPendingAskUserFast(ch, chatID string) bool {
	if ch == "" || chatID == "" {
		return false
	}
	_, ok := a.waitingUserSessions.Load(qualifyChatID(ch, chatID))
	return ok
}

// GetActiveProgress returns the latest progress snapshot for the given channel:chatID.
// The fromIter parameter is the TUI's watermark — only iterations with
// Iteration > fromIter are included in the returned IterationHistory. This keeps
// pull payloads proportional to the number of missing iterations, not the total
// turn length. Pass fromIter=0 (or -1) to get all iterations (for /su switch or
// initial restore).
//
// For agent sessions, corrects Phase from the authoritative running state in
// interactiveSubAgents when the agent is between iterations (Phase="done" but
// still running). This unifies the busy/idle logic across all session types.
func (a *Agent) GetActiveProgress(ch, chatID string, fetch protocol.ProgressFetch) *protocol.ProgressEvent {
	key := ch + ":" + chatID
	v, ok := a.lastProgressSnapshot.Load(key)
	if !ok {
		// Turn has ended (snapshot deleted). Return a minimal snapshot with
		// only todos so the client can restore the TODO list on session switch.
		// Without this, switching to an idle session with todos shows no todos
		// until the next TodoWrite tool call.
		//
		// Distinguish two cases:
		//   - HasTodos=false → the session never ran and never wrote a todo
		//     list → return nil (no active progress, nothing to restore).
		//   - HasTodos=true (possibly empty) → the session ran and its todo
		//     list was cleared (todo_write([]) / turn-end cleanupTodos) → return
		//     done + [] so the frontend LEARNS the list is now empty. Returning
		//     nil for an empty list meant the client could never tell "cleared"
		//     from "no data", so stale items survived refreshes.
		if a.todoManager != nil && a.todoManager.HasTodos(key) {
			snap := &protocol.ProgressEvent{
				Phase: "done",
				Todos: a.GetTodos(ch, chatID),
			}
			// Also include goal so the frontend can display it on idle sessions.
			if a.goalManager != nil {
				snap.Goal = a.goalManager.GoalInfo(key)
			}
			return snap
		}
		// Even without todos, if there's a goal, return it so the
		// frontend can display the active goal on idle sessions.
		if a.goalManager != nil {
			if goal := a.goalManager.GoalInfo(key); goal != nil {
				return &protocol.ProgressEvent{
					Phase: "done",
					Todos: []protocol.TodoItem{},
					Goal:  goal,
				}
			}
		}
		return nil
	}
	snapshot := v.(*protocol.ProgressEvent)
	result := *snapshot

	// Merge live stream state (updated by stream callbacks between structured events).
	// This is the pull-model replacement for stream event push — the client reads
	// live streaming content via tick pull instead of receiving push events.
	a.mergeStreamState(key, &result)

	// Always inject the latest goal state (goal may have been set/cleared/completed
	// via RPC since the snapshot was last refreshed by refreshStructuredTodos).
	if a.goalManager != nil {
		result.Goal = a.goalManager.GoalInfo(key)
	}

	// Agent sessions: correct Phase from authoritative running state.
	// interactiveSubAgents stores entries keyed by interactiveKey (no "agent:" prefix),
	// so we look up with chatID directly. When running=true but Phase="done"
	// (between iterations), correct Phase from iteration history.
	if ch == "agent" {
		if entry, loaded := a.interactiveSubAgents.Load(chatID); loaded {
			ia := entry.(*interactiveAgent)
			ia.mu.Lock()
			isRunning := ia.running
			// 最新 Run 的 turnID：send（action=send）在 Pre-Run reset 中把
			// assignSubAgentTurnID 分配的新 turnID 写回 ia.cfg.TurnID（wireSubAgentProgress
			// 的 stream 闭包与本校正分支都经 ia.cfg 延迟读取同一值）。校正 phase 时必须
			// 同步 turnID —— 否则校正后的 snapshot 仍是【上一个 Run 的 turnID（T1 旧）】，
			// 而 DB 最新 turn 是 T2 → 前端 history_replaced 的 active(T1) 与 DB turns(T2)
			// 不一致 → active.turnID 不在 turns 中 → 创建【重复 live turn T1】→
			// "迭代完成后打开渲染两遍历史"（用户报告）。
			runTurnID := uint64(0)
			if ia.cfg != nil {
				runTurnID = ia.cfg.TurnID
			}
			ia.mu.Unlock()
			if isRunning && result.Phase == "done" {
				corrected := false
				if histPtr, ok := a.iterationHistories.Load(key); ok {
					hist := *histPtr.(*[]protocol.ProgressEvent)
					for i := len(hist) - 1; i >= 0; i-- {
						if hist[i].Phase != "done" {
							result.Phase = hist[i].Phase
							if runTurnID > 0 {
								result.TurnID = runTurnID
							}
							corrected = true
							break
						}
					}
				}
				if !corrected {
					result.Phase = "running"
					if runTurnID > 0 {
						result.TurnID = runTurnID
					}
				}
			}
		}
	}

	if histPtr, ok := a.iterationHistories.Load(key); ok {
		hist := *histPtr.(*[]protocol.ProgressEvent)
		if len(hist) > 0 {
			flat := progressHistoryWithoutNested(hist)
			a.iterationHistories.CompareAndSwap(key, histPtr, &flat)
			filtered := make([]protocol.ProgressEvent, 0, len(flat))
			for _, h := range flat {
				if fetch.Filter(h.Iteration) {
					filtered = append(filtered, h)
				}
			}
			// Gap-too-large guard: when the caller's from_iteration watermark is
			// far behind the server's current iteration (long SSE disconnect /
			// reconnect gap), transferring dozens of iterations is wasteful and
			// error-prone. Signal the client to reload from DB (authoritative)
			// instead — the client already handles resync_required via replay_gap.
			// Only applies to incremental pulls (from_iter >= 0); FetchAll
			// (from_iter=-1, /su switch / initial restore) always returns all.
			if fetch.ToFromIter() >= 0 && len(filtered) > maxIncrementalIterations {
				result.ResyncRequired = true
				result.IterationHistory = nil
				return &result
			}
			result.IterationHistory = filtered
			return &result
		}
	}
	return &result
}

// GetTodos returns the TODO items for the given channel:chatID session.
func (a *Agent) GetTodos(ch, chatID string) []protocol.TodoItem {
	key := ch + ":" + chatID
	if a.todoManager == nil {
		return []protocol.TodoItem{}
	}
	items := a.todoManager.GetTodos(key)
	if len(items) == 0 {
		return []protocol.TodoItem{}
	}
	result := make([]protocol.TodoItem, len(items))
	for i, t := range items {
		result[i] = protocol.TodoItem{ID: t.ID, Text: t.Text, Status: t.Status}
	}
	return result
}

// GetGoal returns the goal state for the given channel:chatID session.
func (a *Agent) GetGoal(ch, chatID string) *protocol.GoalInfo {
	if a.goalManager == nil {
		return nil
	}
	return a.goalManager.GoalInfo(ch + ":" + chatID)
}

// SetGoal sets a goal for the given channel:chatID session.
func (a *Agent) SetGoal(ch, chatID, objective string) {
	if a.goalManager == nil {
		return
	}
	a.goalManager.Set(ch+":"+chatID, objective)
	// Push a progress event so the frontend displays the GoalBanner immediately.
	a.emitGoalProgress(ch, chatID)
}

// ClearGoal clears the goal for the given channel:chatID session.
func (a *Agent) ClearGoal(ch, chatID string) {
	if a.goalManager == nil {
		return
	}
	a.goalManager.Clear(ch + ":" + chatID)
	// Push a progress event with nil goal so the frontend removes the GoalBanner.
	progressKey := ch + ":" + chatID
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)
	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     "",
		Seq:       seq,
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
		Todos:     a.GetTodos(ch, chatID),
		Goal:      nil, // explicitly nil → frontend clears the banner
	}
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
}

// GetExportIterations returns per-iteration records for session export,
// combining the persisted iteration_history table (completed iterations with
// TTFT/TPOT/tokens/timing) with the in-flight iteration's partial stream
// content (graceful shutdown / benchmark timeout).
func (a *Agent) GetExportIterations(ch, chatID string) []protocol.ExportedIteration {
	// 1. Completed iterations from iteration_history (DB authoritative).
	var records []sqlite.IterationRecord
	if a.multiSession != nil && a.multiSession.DB() != nil {
		if sess, err := a.multiSession.GetOrCreateSession(ch, chatID); err == nil {
			if tenantID := sess.TenantID(); tenantID > 0 {
				records, _ = sqlite.NewSessionService(a.multiSession.DB()).GetAllIterationHistory(tenantID)
			}
		}
	}

	iterations := make([]protocol.ExportedIteration, 0, len(records)+1)
	for _, r := range records {
		iterations = append(iterations, protocol.ExportedIteration{
			TurnID:       r.TurnID,
			Iteration:    r.Iteration,
			Content:      r.Content,
			Reasoning:    r.Reasoning,
			Tools:        r.Tools,
			Tokens:       r.Tokens,
			TTFTMs:       r.TTFTMs,
			TPOTMs:       r.TPOTMs,
			TokensPerSec: r.TokensPerSec,
			TotalMs:      r.TotalMs,
		})
	}

	// 2. In-flight iteration (partial stream content) from lastProgressSnapshot.
	key := ch + ":" + chatID
	v, ok := a.lastProgressSnapshot.Load(key)
	if !ok {
		return iterations
	}
	snapshot := v.(*protocol.ProgressEvent)
	result := *snapshot
	a.mergeStreamState(key, &result)

	// The in-flight iteration number is the snapshot's current Iteration
	// (set by beginIteration); derive from history when it's 0.
	inFlightIter := result.Iteration
	if inFlightIter == 0 && len(result.IterationHistory) > 0 {
		inFlightIter = result.IterationHistory[len(result.IterationHistory)-1].Iteration + 1
	}

	// Only add when there's real partial content worth preserving.
	if inFlightIter > 0 && (result.StreamContent != "" || result.ReasoningStreamContent != "" || len(result.ActiveTools) > 0) {
		var ttft, tpot, tps, totalMs int64
		if result.StreamStats != nil {
			ttft = result.StreamStats.TTFTMs
			tpot = result.StreamStats.TPOTMs
			tps = result.StreamStats.TokensPerSec
			totalMs = result.StreamStats.TotalMs
		}
		iterations = append(iterations, protocol.ExportedIteration{
			TurnID:       result.TurnID,
			Iteration:    inFlightIter,
			Content:      result.StreamContent,
			Reasoning:    result.ReasoningStreamContent,
			TTFTMs:       ttft,
			TPOTMs:       tpot,
			TokensPerSec: tps,
			TotalMs:      totalMs,
			InFlight:     true,
		})
	}

	return iterations
}
