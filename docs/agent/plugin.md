# plugin/ — Plugin System

## Key Files

| File | Purpose |
|------|---------|
| `plugin.go` | Plugin interface, PluginManifest, PluginTool, PluginToolV2, ToolCallContext, PluginDependency |
| `context.go` | PluginContext interface + implementations + type-safe Storage helpers + event bus + error callback |
| `manager.go` | PluginManager — discovery, lifecycle, reload, install, uninstall, watchConfig, health, metrics, String |
| `manifest.go` | Manifest parsing, validation, dependency validation |
| `permissions.go` | Permission checker (includes bus.plugin gate) |
| `storage.go` | Per-plugin KV storage (JSON file based) |
| `eventbus.go` | PluginEventBus — in-process pub/sub with panic recovery |
| `runtime.go` | Native + gRPC runtime factories |
| `runtime_wasm_skel.go` | WASM runtime skeleton (Phase 2, wazero placeholder) |
| `integration.go` | WireAll, PluginToolBridge, PluginHookBridge — integration with xbot subsystems |
| `adapter_tool.go` | PluginToolAdapter, SimplePluginTool, BuildToolDef |
| `adapter_hook.go` | PluginHookBridge, hook dispatch, matcher |
| `adapter_enricher.go` | EnricherRegistry |
| `json.go` | JSON line protocol helpers (for gRPC runtime) |
| `examples/hello-world/` | Example plugin demonstrating the API |
| `examples/git-info/` | Git status widget plugin (triggers on Shell/Cd/FileReplace etc.) |
| `examples/file-diff/` | Diff summary widget plugin (triggers on FileReplace/FileCreate) |
| `widget.go` | WidgetRegistry — per-zone rendering, per-workDir isolation, debounce |
| `script_runtime.go` | Script plugin runtime — bash script execution, env var injection |
| `plugin_test.go` | ~100+ tests including snapshot, debounce, trigger, env injection tests |

## Core Concepts

### Plugin Manifest (plugin.json)
- 声明式配置：id, name, version, runtime, activationEvents, permissions, dependencies, contributes
- LoadManifest 验证所有字段，包括 dependencies 格式

### Plugin Interface
- Manifest() → Activate(PluginContext) → Deactivate(PluginContext)
- 三种运行时：native (in-process), gRPC (external process), wasm (sandbox, Phase 2 skeleton)

### PluginContext
- 安全子集接口：RegisterTool, OnPreToolUse/OnPostToolUse, EnrichContext, Storage, Logger
- Type-safe Storage: StorageInt/StorageBool/StorageJSON/StorageGetJSON — 避免手动类型转换
- Event Bus: Subscribe/Publish (需要 bus.plugin + bus.read/bus.write 权限)
- Error Callback: OnPluginError 注册插件级错误回调 (非 tool 错误)

### Plugin States
- StateDiscovered → StateActive → StateDeactivating → StateInactive
- StateError (加载/运行时错误)

## Key Features

### ToolCallContext V2
- PluginToolV2 接口扩展 PluginTool，通过 ToolCallContext 传递 SessionID/Channel/ChatID/UserID/Ctx
- 适配器自动检测 V2，V1 fallback 零开销
- SimplePluginTool 通过 ExecV2Fn 字段启用

### Event Bus
- 进程内 pub/sub，支持多 handler per topic
- Panic recovery per handler，copy-on-read 安全迭代
- 权限：bus.plugin 门控 + bus.read/bus.write 细粒度

### Hot Reload
- Reload(id): 停用 → 删除 entry → 重新扫描 → 激活 onStart
- ReloadAll(): DeactivateAll → 清空 entries → Discover → ActivateAll

### Install / Uninstall
- InstallPlugin(ctx, sourceDir): 验证 → 复制到 xbotHome → 创建 entry → 自动激活
- UninstallPlugin(ctx, id): 停用 → 删除 entry → 删除磁盘目录（安全检查 xbotHome 范围）

### WatchConfig
- 后台 goroutine 轮询 config.json 的 mtime
- 检测 plugins.disabled_plugins 变化，自动启用/禁用插件
- 最小间隔 5s，返回 stop chan

### Auto-Retry
- SetAutoRetry(enabled, maxRetries) 启动后台 retryLoop goroutine
- 指数退避: 1s * 2^(n-1), 上限 30s
- maxRetries=0 表示无限重试
- 成功后重置 retryCount; DeactivateAll 取消 retry context

### Health Check & Metrics
- HealthChecker 可选接口：返回 map[string]error
- PluginMetrics 聚合指标：TotalPlugins/ActivePlugins/TotalTools/TotalHooks/TotalEnrichers
- String(): "PluginManager{total=5, active=3, error=1, disabled=1}"

### Plugin Dependencies
- manifest.dependencies 声明依赖（id + semver 约束）
- 当前仅做格式验证，不做版本解析/安装检查

## CLI Integration

| 命令 | 说明 |
|------|------|
| `/plugin` | 插件状态摘要 |
| `/plugin list` | 列出所有插件 |
| `/plugin reload <id>` | 重载插件 |
| `/plugin reload-all` | 重载所有 |
| `/plugin health` | 健康检查 |
| `/plugin metrics` | 聚合指标 |
| `/plugin install <dir>` | 安装插件 |
| `/plugin uninstall <id>` | 卸载插件 |

