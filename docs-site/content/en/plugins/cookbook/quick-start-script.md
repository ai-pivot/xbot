---
title: "Quick Start: Script Plugin"
weight: 2
---

Build a git status widget with a single bash script — no compilation, no SDK. This recipe is based on the real `git-info` example at `plugin/examples/git-info/`.

## The result

A widget in the CLI **info bar** showing your git branch and uncommitted changes:

```
git:main ✓          # clean
git:feat/x Δ3       # 3 changed files
git: —               # not a git repo
```

## Step 1: Create the script

`~/.xbot/plugins/git-info/git-info.sh`:

```bash
#!/bin/bash
# Output format: "style|text" (style: dim, ok, warn, err, info, accent)

set -euo pipefail

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || true
if [ -z "$branch" ] || [ "$branch" = "HEAD" ]; then
    echo "dim|git: —"
    exit 0
fi

changes=$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ') || changes=0
ahead=$(git rev-list --count @{u}..HEAD 2>/dev/null) || ahead=0
behind=$(git rev-list --count HEAD..@{u} 2>/dev/null) || behind=0

status=""
[ "$changes" -gt 0 ] && status="${status}Δ${changes} "
[ "$ahead" -gt 0 ]   && status="${status}↑${ahead} "
[ "$behind" -gt 0 ]  && status="${status}↓${behind} "

if [ -z "$status" ]; then
    echo "ok|git:${branch} ✓"
elif [ "$changes" -gt 0 ]; then
    echo "warn|git:${branch} ${status}"
else
    echo "info|git:${branch} ${status}"
fi
```

## Step 2: Write the manifest

`~/.xbot/plugins/git-info/plugin.json`:

```json
{
  "id": "git-info",
  "name": "git-info",
  "version": "1.2.0",
  "description": "Shows git branch and working tree status in the info bar.",
  "author": "xbot",
  "runtime": "script",
  "entry": "bash git-info.sh",
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [
      {
        "id": "git-branch",
        "slot": "infoBar",
        "priority": 10,
        "description": "Git branch name and dirty/clean status",
        "refreshInterval": "30s",
        "triggers": [
          "PostToolUse:Shell*",
          "PostToolUse:Cd*",
          "PostToolUse:FileReplace*",
          "PostToolUse:FileCreate*"
        ]
      }
    ]
  }
}
```

## Step 3: Restart xbot

The plugin is discovered from `~/.xbot/plugins/<id>/plugin.json` at startup. Restart xbot and you will see `git:main ✓` in the info bar.

## How it works

1. **`runtime: "script"`** — `plugin.NewScriptRuntime()` (`plugin/script_runtime.go`) spawns `entry` as an external process with a 10s timeout per run, on a refresh loop and on hook triggers.
2. **`contributes.ui[].slot: "infoBar"`** — declares a widget slot. `scriptPlugin.Activate` wraps each UI contribution in a `widgetAdapter` (`plugin/script_runtime.go:163`) and registers it via `ctx.ContributeUI(ui.ID, ui.Slot, adapter, ui.Priority)`.
3. **`refreshInterval: "30s"`** — the script re-runs every 30 seconds (shortest interval across widgets wins).
4. **`triggers: ["PostToolUse:Shell*", ...]`** — after any `Shell*`/`Cd*`/`FileReplace*` tool completes, the script re-runs immediately (`plugin/script_runtime.go:508 subscribeTrigger`).
5. **Output format `style|text`** — `parseScriptOutput` splits on the first `|`; `ok`/`warn`/`dim`/`info`/`err`/`accent` map to `WidgetSpan` styles. Output without a prefix is rendered as plain text.

The script runs with the **session working directory** as its CWD (`cmd.Dir = workDir` in `runScript`, `plugin/script_runtime.go:683`), so `git` sees the repository the agent is currently working in. Outputs are cached **per workDir per widget** (`outputs map[string]map[string]string`) so multiple CLI sessions each see their own branch.

## Try it now

```bash
mkdir -p ~/.xbot/plugins/git-info
# copy git-info.sh and plugin.json there
```

Useful environment variables available inside the script (injected by `runScript`):

| Variable | Content |
|---|---|
| `XBOT_WORK_DIR` | Current session working directory |
| `XBOT_WIDGET_ID` | The widget ID being rendered (multi-widget plugins) |
| `XBOT_TOOL_NAME` / `XBOT_TOOL_INPUT` / `XBOT_TOOL_OUTPUT` | Last hook's tool data |
| `XBOT_PLUGIN_CONFIG` | Merged plugin configuration as JSON |
| `XBOT_HOOK_EVENT` | Last hook event name |

Next: [Script Plugin development guide](../script-plugins/) for the full environment and output contract.
