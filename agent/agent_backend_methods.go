package agent

import (
	"fmt"
	"os"

	"xbot/protocol"
)

// SetCWD sets the current working directory for a session.
// It refreshes plugin workDir with the correct tenantID.
func (a *Agent) SetCWD(ch, chatID, dir string) error {
	if a.sandboxMode != "none" {
		return fmt.Errorf("CWD sync not supported in %s sandbox mode", a.sandboxMode)
	}
	if a.MultiSession() == nil {
		return ErrNoSessionManager
	}
	sess, err := a.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		return err
	}
	// Set CWD — but only for brand new sessions with no persisted CWD.
	// On restart, loadPersistedCWD restores the user's last CWD (which may differ
	// from the terminal dir if the user used the Cd tool). We must not overwrite it.
	// Also handles the edge case where the persisted directory no longer exists
	// (e.g. deleted between runs) by falling back to the terminal CWD.
	existingCWD := sess.GetCurrentDir()
	if existingCWD == "" {
		sess.SetCurrentDir(dir)
	} else if _, err := os.Stat(existingCWD); os.IsNotExist(err) {
		// Persisted CWD is stale (directory removed), fall back to terminal CWD
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
	_, found := a.chatCancelCh.Load(key)
	return found
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
		todos := a.GetTodos(ch, chatID)
		if len(todos) == 0 {
			return nil
		}
		return &protocol.ProgressEvent{
			Phase: "done",
			Todos: todos,
		}
	}
	snapshot := v.(*protocol.ProgressEvent)
	result := *snapshot

	// Merge live stream state (updated by stream callbacks between structured events).
	// This is the pull-model replacement for stream event push — the client reads
	// live streaming content via tick pull instead of receiving push events.
	a.mergeStreamState(key, &result)

	// Agent sessions: correct Phase from authoritative running state.
	// interactiveSubAgents stores entries keyed by interactiveKey (no "agent:" prefix),
	// so we look up with chatID directly. When running=true but Phase="done"
	// (between iterations), correct Phase from iteration history.
	if ch == "agent" {
		if entry, loaded := a.interactiveSubAgents.Load(chatID); loaded {
			ia := entry.(*interactiveAgent)
			ia.mu.Lock()
			isRunning := ia.running
			ia.mu.Unlock()
			if isRunning && result.Phase == "done" {
				corrected := false
				if histPtr, ok := a.iterationHistories.Load(key); ok {
					hist := *histPtr.(*[]protocol.ProgressEvent)
					for i := len(hist) - 1; i >= 0; i-- {
						if hist[i].Phase != "done" {
							result.Phase = hist[i].Phase
							corrected = true
							break
						}
					}
				}
				if !corrected {
					result.Phase = "running"
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
		result[i] = protocol.TodoItem{ID: t.ID, Text: t.Text, Done: t.Done}
	}
	return result
}
