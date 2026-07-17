package cli

import (
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
		log.Glob(log.CatTUI).WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID")
		return
	}
	currentKey := qualifyChatID(m.channelName, m.chatID)
	if msg.payload.ChatID != currentKey {
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
