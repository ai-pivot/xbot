# Plugin System Design

## Overview

xbot 插件系统提供类似 VSCode 的可扩展性，允许第三方开发者通过统一的 Plugin API 扩展 xbot 的行为。

**设计哲学**: 统一 Plugin API + 多运行时支持。插件开发者只关心一套接口，运行时（native/gRPC/WASM）是透明的实现细节。

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                  Plugin API (Go Interface)                │
│  Plugin · PluginManifest · PluginContext                  │
├──────────────┬───────────────────┬───────────────────────┤
│   Native     │     gRPC          │      WASM (Phase 2)   │
│  (in-process)│ (external process)│  (wazero sandbox)     │
├──────────────┴───────────────────┴───────────────────────┤
│                 PluginManager (lifecycle)                  │
├───────────────────────────────────────────────────────────┤
│  Event Bus (pub/sub)                                      │
├───────────────────────────────────────────────────────────┤
│  Integration Layer: Tool Registry · Hooks · Middleware     │
└───────────────────────────────────────────────────────────┘
```

## Core Concepts

### 1. Plugin Manifest (`plugin.json`)

每个插件目录下的声明式配置文件，描述插件的元信息、能力贡献、激活条件和权限需求。

```json
{
  "id": "com.example.code-reviewer",
  "name": "Code Reviewer",
  "version": "1.0.0",
  "description": "AI-powered code review tool",
  "author": "example.com",
  "runtime": "native",
  "entry": "main.go",
  "activationEvents": ["onTool:code_review", "onStart"],
  "permissions": ["tools.register", "hooks.subscribe", "storage.private", "network.outbound"],
  "dependencies": [
    { "id": "com.example.git-helper", "version": "^1.0.0" }
  ],
  "contributes": {
    "tools": [
      {
        "name": "code_review",
        "description": "Review code changes and suggest improvements",
        "inputSchema": { ... }
      }
    ],
    "hooks": [
      { "event": "PostToolUse", "matcher": "Shell(*git commit*)" }
    ],
    "contextEnrichers": [
      { "name": "git_status", "description": "Inject current git status" }
    ]
  }
}
```

### 2. Plugin Interface

```go
type Plugin interface {
    // Manifest returns plugin metadata. Called once during discovery.
    Manifest() PluginManifest

    // Activate initializes the plugin and registers capabilities.
    // Called when an activation event fires.
    Activate(ctx PluginContext) error

    // Deactivate cleans up plugin resources. Called on shutdown.
    Deactivate(ctx PluginContext) error
}
```

### 3. PluginContext Interface

受限的能力接口，不暴露 ToolContext 原始字段：

```go
type PluginContext interface {
    // Tool registration
    RegisterTool(tool PluginTool) error

    // Hook subscription
    OnPreToolUse(matcher string, handler HookHandler) error
    OnPostToolUse(matcher string, handler HookHandler) error
    OnUserPrompt(handler HookHandler) error
    OnAgentStop(handler HookHandler) error

    // Context enrichment (upgrade Skills to executable)
    EnrichContext(name string, enricher ContextEnricher) error

    // Isolated storage (per-plugin namespace)
    Storage() StorageAccessor

    // Logging
    Logger() Logger

    // Metadata
    PluginID() string
    WorkingDir() string
    Channel() string
    ChatID() string

    // Plugin Event Bus (requires bus.plugin + bus.read/bus.write permissions)
    Subscribe(topic string, handler PluginEventHandler) error
    Publish(topic string, data any) error
}
```

### 4. Runtime Types

| Runtime | Isolation | Latency | Language Support | Status |
|---------|-----------|---------|-----------------|--------|
| native  | Interface boundary | ~μs (zero-copy) | Go only | Phase 1 |
| gRPC    | Process isolation | ~1-5ms | Any (via protobuf) | Phase 1 |
| wasm    | Sandbox isolation | ~0.5-2ms | WASM-targeting languages | Phase 2 |

### 5. Plugin Lifecycle

```
Discovery → Load Manifest → [Wait for Activation Event]
  → Activate() → Register capabilities → [Running]
  → [Deactivation Event] → Deactivate() → Cleanup
