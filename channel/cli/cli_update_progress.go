package cli

import (
	"slices"
	"time"
	"xbot/protocol"

	log "xbot/logger"
)

// restoreIterationHistory converts IterationHistory from a reconnect snapshot
// into local iteration history, bootstrapping tool StartedAt timestamps.
func (m *cliModel) restoreIterationHistory(payload *protocol.ProgressEvent) {
	if payload == nil || len(payload.IterationHistory) == 0 || len(m.progressState.iterations) > 0 {
		return
	}
	for _, ih := range payload.IterationHistory {
		snap := cliIterationSnapshot{
			Iteration: ih.Iteration,
			Thinking:  ih.Thinking,
			Reasoning: ih.Reasoning,
			Tools:     ih.CompletedTools,
		}
		for i := range snap.Tools {
			t := &snap.Tools[i]
			if t.StartedAt.IsZero() && t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			}
		}
		m.progressState.iterations = append(m.progressState.iterations, snap)
	}
	if len(m.progressState.iterations) > 0 {
		lastIter := m.progressState.iterations[len(m.progressState.iterations)-1].Iteration
		if lastIter > m.progressState.lastIter {
			m.progressState.lastIter = lastIter
		}
	}
}

// carryForwardProgressState preserves transient state across progress updates
// (StartedAt timers, CompletedTools, Reasoning/Thinking content, SubAgent trees).
func (m *cliModel) carryForwardProgressState(prev *protocol.ProgressEvent) {
	if m.progressState.current == nil {
		return
	}

	// Preserve StartedAt across progress updates so live timers don't reset.
	startedAtMap := make(map[string]time.Time)
	if prev != nil {
		for _, t := range prev.ActiveTools {
			if !t.StartedAt.IsZero() {
				startedAtMap[t.Name] = t.StartedAt
			}
		}
	}
	for i := range m.progressState.current.ActiveTools {
		t := &m.progressState.current.ActiveTools[i]
		if sa, ok := startedAtMap[t.Name]; ok {
			t.StartedAt = sa
		} else if t.StartedAt.IsZero() {
			if t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			} else {
				t.StartedAt = time.Now()
			}
		}
	}

	if prev == nil {
		return
	}
	sameIter := m.progressState.current.Iteration == prev.Iteration || m.progressState.current.Iteration == 0

	// Carry forward CompletedTools from previous progress within the same iteration.
	if len(m.progressState.current.CompletedTools) == 0 && len(prev.CompletedTools) > 0 && sameIter {
		m.progressState.current.CompletedTools = prev.CompletedTools
	}

	// Carry forward Reasoning/Thinking content.
	if m.progressState.current.Reasoning == "" && prev.Reasoning != "" && sameIter {
		m.progressState.current.Reasoning = prev.Reasoning
	}
	if m.progressState.current.Thinking == "" && prev.Thinking != "" && sameIter {
		m.progressState.current.Thinking = prev.Thinking
	}

	// Carry forward StreamContent within the same iteration.
	// Structured payloads from progressFinalizer/GetActiveProgress do not carry
	// StreamContent; losing it mid-stream causes visible "frozen" display after
	// session switch recovery.
	// Skip when Thinking is already set — it contains the same finalized content
	// and carrying StreamContent forward would cause duplicate rendering
	// (renderLiveIteration renders both fields separately).
	if prev.StreamContent != "" && m.progressState.current.StreamContent == "" && sameIter {
		if m.progressState.current.Thinking == "" {
			m.progressState.current.StreamContent = prev.StreamContent
		}
	}

	// Carry forward ReasoningStreamContent.
	// Guard: only when StreamContent is also empty — reasoning stream is the
	// LLM's internal thinking; once the actual text response (StreamContent)
	// starts, reasoning stream from the previous progress shouldn't reappear.
	if m.progressState.current.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && sameIter {
		if m.progressState.current.StreamContent == "" {
			m.progressState.current.ReasoningStreamContent = prev.ReasoningStreamContent
		}
	}

	// Carry forward StreamingTools.
	// Structured payloads from progressFinalizer do not carry StreamingTools;
	// without carry forward, a structured event arriving between two
	// streamToolCallFunc callbacks would erase the first tool's "generating"
	// display before the second tool name arrives.
	// Guard: filter out tools whose Name already appears in ActiveTools —
	// the tool has transitioned from "generating" to "running"/"done", and
	// carrying forward the stale "generating" state would cause the same tool
	// to render twice (once as generating skipping dedup, once as running).
	if len(m.progressState.current.StreamingTools) == 0 && len(prev.StreamingTools) > 0 && sameIter {
		activeNames := make(map[string]bool)
		for _, t := range m.progressState.current.ActiveTools {
			activeNames[t.Name] = true
		}
		var carried []protocol.ToolProgress
		for _, t := range prev.StreamingTools {
			if !activeNames[t.Name] {
				carried = append(carried, t)
			}
		}
		m.progressState.current.StreamingTools = carried
	}

	// SubAgent tree preservation: merge new data into previous tree instead of
	// blindly copying the old tree. This prevents stale/zombie SubAgent entries
	// from persisting after they've completed.
	//
	// The server sends SubAgent data only when SubAgent progress changes.
	// Between updates, SubAgents is empty — we must carry forward the tree
	// so it remains visible. BUT we must merge (not replace) so that:
	//   - Completed SubAgents stay completed (don't revert to "running")
	//   - New SubAgents get added
	//   - SubAgents no longer in the server's tree get removed
	iterationChanged := m.progressState.current.Iteration != prev.Iteration && m.progressState.current.Iteration > 0
	if iterationChanged {
		m.progressState.current.SubAgents = nil
	} else if len(m.progressState.current.SubAgents) > 0 {
		// New progress has SubAgent data — merge into previous tree to preserve
		// completion status for agents no longer reported by the server.
		m.progressState.current.SubAgents = mergeSubAgentTrees(prev.SubAgents, m.progressState.current.SubAgents)
	} else if len(prev.SubAgents) > 0 {
		// No new SubAgent data — carry forward previous tree, but filter out
		// agents that were already done in prev. Done agents have already been
		// displayed to the user and should not linger across subsequent updates.
		m.progressState.current.SubAgents = pruneDoneSubAgents(prev.SubAgents)
	}
}

