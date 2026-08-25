---
title: "Go Plugins"
weight: 7
---

Native Go plugins compile into the xbot binary and run in-process. This is the most powerful runtime: full access to `PluginContext`, the widget registry, hooks, storage, and the event bus. Examples: `plugin/examples/hello-world/` (tools + hooks + enrichers) and `plugin/examples/git-pr-status/` (hook → UI bridge).

## The Plugin interface

`plugin/plugin.go:26` — everything starts here:

```go
type Plugin interface {
	Manifest() PluginManifest
	Activate(ctx PluginContext) error
	Deactivate(ctx PluginContext) error
}
```

- `Manifest()` is called once during discovery.
- `Activate` must be **idempotent** and receives the `PluginContext` — the only gateway to xbot capabilities.
- `Deactivate` runs on shutdown/unload; no further callbacks fire afterwards.

## PluginContext — the capability surface

`PluginContext` (`plugin/context.go:108`) composes ten sub-interfaces:

| Sub-interface | Capabilities |
|---|---|
| `ToolRegistrar` | `RegisterTool`, `RegisterTools`, `UseMiddleware` |
| `HookSubscriber` | `OnPreToolUse`, `OnPostToolUse`, `OnUserPrompt`, `OnAgentStop`, `OnSessionStart`, `OnSessionEnd`, `OnEvent`, `OnAllToolUse`, `OnError` |
| `StorageProvider` | `Storage()`, `StorageInt`, `StorageBool`, `StorageJSON`, `StorageGetJSON` |
| `SessionMetadata` | `PluginID()`, `WorkingDir()`, `Channel()`, `ChatID()`, `TenantID()`, `Logger()` |
| `EventBusPublisher` | `Subscribe`, `Publish` |
| `UIContributor` | `ContributeUI`, `UpdateWidget`, `ContributeTheme`, `RegisterOverlay`, `ShowOverlay`, `HideOverlay`, `RegisterWebActionHandler` |
| `CronScheduler` | `ScheduleCron`, `CancelCron` |
| — (direct methods) | `EnrichContext`, `OnPluginError`, `SetValue`/`GetValue`, `ToolCallCount`/`HookCallCount`, `RegisterChannelProvider`, `RegisterCommand`, `Notify`, `PlaySound`, `Config()`/`SetConfig`/`OnConfigChanged` |

New code may accept narrower sub-interfaces (e.g. just `ToolRegistrar`) — interface segregation is encouraged.

## Hook → UI bridge pattern

`git-pr-status` (`plugin/examples/git-pr-status/main.go`) is the canonical pattern for stateful widgets: a hook detects an event, mutates plugin state, and pushes a widget re-render:

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

// UIWidget implementation
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
	// 🔑 KEY: push widget update to the TUI
	if p.pctx != nil {
		_ = p.pctx.UpdateWidget("git-branch")
	}
	return nil, nil
}
```

Notes:

- `ContributeUI(widgetID, zone, widget, priority)` — the plugin itself implements `UIWidget` (`Render(width int) []WidgetSpan`).
- `UpdateWidget(widgetID)` triggers a re-render immediately — ignore the error (the widget may not be rendered yet, e.g. CLI session not open).
- `detectBranch` reads `payload.Extra["output"]` — hook payloads carry tool output in `Extra` (`HookPayload`, `plugin/plugin.go:674`).
- State is guarded by a `sync.Mutex` because hooks and `Render` run on different goroutines.

## Context enrichers

`EnrichContext(name, enricher)` registers a `ContextEnricher func(ctx context.Context) (string, error)` whose output is injected into the system prompt each turn:

```go
pctx.EnrichContext("hello_status", func(ctx context.Context) (string, error) {
	return fmt.Sprintf("Hello World plugin active (uptime: %s, tool calls served: %d)",
		latency.Round(time.Second), p.callCount.Load()), nil
})
```

SDK shortcuts: `plugin.StaticEnricher(content)` and `plugin.FileEnricher(path)` (`plugin/sdk.go:82-97`).

## SDK helpers

`plugin/sdk.go`:

| Helper | Purpose |
|---|---|
| `QuickManifest(id, name, version, desc, opts...)` | Manifest builder; options `WithPermissions`, `WithActivationEvents`, `WithRuntime`, `WithTools`, `WithHooks`, `WithEnrichers` |
| `ToolFromFunc(name, desc, fn)` | Tool from a `func(ctx, input string) (string, error)` |
| `ToolFromJSONFunc(name, desc, params, fn)` | Tool from a `func(ctx, json.RawMessage) (any, error)` with auto JSON marshalling |
| `AllowHook()` / `DenyHook(msg)` / `LogHook(logger, msg)` | Hook handler factories |
| `FormatToolResult(title, sections)` | Deterministic `"key: value"` output (keys sorted) |
| `FormatListResult(items)` | Numbered list; `"(no items)"` when empty |
| `MustActivate(p, ctx)` | Activate-or-panic for `init()`-style wiring |
| `NewToolResult(content)` / `NewToolError(content)` | Result constructors |
| `NewResultBuilder()` | Fluent builder: `.Content(...).Metadata(k, v).Build()` |

## ToolResult metadata

`ToolResult.Metadata` (map[string]string) is carried through to tool result rendering. For example, the Edit tool stores a unified diff in `Metadata["diff"]` for fancy rendering.

## Threading and lifecycle notes

- `OnModelsLoaded`-style async callbacks must be concurrency-safe — activation and tool calls arrive on different goroutines.
- `PluginManager.RefreshWorkDir(wd, channel, chatID, tenantID)` updates session metadata on all active plugin contexts; implement `WorkDirAware.OnWorkDirChanged(dir)` to react immediately (script plugins do this).
- Never call `PluginManager.ExportConfig` while holding a plugin write lock — it acquires the manager RLock internally.
