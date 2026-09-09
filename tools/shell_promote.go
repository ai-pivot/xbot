package tools

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
)

// ── Foreground shell promote-to-background ──────────────────────────────
//
// Users can move a RUNNING foreground shell command to the background from
// the web UI ("promote"), so the agent iteration no longer blocks on it.
// The command keeps running under BgTaskManager; the tool call returns
// immediately with the task id.
//
// Flow:
//  1. ShellTool.executeForeground runs the command via sandboxExecAsync in a
//     goroutine (output streaming into a buffer) and registers a
//     ForegroundShellHandle in the global registry, keyed by
//     (sessionKey, toolCallID).
//  2. It then select{}s on: exec-done / timeout / toolCtx cancel / PromoteCh.
//  3. The web RPC promote_shell looks the handle up and fires the promote
//     signal; the tool goroutine adopts the running execution into
//     BgTaskManager (AdoptRunning) and returns the task id to BOTH the RPC
//     caller (for instant UI feedback) and the LLM (tool result text).

// ForegroundShellHandle is the registry entry for one running foreground
// shell. The tool goroutine owns the execution; the handle only carries the
// signalling channel and the promote result back to the RPC caller.
type ForegroundShellHandle struct {
	SessionKey string
	CallID     string // LLM tool_call id (correlates with ActiveTools.call_id)
	Command    string

	registeredAt time.Time

	promoteOnce sync.Once
	promoteCh   chan struct{} // closed exactly once on promote
	promoted    chan promoteResult

	// adoptTaskID caches the successful adoption result so a second (racing)
	// promote request returns instantly instead of waiting for the RPC timeout.
	adoptTaskID atomic.Value // string
}

// promoteResult carries the outcome of a promote request back to the RPC
// caller (the tool goroutine fills it after adopting the task).
type promoteResult struct {
	taskID string
	err    error
}

// Promote signals the foreground shell to move itself into the background.
// Returns a channel that receives the result once the tool goroutine has
// adopted the execution as a background task.
func (h *ForegroundShellHandle) Promote() <-chan promoteResult {
	h.promoteOnce.Do(func() {
		close(h.promoteCh) // signal the waiting tool goroutine
	})
	return h.promoted
}

// foregroundShellRegistry is the process-level registry of running
// foreground shells. Keyed by sessionKey → callID → handle.
type foregroundShellRegistry struct {
	mu       sync.Mutex
	sessions map[string]map[string]*ForegroundShellHandle
}

var globalForegroundShells = &foregroundShellRegistry{
	sessions: make(map[string]map[string]*ForegroundShellHandle),
}

// registerForegroundShell adds a handle. Called at the start of every
// foreground shell execution (promotable only when a toolCallID is known).
func registerForegroundShell(sessionKey, callID, command string) *ForegroundShellHandle {
	h := &ForegroundShellHandle{
		SessionKey:   sessionKey,
		CallID:       callID,
		Command:      command,
		registeredAt: time.Now(),
		promoteCh:    make(chan struct{}),
		promoted:     make(chan promoteResult, 1),
	}
	if callID == "" || sessionKey == "" {
		return h // unregistered shell: handle works, Promote() just never fires
	}
	globalForegroundShells.mu.Lock()
	defer globalForegroundShells.mu.Unlock()
	if globalForegroundShells.sessions == nil {
		globalForegroundShells.sessions = make(map[string]map[string]*ForegroundShellHandle)
	}
	m, ok := globalForegroundShells.sessions[sessionKey]
	if !ok {
		m = make(map[string]*ForegroundShellHandle)
		globalForegroundShells.sessions[sessionKey] = m
	}
	m[callID] = h
	return h
}

// unregisterForegroundShell removes a finished/promoted handle. Always called
// via defer by the tool goroutine.
func unregisterForegroundShell(h *ForegroundShellHandle) {
	if h.CallID == "" || h.SessionKey == "" {
		return
	}
	globalForegroundShells.mu.Lock()
	defer globalForegroundShells.mu.Unlock()
	m, ok := globalForegroundShells.sessions[h.SessionKey]
	if !ok {
		return
	}
	if cur, ok := m[h.CallID]; ok && cur == h {
		delete(m, h.CallID)
	}
	if len(m) == 0 {
		delete(globalForegroundShells.sessions, h.SessionKey)
	}
}

// foregroundShellFor looks up a handle by (sessionKey, callID). With an empty
// callID it returns the handle ONLY when exactly one foreground shell is
// registered for the session — the unambiguous case. Parallel foreground
// shells (multiple tool calls in one iteration) must be addressed by callID:
// returning "the most recently registered" could silently promote the WRONG
// command (CR: empty-callID fallback returns most-recent handle). Callers
// (web promote_shell) always pass ToolProgress.CallID, so nil here is a safe
// explicit miss rather than a mis-target.
func foregroundShellFor(sessionKey, callID string) *ForegroundShellHandle {
	globalForegroundShells.mu.Lock()
	defer globalForegroundShells.mu.Unlock()
	m, ok := globalForegroundShells.sessions[sessionKey]
	if !ok {
		return nil
	}
	if callID != "" {
		return m[callID]
	}
	if len(m) != 1 {
		return nil
	}
	for _, h := range m {
		return h
	}
	return nil
}

// PromoteForegroundShell is the RPC-facing entry: it finds the running
// foreground shell for (sessionKey, callID), fires the promote signal and
// waits (bounded) for the tool goroutine to adopt the execution as a
// background task. Returns the new background task id.
func PromoteForegroundShell(sessionKey, callID string) (string, error) {
	if sessionKey == "" {
		return "", fmt.Errorf("session_key is required")
	}
	h := foregroundShellFor(sessionKey, callID)
	if h == nil {
		return "", fmt.Errorf("no running foreground shell in this session")
	}
	// Already promoted before (racing request or a UI retry): return the
	// cached adoption result instead of waiting out the timeout.
	if cached, ok := h.adoptTaskID.Load().(string); ok && cached != "" {
		return cached, nil
	}
	resCh := h.Promote()
	select {
	case res := <-resCh:
		if res.err != nil {
			return "", res.err
		}
		if res.taskID != "" {
			h.adoptTaskID.Store(res.taskID)
		}
		return res.taskID, nil
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("promote timed out — the command may have already finished")
	}
}

// notifyPromoteResult delivers the adopt outcome to the waiting RPC caller.
// Non-blocking: the channel is buffered(1) and the RPC reads at most once.
func notifyPromoteResult(h *ForegroundShellHandle, taskID string, err error) {
	select {
	case h.promoted <- promoteResult{taskID: taskID, err: err}:
	default:
	}
}

// logPromote is a tiny helper so the tool goroutine can record promote events.
func logPromote(sessionKey, callID, taskID string, manual bool) {
	kind := "auto(timeout)"
	if manual {
		kind = "manual(user)"
	}
	log.WithFields(log.Fields{
		"session_key": sessionKey,
		"call_id":     callID,
		"task_id":     taskID,
		"kind":        kind,
	}).Info("Foreground shell promoted to background")
}
