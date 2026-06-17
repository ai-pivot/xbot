# stdio Channel Plugins

External process plugins that act as message channels, communicating with xbot via bidirectional JSON-RPC over stdin/stdout.

## Architecture

```
xbot (serverapp)                    Plugin (separate process)
┌─────────────────┐                 ┌─────────────────┐
│ RPCTable        │◄───stdout───────│ Plugin main loop │
│ (dispatch)      │─────stdin──────►│ (JSON-RPC)       │
│ ChannelPlugin   │                 │ HTTP server /    │
│ Transport       │◄──eventCh───────│ bot framework    │
└─────────────────┘                 └─────────────────┘
```

The channel runs in a **dedicated process** (separate from the plugin activation process). It has full access to xbot's RPC surface — same as a remote CLI client over WebSocket.

## Structure

```
my-channel/
├── plugin.json       # Manifest (runtime: "stdio")
└── main.py           # Any language — Python, Go, Node, etc.
```

## plugin.json

```json
{
  "id": "my.telegram",
  "name": "Telegram Channel",
  "version": "1.0.0",
  "description": "Telegram bot channel for xbot",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "permissions": ["channels.register"],
  "contributes": {
    "channelProvider": {
      "name": "telegram",
      "config_schema": [
        {"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "false"},
        {"key": "bot_token", "label": "Bot Token", "type": "password", "default_value": ""}
      ]
    }
  }
}
```

**Key fields:**
- `runtime`: Must be `"stdio"` for channel plugins
- `entry`: Command to run (e.g. `"python3 main.py"`, `"./my-binary"`)
- `permissions`: Must include `"channels.register"`
- `contributes.channelProvider`: Channel declaration with name and config_schema

## Protocol: Bidirectional JSON-RPC (same as WebSocket)

All communication is JSON lines over stdin/stdout.

**Plugin → xbot (RPC request):**
```json
{"id":"plugin-1","method":"send_inbound","params":{"channel":"telegram","chat_id":"chat1","content":"hello","sender_id":"user1","sender_name":"User","chat_type":"p2p"}}
```

**xbot → Plugin (event push — no id):**
```json
{"type":"progress_structured","chat_id":"chat1","progress":{"phase":"thinking"}}
{"type":"stream_content","chat_id":"chat1","content":"Hello! "}
{"type":"text","chat_id":"chat1","content":"Hello! How can I help you?"}
{"type":"session","session":{"channel":"telegram","chat_id":"chat1","action":"busy"}}
```

**xbot → Plugin (RPC request — has id + method):**
```json
{"id":"srv-1","method":"channel_send","params":{"chat_id":"chat1","content":"Agent reply"}}
```

**Plugin → xbot (RPC response — has id, no method):**
```json
{"id":"srv-1","result":"ok"}
```

## Available RPC Methods (Plugin → xbot)

| Method | Description |
|--------|-------------|
| `send_inbound` | Send user message to agent |
| `get_history` | Get conversation history |
| `list_sessions` | List available sessions |
| `set_cwd` | Set working directory |
| All standard RPC methods | Full access to RPCTable |

## Event Types (xbot → Plugin)

| Type | Description |
|------|-------------|
| `channel_config` | Initial config push (metadata.config = JSON config) |
| `progress_structured` | Progress events (thinking, tools, etc.) |
| `stream_content` | Streaming text + reasoning content |
| `text` | Final agent reply |
| `session` | Session state changes (busy/idle) |
| `inject_user` | Background message injection |
| `card` | Rich card message |
| `ask_user` | Ask user question |
| `plugin_widgets` | Plugin widget update |

## Activation Flow

1. Plugin process starts → xbot sends `{"method":"activate","params":{}}` (old protocol)
2. Plugin responds with `{"result":"ok","channel_provider":{...}}`
3. xbot **spawns a separate process** for the channel (using `entry` from manifest)
4. The new process receives JSON-RPC over stdin/stdout
5. First event is `channel_config` with the channel configuration
6. Plugin starts its HTTP server/bot framework and begins forwarding messages
7. (Optional) Plugin sends `channel_prompt` to declare channel-specific system prompt
8. (Optional) Plugin sends `channel_tools` to declare channel-scoped tools

