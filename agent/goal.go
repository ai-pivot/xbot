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
	// GoalCleared 是**墓碑**状态：只用于持久化"用户已清除"，重启后据此恢复内存墓碑
	// （否则 `Goal` 的 `omitempty` 会把"已清除"退化成"未携带"⇒ 已删目标的横幅重新出现）。
	GoalCleared GoalStatus = "cleared"
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
	// cleared 是"用户已删除目标"的**墓碑**（仅内存）。
	//
	// ⛔ 审计确证（F16）：`Clear` 只是删表项，`GoalInfo` 返回 nil，而 `Goal` 在协议里是
	// `omitempty` ⇒ 权威快照里"没有 goal"**无法表达"已被清除"**。客户端三态语义是
	// 「字段缺失 = 不动 / null = 显式清除」⇒ 清除只存在于一次性事件：事件丢失（断线/切会话/
	// 冷启动）后，另一台设备（或本机重载）的横幅**永远清不掉**。墓碑让快照也能表达"已清除"。
	cleared map[string]bool
}

// NewGoalManager creates a new GoalManager.
func NewGoalManager() *GoalManager {
	return &GoalManager{
		goals:   make(map[string]*Goal),
		loaded:  make(map[string]bool),
		cleared: make(map[string]bool),
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
		// ⛔ 审计确证（state-gate-8-10）：清除时此前**删文件** ⇒ 重启后内存墓碑丢失 ⇒
		//   "已清除"退化成"未携带" ⇒ 已删目标的横幅重新出现。⇒ 有墓碑时**落盘墓碑**。
		gm.mu.RLock()
		cleared := gm.cleared[sessionKey]
		gm.mu.RUnlock()
		if cleared {
			tomb := Goal{Objective: "", Status: GoalCleared, CreatedAt: time.Now()}
			if data, err := json.Marshal(tomb); err == nil {
				dir := goalDir()
				_ = os.MkdirAll(dir, 0o755)
				return os.WriteFile(goalFilePath(sessionKey), data, 0o644)
			}
		}
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
	delete(gm.cleared, sessionKey)   // 重新设目标 ⇒ 墓碑作废
	gm.goals[sessionKey] = &Goal{
		Objective: objective,
		Status:    GoalActive,
		CreatedAt: time.Now(),
	}
	gm.mu.Unlock()
	_ = gm.SaveToFile(sessionKey)
}

// Get returns a COPY of the goal for the given session, or nil.
// Returns a copy (not the internal pointer) to prevent concurrent
// read/write races — Complete/GoalInfo may modify the Goal struct
// while callers read it.
// Lazy-loads from file if not already in memory.
func (gm *GoalManager) Get(sessionKey string) *Goal {
	gm.mu.RLock()
	g, ok := gm.goals[sessionKey]
	gm.mu.RUnlock()
	if ok {
		// Return a copy to prevent data races (Complete modifies in-place).
		copied := *g
		return &copied
	}
	// Lazy load from file — hold lock for the entire load to prevent
	// concurrent callers from racing on LoadFromFile (goroutine A marks
	// loaded=true, releases lock, goroutine B sees loaded=true and returns nil
	// before LoadFromFile writes to map).
	gm.mu.Lock()
	defer gm.mu.Unlock()
	if gm.loaded[sessionKey] {
		// Already loaded — return whatever is in the map (nil if no goal file).
		g, ok := gm.goals[sessionKey]
		if !ok {
			return nil
		}
		copied := *g
		return &copied
	}
	gm.loaded[sessionKey] = true
	// LoadFromFile acquires gm.mu.Lock — but we already hold it (RLock → Lock
	// would deadlock). Instead, do the file read inline (LoadFromFile's logic).
	gm.loadFromFileLocked(sessionKey)
	g, ok = gm.goals[sessionKey]
	if !ok {
		return nil
	}
	copied := *g
	return &copied
}

// loadFromFileLocked loads the goal from disk. Caller MUST hold gm.mu (write lock).
func (gm *GoalManager) loadFromFileLocked(sessionKey string) {
	data, err := os.ReadFile(goalFilePath(sessionKey))
	if err != nil {
		return // file doesn't exist or error — leave map empty
	}
	var g Goal
	if json.Unmarshal(data, &g) != nil {
		return
	}
	// 墓碑：只恢复"已清除"这个事实，不建 goal（`Objective` 为空是墓碑的常态）。
	if g.Status == GoalCleared {
		gm.cleared[sessionKey] = true
		return
	}
	if g.Objective == "" {
		return
	}
	gm.goals[sessionKey] = &g
}

// GoalInfo returns a protocol.GoalInfo snapshot for the given session, or nil.
// Reads fields under RLock to prevent data races with Complete.
func (gm *GoalManager) GoalInfo(sessionKey string) *protocol.GoalInfo {
	gm.mu.RLock()
	g, ok := gm.goals[sessionKey]
	gm.mu.RUnlock()
	if ok {
		return &protocol.GoalInfo{
			Objective: g.Objective,
			Status:    string(g.Status),
			Summary:   g.Summary,
		}
	}
	// Lazy load — same pattern as Get but returns GoalInfo instead of Goal.
	gm.mu.Lock()
	defer gm.mu.Unlock()
	if gm.loaded[sessionKey] {
		g, ok := gm.goals[sessionKey]
		if !ok {
			return nil
		}
		return &protocol.GoalInfo{
			Objective: g.Objective,
			Status:    string(g.Status),
			Summary:   g.Summary,
		}
	}
	gm.loaded[sessionKey] = true
	gm.loadFromFileLocked(sessionKey)
	g, ok = gm.goals[sessionKey]
	if !ok {
		return nil
	}
	return &protocol.GoalInfo{
		Objective: g.Objective,
		Status:    string(g.Status),
		Summary:   g.Summary,
	}
}

// Tombstone 返回"用户已清除目标"的显式标记；没有墓碑时返回 nil。
//
// ⛔ F16：快照（`GetActiveProgress`）用它把"已清除"表达成
// `{objective:"", status:"cleared"}`，客户端据此清空横幅（而不是把"缺失"当"不动"）。
func (gm *GoalManager) Tombstone(sessionKey string) *protocol.GoalInfo {
	gm.mu.RLock()
	ok := gm.cleared[sessionKey]
	gm.mu.RUnlock()
	if !ok {
		return nil
	}
	return &protocol.GoalInfo{Objective: "", Status: protocol.GoalStatusCleared}
}

// Clear removes the goal for the given session.
func (gm *GoalManager) Clear(sessionKey string) {
	gm.mu.Lock()
	delete(gm.goals, sessionKey)
	gm.cleared[sessionKey] = true   // F16：留下墓碑，让权威快照也能表达"已清除"
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

// hasActiveGoal reports whether the session has an unfinished goal.
//
// Used by the turn-less command path (agent.go processMessage command dispatch):
// a command reply returns straight to the channel without ever entering the Run
// loop, and the goal's "work until done" continuation lives ONLY inside the Run
// loop (PreTurnEnd hook → maybeContinueTurn). So without this check any command
// executed while a goal is active (/compress, /settings, …) parks the session in
// idle with work still pending — 2026-10-21 user report "压缩后会话没自动继续".
func (a *Agent) hasActiveGoal(channel, chatID string) bool {
	if a.goalManager == nil {
		return false
	}
	g := a.goalManager.Get(qualifyChatID(channel, chatID))
	return g != nil && g.Status == GoalActive
}

// resumeRunAfterCommand hands control back to the Run loop after a turn-less
// command reply, when the session still has an active goal. Returns true when a
// resume turn was injected.
func (a *Agent) resumeRunAfterCommand(channel, chatID, senderID string) bool {
	if !a.hasActiveGoal(channel, chatID) {
		return false
	}
	a.InjectInboundResume(channel, chatID, senderID)
	return true
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
