# 代码美学与设计原则重构计划

> 基于 2026-06-30 全项目代码扫描（145,242 行非测试 Go 代码，17 个顶级包）生成。
> 分支：`refactor/code-aesthetics-plan`

## 目标

将审查发现的问题按风险和收益分为 4 个阶段，逐步消除 God Object、重复代码、错误吞没、抽象泄漏等问题，提升代码可维护性和设计美学。

## 指导原则

1. **小步快跑**：每个 Task 是独立可提交的原子改动，可单独 build + test 验证
2. **不改变行为**：纯重构，不增减功能，保持外部 API 兼容
3. **先消除风险，再优化结构**：错误吞没 → 重复代码 → God Object → 深度改进
4. **显式优于隐式**：重构过程中遵循项目 "Always Prefer Explicit" 原则
5. **每步有验证**：每个 Task 都有明确的 Done Criteria

## 风险评级

- 🟢 低风险：机械重构，编译器可验证，无行为变化
- 🟡 中风险：涉及接口/签名变更，需更新调用方，有测试覆盖
- 🔴 高风险：涉及核心数据流/并发模型，需全文搜索 + 手动验证

---

## Phase 1: Quick Wins（P0 — 低风险高收益）

> 预估总工时：~2 天
> 风险：🟢 全部为机械重构，编译器保证正确性
> 收益：消除 ~500 行重复代码，修复 66+ 处错误吞没

### Task 1.1：合并 4 个 injectXxx 函数

**问题**：`engine_run.go:1483-1690` 四个函数结构完全一致（生成内容→构造 msg→offload→持久化→进度通知），仅内容生成不同，~160 行重复。

**方案**：提取公共模板函数 `injectSyntheticToolPair`，4 个函数简化为各自 ~15 行的内容生成 + 调用。

```go
// agent/engine_run.go — 新增私有模板函数
func (s *runState) injectSyntheticToolPair(
    ctx context.Context,
    iteration int,
    toolName, toolID, assistantContent, toolContent, progressLabel string,
) {
    // 公共逻辑：offload → 构造 msg → syncMessages → persist → notifyProgress
}

// 4 个函数各自简化为：
func (s *runState) injectBgTaskNotification(ctx context.Context, bgTask *tools.BackgroundTask) {
    content := tools.FormatBgTaskCompletion(bgTask, "")
    s.injectSyntheticToolPair(ctx, s.nextIteration(), "bg_task_completion", genID(), content, content, "后台任务完成")
}
```

**影响文件**：`agent/engine_run.go`

**Done Criteria**：
- [ ] 4 个函数各自 < 20 行
- [ ] `go build ./...` 通过
- [ ] `go test ./agent/...` 通过
- [ ] AGENTS.md 中 "injectBgTaskNotification" 相关注释仍准确

**风险**：🟢 | **工时**：2h

---

### Task 1.2：RemoteSandbox 错误处理去重

**问题**：`tools/remote_sandbox.go` 中 13 个文件操作方法各自有完全相同的 10-15 行 `ProtoError` 解析逻辑。

**方案**：提取 `parseErrorResponse(raw json.RawMessage, opName string) error`，13 处调用统一为一行。

```go
// tools/remote_sandbox.go — 新增私有函数
func parseErrorResponse(raw json.RawMessage, opName string) error {
    var e ErrorResponse
    if err := json.Unmarshal(raw, &e); err != nil {
        return fmt.Errorf("%s error (raw: %s): unmarshal failed: %w", opName, raw, err)
    }
    if e.Code == "ENOENT" {
        return os.ErrNotExist
    }
    if e.Message == "" {
        return fmt.Errorf("%s error (raw: %s)", opName, raw)
    }
    return fmt.Errorf("%s: %s", opName, e.Message)
}

// 每个方法简化为：
if resp.Type == ProtoError {
    return nil, parseErrorResponse(resp.Body, "read file")
}
```

**影响文件**：`tools/remote_sandbox.go`

**Done Criteria**：
- [ ] 消除 13 处重复的 `ProtoError` 解析块
- [ ] `go build ./...` 通过
- [ ] `go test ./tools/...` 通过

**风险**：🟢 | **工时**：2h

---

### Task 1.3：Plugin Get* 快照方法泛型化

**问题**：`plugin/context.go` 中 12 个 `Get*` 方法使用完全相同的 `RLock → copy → RUnlock` 模板。

**方案**：引入泛型辅助函数，12 处简化为一行调用。

```go
// plugin/context.go — 新增泛型辅助
func snapshotSlice[T any](mu *sync.RWMutex, src []T) []T {
    mu.RLock()
    defer mu.RUnlock()
    dst := make([]T, len(src))
    copy(dst, src)
    return dst
}

// 每个方法简化为：
func (pc *pluginContextImpl) GetTools() []PluginTool {
    return snapshotSlice(&pc.mu, pc.tools)
}
```

Map 版本同理：`snapshotMap[K, V]`。

**影响文件**：`plugin/context.go`

**Done Criteria**：
- [ ] 消除 12 处重复模板（~80 行）
- [ ] `go build ./...` 通过
- [ ] `go test ./plugin/...` 通过

**风险**：🟢 | **工时**：1.5h

---

### Task 1.4：Plugin Entry 构造去重

**问题**：`plugin/manager.go` 中 4 处（Discover/Register/Reload/InstallPlugin）构造 `PluginEntry` + storage + context + widgetRegistry 的 7 行代码重复。

**方案**：提取私有工厂方法 `newEntry`。

```go
func (pm *PluginManager) newEntry(m *PluginManifest, dir string, p Plugin) *PluginEntry {
    storage, err := NewFileStorage(dir)
    if err != nil {
        log.WithField("plugin", m.ID).Warn("Failed to create storage: ", err)
        storage = &noopStorage{}
    }
    entry := &PluginEntry{
        Manifest: m, State: StateDiscovered, Dir: dir, Plugin: p,
        Context: newPluginContext(m, storage, newPluginLogger(m.ID, pm.logMgr), pm.bus, pm.configStore, pm),
    }
    entry.Context.SetWidgetRegistry(pm.widgetRegistry)
    return entry
}
```

**影响文件**：`plugin/manager.go`

**Done Criteria**：
- [ ] 4 处构造逻辑统一为 `pm.newEntry(...)` 调用
- [ ] `go build ./...` + `go test ./plugin/...` 通过

**风险**：🟢 | **工时**：1h

---

### Task 1.5：Plugin V1/V2 Wrapper 双路径去重

