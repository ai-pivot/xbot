package agent

import (
	"fmt"
	"os"

	"xbot/config"
	"xbot/protocol"

	log "xbot/logger"
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

// SetModelTiers configures the LLM model tiers via LLMFactory.
func (a *Agent) SetModelTiers(cfg config.LLMConfig) error {
	a.llmFactory.SetModelTiers(cfg)
	return nil
}

// IsProcessingByChannel returns true if there is an active Run for the given channel:chatID.
func (a *Agent) IsProcessingByChannel(ch, chatID string) bool {
	key := ch + ":" + chatID
	_, found := a.chatCancelCh.Load(key)
	return found
}

// GetActiveProgress returns the latest progress snapshot for the given channel:chatID.
func (a *Agent) GetActiveProgress(ch, chatID string) *protocol.ProgressEvent {
	key := ch + ":" + chatID
	v, ok := a.lastProgressSnapshot.Load(key)
	if !ok {
		log.WithFields(log.Fields{
			"ch":     ch,
			"chatID": chatID,
			"key":    key,
		}).Info("DEBUG_SESSION GetActiveProgress: snapshot not found")
		return nil
	}
	snapshot := v.(*protocol.ProgressEvent)
	// Shallow copy to avoid data race: agent may update snapshot fields concurrently.
	result := *snapshot

	log.WithFields(log.Fields{
		"ch":             ch,
		"chatID":         chatID,
		"key":            key,
		"snapshotPhase":  result.Phase,
		"snapshotIter":   result.Iteration,
		"snapshotChatID": result.ChatID,
	}).Info("DEBUG_SESSION GetActiveProgress: snapshot loaded")

	// Agent sessions: correct Phase from authoritative running state.
	// interactiveSubAgents stores entries keyed by interactiveKey (e.g. "cli:/cwd/role:inst"),
	// which is chatID (no "agent:" prefix). Load with chatID directly.
	// When running=true but Phase="done" (between iterations), correct the Phase
	// so CLI correctly shows busy. Main sessions don't need this — their PhaseDone
	// means the turn truly ended. Oneshot SubAgents don't need this either — their
	// progress is embedded in the parent session's live engine state.
	if ch == "agent" {
		entry, loaded := a.interactiveSubAgents.Load(chatID)
		log.WithFields(log.Fields{
			"chatID":     chatID,
			"loaded":     loaded,
			"lookupWith": "chatID",
		}).Info("DEBUG_SESSION GetActiveProgress: interactiveSubAgents lookup (by chatID)")
		if loaded {
			ia := entry.(*interactiveAgent)
			ia.mu.Lock()
			isRunning := ia.running
			ia.mu.Unlock()
			log.WithFields(log.Fields{
				"chatID":      chatID,
				"isRunning":   isRunning,
				"phase":       result.Phase,
				"needCorrect": isRunning && result.Phase == "done",
			}).Info("DEBUG_SESSION GetActiveProgress: running state")
			if isRunning && result.Phase == "done" {
				// Agent still running between iterations. Restore last active Phase
				// from iteration history.
				corrected := false
				if histPtr, ok := a.iterationHistories.Load(key); ok {
					hist := *histPtr.(*[]protocol.ProgressEvent)
					log.WithFields(log.Fields{
						"key":     key,
						"histLen": len(hist),
					}).Info("DEBUG_SESSION GetActiveProgress: iteration history")
					for i := len(hist) - 1; i >= 0; i-- {
						if hist[i].Phase != "done" {
							result.Phase = hist[i].Phase
							corrected = true
							log.WithFields(log.Fields{
								"newPhase": result.Phase,
								"histIdx":  i,
							}).Info("DEBUG_SESSION GetActiveProgress: corrected Phase from history")
							break
						}
					}
				} else {
					log.Info("DEBUG_SESSION GetActiveProgress: no iteration history found")
				}
				if !corrected {
					result.Phase = "running"
					log.Info("DEBUG_SESSION GetActiveProgress: fallback to Phase=running")
				}
			}
		} else {
			// Also try with key (agent:cli:...) in case format differs
			entry2, loaded2 := a.interactiveSubAgents.Load(key)
			log.WithFields(log.Fields{
				"key":    key,
				"loaded": loaded2,
			}).Info("DEBUG_SESSION GetActiveProgress: interactiveSubAgents lookup (by key)")
			if loaded2 {
				_ = entry2
			}
			// Debug: dump all keys in interactiveSubAgents
			a.interactiveSubAgents.Range(func(k, v any) bool {
				ia := v.(*interactiveAgent)
				ia.mu.Lock()
				r := ia.running
				ia.mu.Unlock()
				log.WithFields(log.Fields{
					"storedKey": k,
					"running":   r,
				}).Info("DEBUG_SESSION GetActiveProgress: interactiveSubAgents entry")
				return true
			})
		}
	}

	if histPtr, ok := a.iterationHistories.Load(key); ok {
		hist := *histPtr.(*[]protocol.ProgressEvent)
		if len(hist) > 0 {
			result.IterationHistory = make([]protocol.ProgressEvent, len(hist))
			copy(result.IterationHistory, hist)
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
