package cli

import (
	"slices"
	"time"
	"xbot/protocol"

	log "xbot/logger"
)

// applyProgressSnapshot applies a complete backend snapshot to the local state.
// This is the SINGLE authoritative state update point — called from both
// handleProgressMsg (on push events) and handleTickMsg (every 100ms).
//
// It replaces the old push-based pipeline:
//   - mergeProgressState (120 lines of stream field preservation)
//   - snapshotIterationChange (70 lines of fallback chains)
//   - handleProgressDone (210 lines of 3-path finalization)
//
// The snapshot from GetActiveProgress is always complete and consistent —
// it includes all fields (structured + stream) and IterationHistory.
// We simply replace the local state, no merging needed.
func (m *cliModel) applyProgressSnapshot(snapshot *protocol.ProgressEvent) {
	if snapshot == nil {
		return
	}

	// Defensive copy: GetActiveProgress returns a shallow copy of the engine's
	// snapshot, but slice fields (ActiveTools, etc.) share the backing array.
	// We mutate ActiveTools[i].StartedAt below — without this copy, we'd
	// pollute the engine's stored snapshot. Deep-copy only the slices we
	// mutate; other slices are read-only.
	snap := *snapshot
	if len(snap.ActiveTools) > 0 {
		snap.ActiveTools = make([]protocol.ToolProgress, len(snap.ActiveTools))
		copy(snap.ActiveTools, snapshot.ActiveTools)
	}
	snapshot = &snap

	// Seq check: skip if we've already applied this or a newer snapshot.
	// This deduplicates push events and tick reads — the latest snapshot wins.
	if snapshot.Seq > 0 && snapshot.Seq <= m.progressState.lastAppliedSeq {
		return
	}
	if snapshot.Seq > 0 {
		m.progressState.lastAppliedSeq = snapshot.Seq
	}

	// HistoryCompacted: context compression replaced the engine's message list.
	// Trigger reload from DB. This is a state-change signal, not data.
	if snapshot.HistoryCompacted {
		m.handleHistoryCompactedSignal()
		return
	}

	// Restore iteration history from snapshot (backend tracks this).
	// This replaces snapshotIterationChange — the backend is the single
	// source of truth for completed iterations.
	m.restoreIterationsFromSnapshot(snapshot)

	// Local iteration snapshot for low-latency display between ticks.
	// When iteration increases, snapshot the previous iteration.
	// The authoritative history comes from restoreIterationsFromSnapshot above;
	// this just ensures immediate display without waiting for tick pull.
	if m.progressState.current != nil && snapshot.Iteration > m.progressState.current.Iteration &&
		m.progressState.current.Iteration >= 0 {
		m.snapshotIterationLocal(m.progressState.current)
	}

	// PhaseDone: turn completed. The outbound reply (handleAgentMessage) is
	// the authoritative end-of-turn signal for main sessions. For agent
	// sessions (SubAgent viewer), there's no outbound — finalize here.
	// Note: no m.typing check — PhaseDone can arrive after cancel sets
	// typing=false. Seq dedup prevents double-finalization.
	if snapshot.Phase == "done" {
		m.finalizeTurnFromSnapshot(snapshot)
		return
	}

	// Auto-start: receiving progress for a running session we're not tracking.
	// This handles first-switch to a running SubAgent session.
	if !m.typing && snapshot.Phase != "" && snapshot.Phase != "done" &&
		!m.splashState.suLoading && !m.splashState.compReloading &&
		m.panelState.mode != "askuser" {
		log.WithFields(log.Fields{
			"phase":     snapshot.Phase,
			"iteration": snapshot.Iteration,
		}).Info("applyProgressSnapshot: auto-start turn")
		m.startAgentTurn()
	}

	// Update current state — direct replacement, no merge.
	// The snapshot is always complete (from GetActiveProgress).
	// Preserve stream fields from previous state when new snapshot doesn't
	// carry them and iterations match. Stream fields (StreamContent,
	// ReasoningStreamContent, StreamTokens) come from stream-only events
	// and may not be present in structured events from progressFinalizer.
	prev := m.progressState.current
	if prev != nil && snapshot.Iteration == prev.Iteration {
		if snapshot.StreamContent == "" && prev.StreamContent != "" {
			snapshot.StreamContent = prev.StreamContent
		}
		if snapshot.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" {
			snapshot.ReasoningStreamContent = prev.ReasoningStreamContent
		}
		if snapshot.StreamTokens == 0 && prev.StreamTokens > 0 {
			snapshot.StreamTokens = prev.StreamTokens
		}
	}

	// Preserve StartedAt for running tools across snapshot replacement.
	// Backend sends Elapsed (static ms) but not StartedAt. Without this,
	// the live elapsed timer in renderToolTags resets to 0 on every
	// applyProgressSnapshot call (showing "0ms" instead of ticking).
	if prev != nil {
		prevRunning := make(map[string]time.Time)
		for _, t := range prev.ActiveTools {
			if t.Status == "running" && !t.StartedAt.IsZero() {
				prevRunning[t.Name+t.Label] = t.StartedAt
			}
		}
		for i := range snapshot.ActiveTools {
			t := &snapshot.ActiveTools[i]
			if t.Status == "running" && t.StartedAt.IsZero() {
				if startedAt, ok := prevRunning[t.Name+t.Label]; ok {
					t.StartedAt = startedAt
				} else {
					// New running tool — start timer now.
					t.StartedAt = time.Now()
				}
			}
		}
	} else {
		// No previous state — bootstrap StartedAt for any running tools.
		for i := range snapshot.ActiveTools {
			t := &snapshot.ActiveTools[i]
			if t.Status == "running" && t.StartedAt.IsZero() {
				t.StartedAt = time.Now()
			}
		}
	}

	m.progressState.current = snapshot
	if snapshot.Iteration > m.progressState.lastIter {
		m.progressState.lastIter = snapshot.Iteration
		m.progressState.iterStart = time.Now()
	}

	// Cache token usage for context bar.
	if snapshot.TokenUsage != nil {
		m.cacheTokenUsage(snapshot.TokenUsage)
	}

	// Sync todos (with change detection to avoid unnecessary relayout).
	m.syncProgressTodos(snapshot)
}

