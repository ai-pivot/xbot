---
title: "Go 插件"
weight: 7
---

原生 Go 插件编译进 xbot 二进制并进程内运行。这是功能最全的运行时：完全访问 `PluginContext`、组件注册表、Hook、存储与事件总线。示例：`plugin/examples/hello-world/`（工具 + Hook + 注入器）与 `plugin/examples/git-pr-status/`（Hook → UI 桥接）。

## Plugin 接口

`plugin/plugin.go:26` —— 一切的起点：

```go
type Plugin interface {
	Manifest() PluginManifest
	Activate(ctx PluginContext) error
	Deactivate(ctx PluginContext) error
}
```

- `Manifest()` 在发现阶段调用一次。
- `Activate` 必须**幂等**，接收 `PluginContext`——访问 xbot 能力的唯一入口。
- `Deactivate` 在关闭/卸载时执行；之后不再有任何回调。

## PluginContext —— 能力面

`PluginContext`（`plugin/context.go:108`）组合了十个子接口：

| 子接口 | 能力 |
|---|---|
| `ToolRegistrar` | `RegisterTool`、`RegisterTools`、`UseMiddleware` |
| `HookSubscriber` | `OnPreToolUse`、`OnPostToolUse`、`OnUserPrompt`、`OnAgentStop`、`OnSessionStart`、`OnSessionEnd`、`OnEvent`、`OnAllToolUse`、`OnError` |
| `StorageProvider` | `Storage()`、`StorageInt`、`StorageBool`、`StorageJSON`、`StorageGetJSON` |
| `SessionMetadata` | `PluginID()`、`WorkingDir()`、`Channel()`、`ChatID()`、`TenantID()`、`Logger()` |
| `EventBusPublisher` | `Subscribe`、`Publish` |
| `UIContributor` | `ContributeUI`、`UpdateWidget`、`ContributeTheme`、`RegisterOverlay`、`ShowOverlay`、`HideOverlay`、`RegisterWebActionHandler` |
| `CronScheduler` | `ScheduleCron`、`CancelCron` |
| —（直接方法） | `EnrichContext`、`OnPluginError`、`SetValue`/`GetValue`、`ToolCallCount`/`HookCallCount`、`RegisterChannelProvider`、`RegisterCommand`、`Notify`、`PlaySound`、`Config()`/`SetConfig`/`OnConfigChanged` |

新代码可以接受更窄的子接口（例如只接受 `ToolRegistrar`）——鼓励接口隔离。

## Hook → UI 桥接模式

`git-pr-status`（`plugin/examples/git-pr-status/main.go`）是有状态组件的范式：Hook 检测事件 → 修改插件状态 → 推送组件重渲染：

```go
func (p *GitPRPlugin) Activate(ctx plugin.PluginContext) error {
	p.pctx = ctx
	if err := ctx.ContributeUI("git-branch", "statusBarRight", p, 10); err != nil {
		return fmt.Errorf("contribute UI: %w", err)
	}
	if err := ctx.OnPostToolUse("Shell*", p.onPostToolUse); err != nil {
		return fmt.Errorf("register hook: %w", err)
	}
	return nil
}

// UIWidget 实现
func (p *GitPRPlugin) Render(width int) []plugin.WidgetSpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.branch == "" {
		return []plugin.WidgetSpan{{Text: "git: —", Style: plugin.StyleDim}}
	}
	spans := []plugin.WidgetSpan{
		{Text: "git:", Style: plugin.StyleDim},
		{Text: p.branch, Style: plugin.StyleAccent},
	}
	if p.lastOp != "" && time.Since(p.lastTime) < 5*time.Second {
		spans = append(spans, plugin.WidgetSpan{Text: " " + p.lastOp, Style: plugin.StyleInfo})
	}
	return spans
}

func (p *GitPRPlugin) onPostToolUse(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
	cmd := extractShellCommand(payload.ToolInput)
	if cmd == "" || !isGitCommand(cmd) {
		return nil, nil
	}
	p.mu.Lock()
	p.lastTime = time.Now()
	p.lastOp = summarizeGitCommand(cmd)
	if branch := detectBranch(cmd, payload.Extra); branch != "" {
		p.branch = branch
	}
	p.mu.Unlock()
	// 🔑 关键：把组件更新推送到 TUI
	if p.pctx != nil {
		_ = p.pctx.UpdateWidget("git-branch")
	}
	return nil, nil
}
```

注意：

- `ContributeUI(widgetID, zone, widget, priority)` —— 插件自身实现 `UIWidget`（`Render(width int) []WidgetSpan`）。
- `UpdateWidget(widgetID)` 立即触发重渲染——忽略错误即可（组件可能尚未渲染，例如 CLI 会话未打开）。
- `detectBranch` 读 `payload.Extra["output"]` —— Hook 载荷在 `Extra` 中携带工具输出（`HookPayload`，`plugin/plugin.go:674`）。
- 状态用 `sync.Mutex` 保护——Hook 与 `Render` 运行在不同 goroutine 上。

## 上下文注入器

`EnrichContext(name, enricher)` 注册一个 `ContextEnricher func(ctx context.Context) (string, error)`，其输出每轮注入系统提示词：

```go
pctx.EnrichContext("hello_status", func(ctx context.Context) (string, error) {
	return fmt.Sprintf("Hello World plugin active (uptime: %s, tool calls served: %d)",
		latency.Round(time.Second), p.callCount.Load()), nil
})
```

SDK 快捷方式：`plugin.StaticEnricher(content)` 与 `plugin.FileEnricher(path)`（`plugin/sdk.go:82-97`）。

## SDK 助手

`plugin/sdk.go`：

| 助手 | 用途 |
|---|---|
| `QuickManifest(id, name, version, desc, opts...)` | 清单构建器；选项 `WithPermissions`、`WithActivationEvents`、`WithRuntime`、`WithTools`、`WithHooks`、`WithEnrichers` |
| `ToolFromFunc(name, desc, fn)` | 由 `func(ctx, input string) (string, error)` 构建工具 |
| `ToolFromJSONFunc(name, desc, params, fn)` | 由 `func(ctx, json.RawMessage) (any, error)` 构建工具，自动 JSON 序列化 |
| `AllowHook()` / `DenyHook(msg)` / `LogHook(logger, msg)` | Hook 处理器工厂 |
| `FormatToolResult(title, sections)` | 确定性 `"key: value"` 输出（键排序） |
| `FormatListResult(items)` | 编号列表；空时返回 `"(no items)"` |
| `MustActivate(p, ctx)` | 激活失败即 panic，用于 `init()` 式接线 |
| `NewToolResult(content)` / `NewToolError(content)` | 结果构造器 |
| `NewResultBuilder()` | 流式构建器：`.Content(...).Metadata(k, v).Build()` |

## ToolResult 元数据

`ToolResult.Metadata`（map[string]string）贯穿到工具结果渲染。例如 Edit 工具把 unified diff 存在 `Metadata["diff"]` 中供精美渲染。

## 线程与生命周期注意

- `OnModelsLoaded` 类异步回调必须并发安全——激活与工具调用来自不同 goroutine。
- `PluginManager.RefreshWorkDir(wd, channel, chatID, tenantID)` 更新所有活跃插件上下文的会话元数据；实现 `WorkDirAware.OnWorkDirChanged(dir)` 可立即响应（script 插件就是这么做的）。
- 绝不在持有插件写锁时调用 `PluginManager.ExportConfig`——它内部会获取管理器 RLock。
