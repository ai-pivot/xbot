---
title: "Event Bus"
weight: 15
---

The plugin event bus is an in-process pub/sub system for **plugin-to-plugin communication** (`plugin/eventbus.go`). Use it when plugins need to coordinate without knowing each other's identities — e.g. a git plugin publishing branch changes, and a CI plugin subscribing to them.

## The API

```go
// PluginContext exposes it via EventBusPublisher
type EventBusPublisher interface {
	Subscribe(topic string, handler PluginEventHandler) error
	Publish(topic string, data any) error
}
```

```go
type PluginEventHandler func(ctx context.Context, topic string, data any) error
```

Example:

```go
// Subscriber (in Activate)
ctx.Subscribe("git.branch.changed", func(c context.Context, topic string, data any) error {
	branch, _ := data.(string)
	ctx.Logger().Infof("branch changed: %s", branch)
	return nil
})

// Publisher (from a hook handler)
ctx.Publish("git.branch.changed", newBranch)
```

## Permissions

The bus is gated by three permissions (`plugin/permissions.go`):

| Permission | Grants |
|---|---|
| `bus.plugin` | Use the plugin-to-plugin event bus at all |
| `bus.read` | Subscribe |
| `bus.write` | Publish |

Declare all three for full participation: `"permissions": ["bus.plugin", "bus.read", "bus.write"]`.

## Semantics

`PluginEventBus` (`plugin/eventbus.go:15`):

- **Copy-on-read**: `Publish` snapshots the handler list under RLock, then invokes handlers outside the lock — handlers may subscribe/unsubscribe during iteration safely.
- **Panic recovery per handler**: a panicking handler produces an error entry (via `recover`), never crashes the publisher. `Publish` returns a slice of all handler errors.
- **Unsubscribe** compares function pointers (`funcEqual` via `reflect.ValueOf.Pointer`) — keep the same function value you subscribed with.
- Topics are free-form strings; a convention like `domain.action` (`git.branch.changed`) keeps the namespace readable.

## Tenant scoping

`PluginManager.EventBusFor(tenantID)` (`plugin/manager.go:192`) returns a **per-tenant bus** — tenant-scoped plugin events don't leak across users. `tenantID == 0` returns the global bus. Plugin contexts are wired with the appropriate bus by the manager.

## Distinction: lifecycle notifier

Do **not** confuse the event bus with `PluginEventNotifier` (`plugin/events.go`):

| | `PluginEventBus` | `PluginEventNotifier` |
|---|---|---|
| Audience | plugin → plugin | plugin manager → external consumers (CLI, channels) |
| Topics | arbitrary strings | none — single stream |
| Events | plugin-defined data | `PluginEvent{Type, PluginID, ...}` (activated/deactivated/installed/reloaded/error/config_changed) |
| API | `ctx.Subscribe`/`ctx.Publish` | `PluginManager.OnPluginEvent(cb)` |

Use the notifier to observe plugin **lifecycle** (e.g. a UI panel listing plugin states); use the bus for plugin **data exchange**.

## Design notes

- Bus events are in-memory only — nothing is persisted or replayed. If a subscriber is absent at publish time, the event is gone.
- Publishing from a hook handler runs synchronously in the hook's goroutine — keep handlers fast; heavy work belongs in a goroutine the plugin owns.
- `Publish` collects but does not aggregate errors; inspect the returned slice in debug paths.
