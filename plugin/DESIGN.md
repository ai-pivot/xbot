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
│  Middleware Chain (logging · recovery · timeout · retry)   │
├───────────────────────────────────────────────────────────┤
│  Rate Limiter · Quota Manager · Audit Trail                │
├───────────────────────────────────────────────────────────┤
│  Config Store · Event Bus · SDK Helpers                    │
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
    ],
    "configuration": {
      "properties": {
        "max_retries": {
          "type": "number",
          "default": 3,
          "description": "Maximum retry attempts"
        }
      }
    }
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

    // Configuration
    Config() (map[string]any, error)

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

**Storage**: ~/.xbot/plugins/<id>/storage.json (per-plugin isolated JSON KV store)

**Event Bus**: PluginContext.Subscribe/Publish → PluginEventBus (in-process pub/sub)

**Middleware**: MiddlewareChain → PluginToolBridge（工具调用前经过中间件链处理）

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

### 15. Install / Uninstall

`PluginManager` 支持在运行时安装和卸载插件，无需手动操作文件系统。

```go
func (pm *PluginManager) InstallPlugin(ctx context.Context, sourceDir string) (*PluginEntry, error)
func (pm *PluginManager) UninstallPlugin(ctx context.Context, pluginID string) error
```

#### InstallPlugin 流程

1. **验证源目录 manifest**：调用 `LoadManifest` 解析并验证源目录下的 `plugin.json`，确保格式合法
2. **获取写锁**：持有写锁防 TOCTOU（Time-of-check to time-of-use），确保整个安装过程原子性
3. **检查 ID 冲突**：在 `entries` map 中检查是否已存在相同 ID 的插件
4. **复制到目标目录**：将源目录完整复制到 `xbotHome/plugins/<id>/`
5. **重验证 manifest**：从目标目录重新加载 manifest，确保复制后文件完整
6. **创建 entry**：构建 `PluginEntry`（manifest + PluginContext + Storage + Runtime）
7. **自动激活 onStart 插件**：如果 manifest 包含 `onStart` 事件，立即 Activate
8. **释放写锁**

#### UninstallPlugin 流程

1. **获取写锁**：持有写锁确保操作原子性
2. **停用活跃插件**：如果插件处于 `StateActive`，执行 Deactivate 并设置 `StateInactive`
3. **删除 entries/disabled 条目**：从内存 map 中移除
4. **释放写锁**：先释放锁
5. **删除磁盘目录**：在锁外执行磁盘 I/O，通过 `filepath.EvalSymlinks` 解析真实路径并验证在 `xbotHome` 范围内，防止路径遍历攻击

#### 安全设计

- InstallPlugin 持有写锁期间阻塞所有查询操作（ListPlugins、GetPlugin 等），确保安装过程不被中间状态干扰
- UninstallPlugin 在释放写锁**之后**再执行磁盘删除操作，避免长时间持锁（磁盘 I/O 可能较慢）
- UninstallPlugin 仅删除 `xbotHome/plugins/` 下的目录，通过 `EvalSymlinks` + 前缀检查防止路径遍历

### 16. WatchConfig（配置热更新）

`WatchConfig` 提供配置文件轮询机制，运维修改 `config.json` 后自动生效，无需重启 xbot。

```go
func (pm *PluginManager) WatchConfig(configPath string, interval time.Duration) chan struct{}
```

#### configWatcher 结构体

```go
type configWatcher struct {
    configPath  string        // 配置文件路径
    lastModTime time.Time     // 上次已知的文件修改时间
    lastConfig  *configChangeState // 上次已知的配置快照
    interval    time.Duration // 轮询间隔
}
```

#### 工作流程

1. 启动后台 goroutine，按 `interval` 轮询配置文件的 `mtime`
2. 当 mtime 变化时，读取配置文件并提取 `plugins.disabled_plugins` 字段
3. 调用 `applyDiff` 比较新旧列表：
   - **新增 disabled**：停用对应插件 + 加入 disabled map
   - **移除 disabled**：从 disabled map 删除 → 重新 Discover + 激活
