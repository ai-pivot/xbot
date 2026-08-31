package tools

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPConnections = 20

// errNotInitialized indicates MCP config files don't exist yet.
// The caller should NOT set initialized=true so that the next access retries.
var errNotInitialized = fmt.Errorf("MCP config not found, will retry on next access")

// SessionMCPManager is the per-session VIEW over the shared MCP connection
// pool (tools/mcp_pool.go). All connections live in pool entries keyed by
// the scope 4-tuple (globalConfigPath, userConfigPath, workspaceRoot,
// userID) — N sessions with the same tuple share ONE set of MCP child
// processes.
//
// Historical bugs fixed by pooling:
//   - per-session connection duplication: N sessions × M stdio servers
//     spawned N×M processes for identical config;
//   - the 30-minute inactivity timeout was a dead parameter (unload only
//     ran for sessions evicted from the 24h session cache) — the pool
//     reaper now reclaims idle entries (refCount==0 && idle>30min) every
//     30s, independent of session caches;
//   - unloaded servers never reconnected (initOnce stayed 2) — a reaped
//     pool entry is deleted; the next Acquire builds a fresh one;
//   - UpdateScope disconnected everything on a scope change — it now
//     detaches from the old entry (kept alive for other sharers) and
//     attaches to the new one.
type SessionMCPManager struct {
	mu                sync.RWMutex
	sessionKey        string        // "channel:chatID" (logging only)
	userID            string        // 沙箱容器标识（池 key 的一部分）
	globalConfigPath  string        // 全局 mcp.json 路径（只读）
	userConfigPath    string        // 用户 mcp.json 路径（可写）
	workspaceRoot     string        // 用户命令执行工作区
	entry             *mcpPoolEntry // shared pool entry (lazy-attached)
	sessionLastUsed   time.Time     // 会话级别活跃时间（兼容清理链）
	inactivityTimeout time.Duration // Deprecated: pool reaper owns idle reclamation (kept for API compat)
	closed            uint32        // atomic: 1 = Close() has been called
	onChange          func()        // init-complete callback（转发到池条目）
}

// NewSessionMCPManager 创建会话 MCP 管理器（池视图）。
// 连接在首次使用时从全局池按 scope 4-tuple 懒加载获取。
func NewSessionMCPManager(sessionKey, userID, globalConfigPath, userConfigPath, workspaceRoot string, inactivityTimeout time.Duration) *SessionMCPManager {
	return &SessionMCPManager{
		sessionKey:        sessionKey,
		userID:            userID,
		globalConfigPath:  globalConfigPath,
		userConfigPath:    userConfigPath,
		workspaceRoot:     workspaceRoot,
		sessionLastUsed:   time.Now(),
		inactivityTimeout: inactivityTimeout,
	}
}

// UpdateScope 更新当前会话可见的用户配置与工作区。
// 作用域变化时切换池条目——不断开旧条目的连接（其它共享者继续使用；
// 无人引用后由池 reaper 回收）。
func (sm *SessionMCPManager) UpdateScope(userID, userConfigPath, workspaceRoot string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.userID == userID && sm.userConfigPath == userConfigPath && sm.workspaceRoot == workspaceRoot {
		return
	}

	old := sm.entry
	sm.userID = userID
	sm.userConfigPath = userConfigPath
	sm.workspaceRoot = workspaceRoot
	sm.entry = globalMCPPool.Acquire(sm.globalConfigPath, userConfigPath, workspaceRoot, userID)
	if old != nil {
		old.removeOnChange(sm)
		globalMCPPool.Release(old)
	}
	if sm.onChange != nil {
		sm.entry.addOnChange(sm, sm.onChange)
	}
	log.WithFields(log.Fields{
		"session": sm.sessionKey,
		"user":    userID,
	}).Debug("Session MCP scope switched (pool entry swap, no disconnect)")
}

// ensureEntry returns the current pool entry, lazily acquiring one on first
// use (or re-acquiring after the previous entry was invalidated/reaped).
// Callers must not hold sm.mu.
func (sm *SessionMCPManager) ensureEntry() *mcpPoolEntry {
	sm.mu.Lock()
	if sm.entry == nil || sm.entry.isClosed() {
		old := sm.entry
		sm.entry = globalMCPPool.Acquire(sm.globalConfigPath, sm.userConfigPath, sm.workspaceRoot, sm.userID)
		if old != nil {
			old.removeOnChange(sm)
			globalMCPPool.Release(old)
		}
		if sm.onChange != nil {
			sm.entry.addOnChange(sm, sm.onChange)
		}
	}
	entry := sm.entry
	sm.mu.Unlock()
	return entry
}

