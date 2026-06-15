# 技术方案：Unified Channel-Scoped Tool Injection

> 生成时间：2026-06-15 (v2 — 统一三类 channel 工具)
> 状态：待确认

## 背景与目标

### 问题陈述

用户希望插件系统能支持这样的场景：一个插件接入 GitHub App，当 PR 中 @机器人 时自动 Code Review 并评论。同时，已有代码中飞书专属工具的注册方式需要优化，且 MCP 工具也需要考虑 channel 维度。

### 三类 Channel-Scoped 工具

| 类型 | 来源 | 当前状态 | 典型场景 |
|------|------|----------|----------|
| **A. 内置 Channel 工具** | 编译时静态注册 | ⚠️ 全局注册 + 后置过滤 | 飞书 Card 工具、Feishu MCP 工具 (bitable/wiki/docx) |
| **B. Channel Plugin 工具** | channel 进程运行时声明 | ❌ 不支持 | GitHub App 插件提供 `get_pr_diff`、`post_review_comment` |
| **C. Channel MCP 配置** | channel 声明 MCP server | ❌ 不支持 | GitHub 插件注入 `@modelcontextprotocol/server-github`（带 App 凭证） |

### 当前架构问题

**飞书工具（Type A）的不优雅之处**：

```
当前：注册全局 → 每次 prompt 遍历过滤
  registry.Register(cardCreateTool)     → globalTools["card_create"]
  registry.Register(bitableFieldsTool)  → globalTools["feishu_bitable_fields"]
  ...29 个飞书工具全部进入 globalTools...

每次 callLLM:
  AsDefinitionsForSession → 返回所有 globalTools（含飞书工具）
  visibleToolDefs → 遍历每个工具，检查 ChannelProvider.SupportedChannels()
                   → CLI session: 过滤掉 29 个飞书工具（白做功）
                   → Feishu session: 全部保留
```

问题：
1. **O(N) per-prompt 过滤开销** — 每次构建 prompt 都遍历所有工具做 channel 匹配
2. **两套声明方式** — Card 工具用 `feishuOnlyTool` 包装器，Feishu MCP 工具用 `FeishuToolBase` 嵌入
3. **globalTools 膨胀** — 29 个飞书工具对 CLI/Web/QQ 会话完全无用，却始终在内存和遍历中
4. **flat 模式泄露** — flat 模式下 `Register()` 等同 `RegisterCore()`，飞书工具在 CLI 会话也"可见"（虽然 `visibleToolDefs` 会过滤，但 `load_tools` 的工具列表会泄露）

**MCP 工具的 channel 盲区**：

```
SessionMCPManager.loadConfig() 合并层:
  1. Global config (~/.xbot/mcp.json)        — 所有 channel 共享
  2. User config ({workDir}/.xbot/users/...)  — per-user 隔离

缺少第 3 层：Channel config — 某个 channel 专属的 MCP server
```

### 设计目标

**一个统一的 `channelTools` 维度，三条注册路径**：

```
                    ┌─────────────────────────────────────────┐
                    │          tools.Registry                  │
                    │                                          │
                    │  globalTools     (全局，所有 channel)     │
                    │  coreTools       (始终可见)               │
                    │  tenantTools     (per-tenant)            │
                    │  channelTools    (per-channel) ◀── 统一   │
                    │                                          │
                    └─────────┬───────────┬───────────┬────────┘
                              │           │           │
                    ┌─────────▼──┐ ┌──────▼─────┐ ┌──▼──────────┐
                    │ Type A     │ │ Type B     │ │ Type C      │
                    │ 启动时注册  │ │ 运行时声明  │ │ MCP 配置层  │
                    │ (Feishu)   │ │ (Plugin)   │ │ (Future)    │
                    └────────────┘ └────────────┘ └─────────────┘
```

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `tools/interface.go` | Registry — 工具注册表 | **修改**（核心） |
| `agent/perm_control_helpers.go` | visibleToolDefs — channel 过滤 | **修改**（简化） |
| `agent/engine_wire.go` | buildToolExecutor — 工具查找 | **修改**（增加 channel 查找） |
| `agent/transport_channel_plugin.go` | ChannelPluginTransport | **修改**（协议扩展） |
| `serverapp/channel_plugin.go` | stdioChannelPluginProvider | **修改**（注入 Registry） |
| `serverapp/server.go` | 飞书工具注册点 | **修改**（改为 RegisterForChannel） |
| `agent/agent.go` | Card 工具注册点 | **修改**（改为 RegisterForChannel） |
| `tools/card_tools.go` | feishuOnlyTool 包装器 | **修改**（移除包装器） |
| `tools/feishu_mcp/feishu_mcp.go` | FeishuToolBase | **修改**（移除 SupportedChannels） |
| `plugin/channel_tool_bridge.go` | **新增** — ChannelToolBridge | 新增 |
| `tools/session_mcp.go` | SessionMCPManager | **修改**（Phase 2 — channel config 层） |

