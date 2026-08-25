---
title: "Channel Plugins"
weight: 9
---

Channel plugins are stdio plugins that contribute a **full channel adapter** — a new message transport (like Feishu or QQ) with its own tools, system prompt parts, and web UI. Examples: `plugin/examples/echo-channel/` (HTTP echo channel), `plugin/examples/web-ui-demo/` (declarative web components), and the production `plugins/xbot-genui/` (the `display_html` tool as a channel plugin).

## Two protocol layers

Channel plugins run the stdio runtime, but with **bidirectional JSON-RPC** (requests carry an `id`; responses echo it). The protocol adds async messages both ways:

```
xbot → plugin (event push):  {"type":"progress","progress":{...}}
xbot → plugin (RPC request): {"id":"1","method":"channel_send","params":{...}}
plugin → xbot (RPC request): {"id":"p1","method":"send_inbound","params":{...}}
plugin → xbot (declaration):  {"type":"channel_tools","tools":[...]}
```

This is exactly the shape `plugin/examples/echo-channel/main.py` handles: `handle_incoming` routes by `id`/`method`/`type` presence (`handle_xbot_rpc`, `handle_xbot_event`, `handle_plugin_request`).

## Declaring the channel provider

The `activate` response includes `channel_provider` (`protocol.ChannelProviderDecl`):

```json
{
  "id": "com.example.echo-channel",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["channels.register"],
  "contributes": {
    "channelProvider": {
      "name": "echo",
      "config_schema": [
        { "key": "enabled", "label": "Enable", "type": "toggle", "default_value": "true" },
        { "key": "port", "label": "Port", "type": "number", "default_value": "9876" }
      ]
    }
  }
}
```

The plugin's `activate` handler returns the same declaration at runtime:

```python
def handle_activate(params):
    return {
        "channel_provider": {
            "name": "echo",
            "config_schema": config.get("config_schema", []),
        }
    }
```

Backend wiring: `plugin/channel_provider.go` — `SetChannelProviderFactory` (registered by serverapp) creates a `channel.ChannelProvider` from the declaration + process; `WireChannelProviders(pm)` registers all active plugin providers after `ActivateAll()`.

⚠️ **Channel activation requires `channels.<name>.enabled=true` in config.json.** `stdioChannelPluginProvider.IsEnabled(nil)` returns false (`serverapp/channel_plugin.go`) — installing the plugin is not enough. If `channels` has no entry for the plugin's channel name, the channel instance is never created, `channel_config` is never sent, and declared tools stay invisible.

## The three declaration protocols

After `channel_config` arrives, the plugin pushes its declarations as async type-messages (each hot-updatable — a new message replaces the entire previous set):

### 1. `channel_tools` — channel-scoped tools

```json
{"type":"channel_tools","tools":[
  {"name":"display_html","description":"Render an interactive UI...","parameters":[
     {"name":"code","type":"string","description":"TSX module source","required":true}
   ],
   "channels":["web"],
   "ui":{"mode":"genui","surface":{"kind":"panel","title":"UI","collapsible":true,"fullscreen":true,"default_open":true}}}
]}
```

The `ui` block mirrors `tools.UIDecl` (mode/param/libs/surface). `xbot-genui` (`plugins/xbot-genui/main.go declareTools`) declares `display_html` for the `web` channel this way. Each tool is wrapped in a `ChannelToolBridge` and registered via `RegisterForChannel(channel, tool)` (`plugin/channel_tool_bridge.go`); execution proxies through the `execute_tool` RPC to the plugin process.

### 2. `channel_prompt` — system prompt parts

```json
{"type":"channel_prompt","system_parts":{"05_channel_genui":"..."}}
```

Stored in `channelPluginPromptProvider` (RWMutex-guarded), fired via `OnChannelPrompt` → `Agent.AddChannelPromptProvider` → `ChannelPromptMiddleware` (priority 5). Key convention: `"05_channel_xxx"` prefix (after `"00_base"`, before `"10_skills"`).

### 3. `web_ui` — declarative web components

`plugin/examples/web-ui-demo/main.py` sends `{"type":"web_ui", ...}` declarations (sparkline/table/badge components and free-form code mode). Interactions route back via `web_ui_action` RPC: the owning channel plugin first, then native handlers, then the agent loop.

## Sending messages: send_inbound / channel_send

- **plugin → xbot** user messages: `send_inbound` RPC with `{channel, chat_id, content, sender_id, sender_name, chat_type}` (`echo-channel/main.py send_inbound_message`).
- **xbot → plugin** outgoing replies: `channel_send` RPC with `{content, chat_id}` — the plugin renders/delivers it (`handle_xbot_rpc` records it to history and acks `"ok"`).

The plugin is free to build any transport on top (echo-channel runs an HTTP server; real adapters speak IM APIs).

## The four-step channel tool lifecycle

1. `channel_config` event → plugin sends `channel_tools` declaration.
2. xbot wraps each tool in `ChannelToolBridge`, registered for the channel.
3. Agent calls the tool → `execute_tool` RPC → plugin returns `{"result": ...}`.
4. For GenUI tools, `ChannelToolBridge.Execute` returns `ui_code`; `WebChannel.Send` must recognize the `genui` metadata and forward as `MsgTypeGenUI` — otherwise the TSX is rendered as a plain code block (both paths must work: streamed `GenUIContent` progress events during streaming, `genui` message after).

## Reference implementations

| Plugin | What it demonstrates |
|---|---|
| `plugin/examples/echo-channel/` | Full JSON-RPC loop: routing, send_inbound, history, HTTP front |
| `plugin/examples/web-ui-demo/` | `web_ui` declarations + `web_ui_action` handling |
| `plugins/xbot-genui/` | Zero-dependency Go channel plugin; tool with `ui` metadata; TSX validation; `surface` panels |
| `plugins/xbot-git-fancy/` | `web_plugin_rpc` data source for a frontend view (not a channel) |

When adding new plugin→xbot message types, follow the established pattern (`AGENTS.md`): add a `protocol.MsgTypeXxx` constant + a `peek.Type` branch in `ChannelPluginTransport.handleIncoming` + a `handleChannelXxx` method + an `OnChannelXxx` callback in `ChannelPluginTransportConfig`.
