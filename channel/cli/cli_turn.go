package cli

import (
	"strings"
	"time"

	log "xbot/logger"
	"xbot/protocol"
)

// dedupMessagesGuard is the algorithmic guarantee layer against duplicate
// message rendering. It enforces the invariant that for any (turnID, role)
// pair where turnID > 0, there is AT MOST ONE message in m.messages.
//
// This guard runs at the TOP of updateViewportContent(), before any rendering.
// It uses O(n) map-based identity detection — NOT string matching. Even if a
// race condition or unguarded append path creates a duplicate, it is silently
// purged here before reaching the viewport.
//
// Design rationale: the guard is idempotent and side-effect-free when no
// duplicates exist (the common case). When duplicates ARE found, the LAST
// occurrence is kept (it has the most up-to-date content from upsert), and
// earlier zombies are removed. The streamingMsgIdx is adjusted if it pointed
// to a purged message.
func (m *cliModel) dedupMessagesGuard() {
	if len(m.messages) < 2 {
		return
	}
	// Track last seen index for each (turnID, role) identity.
	// turnID=0 is excluded (system messages, injected user messages that
	// legitimately can share turnID=0).
	type identity struct {
		turnID uint64
		role   string
	}
	lastIdx := make(map[identity]int, len(m.messages))
	counts := make(map[identity]int, len(m.messages))
	for i := range m.messages {
		if m.messages[i].turnID == 0 {
			continue
		}
		id := identity{m.messages[i].turnID, m.messages[i].role}
		lastIdx[id] = i
		counts[id]++
	}
	if len(lastIdx) == 0 {
		return
	}
	// Check if any duplicates exist (any identity with count > 1).
	hasDup := false
	for _, c := range counts {
		if c > 1 {
			hasDup = true
			break
		}
	}
	if !hasDup {
		return
	}
	// Purge zombies: keep only the last occurrence of each identity.
	filtered := m.messages[:0]
	purgeCount := 0
	for i := range m.messages {
		if m.messages[i].turnID == 0 {
			filtered = append(filtered, m.messages[i])
			continue
		}
		id := identity{m.messages[i].turnID, m.messages[i].role}
		if lastIdx[id] == i {
			filtered = append(filtered, m.messages[i])
		} else {
			purgeCount++
		}
	}
	if purgeCount > 0 {
		log.WithFields(log.Fields{
			"purged": purgeCount,
			"before": len(m.messages),
			"after":  len(filtered),
		}).Warn("dedupMessagesGuard: purged duplicate messages before render")
		m.messages = filtered
		// Fix streamingMsgIdx if it shifted due to compaction.
		m.fixStreamingMsgIdx()
		// Invalidate cache since message indices changed.
		m.rc.valid = false
		m.rc.bumpHistGen()
	}
}

// fixStreamingMsgIdx adjusts streamingMsgIdx after message list compaction.
// It searches for the streaming message by turnID+isPartial identity.
// fixStreamingMsgIdx adjusts streamingMsgIdx after message list compaction.
// It searches for the streaming message by turnID+isPartial identity.
func (m *cliModel) fixStreamingMsgIdx() {
	if m.streamingMsgIdx < 0 {
		return
	}
	if m.streamingMsgIdx >= len(m.messages) {
		// Index out of range — try to find by turnID
		m.streamingMsgIdx = -1
		return
	}
	// Verify the message at streamingMsgIdx is still the streaming message.
	// If messages were compacted, it may have shifted.
	if m.messages[m.streamingMsgIdx].isPartial {
		return // still valid
	}
	// Search for the streaming message by isPartial flag.
	for i := range m.messages {
		if m.messages[i].isPartial {
			m.streamingMsgIdx = i
			return
		}
	}
	m.streamingMsgIdx = -1
}

// toggleToolSummary toggles the tool-summary expanded state,
// invalidates all cached rendering, clears cachedHistory, and refreshes the viewport.
// It preserves the viewport scroll position anchored to the first visible message,
// so Ctrl+O doesn't cause a jarring jump when tool summary lines change.
// toggleToolSummary toggles the tool-summary expanded state,
// invalidates all cached rendering, clears cachedHistory, and refreshes the viewport.
// It preserves the viewport scroll position anchored to the first visible message,
// so Ctrl+O doesn't cause a jarring jump when tool summary lines change.
func (m *cliModel) toggleToolSummary() {
	// Find the first visible message index before toggling.
	prevYOffset := m.viewport.YOffset()
	prevAtBottom := m.viewport.AtBottom()
	anchorMsgIdx := -1
	if !prevAtBottom && len(m.msgLineOffsets) > 0 {
		for i := len(m.msgLineOffsets) - 1; i >= 0; i-- {
			if m.msgLineOffsets[i] <= prevYOffset {
				anchorMsgIdx = i
				break
			}
		}
	}

	m.toolSummaryExpanded = !m.toolSummaryExpanded
	m.rc.history = ""
	m.invalidateAllCache(true)

	// Restore scroll position anchored to the same message.
	if !prevAtBottom && anchorMsgIdx >= 0 && anchorMsgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[anchorMsgIdx])
	}
}

