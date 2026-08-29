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

// TodoItem 单个 TODO 项。Status 必填："pending" | "doing" | "done"。
type TodoItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // "pending" | "doing" | "done"
}

// isValidStatus 检查 status 值合法。
func isValidStatus(s string) bool {
	return s == "pending" || s == "doing" || s == "done"
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

// loadFromFileLocked loads the TODO list from disk. Caller MUST hold m.mu (write lock).
// 旧格式磁盘文件（done: boolean）通过 legacyTodoItem 中转迁移——注意主路径
// json.Unmarshal 会忽略未知字段 done 而成功（status 全空 → pending），所以必须
// 先检测原始数据里的 "done" 字段再决定解析路径，否则 legacy 迁移是死代码。
func (m *TodoManager) loadFromFileLocked(sessionKey string) {
	data, err := os.ReadFile(todoFilePath(sessionKey))
	if err != nil {
		return // file doesn't exist or error — leave map empty
	}
	// 旧格式特征：原始 JSON 含 "done" 字段 → legacy 解析（一次性磁盘数据迁移，
	// 迁移后下次 SetTodos 重写为新格式）。不是 LLM API 兼容——工具调用层
	// （TodoWriteTool.Execute）对缺失/非法 status 严格报错，无任何转换。
	if strings.Contains(string(data), `"done"`) {
		var legacy []legacyTodoItem
		if json.Unmarshal(data, &legacy) == nil {
			items := make([]TodoItem, 0, len(legacy))
			for _, l := range legacy {
				st := l.Status
				if st == "" && l.Done {
					st = "done"
				}
				if !isValidStatus(st) {
					st = "pending"
				}
				items = append(items, TodoItem{ID: l.ID, Text: l.Text, Status: st})
			}
			m.todos[sessionKey] = items
			return
		}
	}
	// 新格式直接解析。status 非法（含空）一律归一化 "pending"——磁盘数据无法
	// 报错给任何人，归一化是唯一安全选项。
	var items []TodoItem
	if json.Unmarshal(data, &items) != nil {
		return
	}
	for i := range items {
		if !isValidStatus(items[i].Status) {
			items[i].Status = "pending"
		}
	}
	if items == nil {
		items = []TodoItem{}
	}
	m.todos[sessionKey] = items
}

// legacyTodoItem 老格式 TODO 项（done: boolean）。仅用于加载旧持久化数据。
type legacyTodoItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Done   bool   `json:"done"`
	Status string `json:"status"`
}

// LoadFromFile loads the TODO list for a session from a JSON file.
// If the file doesn't exist, the session starts with an empty TODO list.
// Empty arrays (cleared todos) ARE loaded — HasTodos must return true for
// cleared sessions (distinguishes "cleared" from "never ran").
func (m *TodoManager) LoadFromFile(sessionKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loadFromFileLocked(sessionKey)
	m.loaded[sessionKey] = true
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
// Uses write-lock for the entire load to prevent concurrent caller race
// (goroutine A marks loaded=true, releases lock, goroutine B sees loaded=true
// and returns nil before LoadFromFile writes to map).
func (m *TodoManager) HasTodos(sessionKey string) bool {
	m.mu.RLock()
	_, ok := m.todos[sessionKey]
	m.mu.RUnlock()
	if ok {
		return true
	}
	// Lazy load from file — hold lock for entire load (CR fix: race window)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded[sessionKey] {
		_, ok := m.todos[sessionKey]
		return ok
	}
	m.loaded[sessionKey] = true
	m.loadFromFileLocked(sessionKey)
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
		status := "○"
		switch item.Status {
		case "done":
			done++
			status = "●"
		case "doing":
			status = "◐"
		}
		parts = append(parts, fmt.Sprintf("  %s [%d] %s", status, item.ID, item.Text))
	}
	return fmt.Sprintf("(%d/%d)\n%s", done, len(items), strings.Join(parts, "\n"))
}

