# 计划：Runner 架构重构 — Agent/Runner 分层 + 工具归属重定义

> 生成时间：2026-06-25
> 状态：待确认

## 核心原则

```
Agent 层 = LLM 循环 + 编排工具。零执行能力。
Runner 层 = 工具执行环境。所有 Shell/文件/MCP/网络能力由 Runner 提供。

工具来源（按优先级）：
  1. Agent 核心编排工具
  2. Session 绑定的 Runner 声明的工具（默认 local runner）
  3. Channel 声明的工具
  4. Plugin 声明的工具
```

**Agent 不内置 Shell。不内置 Read。不内置 MCP。** 这些是本地 Runner 提供的。Agent 只负责：组装 prompt → 调 LLM → 解析 tool 调用 → 路由到正确的 ToolProvider → 返回结果。

---

## 一、现状分析

### 当前架构问题

```
┌─────────────────────────────────────────────────────────┐
│                    Agent 层（当前）                        │
│                                                           │
│  ❌ 内置 40+ 工具（Shell/Read/MCP/SubAgent/...）            │
│  ❌ Sandbox 接口（17 方法）是 agent 层的概念                 │
│  ❌ MCP 管理（ManageTools）在 agent 层                      │
│  ❌ 工具注册表（Registry）只有 global/tenant/channel 维度    │
│  ❌ SandboxRouter 是全局单例                               │
│  ❌ Runner 不是一等公民（隐藏在 RemoteSandbox 内）           │
└─────────────────────────────────────────────────────────┘
```

### 目标架构

```
┌─────────────────────────────────────────────────────────┐
│                  Agent 层（极薄）                           │
│                                                           │
│  ✅ LLM 循环：buildPrompt → Generate → executeToolCalls   │
│  ✅ 编排工具：SubAgent, CreateChat, SendMessage, Cron,     │
│              EventTrigger, TodoWrite, AskUser,             │
│              context_edit, config, tui_control             │
│  ✅ Tool 路由：agentCore → runner → channel → plugin       │
│  ✅ 零执行能力 — 不知 Sandbox/MCP/文件/Shell 为何物         │
└─────────────────────────────────────────────────────────┘
          │ Tool 路由
          ▼
┌─────────────────────────────────────────────────────────┐
│              工具来源（ToolProvider）                       │
│                                                           │
│  ┌──────────┐  ┌──────────────┐  ┌────────┐  ┌────────┐ │
│  │ Agent    │  │ Runner       │  │ Channel│  │ Plugin │ │
│  │ Core     │  │ (per-session)│  │ Tools  │  │ Tools  │ │
│  │ Tools    │  │              │  │        │  │        │ │
│  └──────────┘  └──────────────┘  └────────┘  └────────┘ │
│      9 个        动态（取决于          现有        现有    │
│                   runner 能力）                            │
└─────────────────────────────────────────────────────────┘
          │
          ▼
┌─────────────────────────────────────────────────────────┐
│                 Runner 层                                  │
│                                                           │
│  ┌───────────────────────────────────────────────────┐   │
│  │ LocalRunner（默认）                                 │   │
│  │                                                    │   │
│  │ Tools: Shell, Read, Write, Grep, Glob, Cd,         │   │
│  │        Fetch, WebSearch, DownloadFile, Worktree,    │   │
│  │        Skill, TaskManager, + MCP tools              │   │
│  │                                                    │   │
│  │ Transport: ChannelTransport（进程内）               │   │
│  │ MCP: 直接管理 MCP 服务器连接                         │   │
│  │ Sandbox: 内部执行抽象（agent 不知）                  │   │
│  └───────────────────────────────────────────────────┘   │
│                                                           │
│  ┌───────────────────────────────────────────────────┐   │
│  │ RemoteRunner（可选）                                │   │
│  │                                                    │   │
│  │ Tools: 连接时声明（可能和 local 不同）              │   │
│  │ Transport: RemoteTransport（WebSocket）             │   │
│  │ MCP: runner 进程管理 MCP 连接                       │   │
│  └───────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

---

## 二、架构设计

### 2.1 ToolProvider 模型

所有工具来源统一为一个接口：

```go
// tools/tool_provider.go (新建)