// openSettingsFromQuickSwitch restores the settings panel after a subscription quick switch.
// The subscription generation guard (in onSubmit) prevents stale LLM fields from being
// written back. Here we only need to refresh LLM display values from the new active
// subscription and preserve global settings from the backup.
// startAgentTurn transitions the model into the "agent processing" state:
// sets typing=true, updates placeholder, disables input, resets progress,
// and queues a tick command to ensure the spinner/progress chain starts.
// This is the SINGLE source of truth for tick chain initiation — no other
// code path should emit tickCmd() on idle→typing transition.
func (m *cliModel) startAgentTurn() {
	m.agentTurnID++
	m.typing = true
	// Do NOT clear turnCancelled here — it must persist across turn boundaries
	// to block stale PhaseDone/tool_summary from a cancelled turn. It is cleared
	// when the new turn's first non-PhaseDone progress arrives (handleProgressMsg)
	// or by endAgentTurn for the matching turnID (normal cancel completion path).

	// Initialize turnDoneFlags for the new turn.
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	m.turnDoneFlags[m.agentTurnID] = &turnDoneFlag{}
	m.turnAutoStarted = false

	// Clean up old turn entries (keep last 3 for late-arrival safety).
	for id := range m.turnDoneFlags {
		if id+3 < m.agentTurnID {
			delete(m.turnDoneFlags, id)
		}
	}

	// Show initial progress so the user sees immediate feedback (spinner)
	// without waiting for the first progress_structured event.
	if m.progressState.current == nil {
		m.progressState.current = &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: 0,
		}
		m.rc.valid = false
	}
	// NOTE: Callers are responsible for ensuring the tick chain starts:
	//   - Inside Bubble Tea Update: return tickCmd() in the cmd chain
	//   - Outside Update (callbacks): append to m.pendingCmds before calling
	// Sync checkpoint state turn index
	if m.checkpointState != nil {
		m.checkpointState.SetTurnIdx(int(m.agentTurnID))
	}
	// Clear rewind result when new turn starts
	m.rewindResult = nil
	m.updatePlaceholder()
	m.inputReady = false
	m.resetProgressState()
	// Create an empty streaming assistant message at turn start.
	// This allows all progress/iteration data to be rendered inline
	// from the very beginning, eliminating the need for a separate
	// progress panel fallback.
	m.messages = append(m.messages, cliMessage{
		role:      "assistant",
		content:   "",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    m.agentTurnID,
	})
	m.streamingMsgIdx = len(m.messages) - 1
}

// removeLastToolSummary removes only the LAST tool_summary message from m.messages.
//
// When the agent turn is active, ch.ConvertMessagesToHistory produces a tool_summary
// from intermediate assistant messages of the in-progress turn. The progress
// block (m.progressState.current + m.progressState.iterations) owns iteration display for the active
// turn — the static tool_summary from ch.ConvertMessagesToHistory would duplicate
// content with mismatched (globally-cumulative vs per-turn) iteration numbers.
//
// Only the LAST tool_summary is removed. Previous turns' tool_summaries are
// preserved — those have no live progress panel to replace them.
// Earlier tool_summaries in the active turn are also preserved as fallback:
// if IterationHistory is empty (e.g. reconnect before RPC snapshot arrives),
// the tool_summary rendering is better than showing nothing at all.
// removeLastToolSummary removes only the LAST tool_summary message from m.messages.
//
// When the agent turn is active, ch.ConvertMessagesToHistory produces a tool_summary
// from intermediate assistant messages of the in-progress turn. The progress
// block (m.progressState.current + m.progressState.iterations) owns iteration display for the active
// turn — the static tool_summary from ch.ConvertMessagesToHistory would duplicate
// content with mismatched (globally-cumulative vs per-turn) iteration numbers.
//
// Only the LAST tool_summary is removed. Previous turns' tool_summaries are
// preserved — those have no live progress panel to replace them.
// Earlier tool_summaries in the active turn are also preserved as fallback:
// if IterationHistory is empty (e.g. reconnect before RPC snapshot arrives),
// the tool_summary rendering is better than showing nothing at all.
func (m *cliModel) removeLastToolSummary() {
	// Find the last tool_summary message (closest to end of messages).
	lastIdx := -1
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role == "tool_summary" {
			lastIdx = i
			break
		}
	}
	if lastIdx < 0 {
		return
	}
	// Guard: only remove if the tool_summary belongs to the current active turn.
	// If there is a user message AFTER the last tool_summary, the tool_summary
	// belongs to a previous turn (e.g. a Ctrl+C interrupted turn) and must be
	// preserved — removing it would erase iteration history that the active
	// progress block does NOT replace.
	for i := lastIdx + 1; i < len(m.messages); i++ {
		if m.messages[i].role == "user" {
			return // tool_summary belongs to a prior turn — do not remove
		}
	}
	m.messages = append(m.messages[:lastIdx], m.messages[lastIdx+1:]...)
	m.rc.valid = false
}