```

Activation Events:
- `onStart` — xbot 启动时立即激活
- `onTool:<name>` — 首次调用指定工具时激活
- `onHook:<event>` — 首次触发指定钩子事件时激活
- `onCommand:<cmd>` — 用户输入指定命令时激活

### 6. Permission System

| Permission | Description |
|-----------|-------------|
| `tools.register` | 注册新工具 |
| `tools.call` | 调用其他工具 |
| `hooks.subscribe` | 订阅生命周期钩子 |
| `context.enrich` | 注入系统提示内容 |
| `storage.private` | 插件私有 KV 存储 |
| `storage.shared` | 跨插件共享存储 |
| `network.outbound` | 发起网络请求 |
| `bus.read` | 读取消息总线 |
| `bus.write` | 写入消息总线 |
| `bus.plugin` | 启用插件间事件总线（必须同时配合 bus.read/bus.write 使用） |

`bus.plugin` 是一个门控权限：插件必须在 permissions 中同时声明 `bus.plugin` 和对应的 `bus.read`/`bus.write` 才能使用事件总线。Subscribe 需要 `bus.plugin` + `bus.read`，Publish 需要 `bus.plugin` + `bus.write`。

### 7. Integration with Existing Systems

**Tool Registration**: PluginTool → PluginToolAdapter → PluginToolBridge → tools.Tool → Registry.Register()

**Hook Subscription**: Plugin HookHandler → adapter → hooks.CallbackHook → Manager.RegisterBuiltin()

**Context Enrichment**: ContextEnricher → adapter → MessageMiddleware → Pipeline.Use()

**Storage**: ~/.xbot/plugins/<id>/storage.db (per-plugin isolated SQLite)

**Event Bus**: PluginContext.Subscribe/Publish → PluginEventBus (in-process pub/sub)

### 8. Plugin Tool V2 (ToolCallContext)

PluginToolV2 是 PluginTool 的向后兼容扩展，通过 `ToolCallContext` 传递丰富的会话信息：

```go
type ToolCallContext struct {
    SessionID string          // 当前会话 ID
    Channel   string          // 消息渠道（cli/feishu/web）
    ChatID    string          // 聊天 ID
    UserID    string          // 触发用户 ID
    Ctx       context.Context // 取消和超时控制
}

type PluginToolV2 interface {
    PluginTool
    ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error)
}
```

**V2 检测策略**：`PluginToolAdapter` 通过 interface assertion 检测底层 tool 是否实现 `PluginToolV2`：
- V2 工具：调用 `ExecuteWithContext`，传入完整会话信息
- V1 工具：fallback 到 `Execute(ctx context.Context, input)`
- `SimplePluginTool` 同时实现两个接口，通过 `ExecV2Fn` 字段可选启用 V2

**PluginToolBridge**（integration.go）是插件工具与 xbot 内部工具系统的桥梁，实现了完整的 `tools.Tool` 接口：

```go
type PluginToolBridge struct {
    adapter *PluginToolAdapter
}
```

调用链：`tools.Tool.Execute(*tools.ToolContext)` → `PluginToolBridge.Execute` → 从 `tools.ToolContext` 提取 `Ctx` 构建 `ToolCallContext` → `adapter.ExecuteWithContext(tcc, input)` → 将 `plugin.ToolResult` 转换为 `tools.ToolResult`。

`PluginToolBridge` 由 `WirePluginTools()` 自动为每个活跃插件的工具创建并注册到 `tools.Registry`。

### 9. Health Check

可选的 `HealthChecker` 接口允许插件报告自身健康状态：

```go
type HealthChecker interface {
    HealthCheck(ctx context.Context) error
}

func (pm *PluginManager) HealthCheck(ctx context.Context) map[string]error
```

- 仅检查 `StateActive` 的插件
- 未实现 `HealthChecker` 的插件视为健康（返回 nil）
- 实现 `HealthChecker` 且返回 error 的插件视为不健康
- 用于监控面板、运维告警、自动重启等场景

### 10. Metrics

`PluginMetrics` 提供插件系统的聚合指标：

```go
type PluginMetrics struct {
    TotalPlugins   int `json:"totalPlugins"`   // 总插件数
    ActivePlugins  int `json:"activePlugins"`  // 活跃插件数
    TotalTools     int `json:"totalTools"`      // 注册的工具总数
    TotalHooks     int `json:"totalHooks"`      // 注册的钩子总数
    TotalEnrichers int `json:"totalEnrichers"`  // 注册的上下文增强器总数
}