// ToolProvider 是工具来源的统一抽象。
// Agent 不关心工具来自哪里，只通过此接口获取和调用。
type ToolProvider interface {
    // Name 返回 provider 标识（用于日志/调试）
    Name() string

    // ListTools 返回此 provider 对指定 session 可见的所有工具
    ListTools(sessionKey string, tenantID int64) []Tool

    // GetTool 按名称查找工具。未找到返回 nil, false。
    GetTool(sessionKey string, tenantID int64, name string) (Tool, bool)

    // Priority 返回查找优先级（数值越小越优先）
    Priority() int
}
```

**四个 ToolProvider 实现**：

| Provider | Priority | 工具来源 | 示例 |
|----------|----------|----------|------|
| `AgentToolProvider` | 1 | Agent 核心编排工具 | SubAgent, CreateChat, SendMessage, Cron, ... |
| `RunnerToolProvider` | 2 | Session 绑定的 runner 声明的工具 | Shell, Read, Write, MCP tools, ... |
| `ChannelToolProvider` | 3 | Channel 注册的工具 | Feishu card tools |
| `PluginToolProvider` | 4 | Plugin 注册的工具 | plugin-declared tools |

**Agent 的工具解析**：

```go
// agent/tool_routing.go (新建)

func (a *Agent) getToolsForSession(sessionKey string, tenantID int64) []llm.ToolDefinition {
    var defs []llm.ToolDefinition
    for _, p := range a.toolProviders {
        for _, t := range p.ListTools(sessionKey, tenantID) {
            defs = append(defs, t)
        }
    }
    return defs
}

func (a *Agent) findTool(sessionKey string, tenantID int64, name string) (Tool, bool) {
    for _, p := range a.toolProviders {
        if t, ok := p.GetTool(sessionKey, tenantID, name); ok {
            return t, true
        }
    }
    return nil, false
}
```

### 2.2 Registry 增强

保留现有 `Registry`，增加 runner 维度（沿现有 channel 模式）：

```go
// tools/interface.go — Registry 新增字段

type Registry struct {
    // ... 现有字段保持不变 ...

    // runnerTools: runnerID → toolName → Tool
    // Runner 连接时注册，断开时清除
    runnerTools   map[string]map[string]Tool
    runnerToolsMu sync.RWMutex

    // sessionRunner: sessionKey → runnerID
    // Session 切换 runner 时更新
    sessionRunners   map[string]string
    sessionRunnersMu sync.RWMutex
}

// RegisterForRunner 注册 runner 专属工具（runner 连接/重载时调用）
func (r *Registry) RegisterForRunner(runnerID string, tool Tool)

// UnregisterRunnerTools 清除指定 runner 的所有工具（runner 断开时）
func (r *Registry) UnregisterRunnerTools(runnerID string)

// ReplaceRunnerTools 原子替换（热更新 tool set）
func (r *Registry) ReplaceRunnerTools(runnerID string, tools []Tool)

// SetSessionRunner 绑定 session 到 runner
func (r *Registry) SetSessionRunner(sessionKey, runnerID string)

// GetForSession 更新：增加 runner 维度
// 查找顺序：runner → channel → tenant → global
func (r *Registry) GetForSession(name string, tenantID int64, sessionKey string) (Tool, bool)
```

### 2.3 Runner 层设计

**直接复用 `agent.Transport`，不新建接口。**

```go
// runner/types.go (新建)

type RunnerType string
const (
    RunnerLocal  RunnerType = "local"
    RunnerRemote RunnerType = "remote"
)

type RunnerStatus string
const (
    RunnerConnecting RunnerStatus = "connecting"
    RunnerConnected  RunnerStatus = "connected"
    RunnerDisconnect RunnerStatus = "disconnected"
    RunnerError      RunnerStatus = "error"
)

