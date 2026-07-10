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

	// Restore iteration history from snapshot BEFORE the Seq check.
	//
	// Iteration-advance push events carry IterationHistory so TUI never observes
	// current advancing from C to D while C is absent from completed history.
	// Tick pulls also carry IterationHistory and may reuse the same Seq as the
	// last push event, so restoring before Seq dedup keeps completed iterations
	// authoritative even when only history changed.
	//
	// Push events without IterationHistory remain no-ops here: they update current
	// only and never create local completed snapshots.
	m.restoreIterationsFromSnapshot(snapshot)

	// Seq check: skip if we've already applied this or a newer snapshot.
	// This deduplicates push events and tick reads — the latest snapshot wins.
	// Note: restoreIterationsFromSnapshot already ran above, so iteration
	// history is always up-to-date regardless of Seq dedup.
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
	//
	// CRITICAL: when snapshot.Content is non-empty (content is finalized by
	// recordAssistantMsg), do NOT preserve stale StreamContent — it was
	// throttled and may be incomplete. The finalized Content is authoritative.
	// Same for Reasoning vs ReasoningStreamContent.
	// Without this guard, display shows truncated stream text instead of
	// the full finalized content (visual: content "grows" after tool finishes).
	prev := m.progressState.current
	if prev != nil && snapshot.Iteration == prev.Iteration {
		if snapshot.StreamContent == "" && prev.StreamContent != "" && snapshot.Content == "" {
			snapshot.StreamContent = prev.StreamContent
		}
		if snapshot.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && snapshot.Reasoning == "" {
			snapshot.ReasoningStreamContent = prev.ReasoningStreamContent
		}
		if snapshot.StreamTokens == 0 && prev.StreamTokens > 0 {
			snapshot.StreamTokens = prev.StreamTokens
		}
		if len(snapshot.StreamingTools) == 0 && len(snapshot.ActiveTools) == 0 && len(snapshot.CompletedTools) == 0 && len(prev.StreamingTools) > 0 {
			snapshot.StreamingTools = prev.StreamingTools
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

	// Merge SubAgent trees: structured events don't always carry SubAgents
	// (engine only includes them when resolveSubAgents returns non-empty).
	// Without merging, the tree flickers — appears when SubAgents are present,
	// vanishes when the next structured event omits them. mergeSubAgentTrees
	// preserves prev's running agents and marks dropped ones as done.
	if prev != nil {
		snapshot.SubAgents = mergeSubAgentTrees(prev.SubAgents, snapshot.SubAgents)
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

// restoreIterationsFromSnapshot appends completed iterations from the snapshot's
// IterationHistory to the local state. The snapshot may carry:
//   - Push delta: 0 or 1 entries (the iteration that just completed)
//   - Pull response: only entries with Iteration > fromIter (the TUI's watermark)
//
// Per-iteration dedup: iterations already present locally are skipped.
// This replaces the old count-based replace logic which assumed full cumulative
// history in every snapshot. With the delta protocol, snapshots carry only new
// iterations — append is the correct semantics.
func (m *cliModel) restoreIterationsFromSnapshot(snapshot *protocol.ProgressEvent) {
	if len(snapshot.IterationHistory) == 0 {
		return
	}

	for _, ih := range snapshot.IterationHistory {
		// Per-iteration dedup: skip if we already have this iteration.
		already := slices.ContainsFunc(m.progressState.iterations, func(s cliIterationSnapshot) bool {
			return s.Iteration == ih.Iteration
		})
		if already {
			continue
		}
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
// For CLI sessions: handleAgentMessage is the authoritative end-of-turn
// signal (creates the final assistant message). PhaseDone from snapshot
// just snapshots the final iteration and marks turn state.
// For non-CLI sessions (agent viewer, feishu, web, etc.): the outbound
// message goes to the originating channel, not the CLI channel — no
// cliOutboundMsg ever arrives. We create the assistant message here from
// the snapshot's Content (carried via structured progress events).
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
				// Include ALL active tools — the iteration is ending, so all
				// running/pending tools have completed. Mark them as done
				// (the done event may have been lost in progressSlot coalescing).
				for _, t := range cur.ActiveTools {
					if t.Status == "running" || t.Status == "pending" || t.Status == "" {
						t.Status = "done"
					}
					finalTools = append(finalTools, t)
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
		// If already snapshotted (from DB IterationHistory via restoreIterationsFromSnapshot),
		// the data is authoritative and complete — no update needed.
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

	// Non-CLI sessions (agent viewer, feishu, web, etc.): the outbound
	// message goes to the originating channel, not the CLI channel.
	// Create assistant message from snapshot/progress content.
	if m.channelName != "cli" && !m.typing {
		assistantContent := snapshot.Content
		if assistantContent == "" && cur != nil {
			assistantContent = cur.Content
		}
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
			} else if cur != nil && cur.Reasoning != "" {
				asstMsg.reasoning = cur.Reasoning
			}
			// Find existing or append new — single creation point for agent sessions.
			// Guard: never create two consecutive assistant messages. If the last
			// message is already an assistant, update it instead of appending.
			existingIdx := m.findMessageByTurn(turnID, "assistant")
			if existingIdx >= 0 {
				m.messages[existingIdx] = asstMsg
			} else if len(m.messages) > 0 && m.messages[len(m.messages)-1].role == "assistant" {
				m.messages[len(m.messages)-1] = asstMsg
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
