package tools

import (
	"context"

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
	// The implementation routes to the correct backend based on SettingDef.Source:
	//   SourceUserDB        → user_settings DB (via SettingsSvc)
	//   SourceConfigJSON    → config.json (via SaveToFile)
	// Subscription-scoped keys (llm_model, max_output_tokens, etc.) are written via
	// the subscription manager to the user_llm_subscriptions DB.
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

	// ── Runner CRUD (for config tool) ──

	// RunnerCreate creates a new runner and returns the token.
	RunnerCreate func(name, mode, dockerImage, workspace, llmProvider, llmAPIKey, llmModel, llmBaseURL string) (token string, err error)

	// RunnerList returns all runners for the current user.
	RunnerList func() ([]RunnerInfo, error)

	// RunnerDelete deletes a runner by name.
	RunnerDelete func(name string) error

	// RunnerGetActive returns the active runner name for the current user.
	RunnerGetActive func() (string, error)

	// RunnerSetActive sets the active runner by name.
	RunnerSetActive func(name string) error

	// RunnerRename renames a runner.
	RunnerRename func(oldName, newName string) error

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