// GetCatalog 返回此会话所有已连接 MCP Server 的目录信息。
// 首次调用时启动后台初始化（非阻塞），立即返回空 catalog。
func (sm *SessionMCPManager) GetCatalog() []MCPServerCatalogEntry {
	entry := sm.ensureEntry()
	entry.ensureInitAsync(nil)

	var catalog []MCPServerCatalogEntry
	for _, conn := range entry.Snapshot() {
		toolNames := make([]string, len(conn.tools))
		for i, t := range conn.tools {
			toolNames[i] = t.Name
		}
		catalog = append(catalog, MCPServerCatalogEntry{
			Name:         conn.name,
			Instructions: conn.instructions,
			ToolNames:    toolNames,
		})
	}
	return catalog
}

// GetCatalogBlocking blocks until initialization is complete, then returns the catalog.
func (sm *SessionMCPManager) GetCatalogBlocking() []MCPServerCatalogEntry {
	entry := sm.ensureEntry()
	entry.ensureInitAsync(nil)
	entry.InitWait()
	return sm.GetCatalog()
}

// SetOnChange registers a callback invoked after background initialization completes.
// Must be called before GetCatalog to guarantee the callback fires.
func (sm *SessionMCPManager) SetOnChange(fn func()) {
	sm.mu.Lock()
	sm.onChange = fn
	entry := sm.entry
	sm.mu.Unlock()
	if entry != nil {
		entry.addOnChange(sm, fn)
	}
}

// GetSessionTools 懒加载并返回此会话的 MCP 工具（非阻塞）。
// 首次调用时启动后台初始化，立即返回已有工具列表。
func (sm *SessionMCPManager) GetSessionTools() []Tool {
	entry := sm.ensureEntry()
	entry.ensureInitAsync(nil)

	sm.mu.Lock()
	sm.sessionLastUsed = time.Now()
	sm.mu.Unlock()
	entry.touch()

	var tools []Tool
	for _, conn := range entry.Snapshot() {
		for _, tool := range conn.tools {
			remoteTool := newSessionMCPRemoteTool(conn.name, tool, conn.session, sm)
			tools = append(tools, remoteTool)
		}
	}
	return tools
}

// MarkActive 标记服务器为活跃状态（池条目级 touch）。
func (sm *SessionMCPManager) MarkActive(serverName string) {
	sm.mu.Lock()
	sm.sessionLastUsed = time.Now()
	entry := sm.entry
	sm.mu.Unlock()
	if entry != nil {
		entry.touch()
	}
}

// UnloadInactiveServers 兼容接口：返回会话最后活跃时间（用于判断会话
// 是否需要从缓存中移除）。闲置连接回收由池 reaper 统一处理
// （refCount==0 && idle > 30min，独立于会话缓存驱逐）。
func (sm *SessionMCPManager) UnloadInactiveServers() time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.sessionLastUsed
}

// Close 释放会话对池条目的引用（refCount--；连接在无人引用且闲置后由
// 池 reaper 回收，或立即被其它共享者继续使用）。
func (sm *SessionMCPManager) Close() {
	atomic.StoreUint32(&sm.closed, 1)

	sm.mu.Lock()
	entry := sm.entry
	sm.entry = nil
	sm.mu.Unlock()
	if entry != nil {
		entry.removeOnChange(sm)
		globalMCPPool.Release(entry)
		log.WithField("session", sm.sessionKey).Debug("Session MCP released pool entry")
	}
}

// Invalidate 重置池条目，强制下次调用时重新加载配置。
// 该 scope 的池条目被关闭并从池中移除——所有共享该 scope 的会话
// 下次访问时重建连接（读取新配置）。
func (sm *SessionMCPManager) Invalidate() {
	sm.mu.Lock()
	entry := sm.entry
	sm.entry = nil
	sm.mu.Unlock()
	if entry != nil {
		entry.removeOnChange(sm)
		globalMCPPool.Release(entry)
	}
	globalMCPPool.Invalidate(sm.globalConfigPath, sm.userConfigPath, sm.workspaceRoot, sm.userID)
	log.WithField("session", sm.sessionKey).Info("Session MCP invalidated, will reload on next use")
}

