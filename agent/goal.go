package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"xbot/agent/hooks"
	"xbot/protocol"
)

// GoalStatus represents the lifecycle state of a goal.
type GoalStatus string

const (
	GoalActive    GoalStatus = "active"
	GoalCompleted GoalStatus = "completed"
)

// Goal represents a persistent objective for a session.
type Goal struct {
	Objective string     `json:"objective"`
	Status    GoalStatus `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	Summary   string     `json:"summary,omitempty"`
}

// GoalManager manages per-session goals and provides the PreTurnEnd hook
// handler that keeps the agent running while a goal is active.
//
// Persistence: goals are saved to ~/.xbot/goals/<hash>.json on every mutation
// (Set/Complete/Clear) and lazy-loaded from file on first access (Get/GoalInfo)
// if not already in memory. This ensures goals survive server restarts.
type GoalManager struct {
	mu     sync.RWMutex
	goals  map[string]*Goal // key: sessionKey ("channel:chatID")
	loaded map[string]bool  // tracks which sessions have been loaded from file (avoids repeated file reads)
}

// NewGoalManager creates a new GoalManager.
func NewGoalManager() *GoalManager {
	return &GoalManager{
		goals:  make(map[string]*Goal),
		loaded: make(map[string]bool),
	}
}

// goalDir returns the base directory for goal persistence files.
func goalDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xbot", "goals")
}

// goalFilePath returns the file path for a given sessionKey.
func goalFilePath(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(goalDir(), fmt.Sprintf("%s.json", hex.EncodeToString(h[:16])))
}

// SaveToFile persists the goal for a session to a JSON file.
func (gm *GoalManager) SaveToFile(sessionKey string) error {
	gm.mu.RLock()
	g, ok := gm.goals[sessionKey]
	gm.mu.RUnlock()
	if !ok || g == nil {
		// No goal — remove file if it exists
		_ = os.Remove(goalFilePath(sessionKey))
		return nil
	}
	// Deep copy to avoid holding lock during I/O
	saved := *g
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	dir := goalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(goalFilePath(sessionKey), data, 0o600)
}

// LoadFromFile loads the goal for a session from a JSON file.
// If the file doesn't exist, the session starts with no goal.
func (gm *GoalManager) LoadFromFile(sessionKey string) error {
	data, err := os.ReadFile(goalFilePath(sessionKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No saved goal, start fresh
		}
		return err
	}
	var g Goal
	if err := json.Unmarshal(data, &g); err != nil {
		return err
	}
	if g.Objective == "" {
		return nil
	}
	gm.mu.Lock()
	gm.goals[sessionKey] = &g
	gm.loaded[sessionKey] = true
	gm.mu.Unlock()
	return nil
}

// Set creates or replaces the goal for the given session.
func (gm *GoalManager) Set(sessionKey, objective string) {
	gm.mu.Lock()
	gm.goals[sessionKey] = &Goal{
		Objective: objective,
		Status:    GoalActive,
		CreatedAt: time.Now(),
	}
	gm.mu.Unlock()
	_ = gm.SaveToFile(sessionKey)
}

// Get returns the goal for the given session, or nil.
// Lazy-loads from file if not already in memory.
func (gm *GoalManager) Get(sessionKey string) *Goal {
	gm.mu.RLock()
	g, ok := gm.goals[sessionKey]
	gm.mu.RUnlock()
	if ok {
		return g
	}
	// Lazy load from file (once per session)
	gm.mu.Lock()
	if gm.loaded[sessionKey] {
		gm.mu.Unlock()
		return nil // already tried loading, no goal file
	}
	gm.loaded[sessionKey] = true
	gm.mu.Unlock()
	_ = gm.LoadFromFile(sessionKey)
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.goals[sessionKey]
}

// GoalInfo returns a protocol.GoalInfo snapshot for the given session, or nil.
// Lazy-loads from file if not already in memory (same as Get).
func (gm *GoalManager) GoalInfo(sessionKey string) *protocol.GoalInfo {
	g := gm.Get(sessionKey) // Get handles lazy loading
	if g == nil {
		return nil
	}
	return &protocol.GoalInfo{
		Objective: g.Objective,
		Status:    string(g.Status),
		Summary:   g.Summary,
	}
}

// Clear removes the goal for the given session.
func (gm *GoalManager) Clear(sessionKey string) {
	gm.mu.Lock()
	delete(gm.goals, sessionKey)
	gm.mu.Unlock()
	_ = gm.SaveToFile(sessionKey) // deletes file
}

// Complete marks the goal as completed with a summary.
func (gm *GoalManager) Complete(sessionKey, summary string) {
	gm.mu.Lock()
	if g, ok := gm.goals[sessionKey]; ok && g.Status == GoalActive {
		g.Status = GoalCompleted
		g.Summary = summary
	}
	gm.mu.Unlock()
	_ = gm.SaveToFile(sessionKey)
}

// PreTurnEndHook returns a CallbackHook that injects a goal-continuation
// prompt when the session has an active goal. When the goal is completed
// or cleared, the hook does nothing, allowing the turn to end naturally.
func (gm *GoalManager) PreTurnEndHook() *hooks.CallbackHook {
	return &hooks.CallbackHook{
		Name: "goal-pre-turn-end",
		Fn: func(ctx context.Context, event hooks.Event) (*hooks.Result, error) {
			e, ok := event.(*hooks.PreTurnEndEvent)
			if !ok {
				return nil, nil
			}
			sessionKey := e.Channel + ":" + e.ChatID
			goal := gm.Get(sessionKey)
			if goal == nil || goal.Status != GoalActive {
				return nil, nil
			}
			e.Continue = true
			e.Reason = fmt.Sprintf(
				"🎯 You have an active goal: %s\n\n"+
					"Continue working toward this goal. If the goal is complete, "+
					"call the set_goal_complete tool with a summary of what was accomplished. "+
					"Do NOT declare the goal complete without calling set_goal_complete.",
				goal.Objective,
			)
			return nil, nil
		},
	}
}