// snapshotIterationLocal captures a completed iteration for immediate display.
// Simple and best-effort — the authoritative history comes from the backend
// via restoreIterationsFromSnapshot. This just ensures low-latency rendering
// between tick pulls.
func (m *cliModel) snapshotIterationLocal(prev *protocol.ProgressEvent) {
	if prev == nil {
		return
	}
	// Skip if already snapshotted (dedup by iteration number).
	for _, existing := range m.progressState.iterations {
		if existing.Iteration == prev.Iteration {
			return
		}
	}
	tools := prev.CompletedTools
	// Include ALL active tools — when iteration changes, active tools
	// belong to the completed iteration (engine transitions them to done).
	tools = append(tools, prev.ActiveTools...)
	reasoning := prev.Reasoning
	if reasoning == "" {
		reasoning = prev.ReasoningStreamContent
	}
	if len(tools) > 0 || prev.Content != "" || reasoning != "" {
		snap := cliIterationSnapshot{
			Iteration:   prev.Iteration,
			Content:     prev.Content,
			Reasoning:   reasoning,
			Tools:       tools,
			ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
		}
		m.progressState.iterations = append(m.progressState.iterations, snap)
	}
	m.progressState.lastIter = prev.Iteration + 1
	m.progressState.iterStart = time.Now()
	m.rc.invalidateProgress()
}

