---
title: "Environment Variables"
weight: 6
---

Reference for the `XBOT_*` environment variables injected into **script runtime** plugin processes (`plugin/script_runtime.go`). Stdio plugins do not receive these — they use the NDJSON protocol instead.

## Always Injected

| Variable | Content |
|----------|---------|
| `XBOT_WORK_DIR` | The session's current working directory. The script's `cmd.Dir` is also set to this value (if it exists). |
| `XBOT_PLUGIN_CONFIG` | The plugin's merged config serialized as a JSON string (manifest defaults overlaid with user values). |

## Widget Runs

| Variable | Content |
|----------|---------|
| `XBOT_WIDGET_ID` | Set **only** when the script runs on behalf of a specific widget. Multi-widget plugins branch on this value to produce different output per widget (absent when the plugin declares a single widget or for non-widget runs). |

## Hook-Triggered Runs

Available when the script runs as a hook trigger (values come from the hook payload):

| Variable | Content |
|----------|---------|
| `XBOT_HOOK_EVENT` | The hook event name (e.g. `"PostToolUse"`). |
| `XBOT_TOOL_NAME` | Tool name (set only when non-empty). |
| `XBOT_TOOL_OUTPUT` | Tool output (set only when non-empty). |
| `XBOT_TOOL_INPUT` | Tool input (set only when non-empty). |

## Session Context (all hook events)

Populated from `HookPayload.Extra`, injected by the engine after each LLM call and compression (`hooks.SessionContext` → `plugin_bridge.go` → `HookPayload.Extra` → env vars):

| Variable | Content |
|----------|---------|
| `XBOT_MODEL` | The model name of the current session. |
| `XBOT_MAX_CONTEXT` | The session's max context token limit. |
| `XBOT_TOKEN_USAGE` | Combined prompt/completion tokens, format `"<prompt>/<completion>"` (e.g. `"12345/678"`). |
| `XBOT_PROMPT_TOKENS` | Prompt token count. |
| `XBOT_COMP_TOKENS` | Completion token count. |

## Command Runs

| Variable | Content |
|----------|---------|
| `XBOT_COMMAND_NAME` | The slash command name (for command-handler scripts). |
| `XBOT_COMMAND_ARGS` | Everything after the command name. |

## Notes

- Variables are appended to the inherited environment (`os.Environ()`), so the script also sees the host process environment.
- Optional variables are **omitted entirely** when their source value is empty — e.g. `XBOT_WIDGET_ID` is not set (rather than set to an empty string) when no widget ID applies.
- Widgets can display model name, context usage bar, and token costs by reading these variables.