// handleProgressMsg processes progress update events from the agent.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	// Filter by session: only process progress for the currently viewed session.
	// payload.ChatID is set by ProgressEventHandler as "channel:chatID".
	// Fatal guard: ChatID must never be empty — it identifies which session
	// this progress belongs to. Empty ChatID means the progress bypassed
	// session filtering and would leak into the wrong view.
	if msg.payload != nil && msg.payload.ChatID == "" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID — this is a programming error")
		return
	}

	if msg.payload != nil && msg.payload.ChatID != "" {
		currentKey := qualifyChatID(m.channelName, m.chatID)
		if msg.payload.ChatID != currentKey {
			return
		}
	}

	turnID := m.agentTurnID // capture before any mutation
	prev := m.progressState.current

	// Seq monotonic check: discard out-of-order or duplicate progress events.
	// Placed after ChatID filtering, before any state mutation.
	if msg.payload != nil && msg.payload.Seq > 0 {
		if msg.payload.Seq <= m.progressState.lastSeq {
			return
		}
		m.progressState.lastSeq = msg.payload.Seq
	}

	// New turn's first non-PhaseDone progress clears the cancel flag.
	// This allows the new turn (started by bg notification injection or queue flush)
	// to receive progress events, while still blocking stale progress from the
	// cancelled turn. Guard: only clear when typing (turn is active).
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" && msg.payload.Phase != "" && m.typing {
		m.turnCancelled = false
	}

	// Guard: ignore progress after explicit Ctrl+C cancel.
	// PhaseDone is allowed through: it's idempotent (endAgentTurn checks turnID).
	// When switching to a running session with no saved state (first switch),
	// turnCancelled is false and m.typing is false — auto-start below handles it.
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" {
		return
	}

	// Auto-start turn: when receiving progress for current session but not typing,
	// start the turn. This handles first-switch to a running SubAgent session.
	// Guard: !suLoading — during session switch in remote mode, progress events
	// from the old session may arrive before the RPC reconciles state. Starting
	// a turn here would create an inconsistent state with no message history loaded.
	// Guard: panelMode != "askuser" — AskUser panel sets m.typing=false but the
	// turn is paused (not ended). Late progress events from the engine must not
	// trigger startAgentTurn → resetProgressState, which clears iterationHistory
	// and makes all previous iterations disappear.
	if !m.typing && !m.splashState.suLoading && !m.splashState.compReloading && msg.payload != nil && msg.payload.Phase != "done" && m.panelState.mode != "askuser" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
			"active":    len(msg.payload.ActiveTools),
			"chatID":    msg.payload.ChatID,
		}).Info("handleProgressMsg: auto-start turn")
		m.startAgentTurn()
	}

	// suLoading guard: during session switch in remote mode, discard all
	// non-PhaseDone progress events. Only PhaseDone is allowed through
	// (to clear stale turn state). All other events are stale — the RPC
	// (handleSuHistoryLoad) will reconcile with authoritative server data.
	if m.splashState.suLoading && msg.payload != nil && msg.payload.Phase != "done" {
		return
	}
	// suLoading + PhaseDone: server confirmed the turn is done.
	// Record this so handleSuHistoryLoad won't restore stale progress
	// as active (which would create a stuck spinner — typing=true with
	// no more progress events coming from the idle server).
	if m.splashState.suLoading && msg.payload != nil && msg.payload.Phase == "done" {
		m.splashState.suPhaseConfirmed = true
	}

	// Stream-only payloads (from StreamContentFunc/StreamReasoningFunc/StreamToolCallFunc)
	// only carry stream fields. Merge into existing progress instead of replacing to
	// preserve tool/iteration state.
	isStreamOnly := msg.payload != nil &&
		msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "" || len(msg.payload.StreamingTools) > 0)
	if isStreamOnly {
		if m.progressState.current != nil {
			if msg.payload.StreamContent != "" {
				m.progressState.current.StreamContent = msg.payload.StreamContent
			}
			if msg.payload.ReasoningStreamContent != "" {
				m.progressState.current.ReasoningStreamContent = msg.payload.ReasoningStreamContent
			}
			if len(msg.payload.StreamingTools) > 0 {
				m.progressState.current.StreamingTools = msg.payload.StreamingTools
			}
			// Refresh lastTokenUsage from current progress so the context bar
			// stays visible even when structured events are lost to progressCh
			// coalescing (stream-only events evicting structured events).
			m.cacheTokenUsage(m.progressState.current.TokenUsage)
		} else if m.typing {
			// Turn started but no structured progress yet — create minimal payload
			if msg.payload.CWD == "" && m.progressState.current != nil {
				msg.payload.CWD = m.progressState.current.CWD
			}
			// Preserve CWD from previous progress if new payload doesn't have it.
			if msg.payload.CWD == "" && m.progressState.current != nil {
				msg.payload.CWD = m.progressState.current.CWD
			}
			// Preserve CWD from previous progress if new payload doesn't have it.
			if msg.payload.CWD == "" && m.progressState.current != nil {
				msg.payload.CWD = m.progressState.current.CWD
			}
			m.progressState.current = msg.payload
		}
		return
	}
	// Structured (non-stream-only) payload: replace m.progressState.current.
	// Carrying forward stream content (same-iteration only) is handled by
	// carryForwardProgressState below — the single source of truth for all
	// carry-forward logic.
	if msg.payload == nil {
		m.progressState.current = nil
		return
	}
	// Preserve CWD from previous progress if new payload doesn't have it.
	if msg.payload.CWD == "" && m.progressState.current != nil {
		msg.payload.CWD = m.progressState.current.CWD
	}
	m.progressState.current = msg.payload

	if m.cachedMaxContextTokens == 0 {
		m.cachedMaxContextTokens = m.resolveMaxContextTokens()
	}
	if m.cachedCompressRatio == 0 {
		m.cachedCompressRatio = m.resolveCompressRatio()
	}
	if m.cachedMaxOutputTokens == 0 {
		m.cachedMaxOutputTokens = m.resolveMaxOutputTokens()
	}

	// Restore iteration history from reconnect/GetActiveProgress snapshot.
	m.restoreIterationHistory(m.progressState.current)

	m.carryForwardProgressState(prev)

	// Detect iteration reset for SubAgent sessions: when a new background
	// Run starts after interrupt+resend, iteration counter resets to 0.
	// The TUI still has old progress state (m.typing=true, old iterations).
	// Reset progress state and trigger history reload so:
	// 1. Progress panel shows fresh iterations (starting from #0)
	// 2. User message from parent agent appears in message list (from DB)
	// This must run BEFORE snapshotIterationChange, which skips iterations
	// that are <= m.progressState.lastIter.
	if m.progressState.current != nil && prev != nil && m.progressState.current.Iteration < m.progressState.lastIter && m.progressState.lastIter > 0 && m.typing {
		// Snapshot the old turn's final state before resetting.
		// Use the current agentTurnID since we're ending the current turn.
		m.endAgentTurn(m.agentTurnID)
		// Auto-start will trigger on this same progress event
		// (m.typing is now false, and the guard below will start a new turn).
		// Reload messages from DB to show the new user message from the parent agent.
		m.reloadMessagesFromSession(false)
	}

	// Update bg task count from callback
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Update agent count from callback
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}

	// HistoryCompacted: context compression replaced the engine's message list.
	// Clear stale messages immediately so the user doesn't see outdated content
	// during the async reload, then rebuild from session storage.
	// Also clear the token usage bar — compressed context has far fewer tokens.
	if msg.payload != nil && msg.payload.HistoryCompacted {
		m.lastTokenUsage = nil
		// Clear pendingUserMsg: the reload will fetch the authoritative user
		// message from DB (with system guide text prepended). Keeping the raw
		// pendingUserMsg causes a duplicate because its content doesn't match
		// the DB version (content comparison fails in handleHistoryReload).
		m.pendingUserMsg = nil
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.streamingMsgIdx = -1
		// Clear all progress/iteration state. Without this, a stale PhaseDone
		// event from the pre-compression iteration can arrive after clearing
		// and re-insert old iterationHistory as a tool_summary message, causing
		// the TUI to show extra content that doesn't exist after restart.
		m.progressState.iterations = nil
		m.reasoningByIter = nil
		m.progressState.streamReasoningByIter = nil
		m.progressState.lastIter = 0
		m.lastReasoning = ""
		m.lastThinking = ""
		m.invalidateAllCache(true)
		m.rc.invalidateProgress()
		// Do NOT set compReloading=true. The old code blocked auto-start until
		// the async reload completed, but if the reload message was dropped
		// (asyncCh full), compReloading stayed true forever — the streaming
		// message was never recreated, freezing the TUI (busy but no updates).
		// Instead, create the streaming message immediately. Progress events
		// continue to render live content via updateStreamingOnly, even before
		// the reload arrives with compacted history.
		// Do NOT GotoBottom here — compression can happen while the user
		// is scrolled up reading old content. Forcing to bottom would
		// lose their position. The subsequent reloadMessagesFromSession
		// → handleHistoryReload respects userScrolledUp/newContentHint.
		m.reloadMessagesFromSession(true)
		// Create streaming message immediately so progress events have a
		// rendering target. handleHistoryReload preserves it when the
		// compacted history arrives.
		if m.typing {
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   "",
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
				turnID:    m.agentTurnID,
			})
			m.streamingMsgIdx = len(m.messages) - 1
			m.rc.valid = false
		}
	}

	// Cache token usage for context bar display — every progress event
	// carries fresh token counts from the agent's updateTokenUsage().
	// Must run after HistoryCompacted so the compressed estimate overwrites
	// the nil set above, rather than being cleared by it.
	if m.progressState.current != nil {
		m.cacheTokenUsage(m.progressState.current.TokenUsage)
	}

	if msg.payload != nil {
		// Sync todo items from progress event
		m.syncProgressTodos(msg.payload)
		// Detect iteration change and snapshot previous iteration
		m.snapshotIterationChange(msg.payload, prev)

		// Record per-iteration reasoning from structured progress.
		if m.progressState.current != nil && m.progressState.current.Reasoning != "" && m.progressState.current.Iteration >= 0 {
			if m.reasoningByIter == nil {
				m.reasoningByIter = make(map[int]string)
			}
			m.reasoningByIter[m.progressState.current.Iteration] = m.progressState.current.Reasoning
		}

		// §2 工具可视化：快照 CompletedTools 到独立字段
		// Accept all completed tools regardless of their Iteration field — they
		// represent work that finished and should be displayed.
		if len(msg.payload.CompletedTools) > 0 {
			m.lastCompletedTools = make([]protocol.ToolProgress, len(msg.payload.CompletedTools))
			copy(m.lastCompletedTools, msg.payload.CompletedTools)
		}
		if msg.payload.Phase == "done" {
			m.handleProgressDone(msg, prev, turnID)
		}
	}
	m.updateViewportContent()
}