// restoreIterationsFromSnapshot rebuilds local iteration history from the
// backend snapshot's IterationHistory field. This replaces the old
// snapshotIterationChange which tried to detect iteration changes from
// partial push events. The backend already tracks completed iterations
// authoritatively in recordIterationSnapshot.
func (m *cliModel) restoreIterationsFromSnapshot(snapshot *protocol.ProgressEvent) {
	if len(snapshot.IterationHistory) == 0 {
		return
	}

	// Check if we already have these iterations restored.
	// Avoid rebuilding if the count matches (iterations are append-only).
	if len(m.progressState.iterations) >= len(snapshot.IterationHistory) {
		return
	}

	// Rebuild from snapshot — backend is authoritative.
	m.progressState.iterations = make([]cliIterationSnapshot, 0, len(snapshot.IterationHistory))
	for _, ih := range snapshot.IterationHistory {
		snap := cliIterationSnapshot{
			Iteration:   ih.Iteration,
			Content:     ih.Content,
			Reasoning:   ih.Reasoning,
			Tools:       ih.CompletedTools,
			ElapsedWall: ih.ElapsedWall,
		}
		// Bootstrap StartedAt for elapsed-time display.
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

	// Invalidate completed iteration render cache.
	m.rc.invalidateProgress()
}

// finalizeTurnFromSnapshot handles Phase=done from the snapshot.
// For main sessions: handleAgentMessage is the authoritative end-of-turn
// signal (creates the final assistant message). PhaseDone from snapshot
// just snapshots the final iteration and marks turn state.
// For agent sessions (SubAgent viewer): no outbound message arrives,
// so we create the assistant message here from the snapshot's Content.
func (m *cliModel) finalizeTurnFromSnapshot(snapshot *protocol.ProgressEvent) {
	turnID := m.agentTurnID
	cur := m.progressState.current

	// Determine the final iteration number from current state or snapshot.
	finalIter := m.progressState.lastIter
	if cur != nil && cur.Iteration > finalIter {
		finalIter = cur.Iteration
	}
	if snapshot.Iteration > finalIter {
		finalIter = snapshot.Iteration
	}

	// Snapshot the final iteration if not already done.
	// PhaseDone events are often sparse — use current state as primary source.
	if finalIter >= 0 {
		alreadySnapped := slices.ContainsFunc(m.progressState.iterations, func(s cliIterationSnapshot) bool {
			return s.Iteration == finalIter
		})
		if !alreadySnapped {
			// Content: snapshot → current.Content → current.StreamContent (cancel only)
			content := snapshot.Content
			if content == "" && cur != nil {
				content = cur.Content
			}
			if content == "" && cur != nil && m.turnCancelled {
				content = cur.StreamContent
			}
			// Reasoning: snapshot → current.Reasoning → current.ReasoningStreamContent
			reasoning := snapshot.Reasoning
			if reasoning == "" && cur != nil {
				reasoning = cur.Reasoning
			}
			if reasoning == "" && cur != nil {
				reasoning = cur.ReasoningStreamContent
			}
			// Tools: snapshot.CompletedTools → current's completed + done active
			// Filter to only include tools from the current iteration.
			finalTools := snapshot.CompletedTools
			if len(finalTools) == 0 && cur != nil {
				finalTools = cur.CompletedTools
				for _, t := range cur.ActiveTools {
					if t.Status == "done" || t.Status == "error" {
						finalTools = append(finalTools, t)
					}
				}
			}
			// Filter by iteration to prevent cross-iteration tool contamination.
			if finalIter > 0 && len(finalTools) > 0 {
				var filtered []protocol.ToolProgress
				for _, t := range finalTools {
					if t.Iteration == 0 || t.Iteration == finalIter {
						filtered = append(filtered, t)
					}
				}
				if len(filtered) > 0 {
					finalTools = filtered
				}
			}
			snap := cliIterationSnapshot{
				Iteration:   finalIter,
				Content:     content,
				Reasoning:   reasoning,
				Tools:       finalTools,
				ElapsedWall: time.Since(m.progressState.iterStart).Milliseconds(),
			}
			if len(finalTools) > 0 || content != "" || reasoning != "" {
				m.progressState.iterations = append(m.progressState.iterations, snap)
			}
		}
	}

	// Bake iterations into the streaming message before ending turn.
	if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) &&
		m.messages[m.streamingMsgIdx].turnID == turnID {
		if len(m.progressState.iterations) > 0 {
			baked := make([]cliIterationSnapshot, len(m.progressState.iterations))
			copy(baked, m.progressState.iterations)
			m.messages[m.streamingMsgIdx].iterations = baked
			m.messages[m.streamingMsgIdx].dirty = true
		}
	}

	wasCancelled := m.turnCancelled
	m.endAgentTurn(turnID)
	// Restore turnCancelled: endAgentTurn resets it to false, but cancel path
	// needs it to stay true until the cancel ack arrives. Otherwise stale
	// progress events trigger auto-start, creating phantom turns.
	if wasCancelled {
		m.turnCancelled = true
	}

	if turnID == m.agentTurnID {
		m.inputReady = true
		if len(m.messageQueue) > 0 {
			m.needFlushQueue = true
		}
	}

	// Agent session (SubAgent viewer): no outbound message arrives.
	// Create assistant message from snapshot content.
	if m.channelName == "agent" && !m.typing {
		assistantContent := snapshot.Content
		if assistantContent == "" {
			assistantContent = snapshot.StreamContent
		}
		if assistantContent != "" {
			asstMsg := cliMessage{
				role:      "assistant",
				content:   assistantContent,
				timestamp: time.Now(),
				turnID:    turnID,
				dirty:     true,
			}
			if len(m.progressState.iterations) > 0 {
				asstMsg.iterations = make([]cliIterationSnapshot, len(m.progressState.iterations))
				copy(asstMsg.iterations, m.progressState.iterations)
			}
			if snapshot.Reasoning != "" {
				asstMsg.reasoning = snapshot.Reasoning
			}
			// Find existing or append new — single creation point for agent sessions.
			existingIdx := m.findMessageByTurn(turnID, "assistant")
			if existingIdx >= 0 {
				m.messages[existingIdx] = asstMsg
			} else {
				m.messages = append(m.messages, asstMsg)
			}
			m.rc.valid = false
			m.relayoutViewport()
		}
	}
}

// handleHistoryCompactedSignal triggers message reload after context compression.
// The snapshot's HistoryCompacted flag is a state-change signal — the actual
// message data comes from the DB via reloadMessagesFromSession.
func (m *cliModel) handleHistoryCompactedSignal() {
	m.lastTokenUsage = nil
	m.pendingUserMsg = nil
	m.messages = make([]cliMessage, 0, cliMsgBufSize)
	m.streamingMsgIdx = -1
	m.progressState.iterations = nil
	m.progressState.lastIter = 0
	m.progressState.lastAppliedSeq = 0 // reset so post-compression snapshot is applied
	m.progressState.lastStreamSeq = 0
	m.invalidateAllCache(true)
	m.rc.invalidateProgress()
	m.splashState.compReloading = true
	m.reloadMessagesFromSession(true)
}