### 当前飞书工具注册链路

```
agent/agent.go:764-767 (initStores)
  └─ cardBuilder := tools.NewCardBuilder()
  └─ for _, t := range tools.NewCardTools(cardBuilder) {
         registry.Register(t)  // ← 全局注册，feishuOnlyTool 包装
     }

serverapp/server.go:617-663 (InitServer)
  └─ if cfg.OAuth.Enable:
       feishuMCP := feishu_mcp.NewFeishuMCP(oauthManager, ...)
       ag.RegisterTool(&feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
       ... 22 more tools ...
```

### 当前 channel 过滤机制

```go
// agent/perm_control_helpers.go:49-72
func visibleToolDefs(defs []llm.ToolDefinition, settingsSvc, channel, senderID) {
    for _, d := range defs {
        if cp, ok := d.(ChannelProvider); ok {
            if !containsString(cp.SupportedChannels(), channel) {
                continue  // ← 每次都遍历检查
            }
        }
        out = append(out, d)
    }
}
```

**问题**：这是"注册全局 + 运行时过滤"模式。注册时不区分 channel，每次 prompt 构建时才过滤。

## 详细设计

### 核心原则

> **Register where it belongs, not filter where it doesn't.**

工具在注册时就声明归属的 channel，而不是注册到全局再每次过滤。

### 1. Registry 扩展

#### 1.1 新增 channelTools 维度

```go
// tools/interface.go — Registry struct

type Registry struct {
    mu               sync.RWMutex
    globalTools      map[string]Tool
    coreTools        map[string]bool
    sessionActivated map[string]map[string]int64
    sessionRound     map[string]int64
    maxIdleRounds    int64
    sessionMCPMgr    SessionMCPManagerProvider
    globalMCPCatalog []MCPServerCatalogEntry
    flatMode         bool

    tenantTools   map[int64]map[string]Tool
    tenantToolsMu sync.RWMutex

    // NEW: channel-scoped tools
    channelTools   map[string]map[string]Tool // channel → toolName → Tool
    channelToolsMu sync.RWMutex
}
```

#### 1.2 新增方法

```go
// RegisterForChannel 注册 channel 专属工具。
// 该工具仅对指定 channel 的会话可见，无需 ChannelProvider 接口。
func (r *Registry) RegisterForChannel(channel string, tool Tool) {
    if channel == "" {
        r.Register(tool) // fallback to global
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

// UnregisterChannelTools 移除 channel 的所有工具（channel 关闭时调用）。
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

// GetForSession 统一工具查找：channel → tenant → global。
// 替代 GetForTenant，增加 channel 维度优先查找。
func (r *Registry) GetForSession(name string, tenantID int64, sessionKey string) (Tool, bool) {
    // 1. Channel-scoped tools (highest priority in session context)
    channel := channelFromSessionKey(sessionKey)
    if channel != "" {
        if tool, ok := r.GetChannelTool(channel, name); ok {
            return tool, true
        }
    }
    // 2. Tenant → global (existing logic)
    return r.GetForTenant(name, tenantID)
}

// channelFromSessionKey 从 "channel:chatID" 格式提取 channel。
func channelFromSessionKey(sessionKey string) string {
    idx := strings.IndexByte(sessionKey, ':')
    if idx < 0 {
        return ""
    }
    return sessionKey[:idx]
}
```

#### 1.3 AsDefinitionsForSession 增强

```go
// tools/interface.go — AsDefinitionsForSession

func (r *Registry) AsDefinitionsForSession(sessionKey string, tenantID int64) []llm.ToolDefinition {
    // ... existing logic (core, activated, tenant, MCP) ...

    // NEW: merge channel-scoped tools
    channel := channelFromSessionKey(sessionKey)
    if channel != "" {
        r.channelToolsMu.RLock()
        channelToolMap := r.channelTools[channel]
        // Copy reference under lock, iterate after unlock
        r.channelToolsMu.RUnlock()

        for _, tool := range channelToolMap {
            if !seen[tool.Name()] {
                seen[tool.Name()] = true
                defs = append(defs, tool)
            }
        }
    }

    // ... sort and return ...
}
```

**设计决策**：channel 工具始终对其 channel 可见（类似 coreTools），不走激活机制。理由：
- channel 工具是该 channel 能力的一部分，不是可选加载项
- 避免 LLM 需要额外 `load_tools` 步骤
- ChannelProvider 接口的工具（如 AskUser 支持 cli+feishu）仍在 globalTools 中，由现有过滤机制处理