// syncProgressTodos syncs todo items from the progress payload.
func (m *cliModel) syncProgressTodos(payload *protocol.ProgressEvent) {
	if payload == nil {
		return
	}
	if len(payload.Todos) > 0 {
		allDone := true
		for _, t := range payload.Todos {
			if !t.Done {
				allDone = false
				break
			}
		}
		if m.todosDoneCleared && allDone {
			// Already cleared by user input; don't re-accept stale all-done list
		} else {
			// Change detection: skip if todos haven't actually changed.
			// High-frequency progress events carry the same Todos every time;
			// without this guard, each event triggers relayoutViewport → fullRebuild,
			// which destroys render cache and re-runs glamour/chroma on ALL messages.
			// This was responsible for ~34% CPU during agent work (pprof 2026-05-23).
			if todosEqual(m.todos, payload.Todos) {
				return
			}

			countChanged := len(m.todos) != len(payload.Todos)

			m.todos = make([]protocol.TodoItem, len(payload.Todos))
			copy(m.todos, payload.Todos)
			m.todosDoneCleared = false

			if countChanged {
				// Todo count affects layoutViewportHeight (todo bar lines).
				// Must relayout viewport to adjust height.
				m.relayoutViewport()
			} else {
				// Same count, just status/text changed — no height change needed.
				// Only invalidate progress block render so next tick picks it up.
				m.rc.valid = false
			}

			// Persist to TodoManager so todos survive turn end and session switches.
			m.persistTodosToManager()
		}
	}
	// When payload.Todos is empty, do NOT clear m.todos.
	// An empty Todos field only means "this progress event carries no todo data"
	// (e.g. early thinking phase before todo_write executes), not "todos were deleted".
	// TODOs are cleared only by: user sending a new message (todosDoneCleared),
	// turn ending with all done (endAgentTurn), or explicit todo_write([]).
}