type RunnerInstance struct {
    ID          string          `json:"id"`
    Name        string          `json:"name"`
    Type        RunnerType      `json:"type"`
    Status      RunnerStatus    `json:"status"`
    Transport   agent.Transport `json:"-"`           // ★ 直接复用已有接口
    ToolDefs    []llm.ToolDefinition `json:"tools"`  // 连接时声明
    CreatedAt   time.Time       `json:"created_at"`
    LastSeenAt  time.Time       `json:"last_seen_at"`
}
```

**Local Runner 的 Transport**：

```go
// runner/local.go (新建)

// NewLocalRunner 创建本地 runner。
// sandbox 是执行后端（NoneSandbox/DockerSandbox），runner 用它实现标准工具。
func NewLocalRunner(sandbox tools.Sandbox, mcpConfigPath string, ...) *RunnerInstance {
    // 创建 runner 工具列表
    toolList := []Tool{
        NewShellTool(sandbox),
        NewReadTool(sandbox),
        NewWriteTool(sandbox),
        NewGrepTool(sandbox),
        NewGlobTool(sandbox),
        NewCdTool(sandbox),
        NewFetchTool(),
        NewWebSearchTool(),
        NewDownloadFileTool(sandbox),
        NewWorktreeTool(sandbox),
        NewSkillTool(sandbox),
        NewTaskManagerTool(),
        // MCP 由 runner 管理 —— ManageTools 工具 + 所有 MCP server 工具
        NewManageToolsTool(mcpConfigPath),
    }
    
    // 构建 RPCTable，将 "execute_tool" RPC 分发到对应工具
    rpcTable := serverapp.RPCTable{
        "execute_tool": runnerExecuteHandler(toolMap),
        "list_tools":   runnerListToolsHandler(toolMap),
        "get_tool":     runnerGetToolHandler(toolMap),
    }
    
    transport := agent.NewChannelTransport(
        rpcTable.Dispatch,  // dispatch 函数
        nil,                 // ctxFn (runner 不需要 auth)
        nil,                 // eventCh (runner 不推送事件)
    )
    
    return &RunnerInstance{
        ID:        "local",
        Name:      "Local Runner",
        Type:      RunnerLocal,
        Status:    RunnerConnected,
        Transport: transport,
        ToolDefs:  toDefinitions(toolList),
    }
}
```

**Remote Runner 的 Transport**：

```go
// runner/remote.go (新建)

// NewRemoteRunner 连接到远程 runner 进程。
// 远程 runner 通过 WebSocket 连接，发送 register 消息声明工具集。
func NewRemoteRunner(name, serverURL, token string) (*RunnerInstance, error) {
    transport := NewRunnerWSClient(serverURL, token)  // 实现 agent.Transport
    
    // 发起 register 请求，获取 runner 的工具列表
    raw, err := transport.Call("register", marshal(RegisterRequest{
        ServerInfo: serverInfo,
    }))
    // ...
    caps := parseRegisterResponse(raw)  // 包含 tools + metadata
    
    return &RunnerInstance{
        ID:        caps.RunnerID,
        Name:      name,
        Type:      RunnerRemote,
        Status:    RunnerConnected,
        Transport: transport,
        ToolDefs:  caps.Tools,
    }, nil
}
```

### 2.4 RunnerManager

```go
// runner/manager.go (新建)

type RunnerManager struct {
    mu       sync.RWMutex
    runners  map[string]*RunnerInstance  // ID → runner
    sessions map[string]string           // "channel:chatID" → runnerID

    localRunner *RunnerInstance  // 默认 local runner（始终存在）
    
    toolRegistry *tools.Registry  // 注入的全局 Registry（用于注册/注销 runner 工具）
    db           *sqlite.RunnerDB
}

// ResolveRunner 解析 session 应该使用的 runner
func (m *RunnerManager) ResolveRunner(channel, chatID string) *RunnerInstance {
    key := channel + ":" + chatID
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    // 1. Session 显式绑定
    if runnerID, ok := m.sessions[key]; ok {
        if r, ok := m.runners[runnerID]; ok {
            return r
        }
    }
    // 2. DB 持久化绑定
    // 3. 默认 local runner
    return m.localRunner
}

