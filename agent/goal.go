package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xbot/agent/hooks"
)

// GoalStatus represents the lifecycle state of a goal.
type GoalStatus string

const (
	GoalActive    GoalStatus = "active"
	GoalCompleted GoalStatus = "completed"
	GoalCleared   GoalStatus = "cleared"
)

// Goal represents a persistent objective for a session.
type Goal struct {
	Objective string
	Status    GoalStatus
	CreatedAt time.Time
	Summary   string // set by set_goal_complete tool
}

// GoalManager manages per-session goals and provides the PreTurnEnd hook
// handler that keeps the agent running while a goal is active.
type GoalManager struct {
	mu    sync.RWMutex
	goals map[string]*Goal // key: sessionKey ("channel:chatID")
}

// NewGoalManager creates a new GoalManager.
func NewGoalManager() *GoalManager {
	return &GoalManager{
		goals: make(map[string]*Goal),
	}
}

// Set creates or replaces the goal for the given session.
func (gm *GoalManager) Set(sessionKey, objective string) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	gm.goals[sessionKey] = &Goal{
		Objective: objective,
		Status:    GoalActive,
		CreatedAt: time.Now(),
	}
}

// Get returns the goal for the given session, or nil.
func (gm *GoalManager) Get(sessionKey string) *Goal {
	gm.mu.RLock()
	defer gm.mu.RUnlock()
	return gm.goals[sessionKey]
}

// Clear removes the goal for the given session.
func (gm *GoalManager) Clear(sessionKey string) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	if g, ok := gm.goals[sessionKey]; ok {
		g.Status = GoalCleared
	}
}

// Complete marks the goal as completed with a summary.
func (gm *GoalManager) Complete(sessionKey, summary string) {
	gm.mu.Lock()
	defer gm.mu.Unlock()
	if g, ok := gm.goals[sessionKey]; ok && g.Status == GoalActive {
		g.Status = GoalCompleted
		g.Summary = summary
	}
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
