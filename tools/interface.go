package tools

import (
	"context"
	"sort"
	"sync"
	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
)

// SessionMCPManagerProvider: session MCP manager provider interface
type SessionMCPManagerProvider interface {
	GetSessionMCPManager(sessionKey string) *SessionMCPManager
}

// ToolContext: tool execution context
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
	Registry                *Registry                                                                  // tool registration表引用（用于动态注册工具）
	InvalidateAllSessionMCP func()                                                                     // 使所有会话的 MCP 连接失效

	// Letta memory fields (nil when memory provider is not letta)
	TenantID        int64                        // 当前租户 ID
	CoreMemory      *sqlite.CoreMemoryService    // 核心记忆存储
	ArchivalMemory  *vectordb.ArchivalService    // 归档记忆存储（chromem-go 向量数据库）
	MemorySvc       *sqlite.MemoryService        // 事件历史存储（用于 rethink 日志）
	RecallTimeRange vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	ToolIndexer     memory.ToolIndexer           // 工具索引服务（Letta 模式下可用）

	// RootSessionKey: the top-level Agent's session key.
	// In SubAgent context, points to the main Agent's session (offload files stored there);
	// empty in main Agent context (same as SessionKey).
	RootSessionKey string

	// PWD tool optimization: current working directory (mutable, read from session)
	CurrentDir    string           // current working directory（优先级高于 WorkspaceRoot）
	SetCurrentDir func(dir string) // 更新 session 中的 cwd

	// Stream indicates whether the parent Agent is using streaming LLM calls.
	// SubAgents inherit this from the parent to ensure consistent behavior.
	Stream bool

	// Metadata holds ephemeral key-value pairs passed between tool and adapter layers.
	// Used e.g. to propagate background=true from SubAgent tool to spawn adapter.
	Metadata map[string]string

	// BgTaskManager background task manager (nil = not supported)
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
}

// SubAgentManager is the SubAgent management interface (avoids circular dependency)
type SubAgentManager interface {
	// RunSubAgent creates and runs a SubAgent, returning the final response text
	// allowedTools is a tool whitelist; when empty, all tools are used (except SubAgent)
	// caps declares the capabilities the SubAgent can receive (memory, send_message, etc.)
	// model is an optional model override; when empty, inherits the main Agent's model
	RunSubAgent(parentCtx *ToolContext, task string, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, roleName string, model string) (string, error)
}

// --- Tool Registry ---

// ToolResult: tool execution result
type ToolResult struct {
	Summary     string            `json:"summary,omitempty"` // 精简结果，log用
	Detail      string            `json:"detail,omitempty"`  // 详细内容
	Tips        string            `json:"tips,omitempty"`    // 操作指引，帮助 LLM 理解下一步操作
	WaitingUser bool              `json:"-"`                 // 控制字段：是否等待用户响应（不进入 LLM 上下文）
	IsError     bool              `json:"-"`                 // 控制字段：工具本身执行成功但底层操作失败（如 shell 非零退出码），影响进度图标
	Metadata    map[string]string `json:"-"`                 // 额外元数据，传递到 OutboundMessage.Metadata
}

// NewResult creates a simple result where Summary == Detail
func NewResult(content string) *ToolResult {
	return &ToolResult{Summary: content}
}

// NewErrorResult creates a result indicating an underlying operation failure (e.g. shell non-zero exit code)
// distinct from returning error: the tool itself executed successfully (JSON parsing, sandbox startup etc. are fine), but the command/operation failed
func NewErrorResult(content string) *ToolResult {
	return &ToolResult{Summary: content, IsError: true}
}

// NewResultWithUserResponse creates a result and marks it as waiting for user response
func NewResultWithUserResponse(summary string) *ToolResult {
	return &ToolResult{Summary: summary, WaitingUser: true}
}

// NewResultWithDetail creates a result with detail
func NewResultWithDetail(summary, detail string) *ToolResult {
	return &ToolResult{Summary: summary, Detail: detail}
}

// NewResultWithTips creates a result with tips
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