#### 1.4 GetToolGroupsForChannel 增强

```go
func (r *Registry) GetToolGroupsForChannel(channel string) []ToolGroupEntry {
    // ... existing: iterate globalTools with IsChannelSupported ...

    // NEW: also include channel-scoped tools' groups
    r.channelToolsMu.RLock()
    for _, tool := range r.channelTools[channel] {
        if gp, ok := tool.(ToolGroupProvider); ok {
            // add to groups
        }
    }
    r.channelToolsMu.RUnlock()
}
```

#### 1.5 Clone

```go
func (r *Registry) Clone() *Registry {
    // ... existing clone logic ...
    // channelTools NOT cloned — SubAgents get clean channel set
    return clone
}
```

### 2. Type A — 飞书工具迁移

#### 2.1 移除 feishuOnlyTool 包装器

```go
// tools/card_tools.go — BEFORE
type feishuOnlyTool struct { Tool }
func (t *feishuOnlyTool) SupportedChannels() []string { return []string{"feishu"} }

func NewCardTools(builder *CardBuilder) []Tool {
    wrap := func(t Tool) Tool { return &feishuOnlyTool{Tool: t} }
    return []Tool{ wrap(&CardCreateTool{...}), ... }
}

// tools/card_tools.go — AFTER
func NewCardTools(builder *CardBuilder) []Tool {
    return []Tool{
        &CardCreateTool{builder: builder},    // 不再包装
        &CardAddContentTool{builder: builder},
        // ...
    }
}
```

#### 2.2 移除 FeishuToolBase.SupportedChannels

```go
// tools/feishu_mcp/feishu_mcp.go — BEFORE
type FeishuToolBase struct{}
func (b FeishuToolBase) SupportedChannels() []string { return []string{"feishu"} }

// tools/feishu_mcp/feishu_mcp.go — AFTER
type FeishuToolBase struct{}
// SupportedChannels() 移除 — channel 限制通过 RegisterForChannel 实现
// GroupName() 和 GroupInstructions() 保留（工具组功能不依赖 channel）
```

#### 2.3 注册改为 RegisterForChannel

```go
// agent/agent.go:764-767 (initStores) — BEFORE
cardBuilder := tools.NewCardBuilder()
for _, t := range tools.NewCardTools(cardBuilder) {
    registry.Register(t)
}

// agent/agent.go:764-767 (initStores) — AFTER
cardBuilder := tools.NewCardBuilder()
for _, t := range tools.NewCardTools(cardBuilder) {
    registry.RegisterForChannel("feishu", t) // ← channel-scoped
}
```

```go
// serverapp/server.go:617-663 (InitServer) — BEFORE
if cfg.OAuth.Enable && oauthManager != nil {
    feishuMCP := feishu_mcp.NewFeishuMCP(...)
    ag.RegisterTool(&feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
    ...
}

// serverapp/server.go:617-663 (InitServer) — AFTER
if cfg.OAuth.Enable && oauthManager != nil {
    feishuMCP := feishu_mcp.NewFeishuMCP(...)
    ag.RegisterToolForChannel("feishu", &feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
    ...
}

// agent/agent.go — 新增代理方法
func (a *Agent) RegisterToolForChannel(channel string, tool tools.Tool) {
    a.tools.RegisterForChannel(channel, tool)
}
```

#### 2.4 visibleToolDefs 简化

```go
// agent/perm_control_helpers.go — AFTER
func visibleToolDefs(defs []llm.ToolDefinition, settingsSvc *SettingsService, channel, senderID string) []llm.ToolDefinition {
    if isPermControlEnabledFor(settingsSvc, channel, senderID) {
        return defs
    }
    out := make([]llm.ToolDefinition, 0, len(defs))
    for _, d := range defs {
        // 保留 ChannelProvider 过滤 — 仍有工具使用此接口（如 AskUser 支持 cli+feishu）
        if cp, ok := d.(ChannelProvider); ok {
            supported := cp.SupportedChannels()
            if len(supported) > 0 && !containsString(supported, channel) {
                continue
            }
        }
        // channelTools 已由 AsDefinitionsForSession 按 channel 注入，无需在此过滤
        switch d.Name() {
        case "Shell", "FileCreate", "FileReplace":
            out = append(out, &toolDefFilter{base: d, hiddenArgs: map[string]bool{"run_as": true, "reason": true}})
        default:
            out = append(out, d)
        }
    }
    return out
}
```

**注意**：`visibleToolDefs` 的 `ChannelProvider` 过滤逻辑**保留**。因为仍有工具使用此接口：
- `AskUser` — `["cli", "feishu"]`，在 globalTools 中，需要过滤
- 未来可能有其他跨 channel 工具