**问题**：`plugin/middleware.go:209-372` 中 `timeoutTool`、`retryTool` 各需实现 `Execute`(V1) 和 `ExecuteWithContext`(V2)，装饰逻辑几乎相同但复制两份。

**方案**：提取 `executeFunc` 类型别名，V1 和 V2 路径都转换为 `func(context.Context, string) (*ToolResult, error)` 签名后调用同一装饰核心。

```go
type executeFunc func(ctx context.Context, input string) (*ToolResult, error)

// timeoutTool 只实现一个核心：
func (t *timeoutTool) executeWithTimeout(fn executeFunc, ctx context.Context, input string) (*ToolResult, error) {
    // 单一实现：timeout + goroutine + select
}

// V1 路径委托：
func (t *timeoutTool) Execute(ctx context.Context, input string) (*ToolResult, error) {
    return t.executeWithTimeout(t.inner.Execute, ctx, input)
}
// V2 路径委托：
func (t *timeoutTool) ExecuteWithContext(tcc *ToolCallContext, input string) (*ToolResult, error) {
    fn := func(ctx context.Context, input string) (*ToolResult, error) {
        return t.inner.(PluginToolV2).ExecuteWithContext(tcc, input)
    }
    return t.executeWithTimeout(fn, tcc.Ctx, input)
}
```

**影响文件**：`plugin/middleware.go`

**Done Criteria**：
- [ ] timeoutTool/retryTool 各只有一个装饰核心实现
- [ ] `go build ./...` + `go test ./plugin/...` 通过

**风险**：🟡（涉及 V2 类型断言） | **工时**：3h

---

### Task 1.6：错误吞没审计与修复（第一批）

**问题**：全项目 66+ 处 `_ = expr` 形式的错误吞没，最危险的在消息持久化路径。

**方案**：分两批处理。本批处理**高风险路径**（持久化、消息发送、session 清理），加 log.Warn 记录但不改变控制流（避免引入新的 panic）。

**优先处理**：

| 位置 | 当前 | 改为 |
|------|------|------|
| `engine_run.go:1505-1508`（4处注入函数） | `_ = Session.AddMessage(...)` | `if err := Session.AddMessage(...); err != nil { log.Warn(...) }` |
| `engine_wire.go:295` | `_ = sendMessage(...)` | `if err := ...; err != nil { log.Warn(...) }` |
| `interactive.go:581` | `_ = agentTenantSession.Clear()` | `if err := ...; err != nil { log.Warn(...) }` |
| `agent.go:694` | `_ = row.Scan(&oldName)` | `if err := ...; err != nil { log.Warn(...) }` |

**原则**：
- 持久化失败：log.Warn 但继续执行（不 panic）
- DB Scan 失败：log.Warn 并走零值 fallback 路径
- 不在_hot path_的 `_ =`（如 defer Close）可暂不处理

**影响文件**：`agent/engine_run.go`、`agent/engine_wire.go`、`agent/interactive.go`、`agent/agent.go`

**Done Criteria**：
- [ ] 高风险路径（持久化/消息/session）的 `_ =` 全部替换为 `if err :=`
- [ ] 无新增 panic 风险
- [ ] `go build ./...` + `go test ./agent/...` 通过
- [ ] grep `"_ = .*Session.AddMessage"` 返回 0 结果

**风险**：🟡 | **工时**：2h

---

### Task 1.7：魔法数字常量化

**问题**：散落在 agent/、tools/、cli/ 中的大量硬编码数字，无常量名、无注释。

**方案**：创建集中常量文件，按子系统分组。

**新增文件**：

```
agent/defaults.go    — LLM/压缩/上下文相关默认值
tools/limits.go      — 沙箱/工具限制（容量/超时/条数）
```

```go
// agent/defaults.go
const (
    DefaultMaxContextTokens      = 100_000
    DefaultMaxOutputTokens       = 4_096
    MaxOutputTokensHardLimit     = 131_072
    DefaultOffloadThresholdBytes = 10_240
    DefaultCompressionThreshold  = 0.9
    CompactTailFraction          = 0.15
    CompactTokensPerMessage      = 200
    CompactMaxTailMessages       = 300
    CompactMinTailMessages       = 50
    ChatHistoryCapacity          = 200
    StreamIdleTimeout            = 120 * time.Second
)

// tools/limits.go
const (
    MaxSandboxFileSize       = 500 * 1024 * 1024  // 500MB
    MaxNoneDownloadSize      = 100 * 1024 * 1024  // 100MB
    MaxBgOutputSize          = 50 * 1024           // 50KB
    MaxBgTaskLifetime        = 24 * time.Hour
    DefaultShellTimeout      = 120 * time.Second
    MaxShellTimeout          = 600 * time.Second
    MaxGrepMatches           = 200
    MaxGrepFileSize          = 1 * 1024 * 1024
    MaxGrepLineLength        = 500
    MaxDirEntries            = 30
    MaxProjectFilesShown     = 12
    BgTaskNotifyChBuffer     = 64
    SandboxCtxTimeout        = 30 * time.Second
    DownloadTimeout          = 5 * time.Minute
)
```

然后逐一替换各文件中的硬编码数字为常量引用。

**影响文件**：`agent/defaults.go`(新)、`tools/limits.go`(新)、及 ~30 个引用文件

**Done Criteria**：
- [ ] 新建 `agent/defaults.go` 和 `tools/limits.go`
- [ ] agent/ 和 tools/ 中的魔法数字全部替换为常量引用
- [ ] `go build ./...` 通过

**风险**：🟢 | **工时**：3h

---

### Task 1.8：清理死代码

**问题**：多处 deprecated no-op 和未使用代码。

**清单**：

| 位置 | 内容 | 操作 |
|------|------|------|
| `plugin/manager.go:124-162` | 3 个 deprecated no-op 方法 | grep 调用方；无调用方→删除；有→加 log.Warn |
| `channel/cli/cli_mouse.go:205-228` | `handleViewportClick` 总是返回 false | 删除函数及调用点 |
| `tools/sandbox_router.go:53` | `DeniedSandbox` 字段从未被引用 | 删除字段及赋值 |
| `plugin/errors.go:22/42` | 2 个 "Reserved for future" sentinel error | 删除 |
| `session/multitenant.go:228` | `NewMultiTenantWithOptions` 纯别名 | 删除 |
| `agent/lifecycle.go:17-21` | `AgentRunner` interface 仅类型断言使用 | 评估后保留或删除 |

**Done Criteria**：
- [ ] 上述死代码已清理
- [ ] `go build ./...` + `go test ./...` 通过
- [ ] grep 确认无残留调用

**风险**：🟢 | **工时**：1.5h

---

