---
title: "Widget System"
weight: 10
---

Widgets allow plugins to render content in the CLI status bar, title bar, info bar, and footer.

## Widget Zones

| Zone | Location | Description |
|------|----------|-------------|
| `titleBarLeft` | Title bar (left) | Left side of the title bar |
| `titleBarRight` | Title bar (right) | Right side of the title bar |
| `statusBarLeft` | Status bar (left) | Left side of the status bar |
| `statusBarRight` | Status bar (right) | Right side of the status bar |
| `infoBar` | Info bar | Below the main content area |
| `footer` | Footer | Bottom of the screen |

## UIWidget Interface

```go
type UIWidget interface {
    Render(width int) []WidgetSpan
}

type WidgetSpan struct {
    Text  string
    Style string // "default", "bold", "dim", "accent", "muted", "green", "red", "yellow", "blue"
}
```

The `Render` method receives the available width and returns styled spans.

## WorkDirRenderer (Optional)

For widgets that need per-workDir rendering (e.g., git branch):

```go
type WorkDirRenderer interface {
    RenderForWorkDir(width int, workDir string) []WidgetSpan
}
```

If implemented, `RenderForWorkDir` is called instead of `Render` when a workDir is available.

## Registration

**Permission required**: `ui.contribute`

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    widget := &MyWidget{}
    return ctx.ContributeUI("my-widget", "statusBarRight", widget, 100)
}
```

Parameters:
- `widgetID`: Unique widget identifier (per plugin)
- `zone`: Widget zone (see table above)
- `widget`: UIWidget implementation
- `priority`: Ordering within zone (lower = leftmost, default 100)

## Manual Refresh

```go
ctx.UpdateWidget("my-widget")  // Trigger a re-render
```

## WidgetRegistry

The `WidgetRegistry` manages all registered widgets:

```go
type WidgetRegistry struct { ... }

func NewWidgetRegistry() *WidgetRegistry
func (wr *WidgetRegistry) Register(pluginID, widgetID, zone string, provider UIWidget, priority int)
func (wr *WidgetRegistry) Unregister(pluginID, widgetID string)
func (wr *WidgetRegistry) UnregisterAll(pluginID string)
func (wr *WidgetRegistry) RefreshWidget(pluginID, widgetID string, width int, renderFn RenderFunc)
func (wr *WidgetRegistry) RefreshAllWidgets(width int, renderFn RenderFunc)
func (wr *WidgetRegistry) NotifyUpdated()
func (wr *WidgetRegistry) OnUpdated(fn func())
```

## Script Plugin Widgets

Script plugins declare widgets in `plugin.json`:

```json
{
  "contributes": {
    "ui": [
      {
        "id": "git-branch",
        "slot": "statusBarRight",
        "priority": 50,
        "description": "Show current git branch"
      }
    ]
  }
}
```

The script's stdout is rendered as widget content. The `XBOT_WIDGET_ID` environment variable identifies which widget to render.

## Web Widgets

Web plugins contribute widgets via the `web_ui` protocol. Web widgets use structured spans (`WebWidgetSpan`) with semantic styles, not ANSI codes.

## Example: Git Branch Widget

```go
type GitBranchWidget struct{}

func (w *GitBranchWidget) Render(width int) []plugin.WidgetSpan {
    branch := getCurrentBranch()
    return []plugin.WidgetSpan{
        {Text: " ", Style: "default"},
        {Text: branch, Style: "accent"},
    }
}

func (w *GitBranchWidget) RenderForWorkDir(width int, workDir string) []plugin.WidgetSpan {
    branch := getBranchForDir(workDir)
    return []plugin.WidgetSpan{
        {Text: " ", Style: "default"},
        {Text: branch, Style: "accent"},
    }
}
```

## See Also

- [PluginContext API](./plugin-context/) — UIContributor interface
- [Permissions](./permissions/) — `ui.contribute` permission
- [API Reference: Widget Zones](./api-reference/widget-zones/) — Complete zone reference
- [Cookbook: Widget Development](./cookbook/widget-development/) — Step-by-step guide
