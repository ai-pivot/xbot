package tools

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"xbot/llm"
)

// TodoItem 单个 TODO 项
type TodoItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// TodoManager 内存级 TODO 管理，带文件持久化（~/.xbot/todos/<hash>.json）。
// SetTodos 自动保存到文件；GetTodos/HasTodos 在内存未命中时自动从文件加载。
type TodoManager struct {
	mu         sync.RWMutex
	todos      map[string][]TodoItem // sessionKey -> todos
	loaded     map[string]bool       // tracks sessions loaded from file (avoids repeated file reads)
	maxEntries int                   // 最大条目数，超过时淘汰最早的
}

// NewTodoManager 创建 TODO 管理器
func NewTodoManager() *TodoManager {
	return &TodoManager{
		todos:      make(map[string][]TodoItem),
		loaded:     make(map[string]bool),
		maxEntries: 10000, // 默认最多保留 10000 个 session 的 TODO
	}
}

// todoDir returns the base directory for TODO persistence files.
func todoDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xbot", "todos")
}

// todoFilePath returns the file path for a given sessionKey.
func todoFilePath(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(todoDir(), fmt.Sprintf("%x.json", h[:16]))
}

// SaveToFile persists the TODO list for a session to a JSON file.
func (m *TodoManager) SaveToFile(sessionKey string) error {
	m.mu.RLock()
	items, ok := m.todos[sessionKey]
	if !ok {
		m.mu.RUnlock()
		// Remove file if session has no todos
		_ = os.Remove(todoFilePath(sessionKey))
		return nil
	}
	// Deep copy to avoid holding lock during I/O
	saved := make([]TodoItem, len(items))
	copy(saved, items)
	m.mu.RUnlock()

	dir := todoDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return os.WriteFile(todoFilePath(sessionKey), data, 0o600)
}

// LoadFromFile loads the TODO list for a session from a JSON file.
// If the file doesn't exist, the session starts with an empty TODO list.
// Empty arrays (cleared todos) ARE loaded — HasTodos must return true for
// cleared sessions (distinguishes "cleared" from "never ran").
func (m *TodoManager) LoadFromFile(sessionKey string) error {
	data, err := os.ReadFile(todoFilePath(sessionKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No saved todos, start fresh
		}
		return err
	}
	var items []TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	if items == nil {
		items = []TodoItem{}
	}
	m.mu.Lock()
	m.todos[sessionKey] = items
	m.loaded[sessionKey] = true
	m.mu.Unlock()
	return nil
}

// SetTodos 写入/更新指定 session 的 TODO 列表。
// SetTodos 自动持久化到文件（~/.xbot/todos/<hash>.json），确保重启不丢失。
func (m *TodoManager) SetTodos(sessionKey string, items []TodoItem) {
	m.mu.Lock()
	m.loaded[sessionKey] = true // mark as loaded so GetTodos/HasTodos don't re-read file
	if len(items) == 0 {
		// Keep an (empty) record so HasTodos can distinguish "cleared" from
		// "never set". The frontend needs to learn that the server cleared its
		// todos (todo_write([]) or turn-end cleanupTodos) — GetActiveProgress
		// returns done + [] for cleared sessions and nil for never-run sessions.
		if _, ok := m.todos[sessionKey]; ok {
			m.todos[sessionKey] = []TodoItem{}
		}
		m.mu.Unlock()
	} else {
		// 防止 map 无限增长：超过上限时清理最旧的一半条目
		if m.maxEntries > 0 && len(m.todos) >= m.maxEntries {
			count := 0
			target := len(m.todos) / 2
			for k := range m.todos {
				delete(m.todos, k)
				count++
				if count >= target {
					break
				}
			}
		}
		m.todos[sessionKey] = items
		m.mu.Unlock()
	}
	// Persist to file (after releasing lock to avoid deadlock with SaveToFile's RLock)
	_ = m.SaveToFile(sessionKey)
}

// HasTodos reports whether the given session has ever had a todo list written
// (including a cleared/empty one). This distinguishes "turn ended, todos were
// cleared" (HasTodos=true, GetTodos=[]) from "session never ran"
// (HasTodos=false) — the former must produce a done+[] progress event so the
// frontend clears stale todos, the latter returns nil (no active progress).
// Lazy-loads from file if not already in memory (survives server restart).
func (m *TodoManager) HasTodos(sessionKey string) bool {
	m.mu.RLock()
	_, ok := m.todos[sessionKey]
	m.mu.RUnlock()
	if ok {
		return true
	}
	// Lazy load from file (once per session)
	m.mu.Lock()
	if m.loaded[sessionKey] {
		m.mu.Unlock()
		return false // already tried loading, no file
	}
	m.loaded[sessionKey] = true
	m.mu.Unlock()
	_ = m.LoadFromFile(sessionKey)
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok = m.todos[sessionKey]
	return ok
}

// GetTodoSummary 获取指定 session 的 TODO 状态摘要
func (m *TodoManager) GetTodoSummary(sessionKey string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items, ok := m.todos[sessionKey]
	if !ok || len(items) == 0 {
		return ""
	}
	done := 0
	var parts []string
	for _, item := range items {
		if item.Done {
			done++
		}
		status := "○"
		if item.Done {
			status = "●"
		}
		parts = append(parts, fmt.Sprintf("  %s [%d] %s", status, item.ID, item.Text))
	}
	return fmt.Sprintf("(%d/%d)\n%s", done, len(items), strings.Join(parts, "\n"))
}