func (pm *PluginManager) Metrics() PluginMetrics
```

- 仅统计 `StateActive` 插件的 tools/hooks/enrichers
- JSON 标签用于 API 输出和序列化
- 支持运维监控和仪表盘集成

### 11. Plugin Dependency

插件可以通过 manifest 的 `dependencies` 字段声明对其他插件的依赖：

```go
type PluginDependency struct {
    ID      string `json:"id"`      // 依赖插件 ID
    Version string `json:"version"` // semver 约束（如 "^1.0.0", ">=2.0.0", "*"）
}
```

在 `plugin.json` 中声明：

```json
{
  "dependencies": [
    { "id": "com.example.git-helper", "version": "^1.0.0" },
    { "id": "com.example.shared-lib", "version": ">=2.0.0" }
  ]
}
```

**验证规则**（`LoadManifest` 时执行）：
- `id` 非空且格式合法（与插件 ID 相同的 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` 格式）
- `version` 格式合法（接受 `1.0.0`、`^1.0.0`、`>=2.0.0`、`~1.0.0`、`1.x`、`*` 等 semver 模式）
- 空版本字段视为任意版本

**当前状态**：仅做格式验证，不解析版本号也不检查依赖插件是否已安装。版本解析和依赖拓扑排序将在未来迭代实现。

### 12. Event Bus（插件间通信）

`PluginEventBus` 提供进程内的发布/订阅模型，允许插件之间通过 topic 进行异步通信：

```go
type PluginEventBus struct { ... }

func (pm *PluginManager) Bus() *PluginEventBus

// 通过 PluginContext 使用：
Subscribe(topic string, handler PluginEventHandler) error
Publish(topic string, data any) error
```

```go
type PluginEventHandler func(ctx context.Context, topic string, data any) error
```

**特性**：
- **进程内模型**：所有 handler 在发布者 goroutine 中同步执行，无需额外 goroutine
- **多 handler per topic**：一个 topic 可注册多个 handler，按注册顺序执行
- **Panic recovery**：每个 handler 独立捕获 panic，不会影响其他 handler
- **Copy-on-read**：Publish 时复制 handler 列表，handler 中可安全地 Subscribe/Unsubscribe
- **Unsubscribe**：通过函数指针比较（`reflect.ValueOf(a).Pointer()`），topic 无剩余 handler 时自动清理

**权限控制**：
- Subscribe：需要 `bus.plugin` + `bus.read`
- Publish：需要 `bus.plugin` + `bus.write`
- 权限检查在 `pluginContextImpl.Subscribe/Publish` 中执行，不满足则返回 `PermissionError`

**使用示例**：

```go
func (p *MyPlugin) Activate(ctx PluginContext) error {
    // 订阅事件
    ctx.Subscribe("deploy.completed", func(ctx context.Context, topic string, data any) error {
        log.Printf("Deploy completed: %v", data)
        return nil
    })

    // 发布事件
    ctx.Publish("build.started", map[string]any{"repo": "xbot"})
    return nil
}
```

### 13. Hot Reload

`PluginManager` 支持在运行时重新加载插件，无需重启 xbot：

```go
// 重载单个插件
func (pm *PluginManager) Reload(ctx context.Context, pluginID string) error

// 重载所有插件
func (pm *PluginManager) ReloadAll(ctx context.Context) error
```

**Reload 流程**（单插件）：
1. 获取写锁（持有整个操作期间）
2. 如果插件处于 `StateActive`：Deactivate → 设置 `StateInactive`
3. 从 `entries` map 中删除旧条目
4. 重新扫描插件目录的 `plugin.json`（`LoadManifest`）
5. 重建 `PluginEntry`：新的 `PluginContext`、`Storage`、`Runtime` 实例
6. 如果 manifest 包含 `onStart` 事件：自动 Activate
7. 释放写锁

**ReloadAll 流程**：
1. `DeactivateAll` — 停用所有活跃插件
2. 清空 `entries` map
3. `Discover` — 重新扫描所有插件目录
4. `ActivateAll` — 激活所有 `onStart` 插件

**注意事项**：
- Reload 持有写锁，期间所有查询操作（ListPlugins、GetPlugin 等）会阻塞
- Reload 后需重新调用 `WireAll()` 以将新插件工具/钩子注册到 xbot 子系统
- 如果 `LoadManifest` 或 runtime 创建失败，插件进入 `StateError`

### 14. PluginManager.String()

