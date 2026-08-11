# plugin/ — Plugin System

## Key Files

| File | Purpose |
|------|---------|
| `plugin.go` | Plugin interface, PluginManifest, PluginTool, PluginToolV2, ToolCallContext, PluginDependency |
| `audit.go` | AuditLogger — daily-rotating JSONL audit trail with per-plugin query |
| `plog.go` | Per-plugin log infrastructure: rotateWriter, pluginLogManager, unified cleanup |
| `context.go` | PluginContext interface + implementations + type-safe Storage helpers + event bus + error callback + per-plugin Logger |
| `manager.go` | PluginManager — discovery, lifecycle, reload, install, uninstall, watchConfig, health, metrics, String |
| `manifest.go` | Manifest parsing, validation, dependency validation |
| `permissions.go` | Permission checker (includes bus.plugin gate) |
| `storage.go` | Per-plugin KV storage (JSON file based) |
| `eventbus.go` | PluginEventBus — in-process pub/sub with panic recovery |
| `runtime.go` | Native + stdio runtime factories |
| `runtime_wasm_skel.go` | WASM runtime skeleton (Phase 2, wazero placeholder) |
| `integration.go` | WireAll, PluginToolBridge, PluginHookBridge — integration with xbot subsystems |
| `adapter_tool.go` | PluginToolAdapter, SimplePluginTool, BuildToolDef |
| `adapter_hook.go` | PluginHookBridge, hook dispatch, matcher |
| `adapter_enricher.go` | EnricherRegistry |
| `json.go` | JSON line protocol helpers (for stdio runtime) |
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
- 三种运行时：native (in-process), stdio (external process, `"grpc"` alias for backward compat), wasm (sandbox, Phase 2 skeleton)

### PluginContext
- 安全子集接口：RegisterTool, OnPreToolUse/OnPostToolUse, EnrichContext, Storage, Logger
- Type-safe Storage: StorageInt/StorageBool/StorageJSON/StorageGetJSON — 避免手动类型转换
- Event Bus: Subscribe/Publish (需要 bus.plugin + bus.read/bus.write 权限)
- Error Callback: OnPluginError 注册插件级错误回调 (非 tool 错误)
- Logger: per-plugin 日志文件 only（不写主日志，保持主日志简洁），支持 Debugf/Infof/Warnf/Errorf 格式化

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
- `XBOT_WIDGET_ID` — the widget ID that triggered this render (e.g. "git-branch"). Scripts can use this to produce different output for different widgets within the same plugin.
- `XBOT_MODEL` — current LLM model name (e.g. "claude-sonnet-4-20250514")
- `XBOT_MAX_CONTEXT` — maximum context window in tokens (e.g. "200000")
- `XBOT_TOKEN_USAGE` — token usage as `prompt/completion` (e.g. "12345/678")
- `XBOT_PROMPT_TOKENS` — cumulative prompt tokens (input + context)
- `XBOT_COMP_TOKENS` — cumulative completion tokens (output)

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

## Per-Plugin Logging System

Each plugin writes to its own daily-rotated log file, with unified cleanup.

### Architecture

```
plugin/plog.go
  ├── rotateWriter        — daily-rotating io.Writer (custom suffix: .log/.jsonl)
  ├── pluginLogManager    — manages per-plugin rotateWriters + single cleanup goroutine
  ├── cleanLogDir         — shared cleanup for .log/.jsonl files by maxAge
  └── multiWriter         — dynamic writer list (add/remove at runtime)

plugin/context.go
  └── pluginLogger        — writes ONLY to per-plugin file (fallback to global logrus if writer unavailable)
```

### Log File Locations

| Component | Path Pattern | Rotation |
|-----------|-------------|----------|
| Plugin logs | `~/.xbot/plugins/<id>/logs/<id>-YYYY-MM-DD.log` | Daily |
| Audit logs | `~/.xbot/plugins/audit-YYYY-MM-DD.jsonl` | Daily |

### Design Decisions

- **Single cleanup goroutine**: `pluginLogManager.cleanupLoop()` runs hourly, scans all
  `~/.xbot/plugins/*/logs/` subdirectories + audit directory. Default maxAge: 7 days.
- **File-only write**: `pluginLogger` writes ONLY to the per-plugin log file — it does NOT
  write to the global logrus logger. This keeps the main xbot log clean: plugin operational
  logs (tool/hook registration, script execution, widget refresh) are isolated in the plugin's
  own directory. If the per-plugin writer could not be created (fileOut == nil), it falls back
  to global logrus so no logs are silently lost.
- **Script runtime logs isolated**: `script_runtime.go` routes all operational logs (script
  execution, output, trigger firing, refresh) through `p.logger()` → per-plugin log file.
  Only startup events ("Script plugin X started") use global `log.Info`.
- **rotateWriter is standalone**: Implemented in plugin package (not reusing logger.dailyRotateFile)
  to avoid circular dependency (plugin → logger → plugin would be circular).
- **Logger interface extended**: Added `Debugf/Infof/Warnf/Errorf` formatted methods.
  All Logger implementations (pluginLogger, loggerWithFields, testLogger, captureLogger,
  sdkMockLogger) must implement these.
- **No lumberjack dependency**: Pure stdlib rotation by date (same as main logger).

### Integration Points