// endAgentTurn resets all agent-turn tracking state and returns to idle.
// Takes the turnID that triggered this end. If a new turn has already
// started (turnID != m.agentTurnID), the call is a no-op — this prevents
// stale completion signals (cliOutboundMsg / PhaseDone) from killing a
// new turn's animation.
// endAgentTurn resets all agent-turn tracking state and returns to idle.
// Takes the turnID that triggered this end. If a new turn has already
// started (turnID != m.agentTurnID), the call is a no-op — this prevents
// stale completion signals (cliOutboundMsg / PhaseDone) from killing a
// new turn's animation.
func (m *cliModel) endAgentTurn(turnID uint64) {
	if turnID != m.agentTurnID {
		return // new turn already started — stale signal, ignore
	}
	// Persist token usage for ready-status bar before clearing progress
	if m.progressState.current != nil {
		m.cacheTokenUsage(m.progressState.current.TokenUsage)
	}

	// --- relayoutViewport BEFORE clearing progress state ---
	// This ensures updateStreamingOnly renders the turn's final state
	// (all completed iterations + live content) rather than an empty shell.
	// streamingMsgIdx is still valid here → updateStreamingOnly path.
	m.relayoutViewport()

	// --- Preserve progress state for flicker-free rendering ---
	// DO NOT clear progressState.iterations, progressState.current,
	// reasoningByIter, lastReasoning, or invalidateProgress() here.
	// These are needed by updateStreamingOnly to render the turn's final
	// state between PhaseDone and handleAgentMessage. Clearing them causes
	// updateStreamingOnly to render an empty progress block, then
	// appendNewMessagesToCache renders the message with stale content —
	// resulting in a visible flicker (two viewport content changes).
	//
	// Progress state is cleared by:
	// - handleAgentMessage: after the reply is processed (via startAgentTurn
	//   for the next turn, or explicitly when the turn is fully done)
	// - startAgentTurn → resetProgressState: when a new turn begins
	// - /clear, session switch: full state reset
	m.lastCompletedTools = nil
	m.typingStartTime = time.Time{}
	m.progressState.twVisible = 0
	m.progressState.rwVisible = 0
	m.typing = false
	m.progressState.twActive = false
	// Clear pending user message: the turn completed, so the user's message
	// has been persisted to DB. Keeping it set would cause handleHistoryReload
	// (after /compress) to restore the stale message from a pre-compress turn.
	m.pendingUserMsg = nil
	// Do NOT set turnCancelled here — this is normal turn completion,
	// not a user cancel. Setting turnCancelled=true here prevents
	// the next turn (from message queue flush) from receiving progress
	// events, causing Issue #30: queue-flushed messages appear idle.
	m.turnCancelled = false
	// Collapse todos on turn end. If all done, fully clear.
	// Otherwise restore unfinished todos from TodoManager so they
	// persist across turns and are visible in idle state.
	if m.todoManager != nil {
		key := m.sessionKey()
		if items := m.todoManager.GetTodos(key); len(items) > 0 {
			allDone := true
			for _, t := range items {
				if !t.Done {
					allDone = false
					break
				}
			}
			if !allDone {
				m.todos = make([]protocol.TodoItem, len(items))
				copy(m.todos, items)
				m.todosDoneCleared = false
			} else {
				// All todos done — clear display, underlying TodoManager,
				// AND disk file so they don't resurrect on next TUI restart.
				m.todos = nil
				m.todosDoneCleared = true
				m.todoManager.SetTodos(key, nil)
				_ = m.todoManager.SaveToFile(key)
			}
		} else {
			m.todos = nil
			m.todosDoneCleared = false
		}
	} else {
		m.todos = nil
		m.todosDoneCleared = false
	}
	// DO NOT clear streamingMsgIdx here. Keeping it valid ensures the tick
	// handler uses updateStreamingOnly (streaming path) instead of falling
	// through to appendNewMessagesToCache, which would cache the streaming
	// message with incomplete content (reply hasn't arrived yet). That causes
	// a double-flicker: once when the partial content is cached, again when
	// handleAgentMessage re-renders with the final content.
	//
	// The turnID guard at the top of endAgentTurn already prevents stale
	// turns from interfering. handleAgentMessage will set streamingMsgIdx=-1
	// and call rerenderCachedMessage for a single clean transition.
	//
	// If handleAgentMessage never arrives (error/cancel path), the cancel ack
	// or the next startAgentTurn will reset streamingMsgIdx.
	// Refresh agent count so the tick chain continues if agents exist
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	m.updatePlaceholder()
}

