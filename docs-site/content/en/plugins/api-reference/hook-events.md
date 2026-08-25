---
title: "Hook Events"
weight: 5
---

Reference for lifecycle hook events and the `HookPayload` fields (`plugin/plugin.go`).

## HookEvent Constants

```go
type HookEvent string
```

| Constant | String Value | When It Fires |
|----------|-------------|---------------|
| `HookPreToolUse` | `"PreToolUse"` | Before a tool is executed. |
| `HookPostToolUse` | `"PostToolUse"` | After a tool execution succeeds. |
| `HookPostToolUseError` | `"PostToolUseFailure"` | When a tool execution fails. |
| `HookUserPromptSubmit` | `"UserPromptSubmit"` | When the user submits a prompt. |
| `HookAgentStop` | `"AgentStop"` | When the agent loop terminates. |
| `HookSessionStart` | `"SessionStart"` | At the beginning of a new session. |
| `HookSessionEnd` | `"SessionEnd"` | When a session concludes. |
| `HookSubAgentStart` | `"SubAgentStart"` | Before a sub-agent is launched. |
| `HookSubAgentStop` | `"SubAgentStop"` | After a sub-agent completes. |
| `HookPreCompact` | `"PreCompact"` | Before message history compaction. |
| `HookPostCompact` | `"PostCompact"` | After message history compaction. |
| `HookCronFired` | `"CronFired"` | When a scheduled cron job triggers. |
| `HookWebhookReceived` | `"WebhookReceived"` | When an inbound webhook arrives. |

`IsValidHookEvent(name)` validates event names for manifest `contributes.hooks` entries and `onHook:` activation events.

## HookPayload

```go
type HookPayload struct {
    Event         HookEvent      `json:"event"`
    ToolName      string         `json:"tool_name,omitempty"`
    ToolInput     string         `json:"tool_input,omitempty"`
    ToolOutput    string         `json:"tool_output,omitempty"`     // tool execution result (PostToolUse only)
    ToolElapsedMs int64          `json:"tool_elapsed_ms,omitempty"` // tool execution duration in ms
    SessionID     string         `json:"session_id,omitempty"`
    Channel       string         `json:"channel,omitempty"`
    ChatID        string         `json:"chat_id,omitempty"`
    UserID        string         `json:"user_id,omitempty"`
    TenantID      int64          `json:"tenant_id,omitempty"`
    Extra         map[string]any `json:"extra,omitempty"`
}
```

### Important Notes

- `ToolOutput` is **truncated to 8KB** in `HookPayload` — do not rely on it for full file content. Plugins needing full output should use dedicated tool result channels.
- `Extra` carries session context injected by the engine (model name, max context, token usage) — see [Environment Variables](environment-variables/).
- Field availability depends on the event: `ToolName`/`ToolInput`/`ToolElapsedMs` are tool events only; `ToolOutput` is `PostToolUse` only.

## HookHandler, HookResult, HookDecision

```go
type HookHandler func(ctx context.Context, payload *HookPayload) (*HookResult, error)

type HookResult struct {
    Decision HookDecision   `json:"decision"`
    Message  string         `json:"message,omitempty"` // explanation for deny/ask
    Data     map[string]any `json:"data,omitempty"`
}
```

| Decision | String Value | Meaning |
|----------|-------------|---------|
| `DecisionAllow` | `"allow"` | Permit the operation to proceed. |
| `DecisionDeny` | `"deny"` | Block the operation, optionally with a reason. |
| `DecisionAsk` | `"ask"` | Prompt the user for confirmation before proceeding. |
| `DecisionDefer` | `"defer"` | Defer the decision to the next handler in the chain. |

**Decision priority: `deny > defer > ask > allow`.** A low-priority-layer deny cannot be overridden by a high-priority allow.

## SDK Hook Helpers

From `plugin/sdk.go`:

```go
func DenyHook(msg string) HookHandler     // always denies with the given message
func AllowHook() HookHandler              // always allows
func LogHook(logger Logger, msg string) HookHandler // logs the event and allows
```

## Handler Limits

- Max 10 handlers per event.
- Total timeout 60s per event dispatch.
- Excess handlers are silently truncated with a warning log.