4. 返回 `stop chan`，调用方关闭即可停止轮询 goroutine

#### 约束

- 最小间隔 5 秒（低于此值自动调整为 5s），防止过于频繁的文件 I/O
- 配置读取失败时保持上次配置不变，不会误停用插件

### 17. CLI `/plugin` 命令

xbot CLI 提供 `/plugin` 命令用于插件管理：

| 命令 | 说明 |
|------|------|
| `/plugin` | 显示插件状态摘要（总数、活跃数） |
| `/plugin list` | 列出所有插件的详细信息（ID、版本、状态、工具数） |
| `/plugin reload <id>` | 重载指定插件 |
| `/plugin reload-all` | 重载所有插件 |
| `/plugin health` | 健康检查所有活跃插件 |
| `/plugin metrics` | 显示聚合指标 |
| `/plugin install <dir>` | 从指定目录安装插件 |
| `/plugin uninstall <id>` | 卸载指定插件 |

#### 实现细节

- 命令在 `channel/cli_helpers.go` 的 `handlePluginCommand` 函数中实现
- 通过 `CLIChannel.SetPluginManager` 注入 `PluginManager` 回调
- 状态显示使用 `lipgloss` 样式（`pluginStateStyled` 函数）
- `AgentBackend` 接口提供 `PluginManager()` 方法：
  - `LocalBackend` 返回实际 PluginManager 实例
  - `RemoteBackend` 返回 nil（远程模式不支持本地插件管理）

### 18. File Structure

完整的文件结构列表：

```
plugin/
├── plugin.go           # Plugin interface, PluginManifest, PluginTool, PluginToolV2, ToolCallContext, PluginDependency
├── context.go          # PluginContext interface + implementations + event bus integration + resource tracking
├── manager.go          # PluginManager (discovery, lifecycle, reload, install, uninstall, watchConfig, health, metrics, String, auto-retry, audit)
├── manifest.go         # Manifest parsing, validation, dependency validation, checksum verification
├── permissions.go      # Permission constants + PermissionChecker
├── storage.go          # Per-plugin KV storage (JSON file)
├── eventbus.go         # PluginEventBus (pub/sub)
├── config.go           # PluginConfigStore (user-level configuration storage)
├── middleware.go        # MiddlewareChain + built-in middleware (logging, recovery, timeout, retry)
├── ratelimit.go        # PluginRateLimiter + PluginQuotaManager
├── audit.go            # AuditLogger (append-only JSONL audit trail)
├── sdk.go              # SDK helpers (ToolFromFunc, QuickManifest, etc.)
├── runtime.go          # Native + gRPC runtime factories
├── runtime_wasm_skel.go # WASM runtime skeleton (Phase 2)
├── integration.go      # WireAll, PluginToolBridge, rate-limit aware bridge
├── adapter_tool.go     # PluginToolAdapter, SimplePluginTool, BuildToolDef
├── adapter_hook.go     # PluginHookBridge, hook dispatch, matcher
├── adapter_enricher.go # EnricherRegistry
├── json.go             # JSON line protocol helpers
├── examples/hello-world/ # Example plugin
├── plugin_test.go      # Tests
├── middleware_test.go   # Middleware tests
├── ratelimit_test.go   # Rate limiter tests
└── sdk_test.go         # SDK helper tests
```

### 19. Middleware Chain（中间件链）

`MiddlewareChain` 为插件工具调用提供可组合的中间件管道，采用洋葱模型执行：