但飞书的 29 个工具**不再走此路径**（它们在 channelTools 中，只对 feishu channel 的 session 出现）。过滤的数据量从 N 降到 N-29。

#### 2.5 工具执行路径

```go
// agent/engine_wire.go:908-930 — BEFORE
// 1. session MCP
// 2. globalTools.Get(name)
// 3. tenantTools

// agent/engine_wire.go:908-930 — AFTER
// 1. session MCP
// 2. NEW: channelTools (via GetForSession)
// 3. tenant → global (via GetForTenant, called inside GetForSession)

if !ok {
    tool, ok = a.tools.GetForSession(tc.Name, cfg.Session.TenantID(), sessionKey)
}
```

### 3. Type B — Channel Plugin 工具

#### 3.1 协议扩展

**Channel 进程 → xbot：工具声明**

```jsonc
// channel_tools message
{
  "type": "channel_tools",
  "tools": [
    {
      "name": "get_pr_diff",
      "description": "Get the diff of a pull request",
      "parameters": [
        {"name": "repo", "type": "string", "description": "owner/repo", "required": true},
        {"name": "pr_number", "type": "integer", "description": "PR number", "required": true}
      ]
    }
  ]
}
```

**xbot → Channel 进程：工具执行**

```jsonc
// xbot → plugin RPC request
{
  "type": "rpc", "id": "srv-1", "method": "execute_tool",
  "params": { "name": "get_pr_diff", "input": "{\"repo\":\"octocat/Hello-World\",\"pr_number\":42}" }
}

// plugin → xbot RPC response
{ "id": "srv-1", "result": { "content": "diff --git a/...", "is_error": false } }
```

Channel 进程可以多次发送 `channel_tools`（热更新），后发完全替换。

#### 3.2 ChannelToolBridge

```go
// plugin/channel_tool_bridge.go (新增)

// ChannelToolExecutor 由 ChannelPluginTransport 实现（已有 Call 方法）
type ChannelToolExecutor interface {
    Call(method string, payload json.RawMessage) (json.RawMessage, error)
}

// ChannelToolDecl channel 进程声明的工具定义
type ChannelToolDecl struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  []llm.ToolParam `json:"parameters"`
}

// ChannelToolBridge 将 channel 声明的工具适配为 tools.Tool。
// 执行通过 RPC 代理到 channel 进程。
type ChannelToolBridge struct {
    decl     ChannelToolDecl
    executor ChannelToolExecutor // *ChannelPluginTransport
}

func NewChannelToolBridge(decl ChannelToolDecl, executor ChannelToolExecutor) *ChannelToolBridge {
    return &ChannelToolBridge{decl: decl, executor: executor}
}

func (b *ChannelToolBridge) Name() string             { return b.decl.Name }
func (b *ChannelToolBridge) Description() string       { return b.decl.Description }
func (b *ChannelToolBridge) Parameters() []llm.ToolParam { return b.decl.Parameters }

func (b *ChannelToolBridge) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
    params, _ := json.Marshal(struct {
        Name  string `json:"name"`
        Input string `json:"input"`
    }{Name: b.decl.Name, Input: input})

    resultRaw, err := b.executor.Call("execute_tool", params)
    if err != nil {
        return &tools.ToolResult{Content: fmt.Sprintf("Channel tool error: %v", err), IsError: true}, nil
    }

    var result struct {
        Content string `json:"content"`
        IsError bool   `json:"is_error"`
    }
    json.Unmarshal(resultRaw, &result)
    return &tools.ToolResult{Content: result.Content, IsError: result.IsError}, nil
}
```

#### 3.3 ChannelPluginTransport 扩展

```go
// agent/transport_channel_plugin.go

type ChannelPluginTransport struct {
    // ... existing fields ...
    registry    *tools.Registry // NEW: for channel tool registration
    channelName string
}

// handleIncoming — 新增 channel_tools 分支
func (t *ChannelPluginTransport) handleIncoming(raw json.RawMessage) {
    var peek struct {
        ID     string          `json:"id"`
        Method string          `json:"method"`
        Type   string          `json:"type"`    // NEW
        Result json.RawMessage `json:"result"`
        Error  string          `json:"error"`
    }
    json.Unmarshal(raw, &peek)

    switch {
    case peek.Type == "channel_tools":   // NEW
        t.handleChannelTools(raw)
    case peek.Method != "":
        t.handlePluginRPC(peek.ID, peek.Method, raw)
    case peek.ID != "":
        t.handlePluginResponse(peek.ID, peek.Result, peek.Error)
    }
}

func (t *ChannelPluginTransport) handleChannelTools(raw json.RawMessage) {
    var msg struct {
        Tools []plugin.ChannelToolDecl `json:"tools"`
    }
    if err := json.Unmarshal(raw, &msg); err != nil {
        return
    }
    if t.registry == nil {
        return
    }
    // Hot-update: clear old, register new
    t.registry.UnregisterChannelTools(t.channelName)
    for _, decl := range msg.Tools {
        bridge := plugin.NewChannelToolBridge(decl, t) // t implements ChannelToolExecutor
        t.registry.RegisterForChannel(t.channelName, bridge)
    }
}

// Stop — 清理 channel tools
func (t *ChannelPluginTransport) Stop() {
    t.closeOnce.Do(func() {
        close(t.closeCh)
        t.process.close()
        if t.registry != nil {
            t.registry.UnregisterChannelTools(t.channelName)
        }
    })
}
```

