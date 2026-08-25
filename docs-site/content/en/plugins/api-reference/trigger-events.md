---
title: "Trigger Events"
weight: 8
---

Reference for plugin trigger mechanisms: activation events (manifest) and widget hook triggers.

## Activation Events

Declared in `plugin.json` under `activation_events` (`plugin/manifest.go` validation):

| Format | Description | Validation |
|--------|-------------|------------|
| `"onStart"` | Activate at startup. | Exact match. |
| `"onTool:<name>"` | Activate when tool `<name>` is first used. | Name after `onTool:` must be non-empty. |
| `"onHook:<event>"` | Activate when a lifecycle hook event fires. | Event must be a valid hook event (see [Hook Events](hook-events/)). |
| `"onCommand:<cmd>"` | Activate when a slash command runs. | Command after `onCommand:` must be non-empty. |

Empty or missing `activation_events` defaults to `["onStart"]`. Invalid event formats fail manifest validation with the message: `unknown activation event format (expected onStart, onTool:<name>, onHook:<event>, or onCommand:<cmd>)`.

## Widget Triggers

`UISlotContribution.Triggers` (script runtime only) — a list of hook matchers that trigger an instant script run for the widget:

```
Format: "EventName:Matcher"
Example: "PostToolUse:Shell*"
```

- `EventName` is a hook event name (e.g. `PostToolUse`).
- `Matcher` is a tool name pattern (`*` wildcards supported; `Shell*` matches all Shell tools).
- **Sync mode**: when `UISlotContribution.Sync` is `true`, triggers run synchronously inline in the hook goroutine — the tool pipeline blocks until the script finishes, so the engine can read output immediately (used by `toolHint` zone plugins via the `PostToolUse` hook). Default is async via `triggerCh`.

## Hook Subscription (Programmatic)

Plugins subscribe programmatically via `PluginContext` (`HookSubscriber`):

```go
ctx.OnPreToolUse("Shell*", handler)     // matcher "" = all tools
ctx.OnPostToolUse("", handler)
ctx.OnEvent(HookCronFired, "", handler) // any event with matcher
ctx.OnAllToolUse(handler)               // BOTH PreToolUse and PostToolUse
ctx.OnError(handler)                    // PostToolUseFailure
ctx.OnGlobalEvent(event, matcher, handler) // session-agnostic, bypasses session isolation
```

See [Hook Events](hook-events/) for the full event list.