## Phase 2: 结构性重构（P1 — 中风险，高收益）

> 预估总工时：~1.5 周
> 风险：🟡 涉及文件拆分、函数拆分、接口调整
> 前置条件：Phase 1 全部完成并合入

### Task 2.1：拆分 `handleAgentMessage`（569 行 → 5 个子函数）

**问题**：`cli_agent_msg.go:14-583` 承担 8 种独立职责。

**方案**：按职责拆分为 5 个子函数：

```
handleAgentMessage (入口+路由, ~40行)
├── handleCancelAck (~60行)          — 取消回执处理
├── handleStreamingUpdate (~80行)    — 流式内容更新
├── handleToolSummaryCreation (~80行)— 工具摘要生成
├── handleNormalReply (~60行)        — 正常回复处理
└── handleQueueFlush (~50line)       — 队列刷新
```

**影响文件**：`channel/cli/cli_agent_msg.go`（可能新增 `cli_agent_msg_cancel.go` 等）

**Done Criteria**：
- [ ] `handleAgentMessage` 主体 < 60 行
- [ ] 每个子函数 < 100 行
- [ ] TUI 手动测试：正常对话、Ctrl+C 取消、SubAgent 查看 均正常
- [ ] 现有 CLI snapshot 测试全部通过

**风险**：🟡 | **工时**：4h

---

### Task 2.2：拆分 `Update` switch-case（513 行 → 策略注册表）

**问题**：`cli_update.go:22-535` 的 513 行 switch-case 处理 30+ 种消息类型。

**方案**：引入消息处理注册表，每种消息类型注册一个 handler 函数。

```go
// channel/cli/cli_update.go
type msgHandler func(m *cliModel, msg tea.Msg) (tea.Model, tea.Cmd)

var updateHandlers = map[reflect.Type]msgHandler{
    reflect.TypeOf(cliSettingsSavedMsg{}):    (*cliModel).handleSettingsSaved,
    reflect.TypeOf(cliSwitchLLMDoneMsg{}):    (*cliModel).handleSwitchLLMDone,
    reflect.TypeOf(runnerStatusMsg{}):        (*cliModel).handleRunnerStatus,
    // ...
}

func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    if h, ok := updateHandlers[reflect.TypeOf(msg)]; ok {
        return h(m, msg)
    }
    // fallback: generic handling (tick, key, mouse, window size)
    return m.updateGeneric(msg)
}
```

**注意**：`reflect.TypeOf` 在每帧调用有开销。对于高频消息（`tickMsg`、`tea.KeyMsg`、`tea.MouseMsg`）使用 `switch` 快速路径，其余走 map。

**影响文件**：`channel/cli/cli_update.go` 及相关 handler 文件

**Done Criteria**：
- [ ] `Update` 主体 < 80 行
- [ ] 高频消息（tick/key/mouse）仍走 switch 快速路径
- [ ] TUI 性能无可感知退化（tick 渲染 < 2ms）
- [ ] 现有测试通过

**风险**：🟡 | **工时**：6h

---

### Task 2.3：拆分 `SpawnInteractiveSession`（662 行 → 5 个子函数）

**问题**：`interactive.go:459-1121` 涵盖 8 种职责。

**方案**：

```
SpawnInteractiveSession (入口+编排, ~80行)
├── createAgentTenant (~100行)        — 创建 tenant + session
├── buildSubAgentPrompt (~80行)       — 构建 SubAgent system prompt
├── wireSubAgentProgress (~80line)    — CLI 进度回调注入
├── wireSubAgentTick (~60line)        — tick 链注入
└── registerAgentChannel (~60line)    — AgentChannel 注册
```

**影响文件**：`agent/interactive.go`

**Done Criteria**：
- [ ] `SpawnInteractiveSession` 主体 < 100 行
- [ ] 子Agent 创建/查看/交互 手动测试正常
- [ ] `go test ./agent/...` 通过

**风险**：🟡 | **工时**：4h

---

### Task 2.4：拆分 `buildSubAgentRunConfig`（405 行 → Builder 模式）

**问题**：`engine_wire.go:427-832` 构建 RunConfig 需 400+ 行。

**方案**：提取 `subAgentConfigBuilder`，每个 `WithXxx` 方法 < 30 行。

```go
type subAgentConfigBuilder struct {
    cfg *RunConfig
    a   *Agent
}

func (b *subAgentConfigBuilder) WithCWDInheritance(parentCtx *ToolContext) *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) WithWorktree(parentCtx *ToolContext) *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) WithToolWhitelist(whitelist []string) *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) WithPromptBuilder(...) *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) WithSSHKeepalive() *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) WithModelSelection(...) *subAgentConfigBuilder { ... }
func (b *subAgentConfigBuilder) Build() *RunConfig { return b.cfg }
```

**影响文件**：`agent/engine_wire.go`

**Done Criteria**：
- [ ] `buildSubAgentRunConfig` 主体 < 50 行（编排 builder 调用）
- [ ] 每个 `WithXxx` 方法 < 30 行
- [ ] `go test ./agent/...` 通过

**风险**：🟡 | **工时**：4h

---

### Task 2.5：RunConfig 按职责拆分

**问题**：`engine.go:45-302` 的 85 字段无分组。

**方案**：按职责拆为 6 个子结构体。

```go
type RunConfig struct {
    LLM        RunLLMConfig        // client, model, thinkingMode, stream, maxOutputTokens
    Identity   RunIdentityConfig   // channel, chatID, senderID
    Workspace  RunWorkspaceConfig  // workingDir, workspaceRoot, skillsDirs
    Sandbox    RunSandboxConfig    // sandboxMode, sandbox, runnerManager
    Callbacks  RunCallbacks        // ~30 个回调函数
    Session    RunSessionConfig    // session, persistence, offload, mask
    Progress   RunProgressConfig   // structuredProgress, autoNotify
    SubAgent   RunSubAgentConfig   // parentCtx, maxDepth, isSubAgent
}
```

**兼容性**：为了不一次性改所有调用方，添加 getter 方法（如 `cfg.LLMClient()` 返回 `cfg.LLM.Client`）作为过渡。后续逐步迁移调用方。

**影响文件**：`agent/engine.go`、`agent/engine_wire.go`、`agent/engine_run.go`、`agent/interactive.go` 及所有 RunConfig 字段引用处

**Done Criteria**：
- [ ] RunConfig 拆为 6+ 子结构体
- [ ] 提供向后兼容 getter（过渡期）
- [ ] `go build ./...` + `go test ./agent/...` 通过

**风险**：🟡 | **工时**：6h

---