// --- Deterministic rendering helpers ---

// getTurnFlag returns the turnDoneFlag for the given turn, or nil if not tracked.
// getTurnFlag returns the turnDoneFlag for the given turn, or nil if not tracked.
func (m *cliModel) getTurnFlag(turnID uint64) *turnDoneFlag {
	if m.turnDoneFlags == nil {
		return nil
	}
	return m.turnDoneFlags[turnID]
}

// isTurnDoneProcessed returns true if handleProgressDone has already processed
// the given turn (created tool_summary and ended the turn).
// isTurnDoneProcessed returns true if handleProgressDone has already processed
// the given turn (created tool_summary and ended the turn).
func (m *cliModel) isTurnDoneProcessed(turnID uint64) bool {
	f := m.getTurnFlag(turnID)
	return f != nil && f.doneProcessed
}

// isTurnReplyReceived returns true if handleAgentMessage has already received
// the assistant reply for the given turn.
// isTurnReplyReceived returns true if handleAgentMessage has already received
// the assistant reply for the given turn.
func (m *cliModel) isTurnReplyReceived(turnID uint64) bool {
	f := m.getTurnFlag(turnID)
	return f != nil && f.replyReceived
}

// setTurnDoneProcessed marks the turn as having been processed by handleProgressDone.
// setTurnDoneProcessed marks the turn as having been processed by handleProgressDone.
func (m *cliModel) setTurnDoneProcessed(turnID uint64) {
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	f, ok := m.turnDoneFlags[turnID]
	if !ok {
		f = &turnDoneFlag{}
		m.turnDoneFlags[turnID] = f
	}
	f.doneProcessed = true
	f.doneTime = time.Now()
}

// setTurnReplyReceived marks the turn as having received the assistant reply.
// setTurnReplyReceived marks the turn as having received the assistant reply.
func (m *cliModel) setTurnReplyReceived(turnID uint64) {
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	f, ok := m.turnDoneFlags[turnID]
	if !ok {
		f = &turnDoneFlag{}
		m.turnDoneFlags[turnID] = f
	}
	f.replyReceived = true
}

// findMessageByTurn finds the index of the last message with the given turnID and role.
// Returns -1 if not found.
// findMessageByTurn finds the index of the last message with the given turnID and role.
// Returns -1 if not found.
func (m *cliModel) findMessageByTurn(turnID uint64, role string) int {
	// Search from end — the most recent message is the most likely match.
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].turnID == turnID && m.messages[i].role == role {
			return i
		}
	}
	return -1
}

// upsertMessageByTurn finds an existing message with the given turnID+role and
// updates it in-place. If not found, appends the message. Returns the final index.
// This is the core mechanism for deterministic rendering: duplicate events update
// existing slots instead of creating new messages.
//
// Algorithmic dedup guarantee: after this call, there is AT MOST ONE message
// with the given (turnID, role) pair. If multiple zombies existed (created by
// race conditions or fallback paths), they are all purged except the updated one.
// upsertMessageByTurn finds an existing message with the given turnID+role and
// updates it in-place. If not found, appends the message. Returns the final index.
// This is the core mechanism for deterministic rendering: duplicate events update
// existing slots instead of creating new messages.
//
// Algorithmic dedup guarantee: after this call, there is AT MOST ONE message
// with the given (turnID, role) pair. If multiple zombies existed (created by
// race conditions or fallback paths), they are all purged except the updated one.
func (m *cliModel) upsertMessageByTurn(turnID uint64, role string, msg cliMessage) int {
	idx := m.findMessageByTurn(turnID, role)
	if idx >= 0 {
		// Update in-place: preserve position in the message list.
		m.messages[idx] = msg
		m.messages[idx].turnID = turnID
		// Purge zombie duplicates: any OTHER messages with the same turnID+role
		// at different indices. These can be created by fallback paths
		// (e.g. isPartial fallback in handleAgentMessage creating a second
		// streaming message for the same turn). Without purge, both would
		// be rendered, causing visual duplication.
		m.purgeZombieMessages(turnID, role, idx)
		return idx
	}
	// Not found: append at end.
	msg.turnID = turnID
	m.messages = append(m.messages, msg)
	return len(m.messages) - 1
}

