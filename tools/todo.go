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

// TodoManager 内存级 TODO 管理（非持久化）
type TodoManager struct {
	mu         sync.RWMutex
	todos      map[string][]TodoItem // sessionKey -> todos
	maxEntries int                   // 最大条目数，超过时淘汰最早的
}

// NewTodoManager 创建 TODO 管理器
func NewTodoManager() *TodoManager {
	return &TodoManager{
		todos:      make(map[string][]TodoItem),
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
	if len(items) == 0 {
		return nil
	}
	m.mu.Lock()
	m.todos[sessionKey] = items
	m.mu.Unlock()
	return nil
}

// SetTodos 写入/更新指定 session 的 TODO 列表
func (m *TodoManager) SetTodos(sessionKey string, items []TodoItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(items) == 0 {
		delete(m.todos, sessionKey)
		return
	}
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

// GetTodos 获取指定 session 的 TODO 列表
func (m *TodoManager) GetTodos(sessionKey string) []TodoItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]TodoItem, len(m.todos[sessionKey]))
	copy(result, m.todos[sessionKey])
	return result
}

// sessionKey helper
// Uses AgentID to isolate TODOs between main Agent and SubAgents.
// SubAgent AgentID is "parentID/roleName", main Agent is typically "main".
func (m *TodoManager) sessionKey(ctx *ToolContext) string {
	if ctx.Channel != "" && ctx.ChatID != "" {
		if ctx.AgentID != "" {
			return ctx.AgentID + ":" + ctx.Channel + ":" + ctx.ChatID
		}
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