### Task 2.6：ToolContext 按职责拆分

**问题**：`tools/interface.go:22-160` 的 55+ 字段无分组。

**方案**：拆为子结构体 + 内嵌组合。

```go
type ToolContext struct {
    Ctx context.Context

    SandboxCtx    SandboxContext     // Sandbox, WorkingDir, WorkspaceRoot, SandboxEnabled
    MemoryCtx     MemoryContext      // TenantID, CoreMemory, ArchivalMemory, MemorySvc
    CommCtx       CommunicationContext // SendFunc, InjectInbound, MessageSender, ...
    ConfigCtx     ConfigContext      // ConfigGet, ConfigSet, ConfigList, ...
    RunnerCtx     RunnerContext      // RunnerCreate/List/Delete/GetActive/SetActive/Rename
    BgTaskCtx     BgTaskContext      // BgTaskManager, BgTaskListFn
    PluginCtx     PluginContext      // PluginMgr, ReloadPluginsFn
    SessionCtx    SessionContext     // Session, TenantSession, OffloadStore, MaskStore

    // 工具执行辅助
    OriginUserID  string
    SubAgentMgr   SubAgentManager
}
```

**兼容性**：同 RunConfig，提供过渡 getter。工具代码逐步迁移为 `ctx.SandboxCtx.Sandbox` → `ctx.Sandbox()`。

**影响文件**：`tools/interface.go` 及所有工具实现文件（~30 个）

**Done Criteria**：
- [ ] ToolContext 拆为 8+ 子结构体
- [ ] 提供过渡 getter
- [ ] `go build ./...` + `go test ./tools/...` 通过

**风险**：🟡 | **工时**：8h

---

### Task 2.7：`interface.go` 文件拆分

**问题**：`tools/interface.go`（952 行）混合了 ToolContext 定义、Tool 接口、Registry 实现（670 行）、辅助类型。

**方案**：

```
tools/interface.go          → Tool 接口 + ToolResult + ToolContext（保留核心抽象, < 200 行）
tools/registry.go           → Registry 定义 + 所有方法（~670 行）
tools/registry_helpers.go   → ToolGroupEntry, ToolSchema, ChannelProvider, GetToolGroupsForChannel
tools/registry_defaults.go  → DefaultRegistry()
```

**Done Criteria**：
- [ ] `interface.go` < 250 行
- [ ] 4 个文件各有清晰职责
- [ ] `go build ./...` 通过

**风险**：🟢 | **工时**：2h

---

### Task 2.8：PluginContext 拆为组合接口

**问题**：`plugin/context.go:29-242` 的 50+ 方法巨型接口违反 ISP。

**方案**：拆为子接口，`PluginContext` 组合它们（向后兼容）。

```go
type ToolRegistrar interface {
    RegisterTool(tool PluginTool) error
    RegisterTools(tools ...PluginTool) error
    UseMiddleware(middleware PluginMiddleware) error
}

type HookSubscriber interface {
    OnPreToolUse(matcher string, handler HookHandler) error
    OnPostToolUse(matcher string, handler HookHandler) error
    OnUserPrompt(handler HookHandler) error
    OnAgentStop(handler HookHandler) error
    // ...
}

type StorageProvider interface {
    Storage() StorageAccessor
    StorageInt(key string) (int64, bool)
    StorageBool(key string) (bool, bool)
    StorageJSON(key string, value any) error
    StorageGetJSON(key string, target any) error
}

type SessionMetadata interface {
    PluginID() string
    WorkingDir() string
    Channel() string
    ChatID() string
    TenantID() int64
    Logger() Logger
}

// 向后兼容：PluginContext 组合所有子接口
type PluginContext interface {
    ToolRegistrar
    HookSubscriber
    StorageProvider
    SessionMetadata
    EventBusPublisher
    UIContributor
    CronScheduler
    // ...其余按需
}
```

**Done Criteria**：
- [ ] PluginContext 拆为 8+ 子接口
- [ ] 现有插件代码无编译错误（组合兼容）
- [ ] `go build ./...` + `go test ./plugin/...` 通过

**风险**：🟡 | **工时**：4h

---

### Task 2.9：Config env 覆盖去重

**问题**：`config/config.go:752-1050` 约 300 行 `if v := os.Getenv(); v != "" {}` 重复。

**方案**：引入泛型辅助函数，缩小到 ~80 行。

```go
func setStringEnv(env string, dst *string) {
    if v := os.Getenv(env); v != "" { *dst = v }
}
func setIntEnv(env string, dst *int) {
    if v := os.Getenv(env); v != "" {
        if n, err := strconv.Atoi(v); err == nil { *dst = n }
    }
}
func setBoolEnv(env string, dst *bool) {
    if v := os.Getenv(env); v != "" {
        if b, err := strconv.ParseBool(v); err == nil { *dst = b }
    }
}
func setFloatEnv(env string, dst *float64) {
    if v := os.Getenv(env); v != "" {
        if f, err := strconv.ParseFloat(v, 64); err == nil { *dst = f }
    }
}
```

**影响文件**：`config/config.go`

**Done Criteria**：
- [ ] `applyEnvOverrides` < 100 行
- [ ] `go build ./...` + `go test ./config/...` 通过
- [ ] 环境变量覆盖行为不变（手动验证 2-3 个 env var）

**风险**：🟢 | **工时**：2h

---

### Task 2.10：`compactMessages` 拆分

**问题**：`compress.go:365-675` 的 311 行函数涵盖 5 步压缩流程。

**方案**：拆为 5 个 step 函数，每个 < 80 行。

```
compactMessages (编排, ~40行)
├── findTailCutPoint (~60line)    — 找到最后一个 user/assistant 消息作为 tail 起点
├── capTailLength (~40line)       — 限制 tail 长度（maxTailMessages, maxTailContextFraction）
├── assembleToCompress (~30line)  — 组装待压缩消息
├── callCompressLLM (~50line)     — 调用 LLM 执行压缩
└── mergeCompressed (~60line)     — 合并压缩结果 + tail
```

**影响文件**：`agent/compress.go`

**Done Criteria**：
- [ ] `compactMessages` 主体 < 50 行
- [ ] 每个 step < 80 行
- [ ] `go test ./agent/...` 通过（含 compress 测试）

**风险**：🟡 | **工时**：3h

---

### Task 2.11：远程文件拆分

**问题**：`tools/remote_sandbox.go`（1,470 行）和 `tools/docker_sandbox.go`（986 行）单体过长。

**方案**：

