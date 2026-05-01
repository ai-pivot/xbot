# Hooks System

## Overview

`agent/hooks/` — lifecycle event hooks system. Fires on 17 events across the agent lifecycle, supports 4 handler types (command/http/callback/mcp_tool), configured via JSON. Non-invasive: agent code calls `Manager.Emit()` at integration points; all matching/handling lives in the hooks package.

## Files

```
agent/hooks/
├── types.go              # Action, Decision, Result, Executor interface, HookDef, EventGroup, CallbackHook
├── event.go              # Event interface + 17 event structs (SessionStartEvent, PreToolUseEvent, ...)
├── config.go             # HookConfig, LoadHooksConfig: 3-layer merge (user→project→local)
├── matcher.go            # Matcher: match-all / exact / multi-select / regex + if-condition filtering
├── manager.go            # Manager: config load, RegisterBuiltin, RegisterExecutor, Emit dispatch, decision aggregation
├── builtin.go            # LoggingCallback, TimingCallback, ApprovalCallback, CheckpointCallback
├── executor_command.go   # CommandExecutor: stdin JSON → shell, exit code → decision
├── executor_http.go      # HTTPExecutor: POST JSON, SSRF protection (dial-time IP check)
├── executor_mcp.go       # MCPExecutor: MCP tool call with ${...} variable interpolation
├── plugin_bridge.go      # PluginBridgeCallback: hooks.Event → plugin.HookPayload adapter
├── config_test.go
├── matcher_test.go
├── manager_test.go
├── executor_command_test.go
├── executor_http_test.go
├── executor_mcp_test.go
└── plugin_bridge_test.go
```

## 17 Lifecycle Events

| Event | When | Can Block | Blocking Decision |
|-------|------|-----------|-------------------|
| `SessionStart` | Session created/resumed | No | — |
| `SessionEnd` | Session ends | No | — |
| `UserPromptSubmit` | User message received, before LLM | **Yes** | deny/ask blocks prompt |
| `PreToolUse` | Before tool execution | **Yes** | deny/ask blocks tool |
| `PostToolUse` | After tool success | No | — |
| `PostToolUseFailure` | After tool failure | No | — |
| `PostToolBatch` | After all tools in batch complete | **Yes** | deny stops further batches |
| `PermissionRequest` | Permission check triggered | **Yes** | deny/ask controls approval |
| `PermissionDenied` | Permission denied | No | — |
| `SubAgentStart` | SubAgent created | No | — |
| `SubAgentStop` | SubAgent completed | **Yes** | deny blocks result |
| `AgentStop` | Agent response complete | **Yes** | deny blocks reply |
| `AgentError` | LLM API call failed | No | — |
| `PreCompact` | Before context compression | **Yes** | deny blocks compression |
| `PostCompact` | After compression | No | — |
| `CronFired` | Cron job triggered | No | — |
| `WebhookReceived` | External webhook received | No | — |

## Handler Types

### command — Shell Command (most common)

Event payload passed via **stdin (JSON)**. Exit code semantics:
- `0` = allow (success)
- `2` = deny (block operation)
- Other = allow (error logged, not blocking)

```json
{ "type": "command", "command": ".xbot/hooks/lint.sh", "timeout": 30, "async": false }
```

`async: true` runs in background goroutine, does not block agent. Use for logging/notification hooks.

### http — HTTP POST

```json
{ "type": "http", "url": "http://localhost:3000/notify", "timeout": 10 }
```

Event payload as POST body (`Content-Type: application/json`). 2xx = success.

### callback — Go Built-in

```go
type CallbackHook struct {
    Name string
    Fn   func(ctx context.Context, event Event) (*Result, error)
}
```

Used for built-in hooks: logging, timing, approval, masking.

### mcp_tool — MCP Tool Call

```json
{
  "type": "mcp_tool",
  "server": "security",
  "tool": "scan_file",
  "input": { "path": "${tool_input.path}" }
}
```

