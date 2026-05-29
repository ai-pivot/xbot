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

gRPC plugins run as independent child processes, communicating with xbot via JSON-over-stdin/stdout. This is how you add new channels like Telegram, Discord, Slack, etc.

### Structure

```
my-channel/
├── plugin.json       # Manifest (runtime: "grpc")
└── my-channel-bin    # Compiled binary (any language)
```

### plugin.json

```json
{
  "id": "my.telegram",
  "name": "Telegram Channel",
  "version": "1.0.0",
  "description": "Telegram bot channel for xbot",
  "runtime": "grpc",
  "entry": "./my-channel-bin",
  "permissions": ["channels.register"]
}
```

**Key fields:**
- `runtime`: Must be `"grpc"` for channel plugins
- `entry`: Relative path to the binary (resolved from plugin directory)
- `permissions`: Must include `"channels.register"`

### Protocol: xbot → Plugin (request-response)

xbot sends JSON lines to plugin's stdin, plugin responds with JSON lines to stdout.

| Method | Required | Description |
|--------|----------|-------------|
| `activate` | ✅ | Return channel_provider declaration |
| `channel_start` | ✅ | Start channel with config |
| `channel_stop` | ✅ | Stop channel |
| `channel_send` | ✅ | Deliver agent reply to user |
| `channel_history` | ❌ | Load platform conversation history |
| `channel_update_message` | ❌ | Edit previously sent message |
| `channel_delete_message` | ❌ | Delete previously sent message |
| `channel_poll` | ❌ | Legacy Phase 1 polling (return `[]`) |
| `deactivate` | ✅ | Plugin shutdown |

### Protocol: Plugin → xbot (async push)

Plugin can push messages to xbot at any time by writing JSON to stdout:

```json
{"method": "channel_inbound", "params": {"chat_id": "xxx", "content": "hello", "sender_id": "user1", "sender_name": "User"}}
```

Batch mode (multiple messages):
```json
{"method": "channel_inbound", "params": {"messages": [{"chat_id": "c1", "content": "hi", "sender_id": "u1"}, {"chat_id": "c2", "content": "hey", "sender_id": "u2"}]}}
```

### activate Response Format

The `activate` response must include `channel_provider` declaration:

```json
{
  "result": "ok",
  "channel_provider": {
    "name": "telegram",
    "config_schema": [
      {"key": "enabled", "label": "启用", "type": "toggle", "defaultValue": "false", "category": "Telegram"},
      {"key": "bot_token", "label": "Bot Token", "type": "password", "defaultValue": "", "category": "Telegram"}
    ]
  }
}
```

### config_schema Types

| Type | UI Widget |
|------|-----------|
| `toggle` | On/off switch |
| `text` | Text input |
| `password` | Masked text input |
| `select` | Dropdown (needs `options` array) |

### channel_start Request

```json
{"method": "channel_start", "params": {"config": {"enabled": "true", "port": "9876", "bot_token": "xxx"}}}
```

### channel_send Request

```json
{"method": "channel_send", "params": {"chat_id": "xxx", "content": "Agent reply text", "is_partial": false, "waiting_user": false}}
```

### channel_history Request/Response

Request:
```json
{"method": "channel_history", "params": {"chat_id": "xxx", "limit": 50}}
```

Response:
```json
{"result": "[{\"message_id\":\"1\",\"sender_id\":\"u1\",\"content\":\"hello\",\"is_bot\":false,\"time\":\"2026-01-01T00:00:00Z\"}]"}
```

### Minimal Channel Plugin (Go)

```go
package main

import (
    "bufio"
    "encoding/json"
    "fmt"
    "io"
    "os"
)

type request struct {
    Method string         `json:"method"`
    Params map[string]any `json:"params,omitempty"`
}

func main() {
    dec := json.NewDecoder(bufio.NewReader(os.Stdin))
    enc := json.NewEncoder(os.Stdout)

    for {
        var req request
        if err := dec.Decode(&req); err != nil {
            if err == io.EOF { return }
            fmt.Fprintf(os.Stderr, "read error: %v\n", err)
            return
        }

        switch req.Method {
        case "activate":
            enc.Encode(map[string]any{
                "result": "ok",
                "channel_provider": map[string]any{
                    "name": "mychannel",
                    "config_schema": []map[string]any{
                        {"key": "enabled", "label": "Enable", "type": "toggle", "defaultValue": "false"},
                    },
                },
            })
        case "channel_start":
            // Initialize your channel here (start HTTP server, bot client, etc.)
            enc.Encode(map[string]any{"result": "ok"})
        case "channel_send":
            // Deliver agent reply to user
            enc.Encode(map[string]any{"result": "ok"})
        case "channel_stop":
            enc.Encode(map[string]any{"result": "ok"})
        case "deactivate":
            enc.Encode(map[string]any{"result": "ok"})
            return
        default:
            enc.Encode(map[string]any{"error": fmt.Sprintf("unknown method: %s", req.Method)})
        }
    }
}
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
- gRPC channel plugins: `entry` path is relative to the plugin directory
- gRPC plugins must respond to every request with a JSON line (even if just `{"result":"ok"}`)
- gRPC plugins can push `channel_inbound` at any time without waiting for a request (bidirectional)
- Channel names cannot be: `feishu`, `qq`, `napcat`, `web`, `cli`
