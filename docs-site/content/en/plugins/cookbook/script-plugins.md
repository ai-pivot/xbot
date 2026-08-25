---
title: "Script Plugins"
weight: 6
---

Script plugins run an external command (bash, Python, Node — anything executable) on a periodic refresh loop and on hook triggers. The runtime is `plugin/script_runtime.go`; two complete examples live in `plugin/examples/git-info/` (status widget) and `plugin/examples/file-diff/` (tool hint diff).

## The contract

The script:

1. runs with the **session working directory** as CWD;
2. receives context via **environment variables**;
3. writes its output to **stdout**, terminated by a newline;
4. output is interpreted by the `style|text` line format.

## Output format

`parseScriptOutput` (`plugin/script_runtime.go`) parses `style|text` lines into `WidgetSpan`s:

```bash
echo "ok|git:main ✓"      # ok  → accent/green
echo "warn|git:feat/x Δ3" # warn → yellow
echo "err|build failed"   # err  → red
echo "dim|git: —"         # dim  → muted
echo "info|syncing..."    # info → blue
echo "accent|42 items"    # accent → highlighted
echo "plain text"         # no prefix → normal style
```

Two multi-line prefixes are special-cased (`plugin/script_runtime.go:744`): `md|` (markdown, rendered by glamour in the progress panel) and `diff|` (unified diff with ANSI coloring). All other output is single-line.

Example from `file-diff.sh` (tool-hint widget generating a diff):

```bash
echo "md|"
echo "\`\`\`diff"
echo "--- a/path/file.go"
echo "+++ b/path/file.go"
echo "$raw_diff" | head -40
echo "\`\`\`"
```

## Environment variables

Injected by `runScript` (`plugin/script_runtime.go:692`):

| Variable | Source |
|---|---|
| `XBOT_WORK_DIR` | Session working directory |
| `XBOT_WIDGET_ID` | Widget ID being rendered (multi-widget plugins branch on this) |
| `XBOT_PLUGIN_CONFIG` | Merged plugin configuration, JSON string |
| `XBOT_HOOK_EVENT` | Last hook event that triggered a run |
| `XBOT_TOOL_NAME` | Last hook's tool name |
| `XBOT_TOOL_INPUT` | Last hook's tool input (JSON string) |
| `XBOT_TOOL_OUTPUT` | Last hook's tool output |
| `XBOT_MODEL` / `XBOT_MAX_CONTEXT` | Session context from `HookPayload.Extra` |
| `XBOT_TOKEN_USAGE` / `XBOT_PROMPT_TOKENS` / `XBOT_COMP_TOKENS` | Token usage (`"prompt/completion"` format) |
| `XBOT_COMMAND_NAME` / `XBOT_COMMAND_ARGS` | When invoked as a contributed command |

⚠️ `XBOT_WIDGET_ID` matters: when a plugin declares multiple widgets, the script runs **once per widget** with the ID set, and output is cached per `workDir → widgetID`. Without branching on it, all widgets show identical output.

## Widgets, triggers, and sync hints

The manifest `contributes.ui[]` entries drive everything (`UISlotContribution`):

```json
{
  "id": "file-diff",
  "runtime": "script",
  "entry": "bash file-diff.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute", "hooks.subscribe"],
  "contributes": {
    "ui": [
      {
        "id": "diff-summary",
        "slot": "toolHint",
        "priority": 5,
        "sync": true,
        "description": "Shows unified diff in the progress panel after file modifications",
        "triggers": ["PostToolUse:FileReplace*", "PostToolUse:FileCreate*", "PostToolUse:FileEdit*", "PostToolUse:Write*"]
      }
    ]
  }
}
```

- **`slot`** — where the widget renders. `infoBar`, `statusBarRight`, `toolHint`, `footer`, `titleBar` are the CLI zones (see [Widgets](../widgets/)).
- **`triggers`** — `"EventName:Matcher"` strings. `subscribeTrigger` (`plugin/script_runtime.go:508`) parses and subscribes these to hooks; when the hook fires the script re-runs immediately. Supported events: `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`, `AgentStop`, `SessionStart`, `SessionEnd`, `SubAgentStart`, `SubAgentStop`, `PreCompact`, `PostCompact`, `CronFired`, `WebhookReceived`.
- **`sync: true`** — the `toolHint` slot must be synchronous: the hook trigger runs the script **inline** (not via the async trigger channel), strips the `md|`/`diff|` prefix, and stores the output as hint content. The engine reads it immediately after the hook returns via `GetHintContent()` and attaches it to `ToolProgress.ToolHints` — this is how `file-diff` renders a live diff in the progress panel right after an edit tool completes.
- **`refreshInterval`** — Go duration string (`"30s"`, `"1m"`); the shortest interval across all widgets wins (`plugin/script_runtime.go:175`).

⚠️ Script plugin triggers are registered as **global hooks** (`registerGlobalHook`, `plugin/script_runtime.go:608`) — they are session-agnostic and must fire for all sessions. Per-session output is handled by the `workDir → widgetID → output` cache, not by session filtering.

## Internal loop

`scriptPlugin.refreshLoop` (`plugin/script_runtime.go:361`):

1. runs immediately on start, then every `interval`;
2. also fires on `triggerCh` (buffered, size 8 — full channel skips the trigger, the next tick catches up);
3. `runAndUpdate` collects all known workDirs (cached + pending from `OnWorkDirChanged` + current), evicts deleted directories, runs the script once per widget per workDir;
4. change detection compares against the previous snapshot and only calls `widgetReg.NotifyUpdated()` when output actually changed.

## Platform-specific entries

`resolvedEntry` (`plugin/script_runtime.go:620`) picks the right command per OS:

```json
{
  "entry": "bash run.sh",
  "entry_windows": "powershell -File run.ps1",
  "entry_linux": "bash run.sh",
  "entry_darwin": "bash run.sh"
}
```

## Runtime limits

- Each run has a **10s timeout** (`context.WithTimeout(parent, 10*time.Second)`).
- The entry command is split with `strings.Fields` (shell-free); relative script paths resolve against the plugin directory.
- `Deactivate` cancels the background context and waits up to 5s for the loop to exit (so Windows temp-dir cleanup doesn't fail on open file handles).
- Rapid hook triggers **overwrite** the stored `lastHook` — the script only sees the latest event.