```go
type PluginMiddleware func(ctx context.Context, toolName, input string, next PluginMiddlewareNext) (*ToolResult, error)
type PluginMiddlewareNext func(ctx context.Context, toolName, input string) (*ToolResult, error)

type MiddlewareChain struct { ... }

func NewMiddlewareChain(middlewares ...PluginMiddleware) *MiddlewareChain
func (c *MiddlewareChain) Execute(ctx context.Context, toolName, input string, final PluginMiddlewareNext) (*ToolResult, error)
func (c *MiddlewareChain) Use(middleware PluginMiddleware)
func (c *MiddlewareChain) Len() int
```

**设计要点**：
- 中间件按注册顺序执行（洋葱模型），先注册的在最外层
- `Execute()` 方法接收 `final handler`，链式调用
- `Use()` 可动态追加中间件
- 每个中间件通过 `next` 函数传递控制权给下一层

#### 内置中间件

| 中间件 | 说明 |
|--------|------|
| `LoggingMiddleware(logger Logger)` | 记录工具调用的 toolName、duration、error |
| `RecoveryMiddleware(logger Logger)` | panic recovery，捕获 panic 并返回 error |
| `TimeoutMiddleware(timeout time.Duration)` | context 超时控制，超时返回 DeadlineExceeded |
| `RetryMiddleware(maxRetries int)` | 自动重试，在 error 时重试最多 maxRetries 次 |

**使用示例**：

```go
chain := NewMiddlewareChain(
    RecoveryMiddleware(logger),
    LoggingMiddleware(logger),
    TimeoutMiddleware(30 * time.Second),
)

result, err := chain.Execute(ctx, "my_tool", `{"key":"value"}`, func(ctx context.Context, toolName, input string) (*ToolResult, error) {
    // final handler — 实际的工具执行逻辑
    return tool.Execute(ctx, input)
})
```

### 20. Plugin Configuration System（配置系统）

`PluginConfigStore` 提供用户级别的插件配置存储，与插件安装目录独立：

```go
type PluginConfigStore struct { ... }

func NewPluginConfigStore(xbotHome string) *PluginConfigStore
func (s *PluginConfigStore) Load(pluginID string) (map[string]any, error)
func (s *PluginConfigStore) Save(pluginID string, config map[string]any) error
func (s *PluginConfigStore) Update(pluginID, key string, value any) error
func (s *PluginConfigStore) InvalidateCache(pluginID string)
func GetDefaultConfig(manifest *PluginManifest) map[string]any
```

**配置文件位置**：`~/.xbot/plugins/<id>/config.json`

**设计要点**：
- 与插件安装目录独立，用户级别配置
- 原子写入：temp file + rename，防止半写状态
- 内存缓存 + RWMutex 保护并发读写
- `Update()` 在写锁保护下完成 load-modify-save 全流程
- `InvalidateCache()` 清除指定插件的内存缓存，强制下次 Load 从磁盘读取

**Manifest 配置 Schema**：

插件可在 `plugin.json` 的 `contributes.configuration` 中声明配置 schema 和默认值：

```json
{
  "contributes": {
    "configuration": {
      "properties": {
        "max_retries": {
          "type": "number",
          "default": 3,
          "description": "Maximum retry attempts"
        },
        "api_endpoint": {
          "type": "string",
          "default": "https://api.example.com",
          "description": "API endpoint URL"
        }
      }
    }
  }
}
```

`GetDefaultConfig()` 从 manifest 的 `contributes.configuration.properties` 中提取默认值，生成 `map[string]any`。如果用户尚未保存配置，PluginContext.Config() 返回默认值。

**PluginContext 集成**：
- `PluginContext.Config()` 返回当前配置的 `(map[string]any, error)`，包含 manifest 默认值和用户覆盖
- PluginContext 内部通过 `configChange` channel 支持配置变更通知

### 21. Rate Limiting / Quota（速率限制与配额）

#### 速率限制

`PluginRateLimiter` 基于 pluginID 维度的滑动窗口速率限制：

