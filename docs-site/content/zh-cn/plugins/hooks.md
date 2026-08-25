---
title: "Hook 系统"
weight: 9
---

> 本文档的英文版本包含完整的代码示例和 API 参考。请参阅 [English version](../../en/plugins/hooks/) 获取完整内容。

本文档提供 Hook 系统 的中文概览。详细的 API 参考和代码示例请参阅英文版本。


Hooks allow plugins to intercept and modify xbot's behavior at lifecycle points — before/after tool execution, on user prompts, session events, and errors.

## Hook Events

| Event | Method | Description |
|-------|--------|-------------|
| PreToolUse | `OnPreToolUse(matcher, handler)` | Before a tool executes. Can deny, defer, or modify |
| PostToolUse | `OnPostToolUse(matcher, handler)` | After a tool executes. Can modify results |
| UserPrompt | `OnUserPrompt(handler)` | When user sends a message |
| AgentStop | `OnAgentStop(handler)` | When the agent stops |
| SessionStart | `OnSessionStart(handler)` | When a session starts |
| SessionEnd | `OnSessionEnd(handler)` | When a session ends |
| AllToolUse | `OnAllToolUse(handler)` | Before any tool (no matcher) |
| OnError | `OnError(handler)` | When an error occurs |

## HookHandler

```go
type HookHandler func(ctx context.Context, payload *HookPayload) (*HookResult, error)
```

## HookPayload

```go
type HookPayload struct {
    Event       HookEvent    // The event type
    ToolName    string       // Tool name (for tool hooks)
    ToolInput   string       // Tool input (for PreToolUse)
    ToolOutput  string       // Tool output (for PostToolUse, truncated to 8KB)
    SessionID   string       // Session identifier
    WorkDir     string       // Working directory
    Channel     string       // Channel name (cli, web, feishu)
    Error       error        // Error (for OnError)
    Extra       map[string]any // Extra context (model, token usage, etc.)
}
```

## HookResult

```go
type HookResult struct {
    Decision HookDecision  // allow, deny, defer, ask
    Message  string        // Message shown to user (for deny/ask)
    Data     any           // Modified data (for PostToolUse)
}
```

### Decisions

| Decision | Description |
|----------|-------------|
| `DecisionAllow` | Allow the action to proceed |
| `DecisionDeny` | Block the action, show message to user |
| `DecisionDefer` | Defer to the next hook in the chain |
| `DecisionAsk` | Ask the user for confirmation |

**Decision priority**: `deny > defer > ask > allow`. A low-priority layer's deny cannot be overridden by a high-priority allow.

## Matcher

The `matcher` parameter for tool hooks is a tool name pattern:

```go
// Match all tools
ctx.OnPreToolUse("", handler)

// Match specific tool
ctx.OnPreToolUse("Shell", handler)

// Match tools by prefix
ctx.OnPreToolUse("Read", handler)  // Matches "Read"
```

## Usage Examples

### Deny Shell commands containing "rm"

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    return ctx.OnPreToolUse("Shell", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
        if strings.Contains(payload.ToolInput, "rm ") {
            return &plugin.HookResult{
                Decision: plugin.DecisionDeny,
                Message:  "rm commands are blocked by safety policy",
            }, nil
        }
        return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
    })
}
```

### Log all tool usage

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    return ctx.OnPostToolUse("", func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
        logger := ctx.Logger()
        logger.Info("Tool executed",
            plugin.Field{Key: "tool", Value: payload.ToolName},
            plugin.Field{Key: "workDir", Value: payload.WorkDir},
        )
        return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
    })
}
```

### Session lifecycle tracking

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    ctx.OnSessionStart(func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
        ctx.Logger().Info("Session started", plugin.Field{Key: "workDir", Value: payload.WorkDir})
        return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
    })
    
    ctx.OnSessionEnd(func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
        ctx.Logger().Info("Session ended")
        return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
    })
    
    return nil
}
```

## SDK Helpers

```go
// Pre-built hook handlers
plugin.DenyHook("blocked")    // Always deny with message
plugin.AllowHook()             // Always allow
plugin.LogHook(logger, "msg") // Log and allow
```

## Manifest Declaration

Hooks can also be declared in `plugin.json` (for documentation purposes):

```json
{
  "contributes": {
    "hooks": [
      {"event": "PreToolUse", "matcher": "Shell"},
      {"event": "PostToolUse", "matcher": ""}
    ]
  }
}
```

## Script Plugin Hooks

Script plugins receive hooks via environment variables:

```bash
#!/bin/bash
# Access hook data via environment variables
echo "Tool: $XBOT_HOOK_TOOL_NAME"
echo "Input: $XBOT_HOOK_TOOL_INPUT"
```

## See Also

- [PluginContext API](./plugin-context/) — HookSubscriber interface
- [Permissions](./permissions/) — `hooks.register` permission
- [API Reference: Hook Events](./api-reference/hook-events/) — Complete event reference
- [API Reference: HookPayload](./api-reference/hook-payload/) — Payload fields
