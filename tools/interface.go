package tools

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
)

// SessionMCPManagerProvider 会话 MCP 管理器提供者接口
type SessionMCPManagerProvider interface {
	GetSessionMCPManager(sessionKey string) *SessionMCPManager
}

// ToolContext 工具执行上下文
type ToolContext struct {
	Ctx                     context.Context // 可取消的上下文，用于响应 stop 信号
	WorkingDir              string          // Agent 的工作目录
	WorkspaceRoot           string          // 当前用户可读写工作区根目录（宿主机路径）
	ReadOnlyRoots           []string        // 当前用户额外可读目录（只读）
	SandboxReadOnlyRoots    []string        // 当前用户额外可读目录（sandbox 路径，预转换）
	SkillsDirs              []string        // 全局 skill 目录列表（宿主机路径，同步源）
	AgentsDir               string
	MCPConfigPath           string                                                                     // 当前用户 MCP 配置路径
	GlobalMCPConfigPath     string                                                                     // 全局 MCP 配置路径（只读）
	SandboxEnabled          bool                                                                       // 是否启用命令沙箱
	PreferredSandbox        string                                                                     // 沙箱优先级（docker 优先）
	Sandbox                 Sandbox                                                                    // V4 新增：统一 Sandbox 接口实例
	AgentID                 string                                                                     // 当前 Agent 的 ID
	Manager                 SubAgentManager                                                            // Agent 管理器引用（用于创建 SubAgent）
	DataDir                 string                                                                     // 数据持久化目录
	Channel                 string                                                                     // 当前消息来源渠道
	ChatID                  string                                                                     // 当前消息来源会话
	SenderID                string                                                                     // 直接调用者 ID（SubAgent 场景下为父 Agent ID，主 Agent 场景下等于 OriginUserID）
	OriginUserID            string                                                                     // 原始用户 ID（始终为终端用户，用于 LLM 配置、工作区路径等需要原始用户的场景）
	SenderName              string                                                                     // 当前消息发送者姓名
	SendFunc                func(channel, chatID, content string, metadata ...map[string]string) error // 向 IM 渠道发送消息（不经过 Agent），返回错误
	InjectInbound           func(channel, chatID, senderID, content string)                            // 注入入站消息，触发 Agent 完整处理循环
	Registry                *Registry                                                                  // 工具注册表引用（用于动态注册工具）
	InvalidateAllSessionMCP func()                                                                     // 使所有会话的 MCP 连接失效

	// Letta memory fields (nil when memory provider is not letta)
	TenantID        int64                        // 当前租户 ID
	CoreMemory      *sqlite.CoreMemoryService    // 核心记忆存储
	ArchivalMemory  *vectordb.ArchivalService    // 归档记忆存储（chromem-go 向量数据库）
	MemorySvc       *sqlite.MemoryService        // 事件历史存储（用于 rethink 日志）
	RecallTimeRange vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	ToolIndexer     memory.ToolIndexer           // 工具索引服务（Letta 模式下可用）

	// RootSessionKey 顶层 Agent 的 session key。
	// SubAgent 场景下指向主 Agent 的 session（offload 文件存放在该 session 目录下），
	// 主 Agent 场景下为空（与 SessionKey 相同）。
	RootSessionKey string

	// PWD 工具优化：当前工作目录（可变，从 session 读取）
	CurrentDir    string           // 当前工作目录（优先级高于 WorkspaceRoot）
	SetCurrentDir func(dir string) // 更新 session 中的 cwd
	// IsWorktreeIsolated indicates this agent is running in an isolated git worktree.
	// When true, path_guard enforces boundaries even in "none" sandbox mode.
	IsWorktreeIsolated bool
	// AutoWorktreeEnabled controls whether Worktree(init) can create worktrees.
	// Set from config.Agent.Experimental.AutoWorktree. Default: false.
	AutoWorktreeEnabled bool
	// PeerMessageFn sends a peer-to-peer message to another CLI session.
	// If target is busy: injects as fake tool result in current iteration.
	// If target is idle: injects as user message to start a new turn.
	// Returns a delivery status string.
	PeerMessageFn func(targetSessionKey, message string) string

	// Stream indicates whether the parent Agent is using streaming LLM calls.
	// SubAgents inherit this from the parent to ensure consistent behavior.
	Stream bool

	// Metadata holds ephemeral key-value pairs passed between tool and adapter layers.
	// Used e.g. to propagate background=true from SubAgent tool to spawn adapter.
	Metadata map[string]string

	// BgTaskManager 后台任务管理器（nil = 不支持后台任务）
	BgTaskManager *BackgroundTaskManager
	// SessionKey for task scoping (set by engine, not via RunConfig)
	BgSessionKey string
	// MessageSender allows sending messages to any Channel via Dispatcher.
	MessageSender bus.MessageSender
	// RegisterAgentChannel registers an AgentChannel in the Dispatcher.
	// Called by CreateChat/SubAgent after spawning a SubAgent.
	RegisterAgentChannel func(name string, runFn bus.RunFn) error
	// UnregisterAgentChannel removes an AgentChannel from the Dispatcher.
	// Called on unload/cleanup.
	UnregisterAgentChannel func(name string)

	// GroupID is set when this agent is a member of a virtual group (via CreateChat group).
	// It constrains SendMessage: agents can only message other members of the same group.
	GroupID string
	// GroupMembers lists all agent addresses in this agent's group (for system prompt injection).
	GroupMembers []string

	// TUIControl triggers TUI operations from tool goroutines (CLI channel only).
	// Returns result map and error. Actions: "switch", "close", "layout", "theme".
	TUIControl func(action string, params map[string]string) (map[string]string, error)

	// PluginReloader reloads all plugins. Returns error on failure.
	PluginReloader func() error
	// HooksReloader reloads hooks configuration. Returns error on failure.
	HooksReloader func() error

	// ConfigGet reads a configuration value by key (for config tool).
	ConfigGet func(key string) (string, error)

	// ConfigSet writes a configuration value and returns the previous value (for config tool).
	ConfigSet func(key, value string) (string, error)

	// ChatRename renames the current chat session (for config tool's session_name key).
	// Takes the new name, returns the old name.
	ChatRename func(newName string) (oldName string, err error)

	// ConfigList returns all known configuration items with AI metadata.
	// Injected from AllSettingDefs via buildToolContext.
	ConfigList func() []ConfigListItem

	// IsGlobalKey returns true if the setting key is global-scoped (shared config, admin-only).
	IsGlobalKey func(key string) bool

	// ListSubscriptions returns all LLM subscriptions for the current user.
	ListSubscriptions func() []SubscriptionInfo

	// OriginUserIsAdmin is true when the end user has admin privileges.
	// Global-scoped settings should only be modified when this is true.
	OriginUserIsAdmin bool
}

