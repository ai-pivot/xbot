package cli

import (
	"strings"
	"time"

	"xbot/protocol"
)

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

// finalizeStaleStreamingBeforeNewUserMessage converts a completed previous
// turn's streaming placeholder into a normal history message before appending
// a new user message. PhaseDone intentionally keeps streamingMsgIdx/progress
// alive until the final assistant reply arrives, but sendMessage refreshes the
// viewport before startAgentTurn. Without this handoff, that refresh can render
// the previous turn's live stream below the new user message.
func (m *cliModel) finalizeStaleStreamingBeforeNewUserMessage() {
	if m.typing || m.streamingMsgIdx < 0 || m.streamingMsgIdx >= len(m.messages) {
		return
	}
	msg := &m.messages[m.streamingMsgIdx]
	if msg.role != "assistant" || !msg.isPartial {
		m.streamingMsgIdx = -1
		return
	}
	if len(msg.iterations) == 0 && len(m.progressState.iterations) > 0 {
		msg.iterations = make([]cliIterationSnapshot, len(m.progressState.iterations))
		copy(msg.iterations, m.progressState.iterations)
	}
	msg.isPartial = false
	msg.dirty = true
	m.streamingMsgIdx = -1
	m.progressState.current = nil
	m.progressState.iterations = nil
	m.rc.invalidateProgress()
}

// startAgentTurn transitions the model into the "agent processing" state:
// sets typing=true, updates placeholder, disables input, resets progress,
// and queues a tick command to ensure the spinner/progress chain starts.
// This is the SINGLE source of truth for tick chain initiation — no other
// code path should emit tickCmd() on idle→typing transition.
func (m *cliModel) startAgentTurn() {
	// Finalize any stale streaming message from the previous turn BEFORE
	// creating a new one. endAgentTurn preserves streamingMsgIdx for
	// flicker-free rendering, so the old streaming assistant may still be
	// isPartial=true. Without this call, startAgentTurn appends a new
	// streaming assistant directly after the old one — producing two
	// adjacent Assistant blocks that dedupConsecutiveAssistants cannot
	// merge (different turnIDs, old has content/iterations).
	// Callers that already called finalizeStaleStreamingBeforeNewUserMessage
	// (sendMessage, sendToAgent) hit the early return inside (streamingMsgIdx=-1).
	m.finalizeStaleStreamingBeforeNewUserMessage()

	m.agentTurnID++
	m.typing = true
	m.replyProcessed = false
	// Do NOT clear turnCancelled here — it must persist across turn boundaries
	// to block stale PhaseDone/tool_summary from a cancelled turn. It is cleared
	// when the new turn's first non-PhaseDone progress arrives (handleProgressMsg)
	// or by endAgentTurn for the matching turnID (normal cancel completion path).

	m.turnAutoStarted = false
	m.turnStartedProcessed = false

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
	// Show initial progress so the user sees immediate feedback (spinner)
	// without waiting for the first progress_structured event.
	// MUST be called AFTER resetProgressState — resetProgressState clears
	// progressState.current to nil, which causes updateStreamingOnly to
	// fall back to renderMessage (empty content, no pulse). With a non-nil
	// "thinking" state, liveIterationBlocks renders a pulse spinner on the
	// very first tick — immediate visual feedback.
	m.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 0,
	}
	m.rc.valid = false
	// Create an empty streaming assistant message at turn start.
	// This allows all progress/iteration data to be rendered inline
	// from the very beginning, eliminating the need for a separate
	// progress panel fallback.
	// Note: the rendering-level guard (appendNewMessagesToCache/fullRebuild)
	// handles merging duplicate or stale assistants — no removal needed here.
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
	// or invalidateProgress() here.
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