```
tools/remote_sandbox.go              → 类型定义 + New + 生命周期（~150 行）
tools/remote_sandbox_connection.go   → 连接管理 + runner 查找（~230 行）
tools/remote_sandbox_exec.go         → Exec/BgExec/BgKill/StatusBg（~300 行）
tools/remote_sandbox_file.go         → ReadFile/WriteFile/Stat/ReadDir/MkdirAll/Remove/RemoveAll/DownloadFile（~250 行）
tools/remote_sandbox_sync.go         → Runner sync + stdio + 其他（~200 行）

tools/docker_sandbox.go              → 类型定义 + New + 生命周期（~150 行）
tools/docker_sandbox_file.go         → File I/O 方法（~300 行）
tools/docker_sandbox_lifecycle.go    → 容器启动/停止/健康检查（~250 行）
```

**Done Criteria**：
- [ ] 无文件超过 500 行
- [ ] `go build ./...` + `go test ./tools/...` 通过

**风险**：🟢 | **工时**：3h

---

### Task 2.12：`rpc_table.go` handler 分组精细化

**问题**：`serverapp/rpc_table.go`（1,621 行）中 `registerSessionHandlers` 单函数 330 行/18 个 handler。

**方案**：进一步拆分 + 提取权限检查中间件。

```go
// 提取权限检查辅助
func (h *RPCHandler) requireOwnedSubscription(id string) (*Subscription, error) {
    svc, err := h.requireSubscriptionSvc()
    if err != nil { return nil, err }
    sub, err := svc.Get(id)
    if err != nil { return nil, err }
    if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
        return nil, fmt.Errorf("subscription not found")
    }
    return sub, nil
}

// 拆分 registerSessionHandlers
func (h *RPCHandler) registerMemoryHandlers() { ... }     // clear/stat/stats
func (h *RPCHandler) registerChatHandlers() { ... }       // history/delete/rename/create
func (h *RPCHandler) registerProgressHandlers() { ... }   // processing/progress/todos
```

**Done Criteria**：
- [ ] 每个 register 函数 < 150 行
- [ ] 权限检查模式统一为 `requireOwnedSubscription` 调用
- [ ] `go build ./...` + `go test ./serverapp/...` 通过

**风险**：🟡 | **工时**：3h

---

## Phase 3: 深度改进（P2 — 较高风险，中长期）

> 预估总工时：~3 周
> 风险：🔴 涉及核心架构、并发模型、数据流
> 前置条件：Phase 2 全部完成并合入，有充分的测试覆盖

### Task 3.1：Agent God Object 拆分

**问题**：`Agent` struct 80+ 字段 / 98 方法 / 14 个裸 `sync.Map`。

**方案**：按领域提取子系统，Agent 变为协调者。

```
agent/agent.go                 — Agent struct（协调者, < 30 字段）
agent/session_registry.go      — SessionRegistry（管理 sessionMsgIDs, sessionReplyTo, sessionFinalSent, chatCancelCh, pendingCancel）
agent/progress_tracker.go      — ProgressTracker（管理 lastProgressSnapshot, iterationHistories, builtinProgressSeq）
agent/interactive_registry.go  — InteractiveRegistry（管理 interactiveSubAgents, bgSessionStates）
agent/checkpoint_store.go      — CheckpointStore（管理 checkpointStores）
agent/llm_manager.go           — LLMManager（管理 llmConfigSvc, llmFactory, userSemaphores）
```

**sync.Map 封装**：

```go
// agent/session_map.go — 泛型类型安全 Map
type SessionMap[T any] struct {
    m sync.Map
}

func (sm *SessionMap[T]) Load(channel, chatID string) (T, bool) {
    v, ok := sm.m.Load(channel + ":" + chatID)
    if !ok { var zero T; return zero, false }
    return v.(T), true
}

func (sm *SessionMap[T]) Store(channel, chatID string, val T) {
    sm.m.Store(channel+":"+chatID, val)
}

func (sm *SessionMap[T]) Delete(channel, chatID string) { sm.m.Delete(channel + ":" + chatID) }
func (sm *SessionMap[T]) Range(fn func(channel, chatID string, val T) bool) { ... }
```

**迁移策略**：
1. 先引入 `SessionMap[T]` 泛型
2. 逐个 `sync.Map` 字段迁移为 `SessionMap[T]`
3. 提取子系统 struct，迁移相关方法
4. Agent 保留协调接口

**影响文件**：`agent/agent.go` 及几乎全部 agent/ 子文件

**Done Criteria**：
- [ ] Agent struct < 30 字段（从 80+ 缩减）
- [ ] 14 个 `sync.Map` 全部封装为 `SessionMap[T]`
- [ ] 5+ 个子系统提取完成
- [ ] `go build ./...` + `go test ./agent/...` 通过
- [ ] 手动测试：CLI 对话、SubAgent、Cron、插件全部正常

**风险**：🔴 | **工时**：2-3 天

---

### Task 3.2：cliModel God Object 拆分

**问题**：`cliModel` 65+ 字段 / 201+ 方法，同时是 Model+View+Controller。

**方案**：按领域提取组件，cliModel 组合它们。

```
channel/cli/components/
├── chat.go              — ChatComponent（messages, rendering, streaming, viewport）
├── input.go             — InputComponent（textarea, completions, history）
├── panel.go             — PanelManager（settings, askuser, rewind panels）
├── palette.go           — PaletteComponent（command palette state + handlers）
├── session.go           — SessionState（chatID, workDir, connState, savedSessions）
├── agents.go            — AgentPanelState（agent count, list, inspect, sessions）
├── overlay.go           — OverlayManager（palette/quickSwitch/rewind overlay 统一管理）
└── uistate.go           — UIStateMachine（互斥模式状态机）
```

**UIStateMachine** 解决互斥模式问题：

```go
type UIMode int
const (
    ModeNormal UIMode = iota
    ModePalette
    ModeQuickSwitch
    ModeRewind
    ModePanel
    ModeSplash
    ModeEasterEgg
)

type UIStateMachine struct {
    current UIMode
    // push/pop for nested modes
    stack []UIMode
}

func (sm *UIStateMachine) Enter(mode UIMode) error { ... }  // 自动退出当前模式
func (sm *UIStateMachine) Exit() { ... }                     // pop to previous
```

**迁移策略**：
1. 先提取无副作用的纯数据组件（SessionState, PaletteComponent）
2. 再提取有行为的组件（ChatComponent, InputComponent）
3. 最后引入 UIStateMachine 统一管理模式切换

**影响文件**：`channel/cli/cli_model.go` 及 30+ 个 cli 文件

