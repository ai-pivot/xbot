package cli

import (
	"time"

	"xbot/protocol"

	log "xbot/logger"
)

// handleProgressMsg processes progress update events from the agent.
//
// Two types of events:
//  1. Stream-only (Phase=="", Iteration==0): low-latency stream display updates
//     (content, reasoning, tool calls, token usage). Server pushes these at max
//     5/sec (200ms throttle). These update stream fields on current state directly.
//  2. Structured (Phase!="", Iteration>0): iteration transitions, PhaseDone, todos.
//     These go through applyProgressSnapshot for authoritative state update.
//
// Seq monotonic guard: stream events use lastStreamSeq (separate from
// lastAppliedSeq) so high-frequency stream events don't block structured events.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	if msg.payload == nil {
		return
	}

	// Session filter
	if msg.payload.ChatID == "" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID")
		return
	}
	currentKey := qualifyChatID(m.channelName, m.chatID)
	if msg.payload.ChatID != currentKey {
		return
	}

	// turn_started: a new agent turn is beginning. This replaces the old
	// InjectUserMessage side-channel. The event carries the TurnID + trigger
	// type + (for notifications) the user message content. Handle it before
	// the cancel guard — turn_started announces a NEW turn, so any stale
	// cancel flag from the previous turn should be cleared, not block it.
	if msg.payload.Phase == "turn_started" {
		m.handleTurnStarted(msg.payload)
		return
	}

	// Cancel guard: ignore progress after Ctrl+C (except PhaseDone).
	if m.turnCancelled && msg.payload.Phase != "done" {
		return
	}

	// New turn's first non-PhaseDone progress clears the cancel flag.
	if m.turnCancelled && msg.payload.Phase != "done" && msg.payload.Phase != "" && m.typing {
		m.turnCancelled = false
	}

	// Classify event type.
	isStreamOnly := msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "" ||
			len(msg.payload.StreamingTools) > 0 || msg.payload.StreamTokens > 0)

	if isStreamOnly {
		// Stale stream event guard (separate counter — don't block structured events).
		if msg.payload.Seq > 0 && msg.payload.Seq <= m.progressState.lastStreamSeq {
			return
		}
		if msg.payload.Seq > 0 {
			m.progressState.lastStreamSeq = msg.payload.Seq
		}

		if m.progressState.current != nil {
			cur := m.progressState.current
			if msg.payload.StreamContent != "" {
				cur.StreamContent = msg.payload.StreamContent
			}
			if msg.payload.ReasoningStreamContent != "" {
				cur.ReasoningStreamContent = msg.payload.ReasoningStreamContent
			}
			if len(msg.payload.StreamingTools) > 0 {
				cur.StreamingTools = msg.payload.StreamingTools
			}
			if msg.payload.StreamTokens > 0 {
				cur.StreamTokens = msg.payload.StreamTokens
			}
		} else if m.typing {
			// Turn started but no structured progress yet — create minimal state.
			m.progressState.current = msg.payload
		}
		if msg.payload.TokenUsage != nil {
			m.cacheTokenUsage(msg.payload.TokenUsage)
		}
		m.updateViewportContent()
		return
	}

	// Structured event or PhaseDone: apply through snapshot pipeline.
	// Seq gap detection: if Seq jumps by more than 1, we missed events
	// (sendCh full → push dropped). Set gapDetected so the next tick
	// immediately pulls a snapshot instead of waiting 30s.
	// Skip gap detection for Seq=0 (local/agent events without Seq) or
	// when this is the first event of the turn (lastReceivedSeq=0).
	if msg.payload.Seq > 0 && m.progressState.lastReceivedSeq > 0 {
		if msg.payload.Seq > m.progressState.lastReceivedSeq+1 {
			m.progressState.gapDetected = true
		}
	}
	if msg.payload.Seq > 0 && msg.payload.Seq > m.progressState.lastReceivedSeq {
		m.progressState.lastReceivedSeq = msg.payload.Seq
	}
	if msg.payload.Seq > 0 {
		m.progressState.lastSeq = msg.payload.Seq
	}
	m.applyProgressSnapshot(msg.payload)
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
			return
		}

		// Change detection: skip if todos haven't actually changed.
		if todosEqual(m.todos, payload.Todos) {
			return
		}

		countChanged := len(m.todos) != len(payload.Todos)
		m.todos = make([]protocol.TodoItem, len(payload.Todos))
		copy(m.todos, payload.Todos)
		m.todosDoneCleared = false

		if countChanged {
			m.relayoutViewport()
		}
		m.persistTodosToManager()
	}
}

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
	items := make([]protocol.TodoItem, len(m.todos))
	copy(items, m.todos)
	m.todoManager.SetTodos(key, items)
}

