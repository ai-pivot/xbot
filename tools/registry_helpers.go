package tools

import (
	"slices"
	"sort"

	"xbot/llm"
)

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