```go
type RateLimit struct {
    MaxCalls int
    Window            time.Duration
}

type PluginRateLimiter struct { ... }

func NewPluginRateLimiter(config map[string]RateLimit) *PluginRateLimiter
func (rl *PluginRateLimiter) Allow(pluginID string) bool
func (rl *PluginRateLimiter) Remaining(pluginID string) int
func (rl *PluginRateLimiter) Reset(pluginID string)
func (rl *PluginRateLimiter) SetRateLimit(pluginID string, limit RateLimit)
```

**设计要点**：
- 基于 pluginID 维度，每分钟请求数限制
- 滑动窗口实现：每个 pluginID 维护独立的请求时间戳列表
- `Allow()` 检查并淘汰过期窗口外的记录，判断是否允许请求
- `Remaining()` 返回当前窗口内剩余可用配额
- `Reset()` 清除指定插件的速率限制记录
- `SetRateLimit()` 动态设置或更新指定插件的速率限制

#### 配额管理

`PluginQuotaManager` 管理插件级别的资源配额：

```go
type PluginQuota struct {
    MaxToolCallsPerDay int64
    MaxStorageMB       int64
}

type PluginQuotaManager struct { ... }

func NewPluginQuotaManager(quotas map[string]PluginQuota) *PluginQuotaManager
func (qm *PluginQuotaManager) SetQuota(pluginID string, quota PluginQuota)
func (qm *PluginQuotaManager) SetStorage(pluginID string, storage StorageAccessor)
func (qm *PluginQuotaManager) CheckToolCall(pluginID string) (bool, int64)
func (qm *PluginQuotaManager) CheckStorage(pluginID string) (bool, int64)
func (qm *PluginQuotaManager) GetQuotaUsage(pluginID string) (toolCalls int64, storageBytes int64)
func (qm *PluginQuotaManager) ResetDaily()
```

**设计要点**：
- 工具调用次数限制 + 存储字节数限制
- `CheckToolCall()` 检查工具调用次数是否超过 MaxToolCallsPerDay
- `CheckStorage()` 通过 StorageAccessor 检查 storage.json 文件大小是否超过 MaxStorageMB（MB 单位）
- `GetQuotaUsage()` 返回指定插件的配额使用情况（toolCalls, storageBytes）
- `ResetDaily()` 重置所有插件的工具调用计数（每日 UTC 0 点自动调用）
- 与 PluginToolBridge 集成：在 Execute 前检查 `Allow()` + `CheckToolCall()`

### 22. Audit Trail（审计追踪）

`AuditLogger` 提供追加写入的审计日志，记录插件系统的关键操作：

```go
type AuditLogger struct { ... }

func NewAuditLogger(path string) (*AuditLogger, error)
func (al *AuditLogger) Log(entry AuditEntry)
func (al *AuditLogger) Query(filter AuditFilter) []AuditEntry
func (al *AuditLogger) Clear() error
func (al *AuditLogger) Close()
```

#### AuditEntry 结构

```go
type AuditEntry struct {
    Timestamp time.Time      `json:"timestamp"`
    PluginID  string         `json:"plugin_id"`
    Action    string         `json:"action"`
    Details   map[string]any `json:"details,omitempty"`
    Error     string         `json:"error,omitempty"`
}
```

**记录的关键动作**：`activate`、`deactivate`、`install`、`uninstall`、`reload`、`disable`

**设计要点**：
- 追加写入 JSONL 格式（`O_APPEND|O_CREATE|O_WRONLY`），每行一个 JSON 对象
- 静默写入：Write 错误不阻塞调用方，仅记录日志
- Query 支持按 `pluginID` + 时间范围过滤（`AuditFilter`）
- 默认路径：`~/.xbot/plugins/audit.jsonl`
- `PluginManager.AuditLog()` 暴露 logger 实例
- `PluginManager` 内部通过 `audit()` 辅助方法在关键操作点自动记录审计条目

**使用示例**：