- `PluginManager` creates `pluginLogManager` in `NewPluginManager()`
- `Discover()` calls `newPluginLogger(id, pm.logMgr)` which gets a per-plugin rotateWriter
- `PluginManager.Close()` calls `logMgr.CloseAll()` to stop cleanup goroutine + close writers
- AuditLogger uses `newRotateWriterWithSuffix(dir, "audit", ".jsonl")` for daily rotation

## Web UI Plugin System (web_ui protocol)

Web 插件系统让**插件**（而非 agent/LLM）为 web 界面贡献炫酷 UI 组件，复用并扩展现有插件协议。
三层能力，自由度和复杂度递增：

| 层次 | 方式 | 自由度 | 示例 |
|------|------|--------|------|
| 1. 结构化 widget | script plugin 输出 `style\|text` → web 结构化渲染 | 文本+语义色 | 状态栏 Git 状态、infoBar CI 徽章 |
| 2. 声明式组件 | `web_ui` 声明 `{type, props}` | 8 种内置组件 | sparkline 走势、table 构建历史、metric 指标卡 |
| 3. 自由代码 | `web_ui` 声明 `code`/`src` | **完全自主**（iframe 沙箱） | 任意 TSX/HTML 组件、ECharts 大盘 |

### 渠道自订阅架构（WidgetSubscriber）

`channel/interfaces.go` 新增 `WidgetSubscriber` 接口，与 `ProgressSender`/`SessionStateSender` 同构：

```go
type WidgetSubscriber interface {
    SetWidgetRegistry(wr *plugin.WidgetRegistry) // 创建时注入一次
    NotifyWidgetsUpdated()                        // 通知：widget 更新了，渠道自己决定怎么做
}
```

- **agent 只做通知**：`agent.go` 的 OnUpdated 回调遍历所有 channel，类型断言 `WidgetSubscriber` 后调用
  `NotifyWidgetsUpdated()` —— 不再硬编码 CLI/Web 分支
- **渠道自己决定**：CLI channel 渲染 ANSI（`plugin_widgets` 消息），Web channel 渲染结构化 JSON
  （`web_widgets` 消息），未来新渠道实现接口即获得能力
- **WebChannel** 实现：`NotifyWidgetsUpdated` 遍历 web 订阅的 chatID → `RenderSessionWebWidgets`
  （结构化 spans，非 ANSI）→ 增量 diff 后推 `web_widgets` SSE/WS 消息

### 结构化 widget 数据（WebWidgetSpan）

`plugin/web_widget.go` 定义 `WebWidgetSpan{Text, Style, Icon, Href}` —— web 专用结构化 span。
`RenderSessionWebWidgets(wr, getCWD, chatID)` 返回 `map[zone][]WebWidgetSpan`，与 CLI 的
`RenderSessionWidgets`（ANSI 字符串）并列。StyleRaw 内容（终端转义）对 web 端**跳过**。

### web_ui 组件声明协议

Channel plugin 通过 `web_ui` 消息声明 web 组件（热更新覆盖式，同 channel_tools 语义）：

```json
{"type":"web_ui","ui":[
  {"widget_id":"ci-monitor","title":"CI","slot":"right_sidebar","refresh":"30s",
   "component":{"type":"sparkline","props":{"data":[1,5,3,8],"color":"#22c55e"}}}
]}
```

- 后端：`ChannelPluginTransport.handleChannelUI` → `Agent.RegisterChannelWebUI` → `WebUIRegistry`
- 前端：`web_widgets` 消息携带 `{zones, components}` → `PluginWidgetProvider` → `PluginComponentPanel`
  按 slot 渲染（right_sidebar / panel / status_bar_left / info_bar 等）
- 内置组件：`badge` / `progress` / `metric` / `sparkline` / `table` / `list` / `markdown`
- **自由代码**：`code` 字段传任意 TSX 源码 → 前端 `SandboxedUI`（泛化自 `GenUIBlock`）在
  iframe 沙箱内 sucrase 编译 + 独立 React root 渲染；`src` 字段加载外部可信 URL

### web_ui_action 交互协议

组件交互（点击/输入）回传给插件：

```
前端组件 data-action 点击
  → PluginComponentPanel.onAction → POST /api/rpc {method:"web_ui_action"}
  → serverapp rpc_table.go：
      1. WebUIRegistry.Owner(widgetID) 找到归属 channel
      2. Agent.ChannelPluginCall(owner, "web_ui_action", params) → channel plugin transport RPC
      3. 无归属 → native 插件 handler（PluginContext.RegisterWebActionHandler）
      4. 都无 → 注入 agent loop（同 genui_action）
```

- Channel plugin 端：stdio `Handler.WebUIAction`（dispatch case `web_ui_action`）
- Native 插件端：`PluginContext.RegisterWebActionHandler(widgetID, handler)`

### 安全边界

- 声明式组件：props 全走 React 文本节点渲染（自动转义），类型防御性校验
- 自由代码：`SandboxedUI` iframe `sandbox="allow-scripts allow-same-origin"`，插件代码无法访问
  父页面 DOM/Cookie/token（同 GenUIBlock 机制）
- `src` 外部 URL 建议仅可信源（file:/javascript:/data: 被浏览器 sandbox 拒绝）
- 插件信任模型：插件本就有 `execute_tool` 可执行任意命令，iframe 隔离是防御纵深而非信任基础