// ConfigListItem is a single configuration entry returned by config tool's "list" action.
type ConfigListItem struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Permission  string `json:"permission"`
	Scope       string `json:"scope"`
	ValidValues string `json:"valid_values,omitempty"`
	DefaultVal  string `json:"default_value,omitempty"`
	CurrentVal  string `json:"current_value,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	Source      string `json:"source,omitempty"` // "user_db" | "config_json" | "llm_config"
}

// SubscriptionInfo is a single LLM subscription entry.
type SubscriptionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	IsDefault bool   `json:"is_default"`
}

// SubAgentManager SubAgent 管理接口，避免循环依赖
type SubAgentManager interface {
	// RunSubAgent 创建并运行一个 SubAgent，返回最终响应文本
	// allowedTools 为工具白名单，为空时使用所有工具（除 SubAgent）
	// caps 声明 SubAgent 可获得的能力（memory、send_message 等）
	// model 为可选的模型覆盖，为空时继承主 Agent 模型
	// instance 为实例 ID，用于区分同 role 的不同 SubAgent（进度树显示）
	RunSubAgent(parentCtx *ToolContext, task string, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, roleName, instance, model string) (string, error)
}

// --- Tool Registry ---

// ToolResult 工具执行结果
type ToolResult struct {
	Summary     string            `json:"summary,omitempty"` // 精简结果，log用
	Detail      string            `json:"detail,omitempty"`  // 详细内容
	Tips        string            `json:"tips,omitempty"`    // 操作指引，帮助 LLM 理解下一步操作
	WaitingUser bool              `json:"-"`                 // 控制字段：是否等待用户响应（不进入 LLM 上下文）
	IsError     bool              `json:"-"`                 // 控制字段：工具本身执行成功但底层操作失败（如 shell 非零退出码），影响进度图标
	Metadata    map[string]string `json:"-"`                 // 额外元数据，传递到 OutboundMessage.Metadata
}

// NewResult 创建 Summary == Detail 的简单结果
func NewResult(content string) *ToolResult {
	return &ToolResult{Summary: content}
}

// NewErrorResult 创建表示底层操作失败的结果（如 shell 非零退出码）
// 区别于返回 error：工具本身执行成功（JSON 解析、沙箱启动等正常），但命令/操作失败
func NewErrorResult(content string) *ToolResult {
	return &ToolResult{Summary: content, IsError: true}
}

// NewResultWithUserResponse 创建结果并标记为等待用户响应
func NewResultWithUserResponse(summary string) *ToolResult {
	return &ToolResult{Summary: summary, WaitingUser: true}
}

// NewResultWithDetail 创建带详情的结果
func NewResultWithDetail(summary, detail string) *ToolResult {
	return &ToolResult{Summary: summary, Detail: detail}
}

// NewResultWithTips 创建带指引的结果
func NewResultWithTips(summary, tips string) *ToolResult {
	return &ToolResult{Summary: summary, Tips: tips}
}

func (r *ToolResult) WithDetail(detail string) *ToolResult {
	r.Detail = detail
	return r
}

func (r *ToolResult) WithTips(tips string) *ToolResult {
	r.Tips = tips
	return r
}

// Tool 工具接口
type Tool interface {
	llm.ToolDefinition
	Execute(ctx *ToolContext, input string) (*ToolResult, error)
}

// Registry 工具注册表
type Registry struct {
	mu               sync.RWMutex
	globalTools      map[string]Tool           // 所有工具（全局共享）
	sessionMCPMgr    SessionMCPManagerProvider // 会话MCP管理器提供者
	globalMCPCatalog []MCPServerCatalogEntry   // 全局 MCP Server 目录（由 MCPManager.RegisterTools 设置）

	tenantTools   map[int64]map[string]Tool // tenantID → toolName → Tool（per-tenant 工具）
	tenantToolsMu sync.RWMutex

	// channelTools: channel → toolName → Tool
	// Channel-scoped tools registered via RegisterForChannel.
	// Only visible in sessions whose channel matches (extracted from sessionKey).
	channelTools   map[string]map[string]Tool
	channelToolsMu sync.RWMutex

	// runnerTools: runnerID → toolName → Tool
	// Runner-scoped tools registered when a runner connects.
	// Only visible in sessions bound to that runner.
	runnerTools   map[string]map[string]Tool
	runnerToolsMu sync.RWMutex

	// sessionRunners: sessionKey → runnerID
	// Set when a session is bound to a specific runner.
	sessionRunners   map[string]string
	sessionRunnersMu sync.RWMutex
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{
		globalTools:    make(map[string]Tool),
		runnerTools:    make(map[string]map[string]Tool),
		sessionRunners: make(map[string]string),
	}
}

// Register 注册工具（始终在 tool definitions 中可见）
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
}

// RegisterForTenant 注册一个工具仅对特定租户可见。
// tenantID=0 等同于 Register（全局可见）。
func (r *Registry) RegisterForTenant(tenantID int64, tool Tool) {
	if tenantID == 0 {
		r.Register(tool)
		return
	}
	r.tenantToolsMu.Lock()
	defer r.tenantToolsMu.Unlock()
	if r.tenantTools == nil {
		r.tenantTools = make(map[int64]map[string]Tool)
	}
	if r.tenantTools[tenantID] == nil {
		r.tenantTools[tenantID] = make(map[string]Tool)
	}
	r.tenantTools[tenantID][tool.Name()] = tool
}

// RegisterCore 注册工具（与 Register 等效，保留用于语义兼容）
func (r *Registry) RegisterCore(tool Tool) {
	r.Register(tool)
}

// RegisterForChannel 注册 channel 专属工具。
// 该工具仅对指定 channel 的会话可见，不需要 ChannelProvider 接口。
// channel 为空时等同于 Register（全局注册）。
func (r *Registry) RegisterForChannel(channel string, tool Tool) {
	if channel == "" {
		r.Register(tool)
		return
	}
	r.channelToolsMu.Lock()
	defer r.channelToolsMu.Unlock()
	if r.channelTools == nil {
		r.channelTools = make(map[string]map[string]Tool)
	}
	if r.channelTools[channel] == nil {
		r.channelTools[channel] = make(map[string]Tool)
	}
	r.channelTools[channel][tool.Name()] = tool
}

// UnregisterChannelTools 移除指定 channel 的所有工具（channel 关闭时调用）。
func (r *Registry) UnregisterChannelTools(channel string) {
	r.channelToolsMu.Lock()
	defer r.channelToolsMu.Unlock()
	delete(r.channelTools, channel)
}

// GetChannelTool 查找 channel 专属工具。
func (r *Registry) GetChannelTool(channel, name string) (Tool, bool) {
	r.channelToolsMu.RLock()
	defer r.channelToolsMu.RUnlock()
	if tools, ok := r.channelTools[channel]; ok {
		tool, ok := tools[name]
		return tool, ok
	}
	return nil, false
}

// ChannelFromSessionKey 从 "channel:chatID" 格式的 sessionKey 提取 channel 前缀。
// 返回空字符串表示格式不匹配或没有冒号分隔符。
func ChannelFromSessionKey(sessionKey string) string {
	idx := strings.IndexByte(sessionKey, ':')
	if idx < 0 {
		return ""
	}
	return sessionKey[:idx]
}

// --- Runner-scoped tools ---

// RegisterForRunner registers a tool provided by a specific runner.
// Runner tools are only visible in sessions bound to that runner.
func (r *Registry) RegisterForRunner(runnerID string, tool Tool) {
	r.runnerToolsMu.Lock()
	defer r.runnerToolsMu.Unlock()
	if r.runnerTools == nil {
		r.runnerTools = make(map[string]map[string]Tool)
	}
	if r.runnerTools[runnerID] == nil {
		r.runnerTools[runnerID] = make(map[string]Tool)
	}
	r.runnerTools[runnerID][tool.Name()] = tool
}

// ReplaceRunnerTools atomically replaces all tools for a runner.
func (r *Registry) ReplaceRunnerTools(runnerID string, tools []Tool) {
	r.runnerToolsMu.Lock()
	defer r.runnerToolsMu.Unlock()
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Name()] = t
	}
	if r.runnerTools == nil {
		r.runnerTools = make(map[string]map[string]Tool)
	}
	r.runnerTools[runnerID] = m
}

// UnregisterRunnerTools removes all tools for a specific runner.
func (r *Registry) UnregisterRunnerTools(runnerID string) {
	r.runnerToolsMu.Lock()
	defer r.runnerToolsMu.Unlock()
	delete(r.runnerTools, runnerID)
}

// SetSessionRunner binds a session to a runner.
func (r *Registry) SetSessionRunner(sessionKey, runnerID string) {
	r.sessionRunnersMu.Lock()
	defer r.sessionRunnersMu.Unlock()
	if r.sessionRunners == nil {
		r.sessionRunners = make(map[string]string)
	}
	r.sessionRunners[sessionKey] = runnerID
}

// getSessionRunner returns the runner ID bound to a session ("" if none).
func (r *Registry) getSessionRunner(sessionKey string) string {
	r.sessionRunnersMu.RLock()
	defer r.sessionRunnersMu.RUnlock()
	if r.sessionRunners == nil {
		return ""
	}
	return r.sessionRunners[sessionKey]
}

// getRunnerTool looks up a tool by name in a runner's tool set.
func (r *Registry) getRunnerTool(runnerID, name string) (Tool, bool) {
	r.runnerToolsMu.RLock()
	defer r.runnerToolsMu.RUnlock()
	if tools, ok := r.runnerTools[runnerID]; ok {
		tool, ok := tools[name]
		return tool, ok
	}
	return nil, false
}

// GetForSession 统一工具查找：runner → channel → tenant → global。
// 替代 GetForTenant，增加 runner 和 channel 维度优先查找。
func (r *Registry) GetForSession(name string, tenantID int64, sessionKey string) (Tool, bool) {
	// 1. Runner-scoped tools (highest priority — session's bound runner)
	if runnerID := r.getSessionRunner(sessionKey); runnerID != "" {
		if tool, ok := r.getRunnerTool(runnerID, name); ok {
			return tool, true
		}
	}
	// 2. Channel-scoped tools
	channel := ChannelFromSessionKey(sessionKey)
	if channel != "" {
		if tool, ok := r.GetChannelTool(channel, name); ok {
			return tool, true
		}
	}
	// 3. Tenant → global (existing logic)
	return r.GetForTenant(name, tenantID)
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.globalTools, name)
}

// Get 获取工具（先查全局，再查租户）
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.globalTools[name]
	return tool, ok
}

// GetForTenant 获取工具（先查租户，再查全局）
func (r *Registry) GetForTenant(name string, tenantID int64) (Tool, bool) {
	if tenantID != 0 {
		r.tenantToolsMu.RLock()
		if tenantTools, ok := r.tenantTools[tenantID]; ok {
			if tool, found := tenantTools[name]; found {
				r.tenantToolsMu.RUnlock()
				return tool, true
			}
		}
		r.tenantToolsMu.RUnlock()
	}
	return r.Get(name)
}

// List 列出所有工具（按名称排序，保证顺序稳定以优化 KV-cache）
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.globalTools))
	for _, tool := range r.globalTools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})
	return tools
}

// AsDefinitions 转换为 LLM 工具定义列表（全部工具，按名称排序）
func (r *Registry) AsDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var defs []llm.ToolDefinition
	for _, tool := range r.globalTools {
		if mcp, isMCP := tool.(mcpSchemaProvider); isMCP {
			defs = append(defs, &mcpToolDefinition{
				name:   tool.Name(),
				desc:   tool.Description(),
				params: mcp.fullParams(),
			})
		} else {
			defs = append(defs, tool)
		}
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})
	return defs
}

// SetSessionMCPManagerProvider 设置会话 MCP 管理器提供者
func (r *Registry) SetSessionMCPManagerProvider(provider SessionMCPManagerProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionMCPMgr = provider
}

// AsDefinitionsForSession 获取特定会话的工具定义（全量）：
//   - 所有全局注册的工具始终包含（含完整参数 schema）
//   - 全局 MCP 工具始终以完整参数 schema 加入
//   - tenantID>0 时同时包含该租户的专属工具（与全局合并，租户优先）
//   - tenantID=0 时仅返回全局工具（向后兼容）
//   - channel 专属工具和会话 MCP 工具也始终包含
func (r *Registry) AsDefinitionsForSession(sessionKey string, tenantID int64) []llm.ToolDefinition {
	r.mu.RLock()

	// 收集 tenant 专属工具（先获取，在合并时优先）
	var tenantToolMap map[string]Tool
	if tenantID != 0 {
		r.tenantToolsMu.RLock()
		tenantToolMap = r.tenantTools[tenantID]
		r.tenantToolsMu.RUnlock()
	}

	seen := make(map[string]bool)
	var defs []llm.ToolDefinition

	// 辅助函数：将 tool 转为 ToolDefinition 加入列表
	addTool := func(tool Tool) {
		if seen[tool.Name()] {
			return
		}
		seen[tool.Name()] = true
		if mcp, isMCP := tool.(mcpSchemaProvider); isMCP {
			defs = append(defs, &mcpToolDefinition{
				name:   tool.Name(),
				desc:   tool.Description(),
				params: mcp.fullParams(),
			})
			return
		}
		defs = append(defs, tool)
	}

	// 先遍历 tenant 工具（租户优先）
	for _, tool := range tenantToolMap {
		addTool(tool)
	}

	// 再遍历 global 工具
	for _, tool := range r.globalTools {
		addTool(tool)
	}
	r.mu.RUnlock()

	// 追加 runner 专属工具（session 绑定的 runner）
	r.runnerToolsMu.RLock()
	runnerID := ""
	r.sessionRunnersMu.RLock()
	if r.sessionRunners != nil {
		runnerID = r.sessionRunners[sessionKey]
	}
	r.sessionRunnersMu.RUnlock()
	if runnerID != "" {
		if runnerToolMap, ok := r.runnerTools[runnerID]; ok {
			for _, tool := range runnerToolMap {
				if !seen[tool.Name()] {
					seen[tool.Name()] = true
					defs = append(defs, tool)
				}
			}
		}
	}
	r.runnerToolsMu.RUnlock()

	// 追加 channel 专属工具
	r.channelToolsMu.RLock()
	channelToolMap := r.channelTools[ChannelFromSessionKey(sessionKey)]
	r.channelToolsMu.RUnlock()
	for _, tool := range channelToolMap {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			defs = append(defs, tool)
		}
	}

	// 追加会话 MCP 工具（全量，含完整参数 schema）
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			for _, tool := range sm.GetSessionTools() {
				if mcp, ok := tool.(mcpSchemaProvider); ok {
					if !seen[tool.Name()] {
						seen[tool.Name()] = true
						defs = append(defs, &mcpToolDefinition{
							name:   tool.Name(),
							desc:   tool.Description(),
							params: mcp.fullParams(),
						})
					}
				}
			}
		}
	}

	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})

	return defs
}

// Clone 复制工具注册表（用于 SubAgent 工具集继承）
// 复制所有字段：globalTools、tenantTools、channelTools、sessionMCPMgr、globalMCPCatalog
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewRegistry()
	for name, tool := range r.globalTools {
		clone.globalTools[name] = tool
	}
	// 复制 tenant 工具
	r.tenantToolsMu.RLock()
	if len(r.tenantTools) > 0 {
		clone.tenantTools = make(map[int64]map[string]Tool, len(r.tenantTools))
		for tid, tools := range r.tenantTools {
			m := make(map[string]Tool, len(tools))
			for name, tool := range tools {
				m[name] = tool
			}
			clone.tenantTools[tid] = m
		}
	}
	r.tenantToolsMu.RUnlock()
	// 复制 channel 专属工具（Feishu Card 等）
	r.channelToolsMu.RLock()
	if len(r.channelTools) > 0 {
		clone.channelTools = make(map[string]map[string]Tool, len(r.channelTools))
		for ch, tools := range r.channelTools {
			m := make(map[string]Tool, len(tools))
			for name, tool := range tools {
				m[name] = tool
			}
			clone.channelTools[ch] = m
		}
	}
	r.channelToolsMu.RUnlock()
	// 复制 runner 专属工具
	r.runnerToolsMu.RLock()
	if len(r.runnerTools) > 0 {
		clone.runnerTools = make(map[string]map[string]Tool, len(r.runnerTools))
		for rid, tools := range r.runnerTools {
			m := make(map[string]Tool, len(tools))
			for name, tool := range tools {
				m[name] = tool
			}
			clone.runnerTools[rid] = m
		}
	}
	r.runnerToolsMu.RUnlock()
	// 复制 session-runner 绑定
	r.sessionRunnersMu.RLock()
	if len(r.sessionRunners) > 0 {
		clone.sessionRunners = make(map[string]string, len(r.sessionRunners))
		for k, v := range r.sessionRunners {
			clone.sessionRunners[k] = v
		}
	}
	r.sessionRunnersMu.RUnlock()
	// 共享 sessionMCPMgr（provider 指向同一 MultiTenantSession，无副作用）
	clone.sessionMCPMgr = r.sessionMCPMgr
	// 复制全局 MCP 目录
	if len(r.globalMCPCatalog) > 0 {
		clone.globalMCPCatalog = append([]MCPServerCatalogEntry{}, r.globalMCPCatalog...)
	}
	return clone
}

// mcpSchemaProvider 内部接口，MCPRemoteTool 和 SessionMCPRemoteTool 都实现此接口
// 用于获取完整参数 schema（所有工具始终以完整 schema 展示给 LLM）
type mcpSchemaProvider interface {
	fullDescription() string
	fullParams() []llm.ToolParam
	mcpServerName() string
}

// ToolGroupProvider 工具组提供者接口，用于将工具分组显示
// 实现此接口的工具将显示在独立的工具组中，而非 Built-in 分组
type ToolGroupProvider interface {
	GroupName() string         // 工具组名称（如 "Feishu"）
	GroupInstructions() string // 工具组使用说明
}

// ChannelProvider 渠道提供者接口，用于限制工具仅在特定渠道可用
// 未实现此接口的工具在所有渠道可用
type ChannelProvider interface {
	SupportedChannels() []string // 返回支持的渠道列表，空则表示所有渠道
}

// IsChannelSupported 检查工具是否支持指定渠道
// 如果工具未实现 ChannelProvider 接口，则默认支持所有渠道
func IsChannelSupported(tool Tool, channel string) bool {
	if cp, ok := tool.(ChannelProvider); ok {
		channels := cp.SupportedChannels()
		if len(channels) == 0 {
			return true // 空列表 = 所有渠道
		}
		for _, c := range channels {
			if c == channel {
				return true
			}
		}
		return false
	}
	return true // 未实现接口 = 所有渠道可用
}

// ToolGroupEntry 工具组条目
type ToolGroupEntry struct {
	Name         string   // 工具组名称
	Instructions string   // 工具组使用说明
	ToolNames    []string // 工具名称列表
}

// ToolSchema 工具完整 schema 信息（供 load_tools 使用）
type ToolSchema struct {
	ToolName    string
	ServerName  string // 内置工具为空，MCP 工具为 server 名
	Description string
	Params      []llm.ToolParam
}

// GetBuiltinToolNames 返回所有内置（非 MCP、非工具组）工具的名称列表（按名称排序）
// 内置工具不实现 mcpSchemaProvider 接口，也不实现 ToolGroupProvider 接口
func (r *Registry) GetBuiltinToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name, tool := range r.globalTools {
		if _, isMCP := tool.(mcpSchemaProvider); isMCP {
			continue
		}
		if _, hasGroup := tool.(ToolGroupProvider); hasGroup {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// GetToolGroups returns all tool groups (sorted by name).
// Each group contains name, instructions, and tool names.
// Equivalent to GetToolGroupsForChannel("") with no channel filtering.
func (r *Registry) GetToolGroups() []ToolGroupEntry {
	return r.GetToolGroupsForChannel("")
}

// GetToolGroupsForChannel 返回指定渠道可用的工具组（按组名排序）
// channel 为空时不进行渠道过滤，返回所有工具组
func (r *Registry) GetToolGroupsForChannel(channel string) []ToolGroupEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	groups := make(map[string]*ToolGroupEntry)
	for _, tool := range r.globalTools {
		// 渠道过滤（空渠道不过滤）
		if channel != "" && !IsChannelSupported(tool, channel) {
			continue
		}
		if groupProvider, ok := tool.(ToolGroupProvider); ok {
			groupName := groupProvider.GroupName()
			if groups[groupName] == nil {
				groups[groupName] = &ToolGroupEntry{
					Name:         groupName,
					Instructions: groupProvider.GroupInstructions(),
					ToolNames:    []string{},
				}
			}
			groups[groupName].ToolNames = append(groups[groupName].ToolNames, tool.Name())
		}
	}

	// 转换为切片并排序
	result := make([]ToolGroupEntry, 0, len(groups))

	// NEW: 合并 channel 专属工具的工具组
	r.channelToolsMu.RLock()
	for _, tool := range r.channelTools[channel] {
		if groupProvider, ok := tool.(ToolGroupProvider); ok {
			groupName := groupProvider.GroupName()
			if groups[groupName] == nil {
				groups[groupName] = &ToolGroupEntry{
					Name:         groupName,
					Instructions: groupProvider.GroupInstructions(),
					ToolNames:    []string{},
				}
			}
			groups[groupName].ToolNames = append(groups[groupName].ToolNames, tool.Name())
		}
	}
	r.channelToolsMu.RUnlock()

	for _, entry := range groups {
		slices.Sort(entry.ToolNames)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// SetGlobalMCPCatalog 设置全局 MCP Server 目录（由 MCPManager.RegisterTools 调用）
func (r *Registry) SetGlobalMCPCatalog(catalog []MCPServerCatalogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 防御性复制，避免调用方修改切片导致竞争条件
	r.globalMCPCatalog = append([]MCPServerCatalogEntry{}, catalog...)
}

// GetMCPCatalog 获取完整 MCP Server 目录（全局 + 会话特定）
func (r *Registry) GetMCPCatalog(sessionKey string) []MCPServerCatalogEntry {
	r.mu.RLock()
	global := append([]MCPServerCatalogEntry{}, r.globalMCPCatalog...)
	r.mu.RUnlock()

	if r.sessionMCPMgr != nil {
		sessionMCP := r.sessionMCPMgr.GetSessionMCPManager(sessionKey)
		if sessionMCP != nil {
			sessionCatalog := sessionMCP.GetCatalog()
			global = append(global, sessionCatalog...)
		}
	}
	return global
}

// GetToolSchemas 获取指定工具的完整 schema 信息（参数定义、描述等）
// 支持内置工具和 MCP 工具。toolNames 为工具全名列表；传入 nil 返回所有可加载工具的 schema。
// 如果 channel 不为空，则过滤掉不支持该渠道的工具。
func (r *Registry) GetToolSchemas(sessionKey string, toolNames []string) []ToolSchema {
	return r.GetToolSchemasForChannel(sessionKey, toolNames, "")
}

// GetToolSchemasForChannel 获取指定渠道可用的工具 schema 信息
// channel 为空时不进行渠道过滤
func (r *Registry) GetToolSchemasForChannel(sessionKey string, toolNames []string, channel string) []ToolSchema {
	nameSet := make(map[string]bool, len(toolNames))
	matchAll := len(toolNames) == 0
	for _, n := range toolNames {
		nameSet[n] = true
	}

	var schemas []ToolSchema

	r.mu.RLock()
	for name, tool := range r.globalTools {
		if !matchAll && !nameSet[name] {
			continue
		}
		// 渠道过滤
		if channel != "" && !IsChannelSupported(tool, channel) {
			continue
		}
		if p, ok := tool.(mcpSchemaProvider); ok {
			schemas = append(schemas, ToolSchema{
				ToolName:    name,
				ServerName:  p.mcpServerName(),
				Description: p.fullDescription(),
				Params:      p.fullParams(),
			})
		} else {
			schemas = append(schemas, ToolSchema{
				ToolName:    name,
				Description: tool.Description(),
				Params:      tool.Parameters(),
			})
		}
	}
	r.mu.RUnlock()

	// 扫描会话 MCP 工具
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			for _, tool := range sm.GetSessionTools() {
				if !matchAll && !nameSet[tool.Name()] {
					continue
				}
				// 会话 MCP 工具暂不做渠道过滤（MCP 工具通常是通用的）
				if p, ok := tool.(mcpSchemaProvider); ok {
					schemas = append(schemas, ToolSchema{
						ToolName:    tool.Name(),
						ServerName:  p.mcpServerName(),
						Description: p.fullDescription(),
						Params:      p.fullParams(),
					})
				}
			}
		}
	}

	return schemas
}

// DefaultRegistry 创建包含默认工具的注册表。
// 所有工具始终在 tool definitions 中可见（全量激活，无按需加载）。
// 注意：CronTool 需要依赖注入，不在默认注册表中，需单独注册
func DefaultRegistry(memoryProvider string) *Registry {
	r := NewRegistry()
	// 基础文件/系统操作工具
	r.RegisterCore(&ShellTool{})
	r.RegisterCore(&CdTool{})
	r.RegisterCore(&GlobTool{})
	r.RegisterCore(&GrepTool{})
	r.RegisterCore(&ReadTool{})
	r.RegisterCore(&FileCreateTool{})
	r.RegisterCore(&FileReplaceTool{})
	r.RegisterCore(&SubAgentTool{})
	// CreateChatTool — creates agent private chats and moderated group chats.
	r.RegisterCore(&CreateChatTool{})
	r.RegisterCore(&SendMessageTool{})
	r.RegisterCore(&JoinGroupTool{})
	r.RegisterCore(&LeaveGroupTool{})
	r.RegisterCore(&ListGroupMembersTool{})
	// Agent core tool: loads skill knowledge into context (not a runner execution tool)
	r.RegisterCore(&SkillTool{})
	r.RegisterCore(&TaskStatusTool{})
	r.RegisterCore(&TaskKillTool{})
	r.RegisterCore(&TaskReadTool{})
	// CronTool 需要依赖注入，需在 agent 初始化后单独注册
	// DownloadFileTool 和 WebSearchTool 需要凭证注入，在 main.go 中注册
	// WebSearch: always available (requires TAVILY_API_KEY)
	r.RegisterCore(NewFetchTool())
	r.RegisterCore(&AskUserTool{})
	// WorktreeTool — multi-agent workspace isolation via git worktrees
	r.RegisterCore(&WorktreeTool{})
	return r
}
