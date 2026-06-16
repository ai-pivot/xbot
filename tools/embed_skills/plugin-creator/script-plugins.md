# Script Plugins

Shell-based plugins for widgets, hooks, and simple integrations.

## Structure

```
my-plugin/
├── plugin.json     # Manifest
└── my-script.sh    # Entry script (must be executable)
```

## plugin.json Format

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

## Available UI Slots

| Slot | Location |
|------|----------|
| `infoBar` | Bottom info bar |
| `toolHint` | Progress panel |

## Available Triggers

```
PostToolUse:<matcher>        — after tool succeeds (matcher supports glob)
PreToolUse:<matcher>         — before tool executes
PostToolUseFailure:<matcher> — after tool fails
UserPromptSubmit:            — on user prompt
AgentStop:                   — on agent stop
SessionStart: / SessionEnd:  — session lifecycle
PreCompact: / PostCompact:   — context compression
```

## Script Output Format

Widget output: `"style|text"` where style is: `dim`, `ok`, `warn`, `err`, `info`, `accent`, or empty.

```bash
echo "ok|git:main ✓"
echo "warn|3 uncommitted changes"
echo "dim|no repo"
```

## Environment Variables (available in script)

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

## Examples

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

```bash
#!/bin/bash
set -euo pipefail
model="${XBOT_MODEL:-unknown}"
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