#### 3.4 Provider 注入 Registry

```go
// serverapp/channel_plugin.go

type stdioChannelPluginProvider struct {
    decl     *plugin.ChannelProviderDecl
    msgBus   *bus.MessageBus
    rpcDisp  func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
    registry *tools.Registry // NEW
}

func (p *stdioChannelPluginProvider) CreateChannel(cfg map[string]string, msgBus *bus.MessageBus) (channel.Channel, error) {
    // ... existing spawn logic ...
    transport := agent.NewChannelPluginTransport(
        agent.WithName(p.decl.Name),
        agent.WithProcess(proc),
        agent.WithDispatch(p.rpcDisp),
        agent.WithRegistry(p.registry), // NEW
    )
    // ...
}
```

```go
// serverapp/server.go:479 — ChannelProviderFactory
plugin.SetChannelProviderFactory(func(decl, _) (any, error) {
    return &stdioChannelPluginProvider{
        decl:     decl,
        rpcDisp:  ...,
        registry: ag.Tools(), // NEW: pass agent's registry
    }, nil
})
```

### 4. Type C — Channel MCP 配置（Phase 2 设计）

> 此部分为架构设计，实施在 Phase 2。

#### 4.1 场景

Channel plugin 可能需要注入 MCP server（而非直接声明工具）。例如：
- GitHub 插件注入 `@modelcontextprotocol/server-github`，使用 App 凭证
- 飞书插件注入飞书 MCP server，使用 OAuth token

#### 4.2 协议

```jsonc
// channel_mcp message: plugin → xbot
{
  "type": "channel_mcp",
  "servers": {
    "github-api": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "ghs_xxx" }
    }
  }
}
```

#### 4.3 SessionMCPManager 扩展

```go
// tools/session_mcp.go — Phase 2

type SessionMCPManager struct {
    // ... existing fields ...
    channelConfig *MCPConfig // NEW: channel-specific MCP config (in-memory)
}

func (sm *SessionMCPManager) SetChannelConfig(cfg *MCPConfig) {
    sm.mu.Lock()
    defer sm.mu.Unlock()
    if !mcpConfigEqual(sm.channelConfig, cfg) {
        sm.channelConfig = cfg
        sm.initialized = false // trigger reconnect
    }
}

// loadConfig — 增加第三层
func (sm *SessionMCPManager) loadConfig() (*MCPConfig, error) {
    merged := &MCPConfig{MCPServers: map[string]MCPServerConfig{}}
    // 1. Global config (~/.xbot/mcp.json)
    // 2. User config ({workDir}/.xbot/users/{sender}/mcp.json)
    // 3. NEW: Channel config
    if sm.channelConfig != nil {
        for name, server := range sm.channelConfig.MCPServers {
            merged.MCPServers[name] = server // channel overrides user overrides global
        }
    }
    return merged, nil
}
```

#### 4.4 Channel MCP 配置路由

```go
// MultiTenantSession 新增 channel MCP 配置缓存
type MultiTenantSession struct {
    // ... existing fields ...
    channelMCPConfigs map[string]*MCPConfig // channel → MCP config
    channelMCPMu      sync.RWMutex
}

func (m *MultiTenantSession) SetChannelMCPConfig(channel string, cfg *MCPConfig) {
    m.channelMCPMu.Lock()
    defer m.channelMCPMu.Unlock()
    m.channelMCPConfigs[channel] = cfg
    // 通知该 channel 的所有 session 更新 MCP 配置
    m.invalidateSessionsByChannel(channel)
}

func (m *MultiTenantSession) ConfigureSessionMCP(channel, chatID, senderID, workDir string) ([]string, error) {
    sess := m.GetOrCreateSession(channel, chatID)
    mgr := sess.GetMCPManager()

    // NEW: inject channel MCP config
    m.channelMCPMu.RLock()
    channelCfg := m.channelMCPConfigs[channel]
    m.channelMCPMu.RUnlock()
    mgr.SetChannelConfig(channelCfg)

    // ... existing scope update ...
}
```

