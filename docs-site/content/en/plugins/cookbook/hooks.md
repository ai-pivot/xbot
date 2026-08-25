---
title: "Hooks"
weight: 13
---

Hooks let plugins observe and veto the agent's lifecycle: before/after tool use, prompt submission, session boundaries, compaction, cron fires, and webhooks. The types live in `plugin/plugin.go` (HookEvent/HookDecision/HookResult/HookPayload); the dispatch bridge is `plugin/adapter_hook.go`.

## The complete event catalogue

`plugin/plugin.go:623`:

| Event | Fires |
|---|---|
| `PreToolUse` | Before a tool executes (can deny) |
| `PostToolUse` | After a tool succeeds |
| `PostToolUseFailure` | After a tool fails |
| `UserPromptSubmit` | When the user submits a prompt |
| `AgentStop` | When the agent loop terminates |
| `SessionStart` / `SessionEnd` | Session lifecycle |
| `SubAgentStart` / `SubAgentStop` | Sub-agent launch / completion |
| `PreCompact` / `PostCompact` | Before/after history compaction |
| `CronFired` | A scheduled cron job triggers |
| `WebhookReceived` | An inbound webhook arrives |

## Subscribing

```go
// matcher "" = all tools; "Shell*" = wildcard prefix
ctx.OnPreToolUse("Shell*", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
	if strings.Contains(payload.ToolInput, "rm -rf /") {
		return &plugin.HookResult{Decision: plugin.DecisionDeny,
			Message: "destructive command blocked"}, nil
	}
	return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
})

ctx.OnPostToolUse("Shell*", handler)   // observe after success
ctx.OnEvent(plugin.HookAgentStop, "", handler)  // any event by name
ctx.OnAllToolUse(handler)              // pre+post for all tools
ctx.OnError(handler)                   // tool failures
```

Declaration in the manifest (drives activation + discovery):

```json
"contributes": { "hooks": [ { "event": "PostToolUse", "matcher": "Shell*" } ] }
```

## The payload

`HookPayload` (`plugin/plugin.go:674`):

```go
type HookPayload struct {
	Event         HookEvent      `json:"event"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     string         `json:"tool_input,omitempty"`
	ToolOutput    string         `json:"tool_output,omitempty"`     // PostToolUse only
	ToolElapsedMs int64          `json:"tool_elapsed_ms,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	Channel       string         `json:"channel,omitempty"`
	ChatID        string         `json:"chat_id,omitempty"`
	UserID        string         `json:"user_id,omitempty"`
	TenantID      int64          `json:"tenant_id,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}
```

⚠️ **`ToolOutput` is truncated to 8KB** — don't rely on it for full file content. `Extra` carries session context (`model`, `max_context`, `prompt_tokens`, `comp_tokens`) and per-event data (e.g. shell stdout for the git-pr-status `detectBranch` example).

## Decisions

`HookDecision` (`plugin/plugin.go:655`): `allow`, `deny`, `ask`, `defer`. Priority in the bridge (`adapter_hook.go decisionWeight`): **`deny > defer > ask > allow`** — a low-priority layer's deny cannot be overridden by a high-priority allow.

```go
type HookResult struct {
	Decision HookDecision   `json:"decision"`
	Message  string         `json:"message,omitempty"` // explanation for deny/ask
	Data     map[string]any `json:"data,omitempty"`
}
```

Return `(nil, nil)` to abstain (equivalent to defer/allow for observation-only handlers).

## SDK shortcuts

`plugin/sdk.go`:

```go
handler := plugin.AllowHook()              // always allow
handler := plugin.DenyHook("not allowed")  // always deny with message
handler := plugin.LogHook(logger, "event") // log + allow
```

## Stdio plugin hooks

The `hook` method receives `{event, toolName, toolInput, sessionId, channel, chatId}` and responds `{"hook_result":{"decision":"allow"}}` — see `plugin/examples/grpc-python/main.py handle_hook` and `protocol.HookParams`/`protocol.HookResult`.

## Script plugin hooks

Script plugins subscribe via `contributes.ui[].triggers` (`"PostToolUse:Shell*"`) — the runtime re-runs the script and injects the payload as `XBOT_HOOK_EVENT`/`XBOT_TOOL_NAME`/`XBOT_TOOL_INPUT`/`XBOT_TOOL_OUTPUT` env vars. Script triggers are **global** (session-agnostic) by design (`registerGlobalHook`, `plugin/script_runtime.go:608`).

## The toolHint zone + synchronous execution

A `ui` slot with `"sync": true` (the `toolHint` zone) runs the script **inline** inside the hook call. `GetHintContent()` returns the stripped output immediately after — this is how `file-diff` attaches a live diff to `ToolProgress.ToolHints`:

1. `PostToolUse:FileReplace*` fires.
2. `subscribeTrigger`'s `triggerFn` sees `syncWidgets` non-empty → runs `file-diff.sh` synchronously.
3. Output stripped of `md|`/`diff|` prefix → stored as `hintContent`.
4. Engine calls `GetToolHints()` (which **consumes/clears** the hint) → attaches to the tool result.

## Gotchas

- **Matcher semantics**: `""` matches all tools; `"Shell*"` is prefix-wildcard (`matchToolName`). Match exactly what you need — a bare `PostToolUse` with `""` fires on every tool in every session.
- **Session isolation**: native plugin hooks registered via `OnEvent` are session-scoped by the bridge unless registered global (`OnGlobalEvent`). Script triggers are always global.
- **Concurrency**: hook handlers run on agent goroutines — guard shared state with mutexes (see `GitPRPlugin.mu`).
- **`GetToolHints()` consumes** — a second read returns nothing; stale hints never attach to the next tool.
- Max 10 handlers per event, 60s total timeout across handlers; excess handlers are silently truncated with a warning log (`agent/hooks/manager.go`).