// GetTodos 获取指定 session 的 TODO 列表。
// Lazy-loads from file if not in memory (survives server restart).
func (m *TodoManager) GetTodos(sessionKey string) []TodoItem {
	m.mu.RLock()
	items, ok := m.todos[sessionKey]
	m.mu.RUnlock()
	if ok {
		result := make([]TodoItem, len(items))
		copy(result, items)
		return result
	}
	// Lazy load from file (once per session)
	m.mu.Lock()
	if m.loaded[sessionKey] {
		m.mu.Unlock()
		return nil // already tried loading, no file
	}
	m.loaded[sessionKey] = true
	m.mu.Unlock()
	_ = m.LoadFromFile(sessionKey)
	m.mu.RLock()
	defer m.mu.RUnlock()
	items = m.todos[sessionKey]
	result := make([]TodoItem, len(items))
	copy(result, items)
	return result
}

// sessionKey helper
// For SubAgents (AgentID contains "/", e.g. "main/explore"), prepends AgentID
// to isolate their TODOs from the main agent. Main agent (AgentID="main")
// keeps the original Channel:ChatID key so all readers remain compatible.
func (m *TodoManager) sessionKey(ctx *ToolContext) string {
	// SubAgent（AgentID 含 "/"）：用 SessionKey（subAgentID）隔离 —— 与主 Agent
	// 的 todos 分开（SubAgent 的子任务列表独立）。
	if strings.Contains(ctx.AgentID, "/") {
		if ctx.SessionKey != "" {
			return ctx.SessionKey
		}
		if ctx.Channel != "" && ctx.ChatID != "" {
			return ctx.AgentID + ":" + ctx.Channel + ":" + ctx.ChatID
		}
		return ""
	}
	// 主 Agent：用 RootSessionKey（canonical = origin channel:chatID）。
	// 不能用 SessionKey —— web 用户浏览 CLI 会话时 physicalChannel override 会
	// 把它改成 "web:chatID"，而 GetActiveProgress 恢复路径读 "cli:chatID"
	// （canonical）→ turn 结束后（后打开的客户端 / 刷新）读不到 todos
	// （"手机端实时显示、电脑端后打开不显示"的根因）。todos 是会话级状态，
	// 必须用 canonical key 让所有读写路径一致。
	if ctx.RootSessionKey != "" {
		return ctx.RootSessionKey
	}
	if ctx.SessionKey != "" {
		return ctx.SessionKey
	}
	if ctx.Channel != "" && ctx.ChatID != "" {
		return ctx.Channel + ":" + ctx.ChatID
	}
	return ""
}

// --- TodoWriteTool ---

// TodoWriteTool TODO 写入工具
type TodoWriteTool struct {
	Manager *TodoManager
}

func (t *TodoWriteTool) Name() string { return "TodoWrite" }

func (t *TodoWriteTool) Description() string {
	return `管理当前任务的 TODO 列表。传入完整的 todo 数组覆盖更新。
Parameters (JSON):
  - todos: array of {id(number), text(string), done(boolean)}
Example: {"todos": [{"id": 1, "text": "read file", "done": true}, {"id": 2, "text": "edit file", "done": false}]}`
}

func (t *TodoWriteTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "todos",
			Type:        "array",
			Description: "Complete TODO list (overwrites). Each item: {id(number), text(string), done(boolean)}",
			Required:    true,
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"id":   map[string]any{"type": "number"},
					"text": map[string]any{"type": "string"},
					"done": map[string]any{"type": "boolean"},
				},
				Required: []string{"id", "text", "done"},
			},
		},
	}
}

type todoWriteArgs struct {
	Todos []TodoItem `json:"todos"`
}

func (t *TodoWriteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	a, err := parseToolArgs[todoWriteArgs](input)
	if err != nil {
		return nil, err
	}
	sk := t.Manager.sessionKey(ctx)
	slices.SortFunc(a.Todos, func(a, b TodoItem) int { return cmp.Compare(a.ID, b.ID) })
	t.Manager.SetTodos(sk, a.Todos)
	done := 0
	for _, item := range a.Todos {
		if item.Done {
			done++
		}
	}
	if len(a.Todos) == 0 {
		return NewResultWithTips("TODO 列表已清空", "所有 TODO 已清除。继续执行剩余任务。"), nil
	}
	return NewResultWithTips(
		fmt.Sprintf("TODO 列表已更新: %d/%d 完成", done, len(a.Todos)),
		fmt.Sprintf("检查下一项未完成的 TODO 继续推进。(%d 项完成 / %d 项总计)", done, len(a.Todos)),
	), nil
}

// --- TodoListTool ---

// TodoListTool TODO 查看工具
type TodoListTool struct {
	Manager *TodoManager
}

func (t *TodoListTool) Name() string { return "TodoList" }

func (t *TodoListTool) Description() string {
	return "查看当前任务的所有 TODO 项及其完成状态。无需参数。"
}

func (t *TodoListTool) Parameters() []llm.ToolParam {
	return nil
}

func (t *TodoListTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	sk := t.Manager.sessionKey(ctx)
	items := t.Manager.GetTodos(sk)
	if len(items) == 0 {
		return NewResultWithTips("当前没有 TODO 项", "没有活跃的 TODO。如果任务有多个步骤，建议用 TodoWrite 创建 TODO 列表来追踪进度。"), nil
	}
	done := 0
	var lines []string
	for _, item := range items {
		if item.Done {
			done++
		}
		status := "○"
		if item.Done {
			status = "●"
		}
		lines = append(lines, fmt.Sprintf("%s [%d] %s", status, item.ID, item.Text))
	}
	return NewResultWithTips(
		fmt.Sprintf("(%d/%d 完成)\n%s", done, len(items), strings.Join(lines, "\n")),
		fmt.Sprintf("共 %d 项 TODO，%d 项已完成。继续推进未完成项。", len(items), done),
	), nil
}