// BindSession 绑定 session 到指定 runner
func (m *RunnerManager) BindSession(channel, chatID, runnerID string) error {
    // 1. 验证 runner 存在
    // 2. 更新内存映射
    // 3. 更新 Registry: SetSessionRunner
    // 4. 持久化到 DB: SetTenantRunner
}

// OnRunnerConnected runner 连接成功回调
func (m *RunnerManager) OnRunnerConnected(r *RunnerInstance) {
    // 注册 runner 工具到 Registry
    for _, def := range r.ToolDefs {
        m.toolRegistry.RegisterForRunner(r.ID, runnerToolAdapter{def, r.Transport})
    }
}

// OnRunnerDisconnected runner 断开回调
func (m *RunnerManager) OnRunnerDisconnected(runnerID string) {
    // 注销 runner 工具，绑定该 runner 的 session 回退到 local
    m.toolRegistry.UnregisterRunnerTools(runnerID)
}
```

### 2.5 工具分类一览

#### Agent 核心编排工具（`tools/` 保留，约 9 个）

| 工具 | 职责 |
|------|------|
| `SubAgent` | 创建子 agent |
| `CreateChat` | 创建交互会话 |
| `SendMessage` | 向其他 agent/session 发消息 |
| `Cron` | 定时任务管理 |
| `EventTrigger` | Webhook 触发器管理 |
| `TodoWrite` | 任务状态管理 |
| `AskUser` | 向用户提问 |
| `context_edit` | 上下文编辑 |
| `config` | 配置读写 |
| `tui_control` | TUI 控制 |

#### Runner 本地工具（迁移到 `runner/tools/`，约 15 个标准工具）

| 工具 | 对应现有文件 |
|------|------------|
| `Shell` | `tools/shell.go` → `runner/tools/shell.go` |
| `Read` | `tools/read.go` → `runner/tools/read.go` |
| `Write` | `tools/write.go` → `runner/tools/write.go` |
| `Edit` | `tools/edit.go` → `runner/tools/edit.go` |
| `Grep` | `tools/grep.go` → `runner/tools/grep.go` |
| `Glob` | `tools/glob.go` → `runner/tools/glob.go` |
| `Cd` | `tools/cd.go` → `runner/tools/cd.go` |
| `Fetch` | `tools/fetch.go` → `runner/tools/fetch.go` |
| `WebSearch` | `tools/websearch.go` → `runner/tools/websearch.go` |
| `DownloadFile` | `tools/download_file.go` → `runner/tools/download_file.go` |
| `Worktree` | `tools/worktree.go` → `runner/tools/worktree.go` |
| `Skill` | `tools/skill.go` → `runner/tools/skill.go` |
| `TaskManager` | `tools/task_manager.go` → `runner/tools/task_manager.go` |
| `ChatHistory` | `tools/chat_history.go` → `runner/tools/chat_history.go` |
| `ManageTools` (MCP) | `tools/mcp_*.go` → `runner/mcp/` |

**注意**：MCP 工具不再是 agent 的功能。`ManageTools` 是 runner 的工具——它管理的是 runner 的 MCP 连接。Runner 连接 MCP server 后，MCP server 声明的工具自动成为 runner 的工具集的一部分。

### 2.6 工具执行协议

**Local runner**：Transport.Call 直接 dispatch 到工具 Execute 方法（进程内，统一代码路径）。

```go
// runner/local.go — RPCTable
"execute_tool": func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
    var req struct {
        Name      string          `json:"name"`
        Arguments json.RawMessage `json:"arguments"`
    }
    json.Unmarshal(params, &req)
    
    tool := toolMap[req.Name]
    result, err := tool.Execute(toolCtx, string(req.Arguments))
    raw, _ := json.Marshal(result)
    return raw, err
}
```

**Remote runner**：通过 WebSocket 发送 execute_tool 消息，runner 进程内部找到对应工具执行并返回。

```
Agent → findTool("shell") → RunnerToolProvider
  → runner.Transport.Call("execute_tool", {name:"shell", arguments:{command:"ls"}})
    → [local] RPCTable dispatch → Shell.Execute()
    → [remote] WebSocket → runner 进程 → Shell.Execute()
  ← ToolResult
