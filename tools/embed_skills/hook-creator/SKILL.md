---
name: hook-creator
description: Create, modify, and manage xbot hooks. Use when the user asks to set up a hook, configure lifecycle event handlers, or modify hooks.json.
---

# Hook Creator

Create and manage xbot hooks configuration.

## When to Activate

- User wants to create/modify/delete hooks
- User wants to add pre-tool, post-tool, or other lifecycle event handlers
- User asks about hooks configuration

## Hooks Architecture

### Configuration Files (3-layer merge, later overrides earlier)

1. `~/.xbot/hooks.json` — user-level (personal)
2. `<project>/.xbot/hooks.json` — project-level (can commit to git)
3. `<project>/.xbot/hooks.local.json` — local override (git-ignored)

### File Format

```jsonc
{
  "enable_command_hooks": true,
  "hooks": {
    "EventName": [
      {
        "matcher": "ToolName",     // glob pattern, "" = match all
        "hooks": [
          {
            "type": "command",     // "command" | "http" | "mcp_tool"
            "command": "script.sh",
            "if": "Shell(*git*)",  // optional fine-grained filter
            "timeout": 30,
            "async": false
          }
        ]
      }
    ]
  }
}
```

### 17 Lifecycle Events

| Event | When | Blocking |
|-------|------|----------|
| `SessionStart` | Session created/resumed | No |
| `SessionEnd` | Session ends | No |
| `UserPromptSubmit` | Before LLM processing | **Yes** |
| `PreToolUse` | Before tool execution | **Yes** |
| `PostToolUse` | After tool success | No |
| `PostToolUseFailure` | After tool failure | No |
| `PostToolBatch` | After all tools in batch | **Yes** |
| `PermissionRequest` | Permission check | **Yes** |
| `SubAgentStart` | SubAgent created | No |
| `SubAgentStop` | SubAgent completed | **Yes** |
| `AgentStop` | Agent response complete | **Yes** |
| `AgentError` | LLM API call failed | No |
| `PreCompact` | Before context compression | **Yes** |
| `PostCompact` | After compression | No |
| `CronFired` | Cron job triggered | No |
| `WebhookReceived` | Webhook received | No |

### Handler Types

- **command**: Shell command. Event payload via stdin (JSON). Exit 0=allow, 2=deny.
- **http**: POST to URL. 2xx=success.
- **mcp_tool**: Call MCP tool with `${variable}` interpolation.

### Decision Control

Priority: `deny > defer > ask > allow`

### Env Vars Available in Command Hooks

| Variable | Value |
|----------|-------|
| `$XBOT_PROJECT_DIR` | Current project root |
| `$XBOT_SESSION_ID` | Current session ID |
| `$XBOT_HOME` | `~/.xbot` |

## Workflow

1. **Understand requirement**: Which event? What action? Blocking or not?
2. **Choose config layer**: User-level (`~/.xbot/hooks.json`) or project-level (`.xbot/hooks.json`)
3. **Create/update config**: Use FileCreate or FileReplace to write hooks.json
4. **Create handler script** (if command type): Write shell script, make executable
5. **Reload hooks**: Use `tui_control` with action `reload_hooks` to apply changes immediately

## Critical Gotchas

- `enable_command_hooks` defaults to **false**. MUST set to `true` for command-type hooks.
- `if` matches the entire tool input string as text, NOT key=value. `Shell(*git commit*)` works; `Shell(command=*git*)` does NOT.
- PATH in command hooks is minimal. Prepend GOPATH/bin if needed:
  ```json
  "command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make lint || exit 2"
  ```
- Max timeout per handler: 60s. Max 10 handlers per event.

## Example Hooks

### Lint before git commit

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",
        "hooks": [{
          "type": "command",
          "command": ".xbot/hooks/lint.sh",
          "if": "Shell(*git commit*)",
          "timeout": 120
        }]
      }
    ]
  }
}
```

### Notify on errors (async)

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "AgentError": [{
      "matcher": "",
      "hooks": [{
        "type": "command",
        "command": "notify-send 'xbot error' 'LLM call failed'",
        "async": true
      }]
    }]
  }
}
```

## After Creating/Modifying Hooks

**IMPORTANT**: Always reload hooks configuration so changes take effect immediately:

```
tui_control(action="reload_hooks")
```

Without reload, changes to hooks.json only apply after CLI restart.