#### 4.5 ChannelPluginTransport 处理

```go
// agent/transport_channel_plugin.go — Phase 2

func (t *ChannelPluginTransport) handleIncoming(raw json.RawMessage) {
    // ...
    switch {
    case peek.Type == "channel_tools": t.handleChannelTools(raw)
    case peek.Type == "channel_mcp":   t.handleChannelMCP(raw)  // NEW
    // ...
    }
}

func (t *ChannelPluginTransport) handleChannelMCP(raw json.RawMessage) {
    var msg struct {
        Servers map[string]tools.MCPServerConfig `json:"servers"`
    }
    json.Unmarshal(raw, &msg)
    if t.onChannelMCP != nil {
        t.onChannelMCP(t.channelName, &tools.MCPConfig{MCPServers: msg.Servers})
    }
}
```

### 5. 工具可见性决策矩阵（设计后）

| 工具类型 | 存储位置 | 可见性条件 | 执行查找 |
|----------|----------|-----------|---------|
| **Core 工具** (Shell, Read...) | `globalTools` + `coreTools` | 始终可见 | `GetForSession` → `Get` |
| **全局非 Core 工具** (load_tools 激活) | `globalTools` | `ActivateTools` + `maxIdleRounds` | `GetForSession` → `Get` |
| **Tenant 工具** (per-tenant 插件) | `tenantTools` | `tenantID` 匹配 | `GetForSession` → `GetForTenant` |
| **Channel 工具** (飞书 + Plugin) | `channelTools` | sessionKey 的 channel 匹配 | `GetForSession` → `GetChannelTool` |
| **Session MCP 工具** | `SessionMCPManager` | 激活 + `maxIdleRounds` | `buildToolExecutor` MCP 分支 |
| **ChannelProvider 工具** (AskUser) | `globalTools` | `SupportedChannels()` 包含当前 channel | `GetForSession` → `Get` |

**关键区别**：
- `channelTools`（Type A+B）= 注册时就限定 channel，**不需要 ChannelProvider 接口**
- `ChannelProvider`（AskUser 等）= 注册在 globalTools，通过 `visibleToolDefs` 过滤
- 两者互补，不互斥

## 完整数据流（设计后）

### 场景一：飞书用户调用 bitable 工具

```
Feishu webhook → msgBus.Inbound → Agent
  → sessionKey = "feishu:oc_xxx"
  → callLLM()
    → AsDefinitionsForSession("feishu:oc_xxx", tenantID)
      → core tools (Shell, Read, ...)
      → channelTools["feishu"] = [card_create, feishu_bitable_fields, ...29个] ✨
      → (globalTools 中不再有飞书工具)
    → visibleToolDefs → 无需过滤飞书工具（不在 defs 中）
  → LLM 调用 feishu_bitable_fields
    → GetForSession("feishu_bitable_fields", ..., "feishu:oc_xxx")
      → GetChannelTool("feishu", "feishu_bitable_fields") → ✅
    → tool.Execute → FeishuMCP API
```

### 场景二：GitHub 插件自动 CR

```
1. Channel 进程启动 → 声明 channel_tools
   → ChannelPluginTransport.handleChannelTools
   → Registry.RegisterForChannel("github", ChannelToolBridge × 2)

2. GitHub webhook (@code-reviewer)
   → Channel 进程 → send_inbound RPC
   → msgBus.Inbound → Agent
     → sessionKey = "github:octocat/Hello-World-pr-42"
     → callLLM()
       → AsDefinitionsForSession("github:...")
         → core tools
         → channelTools["github"] = [get_pr_diff, post_review_comment] ✨
       → LLM 看到 get_pr_diff, post_review_comment

3. LLM 调用 get_pr_diff
   → GetForSession → GetChannelTool("github", "get_pr_diff")
   → ChannelToolBridge.Execute
     → ChannelPluginTransport.Call("execute_tool", {name, input})
     → channel 进程 → GitHub API → diff
   → 返回 diff

4. LLM 生成 review → 调用 post_review_comment
   → 同上 RPC → channel 进程 → GitHub API → 评论发布
```

### 场景三：CLI 用户（不应看到飞书和 GitHub 工具）

```
CLI input → sessionKey = "cli:/home/user/project"
  → callLLM()
    → AsDefinitionsForSession("cli:...", tenantID)
      → core tools
      → channelTools["cli"] = (不存在) → 空 ✨
      → globalTools (含 AskUser，SupportedChannels=["cli","feishu"])
    → visibleToolDefs → AskUser 通过（cli 在支持列表中）
  → LLM 只看到 CLI 相关工具，不浪费 token 在飞书/GitHub 工具上
```

