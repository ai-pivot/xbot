---
title: "Event Types"
weight: 12
---

Reference for the three event systems plugins interact with: plugin lifecycle events (Go), the typed event bus (`EventMap`, web), and lifecycle hook events (see [Hook Events](hook-events/)).

## Plugin Lifecycle Events (Go)

`plugin/events.go` — `PluginEventNotifier` is a lightweight, topic-free mechanism for external consumers (CLI, channels) that need lifecycle notifications. Distinct from the topic-based `PluginEventBus` used for plugin-to-plugin communication.

```go
type PluginEventType string

const (
    PluginEventActivated     PluginEventType = "activated"
    PluginEventDeactivated   PluginEventType = "deactivated"
    PluginEventInstalled     PluginEventType = "installed"
    PluginEventUninstalled   PluginEventType = "uninstalled"
    PluginEventReloaded      PluginEventType = "reloaded"
    PluginEventError         PluginEventType = "error"
    PluginEventConfigChanged PluginEventType = "config_changed"
)
```

```go
type PluginEvent struct {
    Type      PluginEventType
    PluginID  string
    Timestamp time.Time
    Error     error
    Data      any // optional; recommended: map[string]any
}

type PluginEventCallback func(event PluginEvent)
```

### PluginEventNotifier

```go
func NewPluginEventNotifier() *PluginEventNotifier
func (n *PluginEventNotifier) Subscribe(callback PluginEventCallback) error
func (n *PluginEventNotifier) Unsubscribe(callback PluginEventCallback) error
func (n *PluginEventNotifier) Notify(event PluginEvent)
```

- `Subscribe` rejects nil callbacks.
- `Unsubscribe` matches by function pointer comparison; errors if not found.
- `Notify` uses copy-on-read so callbacks can safely subscribe/unsubscribe during iteration; each callback is wrapped in panic recovery — a panicking callback does not affect others.

## Typed Event Bus (Web, `ctx.events`)

`web/src/plugin-api/events.ts` — the core event table. Backend plugins and the host publish type packages that extend `EventMap` via declaration merging, so frontend plugins subscribing to custom events automatically get payload types.

```ts
export interface EventMap {
  'message.committed': { turnID: number; message: SafeMessage }
  'message.streaming': { turnID: number; iteration: number; content: string }
  'turn.started': { turnID: number; trigger: TurnTrigger }
  'turn.ended': { turnID: number; outcome: 'ok' | 'cancelled' | 'error' }
  'session.switched': { session: SessionSummary }
  'progress.iteration': { iteration: number; tools: readonly ToolProgress[] }
  'context.compressed': { beforeTokens: number; afterTokens: number }
  'command.executed': { commandId: string; args: unknown }
}

export interface EventsAPI {
  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
}
```

### Supporting Types

```ts
export interface SessionSummary {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  tokenUsage: { prompt: number; completion: number }
}

export type TurnTrigger = 'user' | 'notification' | 'resume'
```

Requires the `events` permission (frontend) — `ctx.events` is only available when `events` is declared.

## Plugin-to-Plugin Event Bus (Go)

`EventBusPublisher` on `PluginContext`:

```go
Subscribe(topic string, handler PluginEventHandler) error // "bus.plugin" + "bus.read"
Publish(topic string, data any) error                     // "bus.plugin" + "bus.write"
```

Topics are strings; no schema enforcement. The bus is per-tenant (resolved via `PluginManager.EventBusFor(tenantID)` when session metadata is set).

## Lifecycle Hook Events

The 13 `HookEvent` values (`PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit`, `AgentStop`, `SessionStart`, `SessionEnd`, `SubAgentStart`, `SubAgentStop`, `PreCompact`, `PostCompact`, `CronFired`, `WebhookReceived`) are documented in [Hook Events](hook-events/).