// GetTodos 获取指定 session 的 TODO 列表。
// Lazy-loads from file if not in memory (survives server restart).
// Uses write-lock for the entire load to prevent concurrent caller race
// (same fix as HasTodos — goroutine A marks loaded=true, releases lock,
// goroutine B sees loaded=true and returns nil before LoadFromFile writes to map).
func (m *TodoManager) GetTodos(sessionKey string) []TodoItem {
	m.mu.RLock()
	items, ok := m.todos[sessionKey]
	m.mu.RUnlock()
	if ok {
		result := make([]TodoItem, len(items))
		copy(result, items)
		return result
	}
	// Lazy load from file — hold lock for entire load (CR fix: race window)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded[sessionKey] {
		items, ok := m.todos[sessionKey]
		if !ok {
			return nil
		}
		result := make([]TodoItem, len(items))
		copy(result, items)
		return result
	}
	m.loaded[sessionKey] = true
	m.loadFromFileLocked(sessionKey)
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
参数（JSON）:
  - todos: array of {id(number), text(string), status(string, required: "pending"|"doing"|"done")}

⚠️ 当前正在执行的 TODO 项必须标记 status: "doing"（UI 显示旋转图标+高亮）。
已完成的过时 TODO（不再相关的条目）直接删除，不要保留在列表里。
示例: {"todos": [{"id": 1, "text": "read file", "status": "done"}, {"id": 2, "text": "edit file", "status": "doing"}, {"id": 3, "text": "write file", "status": "pending"}]}`
}

func (t *TodoWriteTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "todos",
			Type:        "array",
			Description: "Complete TODO list (overwrites). Each item: {id(number), text(string), status(string: 'pending'|'doing'|'done')}. 当前正在执行的项必须标记 status='doing'；已完成的过时项直接删除",
			Required:    true,
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"id":     map[string]any{"type": "number"},
					"text":   map[string]any{"type": "string"},
					"status": map[string]any{"type": "string", "enum": []string{"pending", "doing", "done"}},
				},
				Required: []string{"id", "text", "status"},
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
	// 严格校验：每项 status 必填且合法。旧格式 done=true → status 为空 → 报错
	// 让 LLM 自行纠正（不做兼容转换——schema 就是 status: pending|doing|done）。
	for i, item := range a.Todos {
		if item.Status == "" {
			return &ToolResult{
				Summary: fmt.Sprintf("⛔ item %d (%s): missing required field 'status'. The 'done' boolean has been REMOVED. Use status: \"pending\"|\"doing\"|\"done\".", i+1, truncateStr(item.Text, 30)),
				IsError: true,
			}, nil
		}
		if !isValidStatus(item.Status) {
			return &ToolResult{
				Summary: fmt.Sprintf("⛔ item %d (%s): invalid status %q. Valid: \"pending\", \"doing\", \"done\".", i+1, truncateStr(item.Text, 30), item.Status),
				IsError: true,
			}, nil
		}
	}
	todos := a.Todos
	slices.SortFunc(todos, func(a, b TodoItem) int { return cmp.Compare(a.ID, b.ID) })
	t.Manager.SetTodos(sk, todos)
	done := 0
	doing := 0
	for _, item := range todos {
		switch item.Status {
		case "done":
			done++
		case "doing":
			doing++
		}
	}
	if len(todos) == 0 {
		return NewResultWithTips("TODO 列表已清空", "所有 TODO 已清除。继续执行剩余任务。"), nil
	}
	statusStr := fmt.Sprintf("TODO 列表已更新: %d/%d 完成", done, len(todos))
	if doing > 0 {
		statusStr += fmt.Sprintf("（%d 项进行中）", doing)
	}
	return NewResultWithTips(
		statusStr,
		fmt.Sprintf("检查下一项未完成的 TODO 继续推进。(%d 项完成 / %d 项总计)", done, len(todos)),
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
		status := "○"
		switch item.Status {
		case "done":
			done++
			status = "●"
		case "doing":
			status = "◐"
		}
		lines = append(lines, fmt.Sprintf("%s [%d] %s", status, item.ID, item.Text))
	}
	return NewResultWithTips(
		fmt.Sprintf("(%d/%d 完成)\n%s", done, len(items), strings.Join(lines, "\n")),
		fmt.Sprintf("共 %d 项 TODO，%d 项已完成。继续推进未完成项。", len(items), done),
	), nil
}