## 实施计划

### Phase 1：Registry 核心 + 飞书迁移

**目标**：建立 channelTools 基础设施，迁移飞书工具

- [ ] **1.1** Registry 新增 `channelTools` 字段 + `channelToolsMu` — `tools/interface.go`
- [ ] **1.2** 实现 `RegisterForChannel`、`UnregisterChannelTools`、`GetChannelTool`、`channelFromSessionKey` — `tools/interface.go`
- [ ] **1.3** `AsDefinitionsForSession` 合并 channelTools — `tools/interface.go:375`
- [ ] **1.4** 实现 `GetForSession`（channel → tenant → global 查找链）— `tools/interface.go`
- [ ] **1.5** `GetToolGroupsForChannel` 合并 channelTools 的工具组 — `tools/interface.go:652`
- [ ] **1.6** 移除 `feishuOnlyTool` 包装器 — `tools/card_tools.go`
- [ ] **1.7** 移除 `FeishuToolBase.SupportedChannels()` — `tools/feishu_mcp/feishu_mcp.go`
- [ ] **1.8** `agent/agent.go` Card 工具注册改为 `RegisterForChannel("feishu", t)`
- [ ] **1.9** `serverapp/server.go` Feishu MCP 工具注册改为 `RegisterToolForChannel("feishu", ...)`
- [ ] **1.10** `Agent.RegisterToolForChannel` 代理方法 — `agent/agent.go`
- [ ] **1.11** `engine_wire.go` 工具查找改为 `GetForSession` — `agent/engine_wire.go:922`
- [ ] **1.12** 单元测试：channelTools 注册/可见性/查找/清理
- [ ] **1.13** 验证：飞书会话看到飞书工具，CLI 会话看不到

### Phase 2：Channel Plugin 工具

**目标**：Channel 进程可以声明和执行工具

- [ ] **2.1** 新建 `plugin/channel_tool_bridge.go`：`ChannelToolBridge`、`ChannelToolDecl`、`ChannelToolExecutor`
- [ ] **2.2** `protocol/ws.go` 新增 `MsgTypeChannelTools` 常量
- [ ] **2.3** `ChannelPluginTransport.handleIncoming` 新增 `channel_tools` 分支 — `agent/transport_channel_plugin.go`
- [ ] **2.4** `ChannelPluginTransport` 构造函数新增 `WithRegistry` option
- [ ] **2.5** `ChannelPluginTransport.Stop()` 清理 channelTools
- [ ] **2.6** `stdioChannelPluginProvider` 注入 Registry — `serverapp/channel_plugin.go`
- [ ] **2.7** `ChannelProviderFactory` 传入 `ag.Tools()` — `serverapp/server.go`、`cmd/xbot-cli/main.go`
- [ ] **2.8** `spawnChannelProcess` 增加 `cmd.Stderr` 捕获（顺手修）
- [ ] **2.9** 单元测试：handleChannelTools mock + ChannelToolBridge mock executor
- [ ] **2.10** 集成测试：echo-channel 声明工具 → agent 调用

### Phase 3：Channel MCP 配置

**目标**：Channel 进程可以声明 MCP server

- [ ] **3.1** `SessionMCPManager` 新增 `channelConfig` 字段 + `SetChannelConfig` — `tools/session_mcp.go`
- [ ] **3.2** `loadConfig` 增加第三层 channel config 合并 — `tools/session_mcp.go:384`
- [ ] **3.3** `MultiTenantSession` 新增 `channelMCPConfigs` 缓存 — `session/multitenant.go`
- [ ] **3.4** `ConfigureSessionMCP` 注入 channel MCP config — `session/multitenant.go:337`
- [ ] **3.5** `ChannelPluginTransport` 新增 `channel_mcp` 消息处理 — `agent/transport_channel_plugin.go`
- [ ] **3.6** `protocol/ws.go` 新增 `MsgTypeChannelMCP` 常量
- [ ] **3.7** 单元测试：channel config 合并、session 隔离

### Phase 4：示例插件

- [ ] **4.1** GitHub App channel plugin（Python）— `plugin/examples/github-reviewer/`
- [ ] **4.2** plugin.json manifest
- [ ] **4.3** webhook 接收 + send_inbound
- [ ] **4.4** channel_tools 声明 + execute_tool 处理
- [ ] **4.5** 端到端集成测试

## 验证方案

### Phase 1 验证
- **Registry channelTools 单测**：注册 → AsDefinitionsForSession 可见 → 非 channel 不可见 → UnregisterChannelTools 清理
- **GetForSession 单测**：channel tool 优先于 global tool（同名场景）→ 非 channel 回退 global
- **飞书工具迁移验证**：CLI 运行 `go test ./...` 全通过；手动验证飞书会话工具可用

