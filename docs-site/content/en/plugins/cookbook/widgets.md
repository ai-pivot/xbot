---
title: "Widgets"
weight: 12
---

Widgets render plugin content into CLI UI zones — status bar, info bar, title bar, footer — and into the web UI. Three implementation paths exist: script output parsing, Go `UIWidget` implementations, and web view contributions.

## The registry and zones

`WidgetRegistry` (`plugin/widget.go`) holds widget slots keyed by `pluginID:widgetID`, each with a zone (slot), provider (`UIWidget`), and priority:

```go
func (p *HelloWorldPlugin) Activate(ctx plugin.PluginContext) error {
	return ctx.ContributeUI("hello-widget", "statusBarRight", p, 10)
}
```

`WidgetSpan` is the rendering atom:

```go
type WidgetSpan struct {
	Text  string
	Style WidgetStyle
	// ...
}
```

Styles: `StyleDim`, `StyleOk`, `StyleWarn`, `StyleErr`, `StyleInfo`, `StyleAccent` (and `StyleNormal`).

## The UIWidget interface

```go
type UIWidget interface {
	Render(width int) []WidgetSpan
}
```

`width` is the available cell width. Implement `WorkDirRenderer` additionally if your widget output depends on the session working directory:

```go
type WorkDirRenderer interface {
	RenderForWorkDir(width int, workDir string) []WidgetSpan
}
```

## Push re-renders from hooks

The `git-pr-status` example (`plugin/examples/git-pr-status/main.go`) shows the hook → widget bridge: a `PostToolUse` handler mutates state and calls `ctx.UpdateWidget("git-branch")` — the registry re-renders that widget immediately (safe to call before the TUI exists; errors are ignorable).

## Script widgets

Script plugins declare widgets in `contributes.ui[]` and the runtime parses `style|text` output into spans (see [Script Plugins](../script-plugins/)). Each declared widget gets a `widgetAdapter` (`plugin/script_runtime.go:90`) carrying its `widgetID` into the script via `XBOT_WIDGET_ID`.

Zones used by the examples: `infoBar` (git-info), `toolHint` (file-diff, sync), `statusBarRight` (git-pr-status). Additional CLI zones include `titleBar` and `footer`.

## Change notification (debounced)

`WidgetRegistry.NotifyUpdated()` / `FireUpdated()` schedule an update notification; `SetDebounce(d)` controls the coalescing window. `OnUpdated(fn)` registers a listener — the `Agent`'s `WidgetRegistry.OnUpdated` handler does a `channel.WidgetSubscriber` type assertion and calls `NotifyWidgetsUpdated()` per channel, letting each channel decide its own rendering (CLI → ANSI, web → structured JSON).

## Web widgets

For the web UI, widget output is structured, not ANSI. Channel plugins use the `web_ui` protocol (`plugin/examples/web-ui-demo/`) to push declarative components (sparkline/table/badge) or free-form code; interactions come back via `web_ui_action` RPC. Web view contributions (`contributes` in `web.contributes`) declare full React panels instead — see [Web Plugins](../web-plugins/).

## Per-session correctness

- Script plugin outputs are cached per `workDir → widgetID` — never a single shared output (`runAndUpdate`, `plugin/script_runtime.go:380`). `Render()` falls back to the shared `pctx.WorkingDir()`; `RenderForWorkDir` is authoritative for remote multi-session.
- Native plugins: the manager calls `RefreshWorkDir(wd, channel, chatID, tenantID)` on Cd, updating every plugin context; `WorkDirAware` plugins get `OnWorkDirChanged` immediately.
- ⚠️ **Never write the global widget slot cache from background goroutines** — `runAndUpdate` calls `NotifyUpdated()` instead of `UpdateWidget()`. Writing global cache causes cross-session overwrites (session B's git branch overwrites session A's).

## Multi-widget plugins

When a manifest declares N widgets, the script runs once **per widget** (with `XBOT_WIDGET_ID` set). Without branching on that variable, all N widgets render identical output — the classic "5 duplicates of `main +10 ↑13`" bug. Branch in the script:

```bash
case "$XBOT_WIDGET_ID" in
  git-branch)  # branch status
    echo "ok|git:${branch}";;
  git-changes) # change count
    echo "info|Δ${changes}";;
esac
```
