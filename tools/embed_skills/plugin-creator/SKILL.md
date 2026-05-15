---
name: plugin-creator
description: Create, modify, and manage xbot plugins. Use when the user asks to create a plugin, set up script plugins, configure widgets, or register custom tools via plugins.
---

# Plugin Creator

Create and manage xbot plugins. Script plugins are the easiest type — just a `plugin.json` + bash script.

## When to Activate

- User wants to create/modify/delete plugins
- User wants to add custom tools, widgets, or context enrichers
- User asks about plugin system configuration

## Plugin Types

| Type | Complexity | Use Case |
|------|-----------|----------|
| **script** | Low | Shell-based plugins: widgets, notifications, simple tools |
| **native** | Medium | Go in-process plugins (requires compilation) |
| **grpc** | High | External process plugins (Python, etc.) |

**Prefer script plugins** — they require only `plugin.json` + a bash script.

## Plugin Location

- User plugins: `~/.xbot/plugins/<plugin-id>/`
- Project plugins: `<project>/.xbot/plugins/<plugin-id>/`
- Built-in examples: `plugin/examples/`

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

## Complete Examples

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

## Workflow

1. **Understand requirement**: Widget? Tool? What triggers?
2. **Choose plugin location**: `~/.xbot/plugins/<id>/` for user-level
3. **Create plugin.json**: Use FileCreate with the manifest
4. **Create script**: Write the bash script, ensure it's executable (`chmod +x`)
5. **Reload plugins**: Use `tui_control` with action `reload_plugins` to hot-reload

## After Creating/Modifying Plugins

**IMPORTANT**: Always reload plugins so changes take effect immediately:

```
tui_control(action="reload_plugins")
```

This hot-reloads all plugins without restarting the CLI. Alternatively, user can run `/plugin reload-all` in the TUI.

## Gotchas

- Script must be executable: `chmod +x my-script.sh`
- `sync: true` means the widget blocks tool execution until rendered (use for toolHint)
- Widget output is limited in size — keep it short
- Plugin ID must be unique across all plugins
- Script output without `style|` prefix is treated as plain text