// handleTurnStarted processes a turn_started progress event. This replaces the
// old InjectUserMessage side-channel: the notification user message is now
// delivered atomically with the TurnID through the unified progress stream.
//
// Three cases:
//  1. Idle (!m.typing): a notification/resume/cron triggered a new turn.
//     Display the user message (if notification) and start the turn.
//  2. Typing, first turn_started for this turn (!turnStartedProcessed):
//     The user typed a message; sendMessage already created the user message
//     + streaming slot. Just adopt the backend TurnID.
//  3. Typing, turn_started already processed (turnStartedProcessed):
//     Cross-goroutine race — turn_started for turn N+1 arrived before turn
//     N's reply. Finalize turn N as-is (streamed content already visible),
//     then start turn N+1.
func (m *cliModel) handleTurnStarted(ev *protocol.ProgressEvent) {
	// suLoading guard: during session switch in remote mode, discard.
	if m.splashState.suLoading {
		return
	}

	turnID := ev.TurnID
	ts := ev.TurnStart

	// ── Consistency check: TurnID must be strictly monotonic per session ──
	if m.lastReceivedTurnID > 0 {
		if turnID <= m.lastReceivedTurnID {
			log.WithFields(log.Fields{
				"prev_turn_id": m.lastReceivedTurnID,
				"new_turn_id":  turnID,
				"delta":        int64(turnID) - int64(m.lastReceivedTurnID),
				"chat_id":      m.chatID,
				"typing":       m.typing,
				"trigger":      triggerString(ts),
			}).Error("TURN_ID_INVARIANT_VIOLATION (TUI): TurnID must be strictly increasing")
		} else if turnID != m.lastReceivedTurnID+1 {
			log.WithFields(log.Fields{
				"prev_turn_id": m.lastReceivedTurnID,
				"new_turn_id":  turnID,
				"gap":          turnID - m.lastReceivedTurnID - 1,
				"chat_id":      m.chatID,
			}).Warn("TURN_ID_GAP (TUI): TurnID jumped — intermediate turn(s) may have been lost")
		}
	}
	m.lastReceivedTurnID = turnID

	// Case 3: cross-goroutine race — finalize the previous turn before
	// starting the new one. The streamed content is already visible.
	if m.typing && m.turnStartedProcessed {
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			prevTurnID := m.messages[m.streamingMsgIdx].turnID
			if prevTurnID != 0 && prevTurnID != turnID {
				m.endAgentTurn(prevTurnID)
			}
		}
	}

	// Determine whether we need to start a new turn (idle or just finalized).
	needNewTurn := !m.typing

	if needNewTurn {
		// Display the notification/resume user message before starting the turn.
		if ts != nil && ts.Content != "" &&
			(ts.Trigger == "notification" || ts.Trigger == "resume") {
			m.messages = append(m.messages, cliMessage{
				role:           "user",
				content:        ts.Content,
				timestamp:      time.Now(),
				dirty:          true,
				turnID:         turnID,
				isNotification: ts.Trigger == "notification",
			})
			m.updateViewportContent()
			m.viewport.GotoBottom()
			m.newContentHint = false
			m.userScrolledUp = false
		}
		// Start the turn (creates streaming slot with local counter).
		m.startAgentTurn()
		// Adopt the backend TurnID — override the local counter.
		m.agentTurnID = turnID
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			m.messages[m.streamingMsgIdx].turnID = turnID
		}
		if m.checkpointState != nil {
			m.checkpointState.SetTurnIdx(int(turnID))
		}
		// Tick chain is global (handleTickDrain sends cliTickMsg every 100ms
		// regardless of state) — no explicit start needed.
	} else {
		// Case 2: user-typed turn — adopt the backend TurnID on the
		// existing streaming slot created by sendMessage/startAgentTurn.
		m.agentTurnID = turnID
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			m.messages[m.streamingMsgIdx].turnID = turnID
		}
	}

	m.turnStartedProcessed = true
	// A new turn has started — clear stale cancel flag from the previous turn.
	m.turnCancelled = false

	// Refresh bg task / agent counts (notification may have changed them).
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}

	m.rc.valid = false
	log.WithFields(log.Fields{"turn_id": turnID, "trigger": triggerString(ts)}).Debug("handleTurnStarted: turn adopted")
}

// triggerString extracts the trigger string for logging.
func triggerString(ts *protocol.TurnStartInfo) string {
	if ts == nil {
		return "unknown"
	}
	return ts.Trigger
}
