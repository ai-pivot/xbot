---
title: "xbot-genui — GenUI (display_html)"
weight: 2
---

The GenUI plugin provides the `display_html` tool: the LLM writes a self-contained TSX component, and the web frontend renders it as a live, streaming, interactive UI — with a component library, ECharts charts, three.js 3D scenes, and framer-motion animations.

It is a **channel plugin**: a standalone, zero-dependency Go binary (`plugins/xbot-genui/`) driven over JSON-on-stdio, that declares the tool to the `web` channel via the `channel_tools` protocol.

## Features

- **Component library** — `XBOT_UI.Button / Card / Table / Stat / Sparkline / Progress / Badge / Tabs / Modal / Form / Toast`
- **Charts** — `<XBOT_UI.Chart option={...}>` (ECharts, lazy-loaded from CDN)
- **3D** — `XBOT_UI.useThreeScene` (three.js, lazy-loaded from CDN)
- **Animation** — `XBOT_UI.motion` (framer-motion)
- **Theming** — automatic light/dark adaptation (`dark:` Tailwind variants)
- **Interaction** — `data-action` attributes route clicks back to the agent (`🖱️ [UI Action] ...`); pure client-side state via React hooks (`useState`, `useEffect`, ...)
- **Streaming preview** — the TSX is pushed to the frontend while the tool runs; rendered code is stored in iteration history (survives reload)
- **Surface panel** — rendered as a top-level panel (fancy header + collapse + fullscreen, default-open) instead of a folded tool result

## Installation

```bash
cd plugins/xbot-genui
make build          # → bin/genui-plugin (zero-dependency standalone binary)
make install        # → ~/.xbot/plugins/xbot.genui/
```

Or from the repository root: `make plugins-install`.

To run straight from the checkout without installing:

```bash
make plugins-build
XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot
```

## Configuration

The plugin declares a channel config schema in `plugin.json`:

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | toggle | `true` | Enable the GenUI channel plugin (registers `display_html` to the web channel) |
| `libs_cdn` | text | `https://cdn.jsdelivr.net/npm/` | Base URL for lazy-loaded chart/3D libraries (echarts/three) |

**Activation requires the channel entry in `config.json`** — installing the plugin is not enough:

```json
{
  "channels": {
    "genui": { "enabled": "true" }
  }
}
```

`stdioChannelPluginProvider.IsEnabled` returns false for a nil config, so
without this entry the channel is never created, `channel_tools` is never
declared, and `display_html` stays invisible. After changing the config,
restart xbot (channel instances are created at startup in `registerChannels`).

## Architecture

```text
LLM generates TSX
  → display_html tool (declared via channel_tools, channels:["web"])
  → execute_tool RPC → validation → {content, is_error, ui_code}
  → xbot ChannelToolBridge: ui_code → genui message to frontend + Detail stored in history
  → frontend GenUIBlock + XBOT_UI runtime render it
  → data-action click → genui_action → agent loop
```

### Protocol (JSON lines over stdio)

| Direction | Message | Meaning |
|-----------|---------|---------|
| xbot → plugin | `{"method":"activate",...}` | Returns `channel_provider {name:"genui"}` |
| xbot → plugin | `{"type":"channel_config",...}` | Channel is live — plugin replies with `channel_tools` |
| xbot → plugin | `{"id","method":"execute_tool",...}` | Validate TSX, return `ui_code` |
| xbot → plugin | `{"id","method":"web_ui_action",...}` | No-op reply — interactions fall through to the agent loop |
| plugin → xbot | `{"type":"channel_tools","tools":[...]}` | Declares `display_html` for the `web` channel |

### Tool Declaration

The tool carries **UI metadata** (`tools.UIDecl`) instead of relying on its
name — this is the generic design (see `docs/agent/genui-plugin-design.md` §9):

```json
{
  "name": "display_html",
  "channels": ["web"],
  "ui": {
    "mode": "genui",
    "param": "code",
    "libs": ["echarts", "three", "motion"],
    "surface": { "kind": "panel", "collapsible": true, "fullscreen": true, "default_open": true }
  }
}
```

Any plugin tool declaring `ui.mode="genui"` gets the same streaming
extraction, fancy rendering, and interaction callback — xbot itself has no
hard-coded tool name.

### Validation (execute_tool)

Before returning `ui_code`, the plugin validates the LLM-generated TSX
(these checks were migrated from the removed `tools/display_html.go`):

1. `code` argument must be non-empty
2. Markdown fences are stripped (` ```tsx ... ``` `)
3. The code must define an `App` component
4. Brace/paren balance check (`validateSyntax`) — a lightweight scanner that
   skips strings, template literals, and comments
5. Empty-render guard (`isEmptyRender`) — rejects `return null` / `undefined` /
   `false` / `<></>`

On success it returns `content: "🎨 UI rendered (N chars)"` plus the full
`ui_code`. On failure it returns `is_error: true` with an actionable message
the LLM can use to fix and retry.

### Frontend Runtime

- `web/src/genui/runtime.tsx` — `XBOT_UI` component library, ECharts /
  three / motion integrations, theme handling
- `builtinGenuiRenderer` (matches `{uiMode:'genui'}`) renders tool results
  via the `PluginRuntime.renderTool` dispatcher (`messageRenderer`)
- ECharts/three/motion are lazy-loaded from the configured CDN base
- GenUI interaction events (`genui_action`) route back to the agent loop
  (`InjectAsyncMessage`) — the plugin itself is not involved

## Testing

```bash
cd plugins/xbot-genui
go test ./...
```

## Source Files

| File | Role |
|------|------|
| `main.go` | Stdio protocol loop, tool declaration, TSX validation |
| `plugin.json` | Manifest: id `xbot.genui`, channelProvider declaration, config schema |
| `main_test.go` | Unit tests for validation helpers |
| `Makefile` | build / test / install / clean targets |