```go
// PluginManager 内部调用
pm.audit("com.example.my-plugin", "activate", nil, nil)

// 外部查询审计记录
entries := pm.AuditLog().Query(AuditFilter{
    PluginID: "com.example.my-plugin",
    From:     time.Now().Add(-24 * time.Hour),
    To:       time.Now(),
})
```

### 23. Resource Tracking（资源追踪）

PluginContext 实现中的资源追踪字段，用于配额管理和运维监控：

```go
// 在 pluginContextImpl 中
toolCallCount  atomic.Int64  // 工具调用计数
hookCallCount  atomic.Int64  // 钩子调用计数
```

**相关方法**：

| 方法 | 调用位置 | 说明 |
|------|---------|------|
| `incrementToolCallCount()` | `PluginToolAdapter.Execute` | 每次工具调用后递增 |
| `incrementHookCallCount()` | `PluginHookBridge.Dispatch` | 每次钩子分发后递增 |
| `ToolCallCount() int64` | 外部读取 | 返回当前工具调用次数 |
| `HookCallCount() int64` | 外部读取 | 返回当前钩子调用次数 |

**设计要点**：
- 使用 `atomic.Int64` 确保并发安全的无锁计数
- 工具调用计数与 QuotaManager 的 `CheckToolCall()` 配合使用
- 钩子调用计数用于运维监控，辅助诊断插件行为异常

### 24. SDK Helpers（SDK 辅助函数）

`sdk.go` 提供一组简洁的辅助函数，降低插件开发者的入门成本：

#### 工具工厂

```go
// 从简单函数创建 PluginTool（自动包装返回值为 ToolResult）
func ToolFromFunc(name, desc string, fn func(ctx context.Context, input string) (string, error)) PluginTool

// JSON 输入输出的工具（自动 marshal/unmarshal）
func ToolFromJSONFunc(name, desc string, params []ToolParamDef, fn func(ctx context.Context, input json.RawMessage) (any, error)) PluginTool
```

`ToolFromFunc` 将简单的 `func(ctx, input) → (string, error)` 自动包装为 `PluginTool` 实现，开发者无需关心 `ToolResult` 结构。

`ToolFromJSONFunc` 自动处理 JSON 的 marshal/unmarshal，开发者只需处理结构化数据。

#### 钩子工厂

```go
// 拒绝并返回消息
func DenyHook(msg string) HookHandler

// 允许通过
func AllowHook() HookHandler

// 记录日志后允许通过
func LogHook(logger Logger, msg string) HookHandler
```

#### 上下文增强器工厂

```go
// 返回静态内容
func StaticEnricher(content string) ContextEnricher

// 读取文件内容作为增强上下文
func FileEnricher(path string) ContextEnricher
```

#### Manifest 快速构建

```go
// 流式构建 manifest
func QuickManifest(id, name, version, description string, opts ...ManifestOption) PluginManifest

// ManifestOption 函数式选项
func WithPermissions(perms ...string) ManifestOption
func WithActivationEvents(events ...string) ManifestOption
func WithRuntime(rt RuntimeType) ManifestOption
func WithTools(tools ...ToolContribution) ManifestOption
func WithHooks(hooks ...HookContribution) ManifestOption
func WithEnrichers(enrichers ...EnricherContribution) ManifestOption
```

**使用示例**：

```go
manifest := QuickManifest(
    "com.example.review",
    "Code Reviewer",
    "1.0.0",
    "AI-powered code review",
    WithActivationEvents("onStart"),
    WithPermissions("tools.register", "hooks.subscribe"),
    WithTools(ToolContribution{
        Name:        "review",
        Description: "Review code",
    }),
)
```

#### MustActivate

```go
// Activate 并在失败时 panic（用于简单插件的初始化）
func MustActivate(p Plugin, ctx PluginContext)
```

### 25. Auto-Retry / Timeout（自动重试与超时）

PluginManager 支持故障插件的自动重试和激活超时控制：

