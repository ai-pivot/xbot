---
title: "Hooks"
weight: 60
---

# Hooks (Lifecycle Events)

Hooks let you run scripts automatically **before** or **after** xbot performs
actions. Think: run lint before every git commit, send a notification after the
agent responds, auto-format Go files after editing.

{{< hint type=tip >}}
**Let the agent configure it.** No need to write JSON by hand. Say "set up a
hook to run `make lint` before every git commit" or "configure a hook that
sends a desktop notification when the agent errors out." The agent creates
`hooks.json` and reloads it automatically.
{{< /hint >}}

## Configuration File

Hooks are configured in JSON files. Multiple files are merged — later ones
override earlier ones:

| File | Scope |
|------|-------|
| `~/.xbot/hooks.json` | User-level, active in all projects |
| `<project>/.xbot/hooks.json` | Project-level, can be committed to git for team sharing |
| `<project>/.xbot/hooks.local.json` | Local overrides, not committed to git |

### Complete Example

**Scenario:** Run lint before every `git commit`, blocking the commit on failure.

`~/.xbot/hooks.json`:
```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [{
          "type": "command",
          "command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make lint || exit 2",
          "if": "Shell(*git commit*)",
          "timeout": 120
        }]
      }
    ]
  }
}
```

**Key points:**
- `enable_command_hooks: true` — **required**. Command-type hooks are ignored without this explicit opt-in.
- `exit 2` = block the operation, `exit 0` = allow
- `if` is a fine-grained filter — matches the entire tool input text, not key=value pairs

## Hook Events

xbot exposes 17 lifecycle events, divided into two categories:

### Blocking Events

These hooks can **prevent** an action from proceeding:

| Event | Fires | Typical use |
|-------|-------|-------------|
| `UserPromptSubmit` | Before user message is processed | Screen user input |
| `PreToolUse` | Before tool execution | **Most common** — pre-commit lint, pre-write checks |
| `PostToolBatch` | After a batch of tools completes | Batch validation |
| `SubAgentStop` | After a SubAgent completes | Review SubAgent output |
| `AgentStop` | After the agent finishes replying | Final review |
| `PreCompact` | Before context compression | Control compression behavior |

### Non-blocking Events

These hooks can **observe** what happened but cannot prevent it:

| Event | Fires | Typical use |
|-------|-------|-------------|
| `SessionStart` | Session begins | Initialization |
| `SessionEnd` | Session ends | Cleanup |
| `PostToolUse` | After tool executes successfully | Logging, notifications |
| `PostToolUseFailure` | After tool execution fails | Error notifications |
| `AgentError` | LLM call fails | Error alerts |
| `PostCompact` | After compression completes | Logging |
| `CronFired` | Scheduled task triggers | — |
| `WebhookReceived` | External webhook received | — |

## Hook Types

### command (most common)

Runs a shell command. Event data is passed via **stdin** as JSON.

- Exit code `0` = allow
- Exit code `2` = **block** the operation
- Any other exit code = allow (error logged but not blocking)

```json
{
  "type": "command",
  "command": "my-script.sh",
  "timeout": 30,
  "async": false
}
```

`async: true` runs in the background without blocking the agent — ideal for
notifications and logging.

### http

Sends a POST request to a URL. Event data is the request body.

```json
{
  "type": "http",
  "url": "http://localhost:3000/notify",
  "timeout": 10
}
```

### mcp_tool

Calls an MCP tool. Supports `${...}` variable interpolation.

```json
{
  "type": "mcp_tool",
  "server": "security",
  "tool": "scan_file",
  "input": { "path": "${tool_input.path}" }
}
```

## matcher vs if

- **`matcher`**: Matches tool name. `"Shell"` → only Shell tools. `""` → matches all tools.
- **`if`**: Fine-grained filter matching the entire tool input as text.

Example: `if: "Shell(*git commit*)"` checks whether the Shell arguments contain
`git commit`.

{{< hint type=warning >}}
`if` matches the **entire input text**, not key=value pairs. `Shell(*git commit*)`
works, but `Shell(command=*git*)` does not.
{{< /hint >}}

## Environment Variables

Command-type hooks receive these environment variables:

| Variable | Content |
|----------|---------|
| `XBOT_PROJECT_DIR` | Current project root directory |
| `XBOT_SESSION_ID` | Current session ID |
| `XBOT_HOME` | `~/.xbot` directory |

Event data is also sent via stdin as JSON.

## Decision Priority

When multiple hooks fire simultaneously, the final decision follows this
priority order:

**Deny > Defer > Ask > Allow**

In human terms: if **any** hook says "block," the operation is blocked — even
if other hooks say "allow." A low-priority "deny" cannot be overridden by a
high-priority "allow."

## Limits

- Maximum **10 handlers** per event
- Maximum **60 seconds** timeout per handler
- Command hooks require explicit `enable_command_hooks: true`

## Reference

### Complete Config Format

```jsonc
{
  // Must be true for command-type hooks to execute
  "enable_command_hooks": true,

  "hooks": {
    "EventName": [
      {
        "matcher": "ToolName or wildcard",  // "" matches all
        "hooks": [
          {
            "type": "command",        // command | http | mcp_tool
            "command": "script.sh",   // command type: command to run
            "url": "http://...",      // http type: target URL
            "if": "Shell(*pattern*)", // Optional: fine-grained filter
            "timeout": 30,            // Timeout in seconds (max 60)
            "async": false            // true = background, non-blocking
          }
        ]
      }
    ]
  }
}
```

## More Examples

### Desktop notification on agent error (non-blocking)

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "AgentError": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "notify-send 'xbot error' 'LLM call failed — check configuration'",
        "async": true
      }]
    }]
  }
}
```

### Auto-format Go files after editing

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PostToolUse": [{
      "matcher": "FileReplace",
      "hooks": [{
        "type": "command",
        "command": "file=$(echo $XBOT_TOOL_INPUT | jq -r '.path'); [[ \"$file\" == *.go ]] && gofmt -w \"$file\"",
        "async": true
      }]
    }]
  }
}
```

## Hook Ideas

Practical hooks you can create:

- **Auto-format Go** — run `gofmt` after every `FileReplace`
- **Git diff preview** — show diff after every file edit
- **Slack notification** — POST to webhook when agent errors
- **Cost tracker** — log token usage to a file after each LLM call
- **Auto-commit** — `git add` after successful file operations
- **Security scan** — run `gosec` before allowing Shell commands

Ask the agent: "Set up a hook that [your requirement]."

## See also
- [Plugins](/features/plugins/) — plugin system overview
- [Permission Control](/guides/permission-control/) — sandbox and permissions
- [Configuration](/configuration/) — enable_command_hooks setting