```

---

## 三、实施计划

### 阶段一：Runner 包基础设施

| 步骤 | 文件 | 操作 |
|------|------|------|
| 1.1 | `runner/types.go` | `RunnerInstance`, `RunnerType`, `RunnerStatus` 类型定义 |
| 1.2 | `runner/manager.go` | `RunnerManager`：CRUD、session 绑定、`ResolveRunner` |
| 1.3 | `runner/local.go` | `NewLocalRunner`：创建 local runner，构建 RunnerRPCTable，包装 `ChannelTransport` |
| 1.4 | `runner/remote.go` | `NewRemoteRunner`：WebSocket 连接远程 runner |
| 1.5 | `runner/errors.go` | Runner 特有错误类型 |
| 1.6 | `tools/tool_provider.go` | `ToolProvider` 接口定义 + 四个实现骨架 |
| 1.7 | `tools/interface.go` | Registry 新增 `runnerTools`/`sessionRunners` 字段 + `RegisterForRunner`/`SetSessionRunner` 方法 |
| 1.8 | `agent/agent.go` | `InitAgent` 时创建 `RunnerManager` + 默认 local runner + 注册 AgentToolProvider |

**验证**：`go build ./runner/... ./tools/... ./agent/...` 通过

### 阶段二：Registry 集成 Runner 工具

| 步骤 | 文件 | 操作 |
|------|------|------|
| 2.1 | `tools/interface.go` | `GetForSession` / `AsDefinitionsForSession` 增加 runner 维度 |
| 2.2 | `runner/manager.go` | `OnRunnerConnected` → `registry.RegisterForRunner`；`OnRunnerDisconnected` → `registry.UnregisterRunnerTools` |
| 2.3 | `runner/manager.go` | `BindSession` → `registry.SetSessionRunner` + DB 持久化 |
| 2.4 | `storage/sqlite/schema.go` | `tenants` 表新增 `runner_id TEXT DEFAULT ''` |
| 2.5 | `storage/sqlite/migrations.go` | v36 migration |
| 2.6 | `storage/sqlite/tenant.go` | `SetTenantRunner` / `GetTenantRunner` |

**验证**：Runner 工具通过 Registry 可查可调

### 阶段三：Local Runner 工具迁移

**核心步骤**：将 `tools/shell.go` 等执行类工具迁移到 `runner/tools/`，Local runner 的 RPCTable 注册它们。

| 步骤 | 文件 | 操作 |
|------|------|------|
| 3.1 | `runner/tools/shell.go` (新建) | Shell 工具从 `tools/shell.go` 迁移，依赖 `tools.Sandbox` 接口 |
| 3.2 | `runner/tools/read.go` (新建) | Read 工具迁移 |
| 3.3 | `runner/tools/write.go` (新建) | Write 工具迁移 |
| 3.4 | `runner/tools/edit.go` (新建) | Edit 工具迁移 |
| 3.5 | `runner/tools/grep.go` (新建) | Grep 工具迁移 |
| 3.6 | `runner/tools/glob.go` (新建) | Glob 工具迁移 |
| 3.7 | `runner/tools/cd.go` (新建) | Cd 工具迁移 |
| 3.8 | `runner/tools/fetch.go` (新建) | Fetch 工具迁移 |
| 3.9 | `runner/tools/websearch.go` (新建) | WebSearch 工具迁移 |
| 3.10 | `runner/tools/download_file.go` (新建) | DownloadFile 工具迁移 |
| 3.11 | `runner/tools/worktree.go` (新建) | Worktree 工具迁移 |
| 3.12 | `runner/tools/skill.go` (新建) | Skill 工具迁移 |
| 3.13 | `runner/tools/task_manager.go` (新建) | TaskManager 工具迁移 |
| 3.14 | `runner/tools/chat_history.go` (新建) | ChatHistory 工具迁移 |
| 3.15 | `runner/mcp/manage_tools.go` (新建) | ManageTools + 所有 MCP 支持代码迁移到 `runner/mcp/` |
| 3.16 | `runner/local.go` | `runnerRPCTable` 注册上述所有工具 |

**验证**：Agent 通过 local runner 的 execute_tool RPC 可调用所有工具

> **重要**：此阶段旧 `tools/shell.go` 等文件仍然保留，作为 fallback。阶段五才移除。

### 阶段四：Runner 工具声明协议

| 步骤 | 文件 | 操作 |
|------|------|------|
| 4.1 | `internal/runnerproto/runner_proto.go` | `register` 消息扩展：增加 `tools` 字段 |
| 4.2 | `internal/runnerclient/handler.go` | `sendRegister` 发送本地工具列表 |
| 4.3 | `runner/remote.go` | 接收到 register_ok 后，解析 `tools` 字段，注册到 Registry |
| 4.4 | `runner/manager.go` | `OnRunnerConnected` 统一处理工具注册 |

**验证**：远程 runner 连接后，其工具出现在 session 的工具列表

### 阶段五：双路径共存 → 切换

| 步骤 | 文件 | 操作 |
|------|------|------|
| 5.1 | `agent/engine.go` | `buildToolContext`：优先从 RunnerToolProvider 获取 Sandbox；fallback 旧路径 |
| 5.2 | `agent/agent.go` | Agent 注册 `RunnerToolProvider`（优先级在 AgentToolProvider 之后） |
| 5.3 | 全局 | Grep `tools.Register(` 调用点，标记哪些已迁移 |
| 5.4 | `cmd/xbot-cli/main.go` | 启动时确保 local runner 已创建并注册 |
| 5.5 | 全局 | 全量测试通过后，移除旧 `tools/shell.go` 等文件 |

**验证**：全量测试通过，功能零回归

### 阶段六：TUI /settings Runner 管理

| 步骤 | 文件 | 操作 |
|------|------|------|
| 6.1 | `channel/setting_keys.go` | 新增 `session_runner` key（ScopeSession, Runtime=true, Combo type） |
| 6.2 | `channel/i18n.go` (zh/en/ja) | Runner 分类：`session_runner` + `runner_manage` 入口 |
| 6.3 | `channel/cli/cli_panel_runner.go` | **重写**：列表模式（所有 runner + 状态），支持选择/添加/删除 |
| 6.4 | `channel/cli/cli_settings.go` | `saveSettings` session_runner → `runnerManager.BindSession` |
| 6.5 | `agent/setting_runtime.go` | `session_runner` handler → `ApplyFull`（重建 sandbox 连接） |

**验证**：`/settings` → Runner 分类 → 可见 runner 列表 → 可切换

### 阶段七：Config 工具 + 清理

| 步骤 | 文件 | 操作 |
|------|------|------|
| 7.1 | `agent/engine.go:buildToolContext()` | `ConfigSet` 增强：`session_runner` → `runnerManager.BindSession` |
| 7.2 | `tools/sandbox_runner.go` | 标记 deprecated，委托给 `RunnerManager.LocalRunner` |
| 7.3 | `tools/sandbox_router.go` | 路由逻辑迁移到 `RunnerManager.ResolveRunner` |
| 7.4 | `tools/remote_sandbox.go` | LLM 代理功能抽离到 `runner/llm_proxy.go` |
| 7.5 | 全局 | 移除旧 `tools/shell.go` 等文件（若阶段五未完成） |
| 7.6 | 全局 | `golangci-lint run ./...` + 全量测试 |

---

## 四、关键设计决策

### 1. 为什么是 ToolProvider 而不是直接改 Registry？

Registry 的责任是"存储和查找工具"，ToolProvider 的责任是"提供工具集"。分离后：
- `RunnerToolProvider` 封装了"从 RunnerManager 获取 session 绑定的 runner → 取 runner 的工具集"这个逻辑
- Agent 不需要知道 Registry 内部结构
- 新增工具源 = 新增一个 ToolProvider，不改 Registry

### 2. MCP 为什么属于 Runner？

MCP 的本质是"连接外部进程，通过 stdio/SSE 通信，获取额外工具"。这和 Runner 的职责（提供执行环境）完全一致。Runner 内部的 MCP 连接和 runner 的工具声明融为一体——MCP server 的工具就是 runner 的工具。

Agent 不需要知道 MCP 的存在。Agent 只看到"runner 提供了一些工具"。

### 3. Local runner 也有一个 Transport，是不是过度设计？

Local runner 的 Transport（`ChannelTransport`）直接 dispatch 到 RPCTable → 工具 Execute。没有网络调用，没有 JSON 往返（RPCTable handler 内部有序列化，但这是代码统一性的代价）。Remote runner 的 Transport 走 WebSocket。两者通过同一个 `agent.Transport` 接口统一，符合 Transport 设计初衷："adding a new transport only requires implementing Call + Close"。

**用户指出的"统一代码美学"**：local 和 remote 完全相同的代码路径。工具执行逻辑不分支。

### 4. 编排工具为什么留在 Agent？

SubAgent/CreateChat/SendMessage 等工具直接操作 Agent 的内部状态（spawn goroutine、管理 session、发送消息）。把它们放到 runner 里需要 runner 回调 agent，增加不必要的复杂度。它们在概念上是"agent 的编排能力"，不是"执行环境的能力"。

---

## 五、风险与缓解

| 风险 | 缓解 |
|------|------|
| 工具迁移量大（~15 个文件移动） | 每个工具独立迁移，完整测试后再移下一个 |
| MCP 迁移复杂 | MCP 代码已经是自包含的（`tools/mcp_*.go`），移动 + 调整 import |
| SandboxRouter 双路径 | 阶段五之前两个路径共存，`RunnerClient` 路径有开关 |
| 现有测试依赖旧 `tools.Register()` | 迁移期间保留旧注册，逐步替换 |

---

## 六、美学评价

这套设计好在哪：

1. **Agent 真的变薄了**——Agent 不再"知道"Shell/文件/MCP。ToolProvider 接口就是它和外部能力的全部交互面。

2. **Transport 复用是精髓**——`agent.Transport`（2 方法）被 RunnerManager 自然地用起来。local runner 的 `ChannelTransport` 和 remote runner 的 `RemoteTransport` 统一抽象，agent 代码无分支。

3. **工具来源正交**——Runner/Plugin/Channel 三种工具源通过同一个 `ToolProvider` 接口接入。新增工具源 = 新 provider，不改已有代码。

4. **Runner = MCP 的超集**——MCP 是 runner 的一种"连接外部工具"的方式。runner 本身也可以有 native 工具。概念统一。

5. **对称性**——Session-Subscription 绑定和 Session-Runner 绑定是完全对称的模式。维护者只需理解一种模式。

---

## 七、注意事项

- ⚠️ **ToolProvider.Priority() 确保确定性**：同名工具冲突时，优先级决定胜负
- ⚠️ **Runner 断开时工具必须注销**：否则 session 可能调用到不可用的工具
- ⚠️ **`agent/setting_runtime.go` 是 SINGLE source of truth**
- ⚠️ **`max_context_tokens` 教训**：`session_runner` 不传给全局 `ApplySettings`
- ⚠️ **Config 工具的 `isConfigKeyAllowed`** 需要包含 `session_runner`
- ⚠️ **default local runner 不可删除**：始终存在，session 没有显式绑定时默认使用
- ⚠️ **工具名冲突**：runner 工具名和 agent 编排工具名不应重叠。编排工具使用保留前缀或白名单

✅ 自审通过
