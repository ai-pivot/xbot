package agent

import (
	"fmt"
	"strings"

	"xbot/config"
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
	// Set CWD — but don't overwrite an existing worktree path.
	// Worktree CWD is set by AutoDetectAndInit in buildPrompt and persists
	// across restarts. CLI callers sync their terminal CWD on startup, which
	// should only take effect when the session has no CWD yet or the existing
	// CWD is not a worktree path (i.e. the user changed their terminal dir
	// between invocations of the same non-worktree session).
	existingCWD := sess.GetCurrentDir()
	if existingCWD == "" || !strings.Contains(existingCWD, ".xbot-worktrees") {
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
		return nil
	}
	snapshot := v.(*protocol.ProgressEvent)
	// Shallow copy to avoid data race: agent may update snapshot fields concurrently.
	result := *snapshot
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