```go
func (pm *PluginManager) String() string
// 输出示例: PluginManager{total=5, active=3, error=1, disabled=1}
```

人类可读的状态摘要，用于日志输出和调试：

- `total` — entries map 中的插件总数
- `active` — `StateActive` 的插件数
- `error` — `StateError` 的插件数
- `disabled` — 在 disabled 列表中但不在 entries 中的插件数（即被禁用且未加载的）

统计在持有读锁时完成，可安全并发调用。

## File Structure

```
plugin/
├── plugin.go           # Plugin interface, PluginManifest, PluginTool, PluginToolV2, ToolCallContext
├── context.go          # PluginContext interface + implementations + event bus integration
├── manager.go          # PluginManager (discovery, lifecycle, reload, health, metrics, String)
├── manifest.go         # Manifest parsing, validation, dependency validation
├── permissions.go      # Permission checker (includes bus.plugin)
├── storage.go          # Per-plugin KV storage (JSON file)
├── eventbus.go         # PluginEventBus (pub/sub)
├── runtime.go          # Native + gRPC runtime factories
├── runtime_wasm_skel.go # WASM runtime skeleton (Phase 2)
├── integration.go      # WireAll, PluginToolBridge, PluginHookBridge integration
├── adapter_tool.go     # PluginToolAdapter, SimplePluginTool, BuildToolDef
├── adapter_hook.go     # PluginHookBridge, hook dispatch, matcher
├── adapter_enricher.go # EnricherRegistry
├── json.go             # JSON line protocol helpers
├── examples/hello-world/ # Example plugin
└── plugin_test.go      # Tests
```

## Design Decisions

### Why not pure WASM?

Roundtable 结论（5/5 专家同意）：WASM 不适合作为 V1 主运行时。

1. **调试工具链不成熟**：无法 attach debugger，crash 只能拿到 trap，无 coredump
2. **ToolContext 序列化复杂**：45+ 字段包含 6 个 callback 字段无法跨边界传递
3. **开发者体验差**：TS 开发者需要学习 WASM 工具链，无法使用 npm 生态
4. **Go + WASM 生态不成熟**：WASI 支持不完整，wazero 尚未大规模验证

WASM 作为 Phase 2 引入，用于高频轻量 hook 的沙箱场景。

### Why PluginToolV2 with interface embedding (not a breaking change)?

通过 interface embedding (`PluginToolV2` 嵌入 `PluginTool`) 实现 V2 扩展，而非修改现有 `PluginTool` 接口：

1. **零破坏性**：所有现有 PluginTool 实现无需任何修改
2. **渐进式迁移**：插件开发者按需实现 V2，获得会话上下文
3. **运行时检测**：通过 `tool.(PluginToolV2)` 动态判断，零开销 fallback
4. **SimplePluginTool 兼容**：默认走 V1 fallback，设置 ExecV2Fn 即启用 V2

### Why unified Plugin API first?

Platform Engineer 的论点被全员接受：先统一内部扩展点（Tool + Hook + Middleware + Skill），再在 API 下方替换运行时。这确保：
- 插件开发者只学一套 API
- 运行时切换对插件透明
- 现有 Go 工具可渐进迁移到 Plugin 接口

### Why PluginContext instead of ToolContext?

ToolContext 有 45+ 字段，包含 SendFunc、InjectInbound、Registry 等高权限成员。直接暴露给第三方插件等于裸奔。PluginContext 是按权限过滤的安全子集。

### Why in-process Event Bus (not message queue)?

1. **零依赖**：不需要外部基础设施（Redis/NATS），适合单机 xbot 部署
2. **低延迟**：直接函数调用，无序列化/网络开销
3. **简单可靠**：panic recovery 确保一个 handler 崩溃不影响其他
4. **权限可控**：通过 `bus.plugin` 门控 + `bus.read`/`bus.write` 细粒度控制

未来如需跨进程通信，可在 Event Bus 下方替换为消息队列实现，上层 API 不变。

### Why hold write lock during Reload?

Reload 需要原子性地替换插件条目。如果中间状态被读取（如一半旧一半新），会导致不一致。虽然写锁期间会阻塞查询，但 Reload 是低频操作（运维手动触发），短暂阻塞可接受。

## Future (Phase 2)

1. WASM runtime via wazero (lightweight sandbox for trusted env)
2. TypeScript SDK (via protobuf-generated client)
3. Python SDK
4. Plugin Marketplace (registry + install command)