// purgeZombieMessages removes all messages with the given turnID+role EXCEPT
// the one at keepIdx. This is an O(n) sweep but only runs when a duplicate
// is detected (rare). It guarantees structural uniqueness of (turnID, role).
// purgeZombieMessages removes all messages with the given turnID+role EXCEPT
// the one at keepIdx. This is an O(n) sweep but only runs when a duplicate
// is detected (rare). It guarantees structural uniqueness of (turnID, role).
func (m *cliModel) purgeZombieMessages(turnID uint64, role string, keepIdx int) {
	if len(m.messages) <= 1 {
		return
	}
	filtered := m.messages[:0] // compact in-place
	for i := range m.messages {
		if i != keepIdx && m.messages[i].turnID == turnID && m.messages[i].role == role {
			continue // purge zombie
		}
		filtered = append(filtered, m.messages[i])
	}
	if len(filtered) != len(m.messages) {
		log.WithFields(log.Fields{
			"turnID": turnID, "role": role,
			"purged": len(m.messages) - len(filtered),
		}).Debug("purgeZombieMessages: removed duplicate messages")
		m.messages = filtered
	}
}

// removeMessageByTurn removes the last message with the given turnID+role.
// Returns true if a message was removed.
// flushMessageQueue sends the first queued message (if any) when input becomes ready.
// Returns a tea.Cmd to send the message, or nil if queue is empty.
// removeMessageByTurn removes the last message with the given turnID+role.
// Returns true if a message was removed.
// flushMessageQueue sends the first queued message (if any) when input becomes ready.
// Returns a tea.Cmd to send the message, or nil if queue is empty.
// insertUserMessageBeforeStreaming inserts a user message at the position
// immediately before the streaming message. Used when handleInjectedUserMsg
// claims an auto-started turn (progress auto-start created the streaming
// message before the user message arrived via asyncCh).
func (m *cliModel) insertUserMessageBeforeStreaming(content string) {
	userMsg := cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	}
	idx := m.streamingMsgIdx
	if idx < 0 || idx >= len(m.messages) {
		// No streaming message — just append
		m.messages = append(m.messages, userMsg)
		return
	}
	// Insert before streaming message
	m.messages = append(m.messages, cliMessage{}) // grow
	copy(m.messages[idx+1:], m.messages[idx:])    // shift right
	m.messages[idx] = userMsg
	m.streamingMsgIdx++
}

func (m *cliModel) flushMessageQueue() {
	if len(m.messageQueue) == 0 {
		return
	}
	// Only flush messages queued for the current session.
	// If user queued a message in main session and switched to a SubAgent session,
	// skip until we're back in the correct session.
	msg := m.messageQueue[0]
	if msg.chatID != m.chatID {
		return // wrong session, wait for the correct one
	}
	m.messageQueue = m.messageQueue[1:]
	m.queueEditing = false
	m.queueEditBuf = ""
	// Put message into textarea and trigger send
	m.textarea.SetValue(msg.content)
	m.sendMessageFromQueue()
}

// sendMessageFromQueue sends the current textarea content as a queued message.
// Does NOT return tickCmd() — startAgentTurn() inside sendMessage() handles that.
// sendMessageFromQueue sends the current textarea content as a queued message.
// Does NOT return tickCmd() — startAgentTurn() inside sendMessage() handles that.
func (m *cliModel) sendMessageFromQueue() {
	content := strings.TrimSpace(m.textarea.Value())
	if content == "" {
		return
	}
	m.textarea.Reset()
	m.autoExpandInput()
	m.sendMessage(content)
}

// applyThemeAndRebuild applies a theme change synchronously: sets the theme,
// rebuilds styles cache, glamour renderer, and marks all messages dirty.
// Uses setTheme() instead of ApplyTheme() to avoid sending on themeChangeCh,
// which would cause a redundant fullRebuild in the next Update cycle.
