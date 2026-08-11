# Web UI Demo Plugin

Demonstrates the **web_ui protocol** (Web Plugin System): declarative web
components rendered in the xbot web UI, plus `web_ui_action` interactions.

## Components declared

| widget_id | slot | type | description |
|-----------|------|------|-------------|
| `ci-monitor` | `right_sidebar` | sparkline | CI build duration trend (refreshes every 10s) |
| `build-table` | `right_sidebar` | table | Recent builds with a "retry" action cell |
| `deploy-badge` | `title_bar_right` | badge | Build status badge |

## How it works

1. Plugin starts as a **channel plugin** (`channelProvider.name = "webui-demo"`).
2. On `channel_config` it sends a `web_ui` declaration via stdout:
   ```json
   {"type":"web_ui","ui":[{"widget_id":"ci-monitor","slot":"right_sidebar","component":{"type":"sparkline","props":{...}}}]}
   ```
3. The backend (`ChannelPluginTransport.handleChannelUI` → `Agent.RegisterChannelWebUI`)
   stores it in the `WebUIRegistry` and pushes to web clients via `web_widgets`.
4. The web frontend renders each component in its slot (`PluginComponentPanel`).
5. Clicking the failed build's "retry" cell sends `web_ui_action` → routed back to
   this plugin's `web_ui_action` handler → plugin updates state and re-declares UI.

## Try it

```bash
# Enable plugins + this channel in config.json:
#   "plugins": {"enabled": true}
#   "channels": {"webui-demo": {"enabled": true}}   (or plugin's own config schema)
```
