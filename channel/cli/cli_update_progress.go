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

// mergeProgressState merges a structured progress event into the current
// progress state in-place. Structured fields (Phase, Iteration, ActiveTools,
// CompletedTools, Reasoning, Thinking, etc.) are updated from the payload,
// while stream-only fields (StreamContent, ReasoningStreamContent,
// StreamingTools, StreamTokens) persist naturally — they are only written by
// stream-only events and never cleared by structured events.
//
// This eliminates the entire carryForwardProgressState mechanism: instead of
// replacing the whole object (losing stream fields) and then trying to
// recover them with fragile conditional logic, we simply update only the
// structured fields and leave stream fields untouched.
//
// On iteration change, stream fields are cleared (they belong to the previous
// iteration) and iteration-specific structured fields are reset from the
// payload. This replaces the sameIter guards in the old carryForward.
func (m *cliModel) mergeProgressState(payload *protocol.ProgressEvent) {
	if payload == nil {
		m.progressState.current = nil
		return
	}

	cur := m.progressState.current
	if cur == nil {
		m.progressState.current = payload
		return
	}

	// Capture iteration change BEFORE updating any fields.
	oldIter := cur.Iteration
	iterationChanged := payload.Iteration > 0 && oldIter > 0 && payload.Iteration != oldIter

	// Preserve StartedAt for tools that appear in both old and new ActiveTools.
	startedAtMap := make(map[string]time.Time)
	for _, t := range cur.ActiveTools {
		if !t.StartedAt.IsZero() {
			startedAtMap[t.Name] = t.StartedAt
		}
	}

	// --- Update structured fields ---

	cur.Phase = payload.Phase
	cur.Seq = payload.Seq
	if payload.Iteration > 0 || oldIter == 0 {
		cur.Iteration = payload.Iteration
	}

	// ActiveTools — always update (reflects current engine state), preserve StartedAt.
	cur.ActiveTools = payload.ActiveTools
	for i := range cur.ActiveTools {
		t := &cur.ActiveTools[i]
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

	// Remove StreamingTools that have transitioned to ActiveTools — the tool
	// has moved from "generating" (LLM streaming args) to "running"/"done",
	// and keeping the stale "generating" entry causes duplicate display
	// (e.g. "Shell preparing…" persists alongside "Shell: running").
	activeNames := make(map[string]bool)
	for _, t := range cur.ActiveTools {
		activeNames[t.Name] = true
	}
	if len(activeNames) > 0 && len(cur.StreamingTools) > 0 {
		filtered := cur.StreamingTools[:0]
		for _, t := range cur.StreamingTools {
			if !activeNames[t.Name] {
				filtered = append(filtered, t)
			}
		}
		cur.StreamingTools = filtered
	}

	// CompletedTools — reset on iteration change; otherwise preserve when
	// payload doesn't carry them (structured events from progressFinalizer
	// may omit CompletedTools).
	if iterationChanged || len(payload.CompletedTools) > 0 {
		cur.CompletedTools = payload.CompletedTools
	}

	// Reasoning — update when payload carries it, or reset on iteration change.
	if payload.Reasoning != "" || iterationChanged {
		cur.Reasoning = payload.Reasoning
	}

	// Thinking — same logic as Reasoning. When Thinking is finalized
	// (non-empty), clear StreamContent to avoid duplicate rendering —
	// they contain the same finalized text.
	if payload.Thinking != "" {
		cur.Thinking = payload.Thinking
		if !iterationChanged {
			cur.StreamContent = ""
		}
	} else if iterationChanged {
		cur.Thinking = ""
	}

	// TokenUsage, Todos, CWD — always update from payload.
	cur.TokenUsage = payload.TokenUsage
	cur.Todos = payload.Todos
	if payload.CWD != "" {
		cur.CWD = payload.CWD
	}

	// HistoryCompacted, IterationHistory — pass through.
	cur.HistoryCompacted = payload.HistoryCompacted
	cur.IterationHistory = payload.IterationHistory

	// SubAgents — merge logic: on iteration change, clear; on new data, merge;
	// on no new data, carry forward (pruning done agents).
	if iterationChanged {
		cur.SubAgents = nil
	} else if len(payload.SubAgents) > 0 {
		cur.SubAgents = mergeSubAgentTrees(cur.SubAgents, payload.SubAgents)
	} else if len(cur.SubAgents) > 0 {
		cur.SubAgents = pruneDoneSubAgents(cur.SubAgents)
	}

	// On iteration change, clear stream-only fields — they belong to the
	// previous iteration and will be repopulated by new stream events.
	// On same iteration, stream fields persist naturally (never touched by
	// structured events), replacing all the carryForward conditions.
	if iterationChanged {
		cur.StreamContent = ""
		cur.ReasoningStreamContent = ""
		cur.StreamingTools = nil
		cur.StreamTokens = 0
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
	// Shallow-copy current before mergeProgressState modifies it in-place.
	// prev must reflect the pre-merge state for snapshotIterationChange.
	var prev *protocol.ProgressEvent
	if m.progressState.current != nil {
		cp := *m.progressState.current
		prev = &cp
	}

	// Seq monotonic check: discard out-of-order or duplicate progress events.
	// Placed after ChatID filtering, before any state mutation.
	if msg.payload != nil && msg.payload.Seq > 0 {
		if msg.payload.Seq <= m.progressState.lastSeq {
			return
		}
		m.progressState.lastSeq = msg.payload.Seq
	}

	// Stream-only detection — MUST run before auto-start guard.
	// Stream-only events (StreamContent, ReasoningStreamContent, StreamingTools,
	// StreamTokens) have Phase="" and Iteration=0. Without this check here,
	// the auto-start guard below sees Phase != "done" and triggers
	// startAgentTurn() when typing=false — creating a DUPLICATE assistant
	// message. This is the root cause of the double-assistant bug.
	isStreamOnly := msg.payload != nil &&
		msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "" ||
			len(msg.payload.StreamingTools) > 0 || msg.payload.StreamTokens > 0)

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
	// Guard: !isStreamOnly — stream-only events are high-frequency streaming
	// updates (content, reasoning, tool args, token counts), NOT turn-start
	// signals. Without this guard, a StreamTokens-only event arriving after
	// endAgentTurn (typing=false) triggers startAgentTurn → duplicate assistant.
	// Guard: !suLoading — during session switch in remote mode, progress events
	// from the old session may arrive before the RPC reconciles state. Starting
	// a turn here would create an inconsistent state with no message history loaded.
	// Guard: panelMode != "askuser" — AskUser panel sets m.typing=false but the
	// turn is paused (not ended). Late progress events from the engine must not
	// trigger startAgentTurn → resetProgressState, which clears iterationHistory
	// and makes all previous iterations disappear.
	if !m.typing && !isStreamOnly && !m.splashState.suLoading && !m.splashState.compReloading && msg.payload != nil && msg.payload.Phase != "done" && m.panelState.mode != "askuser" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
			"active":    len(msg.payload.ActiveTools),
			"chatID":    msg.payload.ChatID,
		}).Info("handleProgressMsg: auto-start turn")
		m.startAgentTurn()
		m.turnAutoStarted = true
		// Discard stale prev — it was captured from the previous turn's
		// state. After startAgentTurn → resetProgressState, prev no
		// longer matches the current progress state and would cause
		// snapshotIterationChange to create a stale snapshot from the
		// old turn's data.
		prev = nil
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

	// Stream-only payloads (from StreamContentFunc/StreamReasoningFunc/
	// StreamToolCallFunc/StreamUsageFunc) only carry stream fields. Merge
	// into existing progress instead of replacing to preserve tool/iteration
	// state. isStreamOnly is computed above (before auto-start guard).
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
			if msg.payload.StreamTokens > 0 {
				m.progressState.current.StreamTokens = msg.payload.StreamTokens
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
	// mergeProgressState merges structured fields in-place, preserving
	// stream-only fields. This replaces the old whole-replacement +
	// carryForwardProgressState pattern.
	// CWD is preserved inside mergeProgressState when payload doesn't carry it.
	m.mergeProgressState(msg.payload)

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
		m.progressState.streamReasoningByIter = nil
		m.progressState.lastIter = 0
		m.lastThinking = ""
		m.invalidateAllCache(true)
		m.rc.invalidateProgress()
		// Set compReloading=true to block auto-start (startAgentTurn) until the
		// async reload arrives. Without this gate, a PhaseDone between
		// HistoryCompacted and reload triggers endAgentTurn → typing=false,
		// then the next progress event triggers startAgentTurn → resetProgressState
		// → rebuild → flicker. Worse, startAgentTurn creates ANOTHER streaming
		// message, producing duplicate assistants.
		//
		// The old reason for NOT setting compReloading (asyncCh full → permanent
		// freeze) is no longer valid: reloadMessagesFromSession now uses blocking
		// send with 3 retries / 15s timeout. handleHistoryReload ALWAYS clears
		// compReloading (even on error/stale early returns) to prevent leaks.
		m.splashState.compReloading = true
		// Do NOT GotoBottom here — compression can happen while the user
		// is scrolled up reading old content. Forcing to bottom would
		// lose their position. The subsequent reloadMessagesFromSession
		// → handleHistoryReload respects userScrolledUp/newContentHint.
		m.reloadMessagesFromSession(true)
		// Do NOT create a streaming message here. The DB history from reload
		// naturally contains the current turn's assistant message (persisted
		// before compression). handleHistoryReload will find it and mark it
		// as the streaming target (isPartial=true). Creating a separate
		// streaming message here would produce TWO assistants — the one from
		// DB and the one created here — which is the root cause of the
		// duplicate assistant bug after compression.
		//
		// compReloading=true blocks auto-start (startAgentTurn) during the
		// async reload, so no progress event can create another assistant.

		// RETURN: do not fall through to snapshotIterationChange or
		// updateViewportContent. After compression, prev (captured before
		// this handler) holds pre-compression data — snapshotIterationChange
		// would use it to create a stale snapshot, polluting iteration history.
		// The async reload will rebuild messages and viewport from DB.
		return
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
			}
			// If same count, just status/text changed — no height change needed.

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
				prevIterTools = append(prevIterTools, prev.ActiveTools...)
				if len(prevIterTools) > 0 || prev.Thinking != "" || prev.Reasoning != "" {
					snap := cliIterationSnapshot{
						Iteration:   prev.Iteration,
						Thinking:    prev.Thinking,
						Reasoning:   prev.Reasoning,
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
			prevIterTools = append(prevIterTools, prev.ActiveTools...)
			if len(prevIterTools) > 0 || prev.Thinking != "" || prev.Reasoning != "" {
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Thinking:    prev.Thinking,
					Reasoning:   prev.Reasoning,
					Tools:       prevIterTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				m.progressState.iterations = append(m.progressState.iterations, snap)
			}
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
				// Also include tools from prev (live progress before cancel).
				if prev != nil {
					for _, t := range prev.CompletedTools {
						if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
							return existing.Name == t.Name && existing.Label == t.Label
						}) {
							finalTools = append(finalTools, t)
						}
					}
					for _, t := range prev.ActiveTools {
						if t.Status == "done" || t.Status == "error" {
							if !slices.ContainsFunc(finalTools, func(existing protocol.ToolProgress) bool {
								return existing.Name == t.Name && existing.Label == t.Label
							}) {
								finalTools = append(finalTools, t)
							}
						}
					}
				}
				snap := cliIterationSnapshot{
					Iteration:   m.progressState.lastIter,
					Thinking:    msg.payload.Thinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
				}
				if snap.Thinking == "" && prev != nil {
					if prev.Thinking != "" {
						snap.Thinking = prev.Thinking
					} else if prev.StreamContent != "" {
						snap.Thinking = prev.StreamContent
					}
				}
				if prev != nil {
					snap.Reasoning = prev.Reasoning
				}
				if snap.Reasoning == "" {
					snap.Reasoning = msg.payload.Reasoning
				}
				if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
					snap.Tools = finalTools
					m.progressState.iterations = append(m.progressState.iterations, snap)
				}
			}
		}
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
			// PhaseDone events always carry all completed tools in
			// CompletedTools — progressFinalizer (engine_run.go:182-188)
			// moves all ActiveTools → CompletedTools before setting Phase=Done.
			// ActiveTools is always empty at PhaseDone.
			//
			// Filter to only include tools belonging to the current iteration.
			// When PhaseDone payload doesn't carry tools, fall back to current
			// progress state (which preserves tools from the last structured event).
			finalTools := msg.payload.CompletedTools
			if len(finalTools) == 0 && m.progressState.current != nil {
				finalTools = m.progressState.current.CompletedTools
			}
			var filteredTools []protocol.ToolProgress
			for _, t := range finalTools {
				if t.Iteration == m.progressState.lastIter {
					filteredTools = append(filteredTools, t)
				}
			}
			finalTools = filteredTools
			snap := cliIterationSnapshot{
				Iteration:   m.progressState.lastIter,
				Thinking:    msg.payload.Thinking,
				Tools:       finalTools,
				ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
			}
			// Carry over reasoning from prev (pre-merge snapshot) or PhaseDone payload.
			reasoning := ""
			if prev != nil {
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
	}
	// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
	m.endAgentTurn(turnID) // also clears todos and does relayoutViewport

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
	// bake iterations into the assistant reply via progressState.iterations.
	if m.channelName == "agent" && !m.typing {
		assistantContent := msg.payload.Thinking
		if assistantContent == "" {
			assistantContent = msg.payload.StreamContent
		}
		if assistantContent != "" {
			asstMsg := cliMessage{
				role:      "assistant",
				content:   assistantContent,
				timestamp: time.Now(),
				dirty:     true,
			}
			// MUST bake iterations so the SubAgent session view preserves
			// intermediate iteration history (tool tags, reasoning, etc.)
			// after the turn completes — same as handleAgentMessage does
			// for main sessions via bakeIterations.
			if len(m.progressState.iterations) > 0 {
				asstMsg.iterations = make([]cliIterationSnapshot, len(m.progressState.iterations))
				copy(asstMsg.iterations, m.progressState.iterations)
			}
			// Carry over reasoning content for the thinking box.
			if msg.payload.Reasoning != "" {
				asstMsg.thinking = msg.payload.Reasoning
			} else if prev != nil && prev.Reasoning != "" {
				asstMsg.thinking = prev.Reasoning
			}
			// Find existing or append new assistant message for this turn.
			existingIdx := m.findMessageByTurn(turnID, "assistant")
			if existingIdx >= 0 {
				m.messages[existingIdx] = asstMsg
			} else {
				m.messages = append(m.messages, asstMsg)
			}
			m.rc.valid = false
		}
		// SubAgent path needs relayoutViewport because rc.valid was set to false.
		// Main sessions skip this — endAgentTurn already called relayoutViewport
		// BEFORE clearing state, producing a single clean GotoBottom.
		m.relayoutViewport()
	}
}