// Tool: tool interface
type Tool interface {
	llm.ToolDefinition
	Execute(ctx *ToolContext, input string) (*ToolResult, error)
}

const defaultMaxIdleRounds int64 = 5

// Registry: tool registration table
type Registry struct {
	mu               sync.RWMutex
	globalTools      map[string]Tool             // 所有工具（全局共享）
	coreTools        map[string]bool             // 核心工具名（始终在 tool definitions 中）
	sessionActivated map[string]map[string]int64 // sessionKey → toolName → lastUsedRound
	sessionRound     map[string]int64            // sessionKey → 当前 round 计数
	maxIdleRounds    int64                       // 连续多少轮未使用后自动失效
	sessionMCPMgr    SessionMCPManagerProvider   // 会话MCP管理器提供者
	globalMCPCatalog []MCPServerCatalogEntry     // 全局 MCP Server 目录（由 MCPManager.RegisterTools 设置）
	flatMode         bool                        // flat memory 模式：所有工具均为核心，无需 load_tools
}

// NewRegistry creates a new tool registry
func NewRegistry() *Registry {
	return &Registry{
		globalTools:      make(map[string]Tool),
		coreTools:        make(map[string]bool),
		sessionActivated: make(map[string]map[string]int64),
		sessionRound:     make(map[string]int64),
		maxIdleRounds:    defaultMaxIdleRounds,
	}
}

// SetFlatMode sets flat memory mode.
// in flat mode, all tools are core tools; no load_tools activation needed, never expire.
func (r *Registry) SetFlatMode(flat bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flatMode = flat
}

func (r *Registry) IsFlatMode() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.flatMode
}

// Register: register a tool (non-core; must be activated via load_tools before appearing in tool definitions).
// in flat mode, equivalent to RegisterCore.
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
	if r.flatMode {
		r.coreTools[tool.Name()] = true
	}
}

// RegisterCore: register a core tool (always appears in tool definitions, no activation needed)
func (r *Registry) RegisterCore(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
	r.coreTools[tool.Name()] = true
}

// Unregister unregisters a tool
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.globalTools, name)
}

// Get retrieves a tool by name
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.globalTools[name]
	return tool, ok
}

// List: list all tools (sorted by name for stable ordering to optimize KV-cache)
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

// AsDefinitions converts to a list of LLM tool definitions (core tools only, sorted by name)
func (r *Registry) AsDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var defs []llm.ToolDefinition
	for _, tool := range r.globalTools {
		if r.coreTools[tool.Name()] {
			defs = append(defs, tool)
		}
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})
	return defs
}

// SetSessionMCPManagerProvider sets the session MCP manager provider
func (r *Registry) SetSessionMCPManagerProvider(provider SessionMCPManagerProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionMCPMgr = provider
}

// AsDefinitionsForSession: get tool definitions for a specific session:
//   - core tools always included
//   - non-core tools only included when activated and not expired (used within maxIdleRounds)
//   - global MCP tools added with full param schema after activation (not stub mode's empty params)
func (r *Registry) AsDefinitionsForSession(sessionKey string) []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	active := r.activeToolSet(sessionKey)
	flatMode := r.flatMode

	var defs []llm.ToolDefinition
	for _, tool := range r.globalTools {
		if mcp, isMCP := tool.(mcpSchemaProvider); isMCP {
			// Global MCP tools: visible directly in flat mode; otherwise only added with full parameter schema after activation
			if flatMode || active[tool.Name()] {
				defs = append(defs, &mcpToolDefinition{
					name:   tool.Name(),
					desc:   tool.Description(),
					params: mcp.fullParams(),
				})
			}
			continue
		}
		if r.coreTools[tool.Name()] || active[tool.Name()] {
			defs = append(defs, tool)
		}
	}

	// Append session MCP tools: visible directly in flat mode; otherwise only append activated tools
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			if flatMode {
				for _, tool := range sm.GetSessionTools() {
					if def, ok := tool.(llm.ToolDefinition); ok {
						defs = append(defs, def)
					}
				}
			} else {
				defs = append(defs, sm.GetActivatedToolDefs(active)...)
			}
		}
	}

	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})

	return defs
}