## Channel Prompt (Plugin → xbot)

Channel plugins can declare a **channel-specific system prompt** that is automatically injected
into agent sessions for this channel — just like built-in channels (feishu, cli).

### Protocol

After receiving `channel_config`, send a `channel_prompt` message on stdout:

```json
{
  "type": "channel_prompt",
  "system_parts": {
    "05_channel_telegram": "# Telegram Channel Instructions\n\n- Keep replies concise for mobile readability.\n- Use Telegram-flavored Markdown for formatting.\n- Never send messages longer than 4096 characters.\n- Use inline buttons sparingly."
  }
}
```

**Key rules:**
- Key names should use the `"05_channel_xxx"` prefix to ensure correct ordering in the final
  system prompt (after `"00_base"`, before `"10_skills"`).
- **Hot-update**: Sending a new `channel_prompt` message replaces the entire prompt.
- Multiple keys are supported — they are merged into the system prompt in key-sorted order.
- The prompt is **injected automatically** into every agent session of this channel.
- Omit this message entirely if your channel doesn't need custom agent instructions.

### How It Works

```
1. Channel process starts → receives channel_config
2. Plugin sends channel_prompt with system_parts
3. xbot registers a ChannelPromptProvider for this channel
4. When agent processes a message from this channel:
   → ChannelPromptMiddleware matches channel name
   → injects system_parts into the system prompt
   → agent sees channel-specific instructions
```

## Channel Tools

Channel plugins can also declare **channel-scoped tools** — see `channel-tools.md` for details.

## config_schema Types

| Type | UI Widget |
|------|-----------|
| `toggle` | On/off switch |
| `text` | Text input |
| `password` | Masked text input |
| `select` | Dropdown (needs `options` array) |
| `number` | Number input |

## Config Storage

Channel config is stored in `~/.xbot/config.json` under `channels.<name>`:

```json
{
  "channels": {
    "telegram": {
      "enabled": "true",
      "bot_token": "123456:ABC"
    }
  }
}
```

Users can also configure via TUI Settings → Channels panel (auto-rendered from config_schema).

## Channel Name Restrictions

Channel names must NOT conflict with built-in channels: `feishu`, `qq`, `napcat`, `web`, `cli`.

## Minimal Channel Plugin (Python)

```python
#!/usr/bin/env python3
import sys, json, threading, uuid
from http.server import HTTPServer, BaseHTTPRequestHandler

def write_stdout(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def send_inbound(chat_id, content, sender_id="user", sender_name="User"):
    write_stdout({
        "id": f"plugin-{uuid.uuid4().hex}",
        "method": "send_inbound",
        "params": {
            "channel": "mychannel",
            "chat_id": chat_id,
            "content": content,
            "sender_id": sender_id,
            "sender_name": sender_name,
            "chat_type": "p2p",
        }
    })

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode()
        if body:
            send_inbound("default", body)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok\n")
    def log_message(self, *a): pass

for line in sys.stdin:
    msg = json.loads(line.strip())
    if msg.get("method") == "activate":
        write_stdout({"result": "ok", "channel_provider": {
            "name": "mychannel",
            "config_schema": [
                {"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "false"},
                {"key": "port", "label": "Port", "type": "number", "default_value": "9876"},
            ]
        }})
    elif msg.get("type") == "channel_config":
        config = json.loads(msg.get("metadata", {}).get("config", "{}"))
        port = int(config.get("port", "9876"))

        # Optional: declare channel-specific system prompt
        write_stdout({
            "type": "channel_prompt",
            "system_parts": {
                "05_channel_mychannel": "# MyChannel Instructions\n\n- Keep replies concise.\n- Format with Markdown."
            }
        })

        HTTPServer(("0.0.0.0", port), Handler).serve_forever()
```