// todosEqual returns true if two todo slices have identical content.
func todosEqual(a, b []protocol.TodoItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Text != b[i].Text || a[i].Done != b[i].Done {
			return false
		}
	}
	return true
}

// persistTodosToManager writes m.todos to the CLI-side todoManager
// for cross-turn and cross-session persistence.
func (m *cliModel) persistTodosToManager() {
	if m.todoManager == nil {
		return
	}
	key := m.sessionKey()
	if key == "" {
		return
	}
	if len(m.todos) == 0 {
		m.todoManager.SetTodos(key, nil)
		return
	}
	// m.todos is already []protocol.TodoItem, pass directly.
	items := make([]protocol.TodoItem, len(m.todos))
	copy(items, m.todos)
	m.todoManager.SetTodos(key, items)
}

// snapshotIterationChange detects iteration changes and snapshots the previous
// iteration's tools/reasoning into iteration history.
func (m *cliModel) snapshotIterationChange(payload *protocol.ProgressEvent, prev *protocol.ProgressEvent) {
	if payload == nil {
		return
	}
	if payload.Iteration > m.progressState.lastIter && m.progressState.lastIter >= 0 {
		// Guard: only create snapshot if prev actually belongs to lastSeenIteration.
		// After session switch, resetProgressState sets lastSeenIteration=0 but
		// the restored m.progressState.current has Iteration=N. When the next live progress
		// arrives, prev (which came from the restore) has Iteration=N, not 0.
		// Snapshoting "iteration 0" with iteration N's data would cause #0 and #1
		// to display the same reasoning content.
		// Also guard against prev being nil (progress cleared by endAgentTurn).
		if prev != nil && prev.Iteration != m.progressState.lastIter {
			// Data mismatch: prev belongs to a different iteration than what
			// lastSeenIteration claims. Instead of discarding the snapshot
			// entirely (which permanently loses iteration data), create a
			// snapshot tagged with prev.Iteration (the actual iteration number).
			// Guard against duplicate snapshots for the same iteration.
			alreadySnapped := false
			for _, snap := range m.progressState.iterations {
				if snap.Iteration == prev.Iteration {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				prevIterTools := prev.CompletedTools
				for _, t := range prev.ActiveTools {
					if t.Status == "done" || t.Status == "error" {
						prevIterTools = append(prevIterTools, t)
					}
				}
				prevReasoning := prev.Reasoning
				if prevReasoning == "" && m.reasoningByIter != nil {
					prevReasoning = m.reasoningByIter[prev.Iteration]
				}
				// Fallback to streaming reasoning content (same as main path).
				if prevReasoning == "" && prev.ReasoningStreamContent != "" {
					prevReasoning = prev.ReasoningStreamContent
				}
				if len(prevIterTools) > 0 || prev.Thinking != "" || prevReasoning != "" {
					snap := cliIterationSnapshot{
						Iteration:   prev.Iteration,
						Thinking:    prev.Thinking,
						Reasoning:   prevReasoning,
						Tools:       prevIterTools,
						ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
					}
					m.progressState.iterations = append(m.progressState.iterations, snap)
				}
			}
			m.progressState.lastIter = payload.Iteration
			m.progressState.iterStart = time.Now()
			return
		}
		if prev != nil {
			prevIterTools := prev.CompletedTools
			// Also include ActiveTools that completed (status=done/error) but
			// haven't been moved to CompletedTools yet by progressFinalizer.
			for _, t := range prev.ActiveTools {
				if t.Status == "done" || t.Status == "error" {
					prevIterTools = append(prevIterTools, t)
				}
			}
			prevReasoning := prev.Reasoning
			if prevReasoning == "" {
				prevReasoning = m.reasoningByIter[m.progressState.lastIter]
			}
			// Fallback: capture streaming reasoning when structured Reasoning
			// hasn't been finalized yet (LLM still streaming). Without this,
			// the reasoning block in completed iterations briefly disappears
			// when iteration changes — see handleProgressDone cancel path
			// for the same pattern.
			if prevReasoning == "" && prev.ReasoningStreamContent != "" {
				prevReasoning = prev.ReasoningStreamContent
			}
			if len(prevIterTools) > 0 || prev.Thinking != "" || prevReasoning != "" {
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Thinking:    prev.Thinking,
					Reasoning:   prevReasoning,
					Tools:       prevIterTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				m.progressState.iterations = append(m.progressState.iterations, snap)
			}
			m.lastCompletedTools = m.lastCompletedTools[:0]
		}
		m.progressState.lastIter = payload.Iteration
		m.progressState.iterStart = time.Now()
	}
}

// handleProgressDone handles the Phase "done" case: snapshots the final iteration,
// generates tool summary, resets iteration tracking state, and synthesizes agent messages.
func (m *cliModel) handleProgressDone(msg cliProgressMsg, prev *protocol.ProgressEvent, turnID uint64) {
	// When turn was cancelled (Ctrl+C), still generate tool_summary from
	// accumulated iterationHistory so tool records survive rewind operations.
	// Without this, cancelled turns lose their tool records because iterationHistory
	// is cleared by endAgentTurn, and no tool_summary message exists in m.messages.
	// (Restarting the client restores them via ch.ConvertMessagesToHistory from DB,
	// proving the data is valid — we just need to persist it in-memory too.)
	if m.turnCancelled {
		// Snapshot the final iteration (same logic as the non-cancelled path below).
		// This is needed because snapshotIterationChange only fires on iteration
		// *changes* (N→N+1), so a single-iteration turn won't have any snapshots yet.
		if m.progressState.lastIter >= 0 {
			alreadySnapped := slices.ContainsFunc(m.progressState.iterations, func(s cliIterationSnapshot) bool {
				return s.Iteration == m.progressState.lastIter
			})
			if !alreadySnapped {
				var finalTools []protocol.ToolProgress
				finalTools = append(finalTools, msg.payload.CompletedTools...)
				for _, t := range msg.payload.ActiveTools {
					if t.Status == "done" || t.Status == "error" {
						if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
							return existing.Name == t.Name && existing.Label == t.Label
						}) {
							finalTools = append(finalTools, t)
						}
					}
				}
				for _, t := range m.lastCompletedTools {
					if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
						return existing.Name == t.Name && existing.Label == t.Label
					}) {
						finalTools = append(finalTools, t)
					}
				}
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Thinking:    msg.payload.Thinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				// Capture streamed content as fallback when structured Thinking
				// is empty. This happens when Ctrl+C interrupts mid-stream (LLM
				// hasn't finished, recordAssistantMsg hasn't set ThinkingContent).
				// prev holds the live progress the user was watching — its
				// StreamContent/Thinking are correct for the current iteration.
				// This preserves the "what you see is what stays" principle.
				if snap.Thinking == "" && prev != nil {
					if prev.Thinking != "" {
						snap.Thinking = prev.Thinking
					} else if prev.StreamContent != "" {
						snap.Thinking = prev.StreamContent
					}
				}
				if m.reasoningByIter != nil {
					snap.Reasoning = m.reasoningByIter[m.progressState.lastIter]
				}
				if snap.Reasoning == "" {
					snap.Reasoning = m.lastReasoning
				}
				if snap.Reasoning == "" && prev != nil {
					snap.Reasoning = prev.Reasoning
				}
				// Capture streamed reasoning as fallback (LLM was still streaming
				// reasoning when Ctrl+C interrupted). Safe in cancel path: prev is
				// the current iteration's live progress — ReasoningStreamContent is
				// what the user saw on screen.
				if snap.Reasoning == "" && prev != nil && prev.ReasoningStreamContent != "" {
					snap.Reasoning = prev.ReasoningStreamContent
				}
				if snap.Reasoning == "" {
					snap.Reasoning = msg.payload.Reasoning
				}
				if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
					m.progressState.iterations = append(m.progressState.iterations, snap)
				}
			}
		}
		m.setTurnDoneProcessed(turnID)
		// Bake iteration data into the streaming message BEFORE endAgentTurn
		// clears iterationHistory and progress. This preserves tool tags and
		// reasoning in the viewport after Ctrl+C — the user already saw this
		// content rendered inline and expects it to remain visible.
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
			m.messages[m.streamingMsgIdx].turnID == turnID {
			if len(m.progressState.iterations) > 0 {
				baked := make([]cliIterationSnapshot, len(m.progressState.iterations))
				copy(baked, m.progressState.iterations)
				m.messages[m.streamingMsgIdx].iterations = baked
				m.messages[m.streamingMsgIdx].dirty = true
			}
		}
		m.endAgentTurn(turnID)
		// Restore turnCancelled: endAgentTurn resets it to false (correct for
		// normal completion), but in the cancel path we need it to stay true
		// until the cancel ack arrives. Otherwise, stale progress events from
		// the engine (e.g. mid-stream cancellation) trigger auto-start turn
		// via handleProgressMsg's auto-start guard, creating a phantom turn
		// that overwrites the cancel state and loses the user message from
		// the viewport.
		m.turnCancelled = true
		if turnID == m.agentTurnID {
			m.inputReady = true
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}
		return
	}
	// Snapshot the final iteration before clearing progress.
	// This handles the case where PhaseDone arrives before
	// handleAgentMessage (e.g. agent error/cancel).
	// Skip if handleAgentMessage already processed (m.typing == false
	// means the reply arrived and cleaned up iteration state).
	if m.typing && m.progressState.lastIter >= 0 {
		alreadySnapped := slices.ContainsFunc(m.progressState.iterations, func(s cliIterationSnapshot) bool {
			return s.Iteration == m.progressState.lastIter
		})
		if !alreadySnapped {
			var finalTools []protocol.ToolProgress
			// Check progress.CompletedTools first (set by progressFinalizer)
			finalTools = append(finalTools, msg.payload.CompletedTools...)
			// Also include ActiveTools(done) not yet moved by progressFinalizer
			for _, t := range msg.payload.ActiveTools {
				if t.Status == "done" || t.Status == "error" {
					if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
						return existing.Name == t.Name && existing.Label == t.Label
					}) {
						finalTools = append(finalTools, t)
					}
				}
			}
			// Also include any from lastCompletedTools (race safety)
			for _, t := range m.lastCompletedTools {
				if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
					return existing.Name == t.Name && existing.Label == t.Label
				}) {
					finalTools = append(finalTools, t)
				}
			}
			snap := cliIterationSnapshot{
				Iteration:   m.progressState.lastIter,
				Thinking:    msg.payload.Thinking,
				Tools:       finalTools,
				ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
			}
			// Carry over reasoning: priority is reasoningByIter (per-iteration, authoritative)
			// > lastReasoning (captured before progress clear)
			// > prev progress Reasoning (server-authoritative, from ReasoningContent)
			// > PhaseDone payload Reasoning
			reasoning := m.reasoningByIter[m.progressState.lastIter]
			if reasoning == "" {
				reasoning = m.lastReasoning
			}
			if reasoning == "" && prev != nil {
				reasoning = prev.Reasoning
			}
			if reasoning == "" {
				reasoning = msg.payload.Reasoning
			}
			snap.Reasoning = reasoning
			if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
				m.progressState.iterations = append(m.progressState.iterations, snap)
			}
		}
		// Store iterations in pendingToolSummary for handleAgentMessage
		// to bake into the assistant message. Accumulate (not replace) to
		// handle multiple PhaseDone events per logical turn (simulation tests).
		if len(m.progressState.iterations) > 0 {
			if m.pendingToolSummary == nil {
				m.pendingToolSummary = &cliMessage{}
			}
			// Dedup by iteration number to avoid duplicates from repeated PhaseDone.
			existingIters := make(map[int]bool)
			for _, it := range m.pendingToolSummary.iterations {
				existingIters[it.Iteration] = true
			}
			for _, it := range m.progressState.iterations {
				if !existingIters[it.Iteration] {
					m.pendingToolSummary.iterations = append(m.pendingToolSummary.iterations, it)
				}
			}
		}
	}
	// Mark this turn as done-processed (iterations stored in pendingToolSummary).
	m.setTurnDoneProcessed(turnID)

	// Save pendingToolSummary before endAgentTurn clears it via resetProgressState.
	savedPTS := m.pendingToolSummary

	// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
	m.endAgentTurn(turnID) // also clears todos and does relayoutViewport

	// Restore pendingToolSummary so it persists across auto-start-turn cycles.
	m.pendingToolSummary = savedPTS
	if turnID == m.agentTurnID {
		m.inputReady = true
		if len(m.messageQueue) > 0 {
			m.needFlushQueue = true
		}
	}

	// For agent sessions (interactive SubAgent viewer), the outbound
	// message goes back to the parent agent's channel/chatID — it never
	// arrives as a cliOutboundMsg for this session view. So we must
	// synthesize the assistant message from the progress payload's final
	// content (Thinking field carries the last clean assistant text).
	// For main sessions, handleAgentMessage handles this and will
	// bake iterations into the assistant reply via pendingToolSummary.
	if m.channelName == "agent" && !m.typing {
		assistantContent := msg.payload.Thinking
		if assistantContent == "" {
			assistantContent = msg.payload.StreamContent
		}
		if assistantContent != "" {
			m.upsertMessageByTurn(turnID, "assistant", cliMessage{
				role:      "assistant",
				content:   assistantContent,
				timestamp: time.Now(),
				dirty:     true,
			})
			m.setTurnReplyReceived(turnID)
			m.rc.valid = false
		}
	}

	m.relayoutViewport()
}