**Done Criteria**：
- [ ] cliModel 主结构体 < 25 字段（从 65+ 缩减）
- [ ] 8 个组件提取完成
- [ ] UI 状态机保证互斥模式不可同时激活
- [ ] TUI 手动测试：对话/设置面板/命令面板/快速切换/Rewind 全部正常
- [ ] 现有 snapshot 测试通过

**风险**：🔴 | **工时**：3-4 天

---

### Task 3.3：LLM 流式处理统一抽象

**问题**：OpenAI/Anthropic 流式解析完全独立（各 ~200 行，零共享），新增 provider 需重写整个流处理循环。

**方案**：提取 `StreamParser` 接口 + 通用流处理循环。

```go
// llm/stream_parser.go
type StreamParser interface {
    // ParseEvent 解析一个 SSE 事件，返回 0+ 个 StreamEvent
    ParseEvent(data []byte) ([]StreamEvent, error)
    // Done 检查是否收到终止信号
    Done() bool
}

// 通用流处理循环
func processStream(body io.ReadCloser, parser StreamParser, eventChan chan<- StreamEvent, ctx context.Context) {
    scanner := bufio.NewScanner(body)
    for scanner.Scan() {
        if ctx.Err() != nil { break }
        events, err := parser.ParseEvent(scanner.Bytes())
        if err != nil { eventChan <- StreamEvent{Type: EventError, Error: err}; break }
        for _, e := range events { eventChan <- e }
        if parser.Done() { break }
    }
    close(eventChan)
}
```

各 provider 只需实现 `ParseEvent`，不再重写 goroutine + scanner + channel 管理。

**影响文件**：`llm/openai.go`、`llm/anthropic.go`、`llm/openai_responses.go`、`llm/stream.go`(新)

**Done Criteria**：
- [ ] `StreamParser` 接口定义完成
- [ ] OpenAI + Anthropic 各自实现 `ParseEvent`
- [ ] 流式对话 + 工具调用 手动测试通过
- [ ] `go test ./llm/...` 通过

**风险**：🔴 | **工时**：1-2 天

---

### Task 3.4：SQL 操作层改进

**问题**：12 个 Service 手写完整列名 SQL 字符串，schema 变更需修改每个引用处；session.go 中 4 处维护同一表的列列表副本。

**方案**：引入列名常量 + 通用 scan/build 辅助（不引入完整 ORM）。

```go
// storage/sqlite/columns.go — 列名常量集中管理
const (
    sessionMsgCols = "tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls, detail, display_only, reasoning_content, created_at"
    subCols        = "id, sender_id, name, provider, base_url, api_key, model, is_default, max_context, max_output_tokens, thinking_mode, api_type, cached_models, per_model_configs, created_at, updated_at"
)

// storage/sqlite/scan.go — 泛型 scan 辅助
func scanCols[T any](rows *sql.Rows, scanFn func(*sql.Rows) (T, error)) ([]T, error) { ... }
```

**优先处理**：`session.go`（4 处列副本）、`user_llm_subscription.go`（2 处列副本）。

**影响文件**：`storage/sqlite/columns.go`(新)、`storage/sqlite/session.go`、`storage/sqlite/user_llm_subscription.go` 等

**Done Criteria**：
- [ ] 列名常量集中管理
- [ ] 同一表的列列表只出现一次
- [ ] `go test ./storage/...` 通过

**风险**：🟡 | **工时**：1 天

---

### Task 3.5：N+1 查询优化

**问题**：`storage/sqlite/chat.go:74-94` `ListUserChats` 对每个 chatID 执行 2 次查询（tenants + session_messages preview），N 个会话 = 2N 次往返。

**方案**：合并为单次 `LEFT JOIN` 查询。

```go
// 优化前：N+1
for _, cid := range chatIDs {
    conn.QueryRow("SELECT id FROM tenants WHERE channel=? AND chat_id=?", ...)
    conn.QueryRow("SELECT content FROM session_messages WHERE tenant_id=? ORDER BY id DESC LIMIT 1", ...)
}

// 优化后：1 次查询
rows, err := conn.Query(`
    SELECT t.id, t.chat_id, t.last_active_at,
           (SELECT content FROM session_messages sm
            WHERE sm.tenant_id = t.id AND sm.role IN ('user','assistant')
            ORDER BY sm.id DESC LIMIT 1) AS preview
    FROM tenants t
    WHERE t.channel = ? AND t.chat_id IN (`+placeholders(len(chatIDs))+`)`,
    append([]any{channel}, toAnySlice(chatIDs)...)...)
```

**Done Criteria**：
- [ ] `ListUserChats` 从 2N 次查询降为 1 次
- [ ] 返回结果一致
- [ ] `go test ./storage/...` 通过

**风险**：🟡 | **工时**：2h

---

### Task 3.6：`migrations.go` 列检查辅助提取

**问题**：28 个迁移函数中 `pragma_table_info` 列检查模式重复 6+ 次。

**方案**：提取辅助函数。

```go
func columnExists(conn *sql.DB, table, column string) (bool, error) {
    var count int
    err := conn.QueryRow(
        "SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?",
        table, column,
    ).Scan(&count)
    return count > 0, err
}
```

**影响文件**：`storage/sqlite/migrations.go`

**Done Criteria**：
- [ ] 提取 `columnExists` 辅助函数
- [ ] 6+ 处重复替换为函数调用
- [ ] 迁移测试通过（从 v0 到最新版本）

**风险**：🟢 | **工时**：2h

---

### Task 3.7：`textarea.go`（2,128 行）文件拆分

**问题**：全代码库最大文件，功能内聚但导航困难。

**方案**：按功能域拆分。

```
internal/textarea/
├── textarea.go          — Model + 核心方法（~400 行）
├── textarea_view.go     — View() + render 逻辑（~500 行）
├── textarea_update.go   — Update() + key binding（~400 行）
├── textarea_text.go     — 文本操作（Insert/Delete/WordBackward）（~400 行）
├── textarea_scroll.go   — 软换行/滚动/LineInfo（~300 行）
├── memoization/         — 已有
└── runeutil/            — 已有
```

**Done Criteria**：
- [ ] 无文件超过 600 行
- [ ] `go build ./...` 通过
- [ ] CLI 输入框行为不变

**风险**：🟢 | **工时**：3h

---

### Task 3.8：CLI overlay 统一管理

**问题**：`cli_view.go:1151-1242` 中 Palette/QuickSwitch/Rewind 三个 overlay 处理 copy-paste。

**方案**：提取 `OverlayManager` + 注册模式。

