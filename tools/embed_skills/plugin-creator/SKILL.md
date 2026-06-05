---
name: plugin-creator
description: Create, modify, and manage xbot plugins. Use when the user asks to create a plugin, set up script plugins, configure widgets, register custom tools via plugins, or create channel plugins.
---

# Plugin Creator

Create and manage xbot plugins.

## When to Activate

- User wants to create/modify/delete plugins
- User wants to add custom tools, widgets, context enrichers, or channels
- User asks about plugin system configuration

## Plugin Types

| Type | Complexity | Use Case |
|------|-----------|----------|
| **script** | Low | Shell-based plugins: widgets, notifications, simple tools |
| **native** | Medium | Go in-process plugins (requires compilation) |
| **grpc** | High | External process plugins: channels, Python/Go/any language |

**Prefer script plugins** for widgets and hooks. **Use gRPC for channel plugins.**

## Plugin Location

- User plugins: `~/.xbot/plugins/<plugin-id>/`
- Project plugins: `<project>/.xbot/plugins/<plugin-id>/`
- Built-in examples: `plugin/examples/`

**IMPORTANT**: The directory name MUST match the plugin ID in `plugin.json`. For example, if `"id": "my.telegram"`, the directory must be `~/.xbot/plugins/my.telegram/`.

## Script Plugin Structure

```
my-plugin/
├── plugin.json     # Manifest
└── my-script.sh    # Entry script (must be executable)
```

### plugin.json Format

```json
{
  "id": "my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "description": "What this plugin does",
  "author": "your-name",
  "runtime": "script",
  "entry": "bash my-script.sh",
  "entry_windows": "powershell -File my-script.ps1",
  "entry_darwin": "bash my-script-macos.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [{
      "id": "my-widget",
      "slot": "infoBar",
      "priority": 10,
      "description": "Widget description",
      "refreshInterval": "30s",
      "triggers": ["PostToolUse:Shell*", "PostToolUse:FileReplace*"]
    }]
  }
}
```

### Available UI Slots

| Slot | Location |
|------|----------|
| `infoBar` | Bottom info bar |
| `toolHint` | Progress panel |

### Available Triggers

```
PostToolUse:<matcher>        — after tool succeeds (matcher supports glob)
PreToolUse:<matcher>         — before tool executes
PostToolUseFailure:<matcher> — after tool fails
UserPromptSubmit:            — on user prompt
AgentStop:                   — on agent stop
SessionStart: / SessionEnd:  — session lifecycle
PreCompact: / PostCompact:   — context compression
```

### Script Output Format

Widget output: `"style|text"` where style is: `dim`, `ok`, `warn`, `err`, `info`, `accent`, or empty.

```bash
echo "ok|git:main ✓"
echo "warn|3 uncommitted changes"
echo "dim|no repo"
```

### Environment Variables (available in script)

| Variable | Value |
|----------|-------|
| `XBOT_TOOL_NAME` | Tool name (e.g. "FileReplace") |
| `XBOT_TOOL_OUTPUT` | Tool result (truncated to 8KB) |
| `XBOT_TOOL_INPUT` | Tool input as JSON string |
| `XBOT_WORK_DIR` | Current working directory |
| `XBOT_WIDGET_ID` | Widget ID that triggered this render (e.g. "git-branch") |
| `XBOT_MODEL` | Current LLM model name (e.g. "claude-sonnet-4-20250514") |
| `XBOT_MAX_CONTEXT` | Maximum context window in tokens (e.g. "200000") |
| `XBOT_TOKEN_USAGE` | Token usage as `prompt/completion` (e.g. "12345/678") |
| `XBOT_PROMPT_TOKENS` | Cumulative prompt tokens (input + context) |
| `XBOT_COMP_TOKENS` | Cumulative completion tokens (output) |

**Session context variables** (`XBOT_MODEL`, `XBOT_MAX_CONTEXT`, `XBOT_TOKEN_USAGE`, etc.) are available on all hook events and refresh runs. They allow widgets to display model name, context usage bar, or token costs.

### Available Permissions

| Permission | Description |
|-----------|-------------|
| `tools.register` | Register new tools |
| `tools.call` | Call other tools |
| `hooks.subscribe` | Subscribe to lifecycle hooks |
| `context.enrich` | Inject system prompt content |
| `storage.private` | Plugin private KV storage |
| `storage.shared` | Cross-plugin shared storage |
| `network.outbound` | Make network requests |
| `ui.contribute` | Contribute UI widgets |
| `channels.register` | Register custom channels |

## Script Plugin Examples

### Git Branch Widget (infoBar)

```bash
#!/bin/bash
set -euo pipefail
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ] || [ "$branch" = "HEAD" ]; then
    echo "dim|git: —"
    exit 0
fi
changes=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ') || changes=0
if [ "$changes" -gt 0 ]; then
    echo "warn|git:${branch} Δ${changes}"
else
    echo "ok|git:${branch} ✓"
fi
```

### Session Context Widget (infoBar)

Shows model name and token usage using session context variables:

```bash
#!/bin/bash
set -euo pipefail
model="${XBOT_MODEL:-unknown}"
# Shorten model name for display
short_model=$(echo "$model" | sed 's/-[0-9].*//')
prompt="${XBOT_PROMPT_TOKENS:-0}"
comp="${XBOT_COMP_TOKENS:-0}"
max_ctx="${XBOT_MAX_CONTEXT:-0}"

if [ "$max_ctx" -gt 0 ]; then
    pct=$((prompt * 100 / max_ctx))
    echo "dim|${short_model} ${pct}% (${prompt}/${max_ctx})"
else
    echo "dim|${short_model}"
fi
```

