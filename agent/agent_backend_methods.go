package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"xbot/channel"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// maxIncrementalIterations caps how many iteration-history entries
// GetActiveProgress transfers for an incremental pull (from_iter >= 0).
// Beyond this, the gap is too large for delta transfer — the client is
// signalled to reload from DB instead (ResyncRequired).
const maxIncrementalIterations = 30

// ⛔ active_progress 快照的 iteration_history **必须完整**（用户 2026-09-21：
// 「不能有任何 gap，任何 gap 都是破坏线性一致性」/「为什么我的 iter 还是缺」）——
// 这里**禁止**再引入尾部截断。体积/渲染性能归**渲染层**（TurnBody 迭代级窗口化：
// 只挂载视口附近的块 + contain，代价与迭代数解耦），绝不以丢数据换体积。

// SetCWD sets the current working directory for a session.
// It refreshes plugin workDir with the correct tenantID.
// cwdApplyDecision 决定是否采纳新的 cwd（纯函数，便于单测）。
//
// 2026-09-16 用户报告（严重）：「我创建会话时传的是**绝对路径**，为什么不用我的路径？」
// 根因：本函数旧实现（内联在 SetCWD 里）只在 existingDir=="" 或旧目录不存在时才写入
// ⇒ 当 Agent 初始化已把 cwd 设成 workspace 根（事故后 agent.work_dir 丢失 ⇒ 该根是
// **相对路径** `.xbot/users/<uid>/workspace`，且相对服务进程 cwd 恰好存在）时，
// 用户的绝对路径被**静默丢弃** ⇒ 新会话 pwd 既不是用户选的、还伴随
// `no such file or directory`（活日志实证）。
//
// 规则：
//   - force（显式用户动作，如新建会话弹窗的 set_cwd）→ **总是采纳**；
//   - 否则（自动路径，如 CLI 终端目录同步/重启恢复）：仅在无既有 cwd、或既有 cwd
//     已不存在时才采纳 —— 保留"不要用终端目录覆盖已持久化 cwd"的既有语义。
func cwdApplyDecision(existingDir string, existingExists bool, force bool) bool {
	if force {
		return true
	}
	if existingDir == "" {
		return true
	}
	return !existingExists
}

func (a *Agent) SetCWD(ch, chatID, dir string) error {
	return a.setCWD(ch, chatID, dir, false)
}

// SetCWDForced 应用一次**显式**的工作目录选择（用户/API 直接指定）。
//
// 用户要求（2026-09-16，原话）：「我不管你用什么方法，那个接口我传的是什么路径就得是
// 什么路径，而不是给我转换。」⇒ 本函数**原样应用**传入的 dir：
//
//	· 不做任何改写（不 join workspace 根、不相对化、不 trim 成别的路径）；
//	· 不因"不是绝对路径"或"目录不存在"而拒绝（仅记 warning 便于排查）——
//	  传什么就是什么。
//
// 与 SetCWD 的区别：SetCWD 是**自动**路径（终端目录同步/重启恢复），它不得覆盖已持久化
// 且仍存在的 cwd；SetCWDForced 是**显式**动作，总是生效（否则用户的绝对路径会被
// Agent 初始化写下的 workspace 根静默顶掉 —— 线上 bug）。
func (a *Agent) SetCWDForced(ch, chatID, dir string) error {
	if dir == "" {
		return fmt.Errorf("cwd is required")
	}
	if !filepath.IsAbs(dir) {
		log.WithFields(log.Fields{"cwd": dir, "session": ch + ":" + chatID}).
			Warn("SetCWDForced received a non-absolute path — applying it verbatim as requested")
	}
	// 显式工作目录（新建会话弹窗 / 会话信息里改路径）**不存在则自动创建**
	// （2026-09-20 用户要求：「创建新会话时如果选择了一个不存在的目录则自动创建」）。
	// 旧实现只打一条 Warn 然后原样落库 ⇒ 会话卡在一个不存在的 cwd 上（终端/工具/Cd
	// 全在错地方），而用户以为"路径没生效"。这里只创建目录本身（含多级父目录）；
	// 真正的错误（路径被普通文件占住、权限不足）必须**显式返回**，让 UI 报出
	// "工作目录设置失败"，绝不静默落一个坏 cwd。
	if info, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", dir, err)
		}
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			return fmt.Errorf("create working directory %s: %w", dir, mkErr)
		}
		log.WithFields(log.Fields{"cwd": dir, "session": ch + ":" + chatID}).
			Info("SetCWDForced created a missing working directory")
	} else if !info.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", dir)
	}
	return a.setCWD(ch, chatID, dir, true)
}