```go
func (pm *PluginManager) SetAutoRetry(enabled bool, maxRetries int)
func (pm *PluginManager) SetRetryInterval(d time.Duration)
```

#### 自动重试

**后台 goroutine** 周期性扫描 `StateError` 的插件并尝试重新激活：

- **指数退避**：初始间隔 1s，每次翻倍，上限 30s
- **重试次数**：`maxRetries=0` 表示无限重试
- **重试成功**：重置 `retryCount`，插件恢复 `StateActive`
- **扫描间隔**：可通过 `SetRetryInterval` 调整（默认 5s）
- **生命周期**：`DeactivateAll` 时自动停止 retry goroutine

**PluginEntry 记录**：

```go
type PluginEntry struct {
    // ...
    retryCount  int       // 当前重试次数
    lastError   error    // 最近一次错误信息
    lastErrorAt time.Time // 最近一次错误时间
}
```

#### 激活超时

- 通过 manifest 的 `timeout` 字段配置激活超时时间（默认 30s）
- Activate 超时后插件进入 `StateError`
- 进入 `StateError` 的插件可被 auto-retry goroutine 自动重新激活

**使用示例**：

```go
// 启用自动重试，最多 10 次
pm.SetAutoRetry(true, 10)

// 调整扫描间隔为 10 秒
pm.SetRetryInterval(10 * time.Second)

// 无限重试
pm.SetAutoRetry(true, 0)
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

### Why middleware chain pattern (洋葱模型)?

1. **便于组合**：logging + recovery + timeout 可按任意顺序组合，每个中间件单一职责
2. **洋葱模型**：先注册的在最外层，请求从外到内穿透，响应从内到外返回，适合横切关注点（如日志记录开始和结束时间）
3. **可扩展**：插件开发者可通过 `Use()` 动态追加自定义中间件
4. **与工具调用解耦**：中间件链与具体工具实现无关，一处注册全局生效

### Why append-only JSONL for audit?

1. **追加写入保证不丢失**：`O_APPEND` 标志确保每次写入都是原子追加，即使进程崩溃也不丢失已有记录
2. **JSONL 便于 grep/jq**：每行一个 JSON 对象，运维可直接用 `grep`、`jq` 等标准工具分析
3. **无需数据库依赖**：单机部署场景不需要引入外部数据库，纯文件即可满足审计需求
4. **简单可靠**：无索引维护、无压缩逻辑，写入路径极短

### Why in-memory rate limiting?

1. **单机部署场景足够**：xbot 是单进程应用，所有插件在同一进程内，内存级别速率限制完全满足需求
2. **无需 Redis**：避免引入外部依赖，保持部署简单
3. **简单可靠**：滑动窗口实现逻辑清晰，易于理解和调试

### Why atomic config write?

1. **temp file + rename 防止半写状态**：如果进程在写入过程中崩溃，旧文件保持完整
2. **rename 是原子操作**：在 POSIX 系统上 `rename(2)` 是原子的，确保配置文件要么是旧的要么是新的
3. **无损坏风险**：不会出现 JSON 解析到一半的情况

### Why exponential backoff for retry?

1. **避免雪崩**：故障插件如果被高频重试，会消耗大量资源并可能影响其他插件
2. **给故障插件恢复时间**：指数退避让底层依赖有时间恢复（如网络抖动、外部服务过载）
3. **上限控制**：30s 上限防止退避间隔过长，平衡恢复速度和资源消耗

## Future (Phase 2)

1. **WASM runtime via wazero**：骨架已实现（`runtime_wasm_skel.go`），包含 `wasmRuntime` 结构体和 `RuntimeFactory` 实现、`wasmPlugin` 结构体（当前为 no-op，`Activate` 时仅打日志）、`WASMHostAPI` 接口定义（未来的 ABI）。真正的 WASM 支持需要添加 `wazero` 依赖
2. TypeScript SDK (via protobuf-generated client)
3. Python SDK
4. Plugin Marketplace (registry + install command)
