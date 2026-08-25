---
title: "Widget Zones"
weight: 9
---

Reference for UI widget zones (`plugin/manifest.go` `validUISlots` + `plugin/widget.go` `RenderSessionWidgets`).

## Zone List

| Zone | Location |
|------|----------|
| `titleBarLeft` | Title bar, left side. |
| `titleBarRight` | Title bar, right side. |
| `statusBarLeft` | Status bar, left side. |
| `statusBarRight` | Status bar, right side. |
| `infoBar` | Info bar (web bottom status bar area). |
| `footer` | Footer area. |
| `toolHint` | Plugin-provided markdown hint rendered alongside tool output in the progress panel. |

All zones are rendered per-session using the session's CWD — `RenderSessionWidgets` iterates exactly this list:

```go
for _, z := range []string{"titleBarLeft", "titleBarRight", "statusBarLeft", "statusBarRight", "infoBar", "footer", "toolHint"} {
    zones[z] = wr.RenderZoneForWorkDir(z, cwd)
}
```

## Usage

### Manifest Declaration

```json
{
  "contributes": {
    "ui": [
      { "id": "git-status", "slot": "statusBarLeft", "priority": 10 }
    ]
  }
}
```

Requires `"ui.contribute"` permission; max 10 widgets per plugin; widget IDs unique per plugin.

### Programmatic Registration

```go
ctx.ContributeUI(widgetID, zone string, widget UIWidget, priority int) error
```

The `widgetID` must match a declared manifest contribution. Priority ordering: lower = earlier/leftmost (default 100).

## Widget Interfaces

```go
// UIWidget renders styled spans for a given width (0 = unbounded).
type UIWidget interface {
    Render(width int) []WidgetSpan
}

// Optional: render for a specific workDir without modifying shared PluginContext.
type WorkDirRenderer interface {
    RenderForWorkDir(width int, workDir string) []WidgetSpan
}
```

## StyleClass

```go
type WidgetSpan struct {
    Text  string
    Style StyleClass
}
```

| StyleClass | Meaning |
|------------|---------|
| `normal` | Default text. |
| `dim` | Dimmed text. |
| `accent` | Accent/highlight color. |
| `success` | Success (green). |
| `warning` | Warning (yellow). |
| `error` | Error (red). |
| `info` | Info (blue). |
| `muted` | Muted (white dim). |
| `raw` | Pass-through — text contains its own ANSI escapes; no wrapping applied. |

Plugins must NOT output raw ANSI escape sequences in `Text` (except `StyleRaw` spans). The TUI maps `StyleClass` to theme colors; `BasicANSIRender` is the server-side default renderer.

## Update Mechanisms

- **Push (preferred)**: `ctx.UpdateWidget(widgetID)` triggers an async re-render.
- **Poll (advisory)**: `UISlotContribution.RefreshInterval` (e.g. `"30s"`).
- **Hook triggers**: `UISlotContribution.Triggers` — instant script run on hook events (script runtime).

`WidgetRegistry` supports debounce (`SetDebounce`) and suppression (`SuppressUpdates`) to coalesce rapid updates during batch operations.