### Diff Summary Widget (toolHint, sync)

```json
{
  "id": "my-diff",
  "name": "Diff Summary",
  "version": "1.0.0",
  "description": "Shows diff after file changes",
  "runtime": "script",
  "entry": "bash diff.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [{
      "id": "diff",
      "slot": "toolHint",
      "priority": 5,
      "sync": true,
      "triggers": ["PostToolUse:FileReplace*", "PostToolUse:FileCreate*"]
    }]
  }
}
```

## gRPC Channel Plugin

gRPC plugins run as independent child processes, communicating with xbot via **bidirectional JSON-RPC over stdin/stdout** (same protocol as WebSocket clients). This is how you add new channels like Telegram, Discord, Slack, etc.

### Architecture

```
xbot (serverapp)                    Plugin (separate process)
┌─────────────────┐                 ┌─────────────────┐
│ RPCTable        │◄───stdout───────│ Plugin main loop │
│ (dispatch)      │─────stdin──────►│ (JSON-RPC)       │
│ GrpcPlugin      │                 │ HTTP server /    │
│ Transport       │◄──eventCh───────│ bot framework    │
└─────────────────┘                 └─────────────────┘
```

The channel runs in a **dedicated process** (separate from the plugin activation process). It has full access to xbot's RPC surface — same as a remote CLI client over WebSocket.

### Structure

```
my-channel/
├── plugin.json       # Manifest (runtime: "grpc")
└── main.py           # Any language — Python, Go, Node, etc.
```

### plugin.json

```json
{
  "id": "my.telegram",
  "name": "Telegram Channel",
  "version": "1.0.0",
  "description": "Telegram bot channel for xbot",
  "runtime": "grpc",
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
- `runtime`: Must be `"grpc"` for channel plugins
- `entry`: Command to run (e.g. `"python3 main.py"`, `"./my-binary"`)
- `permissions`: Must include `"channels.register"`
- `contributes.channelProvider`: Channel declaration with name and config_schema

### Protocol: Bidirectional JSON-RPC (same as WebSocket)

All communication is JSON lines over stdin/stdout. Messages follow the WS protocol:

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

### Available RPC Methods (Plugin → xbot)

| Method | Description |
|--------|-------------|
| `send_inbound` | Send user message to agent |
| `get_history` | Get conversation history |
| `list_sessions` | List available sessions |
| `set_cwd` | Set working directory |
| All standard RPC methods | Full access to RPCTable |

### Event Types (xbot → Plugin)

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

### Activation Flow

1. Plugin process starts → xbot sends `{"method":"activate","params":{}}` (old protocol)
2. Plugin responds with `{"result":"ok","channel_provider":{...}}`
3. xbot **spawns a separate process** for the channel (using `entry` from manifest)
4. The new process receives JSON-RPC over stdin/stdout
5. First event is `channel_config` with the channel configuration
6. Plugin starts its HTTP server/bot framework and begins forwarding messages

### config_schema Types

| Type | UI Widget |
|------|-----------|
| `toggle` | On/off switch |
| `text` | Text input |
| `password` | Masked text input |
| `select` | Dropdown (needs `options` array) |
| `number` | Number input |

### Minimal Channel Plugin (Python)

```python
#!/usr/bin/env python3
import sys, json, threading
from http.server import HTTPServer, BaseHTTPRequestHandler

def write_stdout(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def send_inbound(chat_id, content, sender_id="user", sender_name="User"):
    write_stdout({
        "id": f"plugin-{id(content)}",
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
        HTTPServer(("0.0.0.0", port), Handler).serve_forever()
```

### Config Storage

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

### Channel Name Restrictions

Channel names must NOT conflict with built-in channels: `feishu`, `qq`, `napcat`, `web`, `cli`.

## Workflow

1. **Understand requirement**: Widget? Tool? Channel?
2. **Choose plugin type**: script for widgets/hooks, grpc for channels
3. **Choose plugin location**: `~/.xbot/plugins/<id>/` for user-level
4. **Create plugin.json**: Ensure directory name matches plugin ID
5. **Create entry point**: Script or compiled binary
6. **Ensure executable**: `chmod +x` for scripts and binaries
7. **Reload plugins**: Use `tui_control(action="reload_plugins")` to hot-reload

## After Creating/Modifying Plugins

**IMPORTANT**: Always reload plugins so changes take effect immediately:

```
tui_control(action="reload_plugins")
```

This hot-reloads all plugins without restarting the CLI. Alternatively, user can run `/plugin reload-all` in the TUI.

## Gotchas

- **Directory name MUST match plugin ID** in plugin.json. `"id": "my.telegram"` → `~/.xbot/plugins/my.telegram/`
- Script must be executable: `chmod +x my-script.sh`
- `sync: true` means the widget blocks tool execution until rendered (use for toolHint)
- Widget output is limited in size — keep it short
- Plugin ID must be unique across all plugins
- Script output without `style|` prefix is treated as plain text
- gRPC channel plugins: `entry` command spawns a **dedicated** process for the channel (separate from activation)
- gRPC plugins use bidirectional JSON-RPC (same as WS protocol), NOT the old request-response protocol
- Plugin must handle both old-style activation (`{"method":"activate"}`) and new-style JSON-RPC events
- gRPC plugins receive `channel_config` event with initial configuration, NOT `channel_start` request
- gRPC plugins send `send_inbound` RPC to push messages, NOT `channel_inbound` async push
- Channel names cannot be: `feishu`, `qq`, `napcat`, `web`, `cli`