### Phase 2 验证
- **ChannelToolBridge 单测**：mock executor 验证 RPC 序列化/反序列化
- **handleChannelTools 单测**：mock processIO 验证消息解析和 Registry 注册
- **集成测试**：echo-channel 声明工具 → 验证 agent 能调用

### Phase 3 验证
- **channel config 合并单测**：global + user + channel 三层合并优先级
- **session 隔离单测**：channel A 的 MCP 工具不出现在 channel B 的 session

## 风险与缓解

| 风险 | 影响 | 缓解措施 |
|------|------|----------|
| 飞书工具迁移后 `visibleToolDefs` 遗漏 | 飞书工具在非飞书 channel 可见 | channelTools 不经过 visibleToolDefs — 它们只在匹配 channel 的 AsDefinitionsForSession 中出现 |
| ChannelProvider 接口工具（AskUser）与 channelTools 的关系 | 可能混淆两套机制 | 文档明确：channelTools = 注册时限定（不可变）；ChannelProvider = 运行时过滤（灵活）。AskUser 支持 cli+feishu 属于后者 |
| Channel 进程崩溃 → 工具不可用 | LLM 调用时 RPC 失败 | ChannelToolBridge.Execute 返回 `is_error=true`，LLM 可理解并告知用户 |
| sessionKey 格式异常 | channelFromSessionKey 解析失败 | 返回空字符串，跳过 channelTools 合并 — 安全降级 |
| 工具名冲突 | channel tool 与 global tool 同名 | GetForSession 中 channel 优先（设计意图）；同名时 channel 上下文使用 channel 版本 |
| `RegisterForChannel` 的 flat 模式行为 | flat 模式下 channel 工具是否自动可见 | channelTools **不受 flatMode 影响** — 它们始终对其 channel 可见，不走 core/activate 逻辑 |
| MCP channel config 触发全量重连 | 用户 session 的 MCP 连接中断 | `SetChannelConfig` 仅在配置变化时重置 `initialized`，且热更新频率低 |

## 对现有系统的影响

### 向后兼容性

| 改动 | 兼容性 | 说明 |
|------|--------|------|
| Registry 新增 channelTools | ✅ 完全兼容 | 新增字段和方法，不修改现有 API |
| AsDefinitionsForSession | ✅ 签名不变 | 内部增加 channelTools 合并 |
| visibleToolDefs | ✅ 逻辑不变 | 仍保留 ChannelProvider 过滤（AskUser 等仍需要） |
| feishuOnlyTool 移除 | ⚠️ 内部改动 | 外部行为不变 — 飞书工具仍只对 feishu 可见，但通过 channelTools 而非过滤实现 |
| FeishuToolBase.SupportedChannels 移除 | ⚠️ 内部改动 | 同上 |
| GetForSession 替代 GetForTenant | ✅ 向后兼容 | 新方法封装旧方法查找链 |

### 性能影响

| 指标 | Before | After | 说明 |
|------|--------|-------|------|
| AsDefinitionsForSession (CLI session) | O(N) globalTools 遍历 | O(N-29) globalTools + O(1) channelTools lookup | 减少 29 个飞书工具的遍历 |
| visibleToolDefs | O(N) ChannelProvider 检查 | O(N-29) | 飞书工具不再进入此函数 |
| AsDefinitionsForSession (Feishu session) | O(N) globalTools | O(N-29) globalTools + O(29) channelTools | 相当，但语义更清晰 |
| GetForSession | O(1) Get + O(1) tenant | O(1) channel + O(1) tenant + O(1) global | 额外一次 map lookup |

## ✅ 自审通过

- [x] **目标一致性**：三类工具统一到 channelTools，每个阶段服务于统一目标
- [x] **步骤可执行性**：所有改动精确到文件和函数级别
- [x] **遗漏检查**：
  - 飞书 Card 工具（6个）✅ 移除包装器
  - 飞书 MCP 工具（23个）✅ 移除 SupportedChannels
  - ChannelProvider 接口（AskUser 等）✅ 保留 visibleToolDefs 过滤
  - MCP channel config ✅ Phase 3 设计
- [x] **依赖检查**：Phase 1（Registry+飞书）→ Phase 2（Plugin 工具）→ Phase 3（Plugin MCP）→ Phase 4（示例）
- [x] **文件准确性**：所有文件路径和行号已通过代码验证
- [x] **风险评估**：7 个风险点均有缓解措施
- [x] **计划自洽性**：channelTools 是唯一真相源，三条注册路径汇聚到同一存储