```go
type OverlayRenderer interface {
    Render(width, height int) string
    TrackZones(zones *mouseZoneBuilder)
    IsActive() bool
}

type OverlayManager struct {
    overlays []OverlayRenderer
}

func (om *OverlayManager) RenderActive(width, height int, zones *mouseZoneBuilder) (string, bool) {
    for _, o := range om.overlays {
        if o.IsActive() {
            content := o.Render(width, height)
            o.TrackZones(zones)
            return content, true
        }
    }
    return "", false
}
```

**Done Criteria**：
- [ ] 3 个 overlay 统一为 OverlayManager 管理
- [ ] 新增 overlay 只需实现 OverlayRenderer 接口 + 注册
- [ ] TUI 手动测试：3 种 overlay 显示/退出正常

**风险**：🟡 | **工时**：3h

---

### Task 3.9：`buildDirectoryTree` 双胞胎合并

**问题**：`tools/cd.go:266-383` 本地版和 Sandbox API 版 60+ 行重复。

**方案**：提取统一数据源接口。

```go
type dirEntryInfo struct {
    Name  string
    IsDir bool
    Size  int64
}

func buildDirectoryTreeFromEntries(entries []dirEntryInfo, maxEntries int) string {
    // 统一的排序 + 格式化逻辑
}

// 本地版：
entries := collectLocalEntries(dir)
return buildDirectoryTreeFromEntries(entries, MaxDirEntries)

// Sandbox版：
entries := collectSandboxEntries(ctx, dir)
return buildDirectoryTreeFromEntries(entries, MaxDirEntries)
```

**Done Criteria**：
- [ ] 合并为 1 个核心函数 + 2 个数据采集函数
- [ ] `go test ./tools/...` 通过

**风险**：🟢 | **工时**：1h

---

### Task 3.10：命名统一化

**问题**：多处命名不一致。

**方案**：全局批量修正。

| 问题 | 范围 | 方案 |
|------|------|------|
| `enable` vs `enabled` | config 结构体 | 统一为 `Enabled`（JSON key `enabled`），保留 `enable` 反序列化兼容 |
| `render` vs `view` 方法命名 | `channel/cli/` | 纯字符串拼接→`render`；顶层 View→`View()`；面板级渲染→统一 `render` |
| `clickPanel*` vs `handleXxxClick` | `channel/cli/cli_mouse.go` | 统一为 `handleXxxClick` |
| Dispatch 常量/字符串混用 | `internal/runnerclient/handler.go` | 全部替换为 `runnerproto.ProtoXxx` 常量 |
| `parseSQLiteTime` 未使用 | `storage/sqlite/cron.go` | 替换 4 处 `time.Parse` 为 `parseSQLiteTime` |

**Done Criteria**：
- [ ] 上述命名统一
- [ ] `go build ./...` + `go test ./...` 通过

**风险**：🟡 | **工时**：4h

---

### Task 3.11：Plugin renderFor* 去重 + PermissionError 一致性

**问题 1**：`plugin/script_runtime.go:260-297` 和 `:320-352` 几乎完全重复。
**问题 2**：`plugin/context.go:1038-1040` 唯一一处用 `fmt.Errorf` 而非 `&PermissionError{}`。

**方案**：
1. 提取 `renderForWorkDir(widgetID, width, workDir)`，两个公开方法委托
2. `RegisterChannelProvider` 改用 `&PermissionError{}`
3. `RegisterChannelProvider` 的 PermissionError 补充 `PluginID` 字段

**Done Criteria**：
- [ ] 两个 renderFor* 合并为一个核心实现
- [ ] 全部 20 处权限检查统一用 `&PermissionError{}`
- [ ] `go test ./plugin/...` 通过

**风险**：🟢 | **工时**：1.5h

---

### Task 3.12：LLMFactory 拆分

**问题**：`agent/llm_factory.go`（1,181 行 / 59 方法）混合了 tier 管理、订阅缓存、信号量、用户配置查询、缓存失效、设置读写。

**方案**：按职责拆分。

```
agent/llm_factory.go         — LLMFactory 核心 + GetLLM/GetLLMForChat/GetLLMForModel（~300 行）
agent/llm_tier.go            — Tier 管理 + fallback 链（~200 行）
agent/llm_cache.go           — 订阅缓存 + Invalidate 系列（~300 行）
agent/llm_config_handler.go  — 设置读写（已有，可能需调整）
```

**Done Criteria**：
- [ ] 无文件超过 500 行
- [ ] `go build ./...` + `go test ./agent/...` 通过

**风险**：🟡 | **工时**：4h

---

### Task 3.13：Runner handler 模板化去重

**问题**：`internal/runnerclient/handler.go:209-399` 8 个文件操作 handler 模板化重复。

**方案**：提取泛型中间层。

```go
func handleFileOp[T any](
    h *Handler, msg runnerproto.RunnerMessage,
    fn func(path string, req T) (any, error),
) *runnerproto.RunnerMessage {
    var req T
    if err := json.Unmarshal(msg.Body, &req); err != nil {
        return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
    }
    path, err := h.safePath(req.Path)
    if err != nil {
        return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
    }
    result, err := fn(path, req)
    if err != nil {
        return runnerproto.MakeError(msg.ID, "EIO", err.Error())
    }
    if h.Verbose { callLogf(h.LogFunc, "  op %s", req.Path) }
    return runnerproto.MakeOK(msg.ID, result)
}
```

**Done Criteria**：
- [ ] 8 个 handler 各 < 10 行
- [ ] `go build ./...` + runner 手动测试通过

**风险**：🟡 | **工时**：3h

---

### Task 3.14：Docker/Native Executor exitCode 逻辑统一

**问题**：`docker.go:216-226` 和 `native.go:64-77` 的 exitCode/timeout 处理逻辑重复但顺序不一致。

**方案**：提取 `extractExitInfo` 函数。

```go
func extractExitInfo(err error, ctxErr error) (exitCode int, timedOut bool, rawErr error) {
    if ctxErr == context.DeadlineExceeded {
        return -1, true, nil
    }
    if err == nil {
        return 0, false, nil
    }
    if exitErr, ok := err.(*exec.ExitError); ok {
        return exitErr.ExitCode(), false, nil
    }
    return 0, false, err
}
```

**Done Criteria**：
- [ ] 两个 Executor 调用统一函数
- [ ] 逻辑顺序统一：先 timeout 再 exitError 最后其他
- [ ] runner 手动测试通过

**风险**：🟡 | **工时**：2h

---

## Phase 4: 清理与收尾（P3 — 低风险）

> 预估总工时：~1 天
> 风险：🟢

### Task 4.1：错误吞没审计（第二批）