func (a *Agent) setCWD(ch, chatID, dir string, force bool) error {
	// CWD 是**会话级**状态（DB tenants.cwd），与沙箱无关：本机（none）与 runner
	// （remote）都支持同步。旧代码对任何非 none 模式直接拒绝 —— 而 Agent 在
	// cfg.SandboxMode 为空时曾把模式写成 "docker"，于是**未配置 sandbox 的部署
	// 新建会话必失败**（2026-09-16 P0：「CWD sync not supported in docker sandbox
	// mode」）。本地 docker sandbox 已整体删除，这条拒绝分支不再需要。
	if a.MultiSession() == nil {
		return ErrNoSessionManager
	}
	sess, err := a.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		return err
	}
	// Set CWD — 详见 cwdApplyDecision 的语义说明（显式 force 总是生效；自动路径
	// 不得覆盖已持久化且仍存在的 cwd）。
	existingCWD := sess.GetCurrentDir()
	existingExists := false
	if existingCWD != "" {
		if _, statErr := os.Stat(existingCWD); statErr == nil {
			existingExists = true
		}
	}
	if cwdApplyDecision(existingCWD, existingExists, force) {
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
// prompt, using the SAME authoritative path as GetPendingAskUser: the
// in-memory entry is validated against the persisted ask_question/ask_answer
// records, and a memory miss probes the DB first (one O(1) indexed lookup via
// idx_sm_tenant_record; the expensive Replay only runs when the DB says a
// question is genuinely pending). Both entry points therefore always agree.
// Used by the session tree to mark waiting_input rows so the sidebar and the
// panel agree during a WaitingUser pause: chatCancelCh is already deregistered
// there, so IsProcessingByChannel reports false and the sidebar would
// otherwise show idle while the panel is waiting for an answer.
func (a *Agent) HasPendingAskUserFast(ch, chatID string) bool {
	if ch == "" || chatID == "" {
		return false
	}
	_, entry := a.loadPendingAskUserEntry(ch, chatID)
	return entry != nil
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
		result[i] = protocol.TodoItem{Text: t.Text, Status: t.Status}
	}
	return result
}

// SetTodos replaces the persisted TODO list for a session (user edit from the
// web UI: rename an item, toggle status, or drop one). Emits a progress event
// carrying the new list so every client — and the active-progress snapshot
// used on session switch — sees the edit immediately instead of waiting for
// the next agent iteration.
func (a *Agent) SetTodos(ch, chatID string, items []protocol.TodoItem) {
	if a.todoManager == nil {
		return
	}
	key := ch + ":" + chatID
	todos := make([]tools.TodoItem, len(items))
	for i, it := range items {
		todos[i] = tools.TodoItem{Text: it.Text, Status: it.Status}
	}
	a.todoManager.SetTodos(key, todos)
	a.emitTodosProgress(ch, chatID)
}

// emitTodosProgress pushes a lightweight progress event carrying the current
// TODO list (plus the goal when one is active). Mirrors emitGoalProgress, but
// does not require a goal to exist — an edit to the todo list must reach the UI
// even in a session with no goal.
func (a *Agent) emitTodosProgress(chName, chatID string) {
	progressKey := qualifyChatID(chName, chatID)
	seqPtr, _ := a.builtinProgressSeq.LoadOrStore(progressKey, &atomic.Uint64{})
	seq := seqPtr.(*atomic.Uint64).Add(1)
	payload := &protocol.ProgressEvent{
		ChatID:    progressKey,
		Phase:     "",
		Seq:       seq,
		TurnID:    a.getActiveTurnID(progressKey),
		Iteration: 0,
		Todos:     a.GetTodos(chName, chatID),
		Goal:      a.GetGoal(chName, chatID),
	}
	if a.channelRange != nil {
		a.channelRange(func(_ string, ch channel.Channel) bool {
			if sender, ok := ch.(channel.ProgressSender); ok {
				sender.SendProgress(chatID, cloneProgressEvent(payload))
			}
			return true
		})
	}
	// Keep the snapshot fresh so GetActiveProgress returns the edited list.
	a.lastProgressSnapshot.Store(progressKey, progressSnapshotWithoutHistory(payload))
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
		Goal:      &protocol.GoalInfo{Objective: "", Status: protocol.GoalStatusCleared}, // explicit cleared marker (objective:"" survives GoalInfo's non-omitempty json tag) — Goal: nil is invisible to the frontend (ProgressEvent.Goal omitempty drops the field entirely, indistinguishable from "not carried"), which broke the clear chain (xbotgh CR: "用户删除目标后 GoalBanner 不消失")
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
