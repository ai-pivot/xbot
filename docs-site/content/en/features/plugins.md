---
title: "Plugin System"
weight: 55
---

# Plugin System

Plugins are "mini-apps" for xbot — show git status in the info bar, auto-display
diff previews, run lint before commits, or send desktop notifications. No code
changes required: just a JSON manifest and a script.

{{< hint type=tip >}}
**Let the agent do it.** You don't need to write config files. Say "add a plugin
that shows the current git branch in the status bar" or "install a plugin that
shows a diff after every file edit" — the agent creates `plugin.json` and the
script, then reloads automatically.
{{< /hint >}}

## Reloading Plugins

The agent reloads plugins automatically after creating or modifying them. You
can also reload manually:

| Method | Command |
|--------|---------|
| In conversation | "Reload all plugins" |
| TUI slash command | `/plugin reload-all` |
| Reload a single plugin | `/plugin reload <id>` |
| View plugin status | `/plugin` |

## Plugin Structure

A plugin is a directory with at least two files:

```
~/.xbot/plugins/my-plugin/
├── plugin.json     ← Plugin manifest (name, capabilities, triggers)
└── my-script.sh    ← The script that does the actual work
```

### Example: Git Status Widget

`plugin.json`:
```json
{
  "id": "my-git",
  "name": "My Git Status",
  "version": "1.0.0",
  "description": "Shows current git branch and change status",
  "runtime": "script",
  "entry": "bash git.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [{
      "id": "git-branch",
      "slot": "infoBar",
      "priority": 10,
      "triggers": ["PostToolUse:Shell*", "PostToolUse:FileReplace*"]
    }]
  }
}
```

`git.sh`:
```bash
#!/bin/bash
set -euo pipefail
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ]; then
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

The output format is `style|text`. Available styles: `dim`, `ok`, `warn`, `err`,
`info`, `accent`.

## Plugin Locations

| Location | Scope |
|----------|-------|
| `~/.xbot/plugins/<id>/` | User-level — available in all projects |
| `<project>/.xbot/plugins/<id>/` | Project-level — only active in this project (can be committed to git for team sharing) |

## Configuration

Plugins are **opt-in**. Enable in `~/.xbot/config.json`:

```json
{
  "plugins": {
    "enabled": true,
    "allow_unverified": false
  }
}
```

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | bool | Enable the plugin system. **Default: false.** |
| `dirs` | []string | Additional directories to scan for plugins (default: `~/.xbot/plugins/`). |
| `disabled_plugins` | []string | Plugin IDs to skip during discovery. |
| `allow_unverified` | bool | Load plugins without verified manifests. **Not recommended.** |

{{< hint type=warning >}}
**Security:** Plugins run as scripts on your machine. Only install plugins from
sources you trust. Keep `allow_unverified` disabled unless you know what you're
doing.
{{< /hint >}}

## What Plugins Can Do

### Display Information (Widgets)

Show dynamic content in the TUI:

| Slot | Value | Best for |
|------|-------|----------|
| Info bar | `infoBar` | Status info: git branch, environment name, countdowns |
| Tool hint | `toolHint` | One-time hints: diff summary, test results |

### Trigger Timing

`triggers` control when a plugin's widget runs:

| Trigger | Fires when |
|---------|-----------|
| `PostToolUse:Shell*` | After a Shell command runs |
| `PostToolUse:FileReplace*` | After editing a file |
| `PostToolUse:FileCreate*` | After creating a file |
| `AgentStop:` | After the agent finishes replying |
| `SessionStart:` | When a session starts |
| `PreToolUse:Shell*` | Before a Shell command runs |

Triggers support wildcards: `Shell*` matches all Shell tool calls.

### Script Environment Variables

When a script runs, these environment variables are automatically available:

| Variable | Content |
|----------|---------|
| `XBOT_TOOL_NAME` | The tool that triggered this, e.g., `Shell`, `FileReplace` |
| `XBOT_TOOL_OUTPUT` | Tool execution result (truncated to 8 KB) |
| `XBOT_TOOL_INPUT` | Tool input parameters (JSON) |
| `XBOT_WORK_DIR` | Current working directory |
| `XBOT_MODEL` | Current model name |
| `XBOT_MAX_CONTEXT` | Maximum context tokens |
| `XBOT_TOKEN_USAGE` | Format: `"prompt/completion"` |
| `XBOT_PROMPT_TOKENS` | Prompt token count |
| `XBOT_COMP_TOKENS` | Completion token count |
| `XBOT_WIDGET_ID` | Widget identifier (when plugin has multiple widgets) |

## Reference

### Complete plugin.json Fields

```json
{
  "id": "my-plugin",           // Required, globally unique ID
  "name": "My Plugin",         // Display name
  "version": "1.0.0",          // Semantic version
  "description": "What it does", // One-line description
  "author": "your-name",
  "runtime": "script",         // "script" | "native" | "grpc"
  "entry": "bash main.sh",     // Entry command (script type)
  "timeout": "30s",            // Execution timeout (Go duration string, max 5m)
  "permissions": [...],        // Required permissions
  "contributes": {             // What the plugin contributes
    "ui": [...],               // UI components (widgets)
    "tools": [...],            // Custom tools
    "hooks": [...]             // Lifecycle hooks
  }
}
```

### Permissions

| Permission | Purpose |
|------------|---------|
| `ui.contribute` | Display UI widgets |
| `hooks.subscribe` | Subscribe to lifecycle events |
| `tools.register` | Register custom tools |
| `tools.call` | Call other tools |
| `storage.private` | Private plugin storage |
| `context.enrich` | Inject into system prompt |
| `bus.plugin` | Plugin event bus (publish/subscribe) |

### Advanced: Auto-Refresh

Add `refreshInterval` to a widget for periodic auto-refresh — no trigger needed:

```json
"ui": [{
  "id": "clock",
  "slot": "infoBar",
  "priority": 0,
  "refreshInterval": "30s",
  "triggers": ["SessionStart:"]
}]
```

### Advanced: Synchronous Tool Hints

`toolHint` widgets can set `"sync": true` to run synchronously when a tool
completes, ensuring hints appear instantly:

```json
"ui": [{
  "id": "diff",
  "slot": "toolHint",
  "sync": true,
  "triggers": ["PostToolUse:FileReplace*"]
}]
```

### CLI Quick Reference

| Command | Description |
|---------|-------------|
| `/plugin` | Plugin status overview |
| `/plugin list` | List all plugins |
| `/plugin reload <id>` | Reload a single plugin |
| `/plugin reload-all` | Reload all plugins |
| `/plugin health` | Health check |
| `/plugin install <dir>` | Install a plugin from directory |
| `/plugin uninstall <id>` | Uninstall a plugin |
| `/plugin widgets` | View widget status |
