---
title: "Script Runtime"
weight: 12
---

Script plugins run external scripts (bash, Python, Node, anything executable). They're the simplest way to create widgets, hooks, and commands without writing Go code.

## Overview

Script plugins use `runtime: "script"` in the manifest. The `entry` field specifies the command to execute. The script's stdout becomes widget content or command response.

## Manifest

```json
{
  "id": "my-script-plugin",
  "name": "My Script Plugin",
  "version": "1.0.0",
  "runtime": "script",
  "entry": "bash main.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute"],
  "contributes": {
    "ui": [
      {
        "id": "status",
        "slot": "statusBarRight",
        "priority": 50,
        "description": "Show status info",
        "refreshInterval": "10s",
        "triggers": ["PostToolUse:Shell"]
      }
    ]
  }
}
```

### Platform-Specific Entry

```json
{
  "entry": "bash main.sh",
  "entry_windows": "powershell main.ps1",
  "entry_darwin": "bash main.sh",
  "entry_linux": "bash main.sh"
}
```

Platform-specific entries take precedence over the generic `entry` for the matching OS.

## Widget Refresh

Widgets refresh on three triggers:

1. **Periodic**: `refreshInterval` field (e.g. `"10s"`, `"1m"`). Default: 30 seconds.
2. **Hook-triggered**: `triggers` field (e.g. `["PostToolUse:Shell"]`). Fires immediately when the hook matches.
3. **Directory change**: When the session's working directory changes, the script re-runs for the new directory.

### Trigger Events

| Trigger | Description |
|---------|-------------|
| `PreToolUse:<matcher>` | Before a tool executes (matcher = tool name pattern) |
| `PostToolUse:<matcher>` | After a tool executes successfully |
| `PostToolUseFailure:<matcher>` | After a tool fails |
| `UserPromptSubmit` | When user sends a message |
| `AgentStop` | When the agent stops |
| `SessionStart` | When a session starts |
| `SessionEnd` | When a session ends |
| `SubAgentStart` | When a SubAgent starts |
| `SubAgentStop` | When a SubAgent stops |
| `PreCompact` | Before context compression |
| `PostCompact` | After context compression |
| `CronFired` | When a cron job fires |
| `WebhookReceived` | When a webhook is received |

### Sync Mode

Set `"sync": true` on a UI contribution to run the script synchronously on hook triggers. The output is available immediately as hint content for the engine:

```json
{
  "ui": [
    {
      "id": "diff-hint",
      "slot": "infoBar",
      "sync": true,
      "triggers": ["PostToolUse:FileReplace"]
    }
  ]
}
```

## Environment Variables

Scripts receive context via environment variables:

| Variable | Description | Available When |
|----------|-------------|----------------|
| `XBOT_WORK_DIR` | Current working directory | Always |
| `XBOT_WIDGET_ID` | Widget ID being rendered | Widget rendering |
| `XBOT_PLUGIN_CONFIG` | Plugin configuration (JSON) | Always (if config exists) |
| `XBOT_HOOK_EVENT` | Hook event name | Hook triggers |
| `XBOT_TOOL_NAME` | Tool name that triggered the hook | Tool hooks |
| `XBOT_TOOL_OUTPUT` | Tool output (truncated to 8KB) | PostToolUse hooks |
| `XBOT_TOOL_INPUT` | Tool input | Tool hooks |
| `XBOT_MODEL` | Current LLM model name | Hook events with session context |
| `XBOT_MAX_CONTEXT` | Max context tokens | Hook events with session context |
| `XBOT_TOKEN_USAGE` | Token usage as `prompt/completion` | Hook events with token data |
| `XBOT_PROMPT_TOKENS` | Prompt token count | Hook events with token data |
| `XBOT_COMP_TOKENS` | Completion token count | Hook events with token data |
| `XBOT_COMMAND_NAME` | Command name | Command execution |
| `XBOT_COMMAND_ARGS` | Command arguments | Command execution |

## Output Format

Script stdout is parsed for style hints:

| Format | Style | Description |
|--------|-------|-------------|
| `text` | Normal | Default style |
| `dim\|text` | Dim | Muted/dimmed text |
| `ok\|text` | Success | Green text |
| `warn\|text` | Warning | Yellow text |
| `err\|text` | Error | Red text |
| `info\|text` | Info | Blue text |
| `accent\|text` | Accent | Highlighted text |
| `md\|<markdown>` | Raw | Multi-line markdown content |
| `diff\|<diff>` | Raw | Multi-line unified diff (preserves ANSI) |

The `|` separator splits style from content. For `md|` and `diff|`, the full multi-line content after the prefix is preserved.

## Per-WorkDir Output Cache

Script plugins maintain a per-workDir output cache: `workDir → widgetID → output`. Each CLI window (different workDir) sees its own content. The cache is:

- Populated on refresh (periodic or triggered)
- Evicted when the directory no longer exists
- Change-detected: `NotifyUpdated()` only fires when output actually changes

## Commands

Scripts can register slash commands:

```json
{
  "contributes": {
    "commands": [
      {
        "name": "/deploy",
        "description": "Deploy the current project"
      }
    ]
  }
}
```

When the user types `/deploy production`, the script runs with `XBOT_COMMAND_NAME=deploy` and `XBOT_COMMAND_ARGS=production`. The script's stdout becomes the command response.

## Configuration Injection

Plugin configuration is injected as JSON via `XBOT_PLUGIN_CONFIG`:

```bash
#!/bin/bash
# Read plugin config
config=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.greeting // "Hello"')
echo "$config, World!"
```

## Complete Example

```bash
#!/bin/bash
# main.sh — Git branch widget

# Get current git branch
branch=$(git -C "$XBOT_WORK_DIR" rev-parse --abbrev-ref HEAD 2>/dev/null)

if [ -n "$branch" ]; then
    # Check for uncommitted changes
    if [ -n "$(git -C "$XBOT_WORK_DIR" status --porcelain 2>/dev/null)" ]; then
        echo "warn|$branch*"
    else
        echo "ok|$branch"
    fi
else
    echo "dim|no-git"
fi
```

## See Also

- [Getting Started](./getting-started/) — Create your first script plugin
- [Widgets](./widgets/) — Widget system overview
- [Hooks](./hooks/) — Hook events
- [Configuration](./configuration/) — Plugin configuration
- [API Reference: Environment Variables](./api-reference/environment-variables/) — Complete env var reference
