package tools

import (
	"sort"
	"strings"
	"sync"

	"xbot/llm"
)

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