`${tool_input.path}` auto-interpolated from event payload.

## Configuration

### 3-Layer Merge

```
~/.xbot/hooks.json           (user-level, personal)
  → <project>/.xbot/hooks.json       (project-level, can commit to git)
    → <project>/.xbot/hooks.local.json  (local override, git-ignored)
```

Later layers override earlier. Same event+matcher: handlers appended. Different matcher: new group added.

### File Format

```jsonc
{
  "enable_command_hooks": true,  // MUST be true, command type disabled by default
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Shell",       // tool name pattern, "" = match all
        "hooks": [
          {
            "type": "command",
            "command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make lint || exit 2",
            "if": "Shell(*git commit*)",  // optional: fine-grained filter
            "timeout": 120,
            "async": false
          }
        ]
      }
    ]
  }
}
```

### Key Parameters

| Field | Description |
|-------|-------------|
| `enable_command_hooks` | Global toggle for command type. **Default: false (disabled).** |
| `matcher` | Tool name glob. `""` matches all. For non-tool events, only `""` works. |
| `hooks[].type` | `command` / `http` / `mcp_tool` / `callback` |
| `hooks[].command` | Shell command. Stdin receives event JSON. |
| `hooks[].if` | Fine-grained filter: `ToolName(*pattern*)`. Matches against entire tool input string. |
| `hooks[].async` | Run in background goroutine. Does not block agent. |
| `hooks[].timeout` | Seconds (default: 30). Max handler timeout: 60s. |

### Env Vars Available in Command

| Variable | Value |
|----------|-------|
| `$XBOT_PROJECT_DIR` | Current project root |
| `$XBOT_SESSION_ID` | Current session ID |
| `$XBOT_HOME` | `~/.xbot` |

## Decision Control

| Decision | Meaning |
|----------|---------|
| `allow` | Proceed (default) |
| `deny` | Block operation (highest priority) |
| `ask` | Prompt user for decision |
| `defer` | Let next handler decide |

Priority chain: **deny > defer > ask > allow**. Low-priority deny cannot be overridden by high-priority allow.

## Integration Points (where Emit is called)

```
agent.go:1890      → SessionStart
agent.go:1904      → SessionEnd
engine.go:403      → AgentStop (defer on exit)
engine.go:421      → UserPromptSubmit (before main loop)
engine.go:501      → PostToolBatch (after batch complete)
engine.go:571      → PreToolUse (before tool invocation)
engine.go:619      → PostToolUse (after tool success)
engine_run.go:532  → AgentError (LLM API call failed)
engine_run.go:823  → PreCompact (before context compression)
engine_run.go:870  → PostCompact (after compression)
engine_wire.go:1223 → SubAgentStart
engine_wire.go:1262 → SubAgentStop
```

## Gotchas

### 1. `enable_command_hooks` Defaults to False
Command-type hooks are **silently skipped** unless `"enable_command_hooks": true` is set at the config root.

### 2. Config Requires Restart
Hooks are loaded at CLI startup. Modifying `hooks.json` requires restarting the CLI.

### 3. `if` Matches Tool Input String, Not Params
`if: "Shell(*git commit*)"` works because the entire tool input JSON contains the string `git commit`. `if: "Shell(command=*git commit*)"` does NOT work — `command=` is matched as literal text.

### 4. PATH in Command Hooks
Command hooks run as subprocesses with minimal PATH. Always prepend GOPATH/bin if using Go tools:

```json
"command": "PATH=\"$PATH:$(go env GOPATH)/bin\" make lint || exit 2"
```

### 5. Max 10 Handlers per Event
Total timeout across all handlers: 60s. Excess handlers silently truncated with warning log.

### 6. Async Handlers Use `context.WithoutCancel`
Async goroutines are not canceled when parent context is done, but they still carry the parent's deadline (60s from Emit).

### 7. `.gitignore` for Project Hooks
To track project hooks while ignoring local overrides:

```gitignore
.xbot/*
!.xbot/hooks.json
```
