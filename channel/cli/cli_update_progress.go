package cli

import (
	"xbot/protocol"

	log "xbot/logger"
)

// handleProgressMsg processes progress update events from the agent.
//
// SIMPLIFIED: All complex state management (mergeProgressState,
// snapshotIterationChange, handleProgressDone) has been replaced by
// applyProgressSnapshot in cli_pull.go, which is called from both
// here (push events) and the tick handler (100ms pull).
//
// Push events provide low-latency display updates; the tick pull
// provides consistency guarantee by reading the complete backend snapshot.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	if msg.payload == nil {
		return
	}

	// Session filter: only process progress for the currently viewed session.
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

	// Cancel guard: ignore progress after Ctrl+C (except PhaseDone).
	if m.turnCancelled && msg.payload.Phase != "done" {
		return
	}

	// New turn's first non-PhaseDone progress clears the cancel flag.
	if m.turnCancelled && msg.payload.Phase != "done" && msg.payload.Phase != "" && m.typing {
		m.turnCancelled = false
	}

	// PULL MODEL: stream-only push events no longer exist — backend writes
	// to atomic streamState, clients read via GetActiveProgress tick pull.
	// All progress events that arrive here are structured (Phase/Iteration).
	// Seq check within applyProgressSnapshot handles dedup.
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