实现位置：channel/cli_helpers.go handlePluginCommand
注入方式：CLIChannel.SetPluginManager → AgentBackend.PluginManager()

## Permissions

| Permission | Description |
|-----------|-------------|
| `tools.register` | 注册新工具 |
| `tools.call` | 调用其他工具 |
| `hooks.subscribe` | 订阅生命周期钩子 |
| `context.enrich` | 注入系统提示内容 |
| `storage.private` | 插件私有 KV 存储 |
| `storage.shared` | 跨插件共享存储 |
| `network.outbound` | 发起网络请求 |
| `bus.plugin` | 启用插件间事件总线（门控） |
| `bus.read` | 读取消息总线 |
| `bus.write` | 写入消息总线 |

## Integration Points

- **Tool Registration**: PluginTool → PluginToolAdapter → PluginToolBridge → tools.Tool → Registry
- **Hook Subscription**: HookHandler → adapter → hooks.CallbackHook → Manager.RegisterBuiltin
- **Context Enrichment**: ContextEnricher → adapter → MessageMiddleware → Pipeline.Use
- **Storage**: ~/.xbot/plugins/<id>/storage.json (per-plugin JSON file)
- **Event Bus**: PluginEventBus (in-process pub/sub)
- **CLI**: /plugin 命令 → handlePluginCommand → PluginManager methods

## File Counts
- 16 Go source files, ~5000+ lines, 100+ tests
- 18 commits in plugin branch

## Script Plugin Runtime

### Triggers
Script plugins support hook-based triggers declared in `plugin.json`:
```json
"triggers": ["PostToolUse:Shell*", "PostToolUse:FileReplace*", "PreToolUse:File*"]
```

Supported trigger events:
- `PostToolUse:<matcher>` — after tool succeeds (matcher supports glob: `Shell*`, `FileReplace*`, etc.)
- `PreToolUse:<matcher>` — before tool executes
- `PostToolUseFailure:<matcher>` — after tool fails
- `UserPromptSubmit:` — on user prompt submission
- `AgentStop:` — on agent stop
- `SessionStart:` / `SessionEnd:` — session lifecycle
- `SubAgentStart:` / `SubAgentStop:` — subagent lifecycle
- `PreCompact:` / `PostCompact:` — context compression
- `CronFired:` / `WebhookReceived:` — scheduled/webhook events

### Environment Variables
When a trigger fires, the script receives environment variables:
- `XBOT_TOOL_NAME` — tool name (e.g. "FileReplace")
- `XBOT_TOOL_OUTPUT` — tool execution result (truncated to 8KB)
- `XBOT_TOOL_INPUT` — tool input parameters as JSON string
- `XBOT_WORK_DIR` — current working directory

### Widget Rendering
Script output format: `"style|text"` where style is one of:
`dim`, `ok`, `warn`, `err`, `info`, `accent`, or empty for normal.

## Widget Push (Remote Mode)

### Multi-Session Isolation (Bug Fix)
Widget rendering is **per-session**: each WebSocket client receives widget content
rendered for its own working directory. This prevents the cross-session overwrite
bug where session B (non-git dir) would overwrite session A's (git repo) content.

**How it works:**
1. `runAndUpdate()` runs the script for ALL known workDirs, not just the current one
2. `NotifyUpdated()` triggers the OnUpdated callback without writing global slot cache
3. Push path uses `RenderZoneForWorkDir(zone, workDir)` per chatID
4. Each client receives only its session-specific widget content

### Script Plugin Triggers Are Global Hooks
Script plugin triggers (from `plugin.json` triggers) are registered as **global hooks**
via `registerGlobalHook()` / `pluginContextImpl.onEvent(..., global=true)`. This means:

- They bypass session isolation in `bridge.Dispatch` (the `ChatID != payload.ChatID`
  check that would otherwise silently skip them)
- They fire regardless of which session triggered the hook event
- This is safe because script plugins manage per-workDir output caches internally
  and the push path renders per-session content via `RenderZoneForWorkDir`

Without this, in multi-session remote CLI, only the session that last called
`RefreshWorkDir` would receive hook triggers; all other sessions' triggers would
be silently filtered, falling back to the 30s ticker only.

### Change Detection in runAndUpdate
`runAndUpdate()` snapshots outputs before re-running scripts, then compares
afterward. Only calls `NotifyUpdated()` when at least one output actually changed.
This avoids unnecessary WebSocket pushes on every 30s ticker when nothing changed.

### Debounce
`WidgetRegistry.SetDebounce(d)` coalesces rapid widget updates into a single
push notification. Default: disabled (immediate). Server-side uses 200ms.

### Incremental Updates
`PushPluginWidgetsPerSession` compares new zones against last-pushed content and skips
the push if nothing changed for that chatID. Reduces WebSocket traffic for idle sessions.

### Per-WorkDir Isolation
`RenderZoneForWorkDir(zone, workDir)` renders widgets using workDir-specific
caches so multiple CLI windows in different git repos see correct branch info.

## HookPayload Extensions
`HookPayload` now carries `ToolOutput` and `ToolElapsedMs` fields from
`PostToolUseEvent`. This enables plugins to inspect tool results (e.g. diff
plugins reading FileReplace output).