处理 Phase 1 遗留的中低风险 `_ =` 处（如 `defer file.Close()`、`_ = conn.Close()` 等）。
评估每处是否需要加 log 或可安全忽略，加注释说明忽略原因。

**Done Criteria**：
- [ ] 全项目 `_ =` 每处都有明确注释（忽略原因或已加 log）
- [ ] grep `"_ ="` 结果中无未注释项

**风险**：🟢 | **工时**：2h

---

### Task 4.2：`log.Fatalf` 从库代码移除

**问题**：`agent/context.go:74` 在库代码中使用 `log.Fatalf`。

**方案**：改为 `return error`，由 `main.go` / `server.go` 调用方决定是否 Fatal。

**影响文件**：`agent/context.go`、`cmd/xbot-cli/main.go`（或 `serverapp/server.go`）

**Done Criteria**：
- [ ] agent/ 包内无 `log.Fatal` / `os.Exit` 调用
- [ ] 调用方正确处理 error

**风险**：🟡 | **工时**：1h

---

### Task 4.3：ChatService 统一使用 `*DB`

**问题**：`storage/sqlite/chat.go` 唯一一个直接使用 `*sql.DB` 的 Service。

**方案**：改为 `*DB` wrapper，与其他 11 个 Service 一致。

**影响文件**：`storage/sqlite/chat.go`、`serverapp/rpc_table.go:780`

**Done Criteria**：
- [ ] ChatService 使用 `*DB`
- [ ] `go build ./...` + `go test ./storage/...` 通过

**风险**：🟢 | **工时**：1h

---

### Task 4.4：RunAs/Reason 模式去重

**问题**：`shell.go`、`edit.go`(2处) 相同的 6 行权限控制清除模式重复。

**方案**：提取 `sanitizeRunAs` 函数，3 处统一调用。

**Done Criteria**：
- [ ] 3 处统一为函数调用
- [ ] `go test ./tools/...` 通过

**风险**：🟢 | **工时**：1h

---

### Task 4.5：LLM 双日志消除

**问题**：provider 层和 retry 层都 log 同一个 error。

**方案**：provider 层仅返回 error（不 log），retry 层负责日志。或反之——统一只在一层 log。

**Done Criteria**：
- [ ] 同一请求同一 error 在日志中只出现一次
- [ ] `go test ./llm/...` 通过

**风险**：🟢 | **工时**：1h

---

### Task 4.6：更新 AGENTS.md 与知识库

**方案**：完成所有重构后，更新以下文档反映新架构：
- `AGENTS.md` — 更新 Gotchas 中已被修复的条目
- `docs/agent/architecture.md` — 更新包地图和接口列表
- `docs/agent/tools.md` — 更新 ToolContext 拆分后的结构

**Done Criteria**：
- [ ] 文档与代码一致
- [ ] 新增的子系统/组件/接口有文档记录

**风险**：🟢 | **工时**：2h

---

## 里程碑与依赖关系

```
Phase 1 (Week 1)          Phase 2 (Week 2-3)         Phase 3 (Week 4-6)        Phase 4 (Week 7)
┌─────────────────┐      ┌───────────────────┐      ┌───────────────────┐      ┌──────────────┐
│ Task 1.1 Inject │       │ Task 2.1 handleAg │       │ Task 3.1 Agent   │       │ Task 4.1-4.6 │
│ Task 1.2 Remote │─────▶│ Task 2.2 Update   │─────▶│ Task 3.2 cliModel│─────▶│ 清理与收尾    │
│ Task 1.3 Plugin │       │ Task 2.3 SpawnIA  │       │ Task 3.3 LLM Str │       │              │
│ Task 1.4 Entry  │       │ Task 2.4 Builder  │       │ Task 3.4 SQL     │       │              │
│ Task 1.5 V1/V2  │       │ Task 2.5 RunConfig│       │ Task 3.5 N+1     │       │              │
│ Task 1.6 ErrLog │       │ Task 2.6 ToolCtx  │       │ Task 3.6-3.14    │       │              │
│ Task 1.7 Const  │       │ Task 2.7-2.12     │       │                   │       │              │
│ Task 1.8 Dead   │       │                   │       │                   │       │              │
└─────────────────┘      └───────────────────┘      └───────────────────┘      └──────────────┘

依赖关系：
- Task 1.1 ← Task 1.6（先合并函数，再统一加错误处理）
- Task 2.5 ← Task 2.6（RunConfig 和 ToolContext 拆分模式一致）
- Task 2.1 ← Task 2.2（Update 拆分后再改 handleAgentMessage 路由）
- Task 3.1 ← 依赖 Phase 1+2 全部完成（Agent 拆分需稳定的子系统边界）
- Task 3.2 ← 依赖 Task 2.1 + 2.2（cliModel 拆分需先稳定 Update/handle 路由）
- Task 4.6 ← 最后执行（文档更新反映全部重构结果）
```

## 统计预估

| 阶段 | Task 数 | 预估工时 | 可消除重复行 | 风险 |
|------|---------|---------|-------------|------|
| Phase 1 | 8 | ~16h (~2天) | ~500 行 | 🟢 |
| Phase 2 | 12 | ~52h (~1.5周) | ~300 行 | 🟡 |
| Phase 3 | 14 | ~82h (~3周) | ~200 行 + 架构改善 | 🔴 |
| Phase 4 | 6 | ~8h (~1天) | 清理 | 🟢 |
| **总计** | **40** | **~158h (~6周)** | **~1000 行 + 架构质变** | |

## 验证策略

每个 Task 完成后必须通过：

1. `go build ./...` — 编译通过
2. `go test ./...` — 全量测试通过
3. `golangci-lint run ./...` — Lint 通过
4. **手动验证**（涉及 TUI / Agent 交互的 Task）：
   - CLI 正常对话
   - SubAgent 创建/查看/交互
   - Ctrl+C 取消
   - 设置面板
   - 命令面板
   - Cron 任务

## 亮点保留清单

以下设计在重构中**必须保持不变**：

1. ✅ `rpc0`/`rpc1`/`rpc1void` 泛型适配器 — 不改动
2. ✅ `CollectStreamWithCallback` — 不改动（Task 3.3 仅提取解析层）
3. ✅ 全量 SQL 参数化查询 — 保持 `?` 占位符
4. ✅ `parseToolArgs[T]` 泛型 — 不改动
5. ✅ Plugin CAS 状态机 + 双重检查锁 — 不改动
6. ✅ `SaveToFile` 深度 JSON 合并 — 不改动
7. ✅ 包职责边界 — 不改动（`runnerproto`/`bus`/`prompt`/`cmdbuilder`）
