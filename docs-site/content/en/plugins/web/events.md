---
title: "Typed Event Bus"
weight: 5
---

The typed event bus uses **indexed access over an event table**: subscribing to `'message.committed'` infers the payload type `{ turnID: number; message: SafeMessage }` with zero casts. Defined in `web/src/plugin-api/events.ts`.

## EventMap

```ts
/** turn trigger. */
export type TurnTrigger = 'user' | 'notification' | 'resume'

/** Session summary (sanitized copy). */
export interface SessionSummary {
  chatID: string
  title: string
  model: string
  busy: boolean
  maxContext: number
  tokenUsage: { prompt: number; completion: number }
}

/** Core event table: backend/other plugins extend it via declaration merging. */
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
```

## EventsAPI

```ts
export interface EventsAPI {
  /** Subscribe; returns a disposable. Payload type is inferred from the event name. */
  on<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
  /** One-shot subscription. */
  once<K extends keyof EventMap>(name: K, handler: (payload: EventMap[K]) => void): Disposable
}
```

`SafeMessage` (`web/src/plugin-api/safe.ts`) is the sanitized public message type — plugins never touch internal fields (`persisted`/`eventSeq`/`dbID`/…):

```ts
export interface SafeMessage {
  id: number
  turnID: number
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  createdAt: string
}

export interface SafeAssistantMessage extends SafeMessage {
  role: 'assistant'
  iterations?: readonly SafeIteration[]
  reasoning?: string
}

export interface SafeUserMessage extends SafeMessage {
  role: 'user'
  /** Notification-injected message (🔔). */
  isNotification?: boolean
}
```

## Runtime implementation

`PluginEventBus` (`web/src/plugin-runtime/events.ts`) implements `EventsAPI`:

- **Wide-type internal storage**: handlers are stored as `(payload: never) => void` (erased). The generics live only on the public API surface — strongly typed outward, loosely stored inward, avoiding covariance traps.
- **Per-plugin attribution**: the internal `subscribe(pluginId, name, handler)` binds a subscription to a plugin; `unsubscribePlugin(pluginId)` removes all of a plugin's subscriptions on unload (hot reload requirement).
- **Crash isolation**: a handler throwing does not break other subscribers — the error is logged and delivery continues. The subscriber set is copied before iteration, so handlers may unsubscribe/re-enter safely.
- **`once`** wraps `subscribe` with a self-disposing wrapper.

## Backend event bridge

The backend pushes agent lifecycle events to the frontend as `web_plugin_event` WS messages. `PluginRuntimeBootstrap` (`web/src/plugin-runtime/usePluginRuntimeHost.ts`) parses `{ name, payload }` and re-emits through the bus:

```ts
} else if (msg.type === 'web_plugin_event') {
  const evt = JSON.parse(msg.content ?? '{}') as { name?: string; payload?: unknown }
  if (evt.name) {
    // Payload is runtime JSON; type safety is guaranteed by EventMap on the
    // subscribing side. Delivered with unknown-erased cast.
    runtime.events.emit(evt.name as keyof import('@/plugin-api').EventMap, evt.payload as never)
  }
}
```

Plugins can only **subscribe** — publishing is host-internal (`emit` is not exposed on `EventsAPI`).

## Extension via declaration merging

A backend plugin publishes a `.d.ts` type package that merges new events into `EventMap`; frontend plugins importing the package automatically get payload types for the custom events:

```ts
// plugin-api extension package (e.g. @xbot/plugin-myplugin-types)
declare module '@xbot/plugin-api' {
  interface EventMap {
    'myplugin.data.arrived': { batch: number; rows: readonly string[] }
  }
}
```

## Example

```ts
export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  const disposables = [
    ctx.events.on('turn.started', (ev) => {
      // ev: { turnID: number; trigger: 'user' | 'notification' | 'resume' }
      console.log(`turn ${ev.turnID} started by ${ev.trigger}`)
    }),
    ctx.events.once('context.compressed', (ev) => {
      // ev: { beforeTokens: number; afterTokens: number }
      console.log(`compressed ${ev.beforeTokens} → ${ev.afterTokens}`)
    }),
  ]
  return () => disposables.forEach((d) => d())
}
```