// ---- SessionMCPRemoteTool: 会话感知的 MCP 远程工具 ----

// SessionMCPRemoteTool 封装一个远程 MCP 工具为 xbot Tool（会话感知）
type SessionMCPRemoteTool struct {
	serverName    string
	tool          *mcp.Tool
	session       *mcp.ClientSession
	sessionMCPMgr *SessionMCPManager // 会话 MCP 管理器
	params        []llm.ToolParam
	description   string
}

// newSessionMCPRemoteTool 创建 SessionMCPRemoteTool
func newSessionMCPRemoteTool(serverName string, tool *mcp.Tool, session *mcp.ClientSession, sessionMCPMgr *SessionMCPManager) *SessionMCPRemoteTool {
	params := convertMCPParams(tool)
	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", serverName)
	}

	return &SessionMCPRemoteTool{
		serverName:    serverName,
		tool:          tool,
		session:       session,
		sessionMCPMgr: sessionMCPMgr,
		params:        params,
		description:   desc,
	}
}

func (t *SessionMCPRemoteTool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", t.serverName, t.tool.Name)
}

func (t *SessionMCPRemoteTool) Description() string {
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, t.description)
}

func (t *SessionMCPRemoteTool) Parameters() []llm.ToolParam {
	// Stub mode: return nil so full schemas are not loaded into LLM context.
	// (Schema is always provided via mcpSchemaProvider in AsDefinitionsForSession.)
	return nil
}

// fullDescription returns the original server description.
func (t *SessionMCPRemoteTool) fullDescription() string {
	return t.description
}

// fullParams returns the complete parameter list.
func (t *SessionMCPRemoteTool) fullParams() []llm.ToolParam {
	return t.params
}

// mcpServerName returns the MCP server name this tool belongs to.
func (t *SessionMCPRemoteTool) mcpServerName() string {
	return t.serverName
}

func (t *SessionMCPRemoteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	if t.sessionMCPMgr != nil {
		t.sessionMCPMgr.MarkActive(t.serverName)
	}

	// 检查 session 是否仍然有效（可能已被 Close/Invalidate 关闭）
	if t.session == nil {
		return nil, fmt.Errorf("MCP session for server %q has been closed", t.serverName)
	}

	args := map[string]any{}
	if input != "" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	result, err := t.session.CallTool(ctx.Ctx, &mcp.CallToolParams{
		Name:      t.tool.Name,
		Arguments: args,
	})
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"server": t.serverName,
			"tool":   t.tool.Name,
		}).Warn("MCP tool call failed")
		return nil, fmt.Errorf("MCP call %s/%s: %w", t.serverName, t.tool.Name, err)
	}

	content := formatMCPResult(result)

	if result.IsError {
		log.WithFields(log.Fields{
			"server": t.serverName,
			"tool":   t.tool.Name,
		}).Warnf("MCP tool returned error: %s", content)
		return NewResult("Error: " + content), nil
	}

	return NewResult(content), nil
}

// ---- MCP 工具激活机制 ----

// GetActivatedToolDefs 返回已激活 MCP 工具的 LLM 工具定义（含完整参数 schema）。
// kept for backward compatibility — all tools are always visible now.
func (sm *SessionMCPManager) GetActivatedToolDefs(activated map[string]bool) []llm.ToolDefinition {
	if len(activated) == 0 {
		return nil
	}
	entry := sm.ensureEntry()

	var defs []llm.ToolDefinition
	for _, conn := range entry.Snapshot() {
		for _, tool := range conn.tools {
			fullName := fmt.Sprintf("mcp_%s_%s", conn.name, tool.Name)
			if !activated[fullName] {
				continue
			}
			params := convertMCPParams(tool)
			desc := tool.Description
			if desc == "" {
				desc = fmt.Sprintf("MCP tool from %s", conn.name)
			}
			defs = append(defs, &mcpToolDefinition{
				name:   fullName,
				desc:   fmt.Sprintf("[MCP:%s] %s", conn.name, desc),
				params: params,
			})
		}
	}
	return defs
}

// mcpToolDefinition 是已激活 MCP 工具的 LLM 工具定义（含完整参数 schema）。
type mcpToolDefinition struct {
	name   string
	desc   string
	params []llm.ToolParam
}

func (d *mcpToolDefinition) Name() string                { return d.name }
func (d *mcpToolDefinition) Description() string         { return d.desc }
func (d *mcpToolDefinition) Parameters() []llm.ToolParam { return d.params }