// activeToolSet returns in the specified sessionthe set of active, non-expired tool names (caller must hold r.mu read lock)
func (r *Registry) activeToolSet(sessionKey string) map[string]bool {
	toolRounds := r.sessionActivated[sessionKey]
	if len(toolRounds) == 0 {
		return nil
	}
	curRound := r.sessionRound[sessionKey]
	active := make(map[string]bool, len(toolRounds))
	for name, lastRound := range toolRounds {
		if curRound-lastRound <= r.maxIdleRounds {
			active[name] = true
		}
	}
	return active
}

// TickSession advances the session round counter (called on each new user message) and cleans up expired tools.
// Returns the new round number.
func (r *Registry) TickSession(sessionKey string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionRound[sessionKey]++
	curRound := r.sessionRound[sessionKey]

	// Clean up expired tools to prevent unbounded map growth
	if toolRounds := r.sessionActivated[sessionKey]; len(toolRounds) > 0 {
		for name, lastRound := range toolRounds {
			if curRound-lastRound > r.maxIdleRounds {
				delete(toolRounds, name)
			}
		}
	}

	return curRound
}

// ActivateTools activates tools for the specified session, recording the current round (built-in + MCP all go through this method)
func (r *Registry) ActivateTools(sessionKey string, toolNames []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.sessionActivated[sessionKey]
	if m == nil {
		m = make(map[string]int64, len(toolNames))
		r.sessionActivated[sessionKey] = m
	}
	curRound := r.sessionRound[sessionKey]
	for _, name := range toolNames {
		m[name] = curRound
	}
}

// TouchTool refreshes the tool's last-used round (called when a tool is actually executed)
func (r *Registry) TouchTool(sessionKey, toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coreTools[toolName] {
		return
	}
	if m := r.sessionActivated[sessionKey]; m != nil {
		if _, exists := m[toolName]; exists {
			m[toolName] = r.sessionRound[sessionKey]
		}
	}
}

// IsToolActive checks if a tool is available for the specified session (core tools always return true, expired tools return false)
func (r *Registry) IsToolActive(sessionKey, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.coreTools[toolName] {
		return true
	}
	lastRound, ok := r.sessionActivated[sessionKey][toolName]
	if !ok {
		return false
	}
	return r.sessionRound[sessionKey]-lastRound <= r.maxIdleRounds
}

// DeactivateSession clears all activation state and round counts for the specified session
func (r *Registry) DeactivateSession(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessionActivated, sessionKey)
	delete(r.sessionRound, sessionKey)
}

// Clone clones the tool registry
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewRegistry()
	for name, tool := range r.globalTools {
		clone.globalTools[name] = tool
	}
	for name := range r.coreTools {
		clone.coreTools[name] = true
	}
	return clone
}

// mcpSchemaProvider: internal interface implemented by both MCPRemoteTool and SessionMCPRemoteTool
// Used by load_tools to get full parameter information
type mcpSchemaProvider interface {
	fullDescription() string
	fullParams() []llm.ToolParam
	mcpServerName() string
}

// ToolGroupProvider is the tool group provider interface for grouping tools in display
// Tools implementing this interface are displayed in a separate tool group, not the Built-in group
type ToolGroupProvider interface {
	GroupName() string         // 工具组名称（如 "Feishu"）
	GroupInstructions() string // 工具组使用说明
}

// ChannelProvider is the channel provider interface for restricting tools to specific channels
// Tools not implementing this interface are available on all channels
type ChannelProvider interface {
	SupportedChannels() []string // 返回支持的渠道列表，空则表示所有渠道
}

// IsChannelSupported checks if the tool supports the specified channel
// if tool doesn't implement ChannelProvider, default to supporting all channels
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

// ToolGroupEntry is a tool group entry
type ToolGroupEntry struct {
	Name         string   // 工具组名称
	Instructions string   // 工具组使用说明
	ToolNames    []string // 工具名称列表
}

// ToolSchema: complete tool schema information (used by load_tools)
type ToolSchema struct {
	ToolName    string
	ServerName  string // 内置工具为空，MCP 工具为 server 名
	Description string
	Params      []llm.ToolParam
}

// GetBuiltinToolNames returns names of all built-in (non-MCP, non-tool-group) tools (sorted by name)
// Built-in tools do not implement the mcpSchemaProvider or ToolGroupProvider interfaces
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
	sort.Strings(names)
	return names
}

// GetToolGroups returns all tool groups (sorted by name).
// Each group contains name, instructions, and tool names.
// Equivalent to GetToolGroupsForChannel("") with no channel filtering.
func (r *Registry) GetToolGroups() []ToolGroupEntry {
	return r.GetToolGroupsForChannel("")
}

// GetToolGroupsForChannel returns tool groups available for the specified channel (sorted by group name)
// When channel is empty, no channel filtering is applied; returns all tool groups
func (r *Registry) GetToolGroupsForChannel(channel string) []ToolGroupEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	groups := make(map[string]*ToolGroupEntry)
	for _, tool := range r.globalTools {
		// Channel filter (empty channel = no filter)
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

	// Convert to slice and sort
	result := make([]ToolGroupEntry, 0, len(groups))
	for _, entry := range groups {
		sort.Strings(entry.ToolNames)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// SetGlobalMCPCatalog sets the global MCP Server catalog (called by MCPManager.RegisterTools)
func (r *Registry) SetGlobalMCPCatalog(catalog []MCPServerCatalogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Defensive copy to prevent callers from modifying the slice and causing race conditions
	r.globalMCPCatalog = append([]MCPServerCatalogEntry{}, catalog...)
}

// GetMCPCatalog returns the full MCP Server catalog (global + session-specific)
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

// GetToolSchemas returns full schema info for specified tools (param definitions, descriptions, etc.)
// Supports built-in and MCP tools. toolNames is a list of full tool names; pass nil to return schemas for all loadable tools.
// if channel is not empty, filter out tools that don't support it.
func (r *Registry) GetToolSchemas(sessionKey string, toolNames []string) []ToolSchema {
	return r.GetToolSchemasForChannel(sessionKey, toolNames, "")
}

// GetToolSchemasForChannel returns tool schema info available for the specified channel
// channel 为空时不进行Channel filter
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
		// Channel filter
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
		} else if !r.coreTools[name] {
			schemas = append(schemas, ToolSchema{
				ToolName:    name,
				Description: tool.Description(),
				Params:      tool.Parameters(),
			})
		}
	}
	r.mu.RUnlock()

	// scan session MCP tools
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			for _, tool := range sm.GetSessionTools() {
				if !matchAll && !nameSet[tool.Name()] {
					continue
				}
				// 会话 MCP 工具暂不做Channel filter（MCP 工具通常是通用的）
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

// DefaultRegistry creates a registry with default tools
// Core tools (RegisterCore) are always in tool definitions; others must be activated via load_tools.
// in flat mode, all tools are core tools; LoadToolsTool is not registered.
// Note: CronTool requires dependency injection, is not in the default registry, and must be registered separately
func DefaultRegistry(memoryProvider string) *Registry {
	r := NewRegistry()
	if memoryProvider == "flat" {
		r.SetFlatMode(true)
	}
	// Core tools: basic file/system operations, always available
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
	r.RegisterCore(&SkillTool{})
	r.RegisterCore(&TaskStatusTool{})
	r.RegisterCore(&TaskKillTool{})
	r.RegisterCore(&TaskReadTool{})
	// CronTool requires dependency injection; register after agent initialization
	// DownloadFileTool and WebSearchTool need credential injection; registered in main.go
	// WebSearch: always available (requires TAVILY_API_KEY)
	r.RegisterCore(NewFetchTool())
	r.RegisterCore(&AskUserTool{})
	// LoadToolsTool is only registered in letta mode (flat mode: all tools directly available)
	if memoryProvider != "flat" {
		r.RegisterCore(&LoadToolsTool{})
	}
	return r
}
