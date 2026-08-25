---
title: "PluginContext API"
weight: 4
---

`PluginContext<P>` is a **type function over permissions**: declared capabilities are available as fully-typed interfaces; undeclared ones are `never`. Defined in `web/src/plugin-api/context.ts`.

## The type

```ts
interface PermissionAPI {
  events: EventsAPI
  commands: CommandsAPI
  rpc: RPCAPI
  state: StateAPI
  ui: UIAPI
  plugins: PluginsAPI
  config: ConfigAPI
}

export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & {
  /** Runtime metadata (available to ALL plugins). */
  readonly meta: PluginMeta
  /** Dynamic contribution registration (available to ALL plugins). */
  readonly contributes: ContributionAPI
}
```

Two members are **always** present regardless of permissions: `meta` and `contributes`.

## Runtime construction

`buildContext(permissions, svc)` in `web/src/plugin-runtime/context.ts` builds the runtime object that mirrors the type shape — only declared capabilities are injected; undeclared ones are absent on the object (defense in depth, authority at compile time):

```ts
export function buildContext(
  permissions: readonly Permission[],
  svc: ContextServices,
): PluginContext<readonly Permission[]> {
  const ctx: Record<string, unknown> = {
    meta: svc.meta,
    contributes: {
      register: svc.registerContribution,
      registerAll: (contributions) => {
        const disposables = contributions.map((c) => svc.registerContribution(c))
        return () => { for (const d of disposables.reverse()) d() }
      },
    } satisfies ContributionAPI,
  }
  const has = (p: Permission) => permissions.includes(p)
  if (has('events')) ctx.events = svc.events
  if (has('commands')) ctx.commands = svc.commands
  if (has('rpc')) ctx.rpc = svc.rpc
  if (has('state')) ctx.state = svc.state
  if (has('ui')) ctx.ui = svc.ui
  if (has('plugins')) ctx.plugins = svc.plugins
  if (has('config')) ctx.config = svc.config
  return ctx as PluginContext<readonly Permission[]>
}
```

The runtime instantiates all services once in the `PluginRuntime` constructor and shares them across plugins; `config` is the exception — each plugin receives its own `PluginConfigService.forPlugin(pluginId)` binding (see `web/src/plugin-runtime/index.ts` `activateModule`).

## Capability reference

| Capability | Permission | API surface | Doc |
|---|---|---|---|
| `ctx.events` | `events` | `on` / `once` over `EventMap` | [Events](events.md) |
| `ctx.commands` | `commands` | `register` / `execute` / `registerKeybinding` | (this page) |
| `ctx.rpc` | `rpc` | `call` / `notify` over `BackendRPC` | [RPC](rpc.md) |
| `ctx.state` | `state` | `getSession` / `getMessages` / `getPlugins` | (this page) |
| `ctx.ui` | `ui` | `showToast` / `openPanel` / `openViewTab` / `openFileTab` / `openDiffTab` | [Editor View API](editor-view.md) |
| `ctx.plugins` | `plugins` | `get` / `require` / `onActivated` / `onDeactivated` | [Interop](interop.md) |
| `ctx.config` | `config` | `get` / `set` / `onConfigChange` | (this page) |
| `ctx.meta` | — (always) | `{ id, version }` | — |
| `ctx.contributes` | — (always) | `register` / `registerAll` | [Manifest](manifest.md) |

## CommandsAPI

```ts
export interface CommandsAPI {
  /** Register a command handler; returns a disposable for unload. */
  register(id: string, handler: (args: unknown) => void | Promise<void>): Disposable
  /** Execute a registered command. */
  execute(id: string, args?: unknown): Promise<void>
  /** Register a keybinding (syntax identical to the contribution point). */
  registerKeybinding(keybinding: string, commandId: string): Disposable
}
```

Backed by `CommandRegistry` (`web/src/plugin-runtime/commands.ts`): duplicate registration overwrites with a warning; the host dispatches `KeyboardEvent`s through `dispatchKey` (normalized by `keybindingFromEvent` to strings like `ctrl+shift+h`); unloading a plugin removes all its commands and keybindings via `removePlugin`.

## StateAPI — read-only snapshots

```ts
export interface StateAPI {
  /** Current session summary (read-only snapshot). */
  getSession(): SessionSummary | null
  /** Paged read-only message list (sanitized copies). */
  getMessages(options?: { limit?: number; before?: number }): readonly SafeMessage[]
  /** All plugin states. */
  getPlugins(): readonly { id: string; version: string; enabled: boolean }[]
}
```

The implementation (`web/src/plugin-runtime/state.ts`) returns `structuredClone`d data — a plugin can never hold internal object references, and thus cannot observe subsequent mutations. Messages pass through `toSafeMessage` (`web/src/plugin-runtime/sanitize.ts`), which keeps only public fields (`id`/`turnID`/`role`/`content`/`createdAt`) and drops internal state (`persisted`/`eventSeq`/`dbID`/…).

## ConfigAPI

```ts
export interface ConfigAPI {
  /** Read merged configuration (defaults + user overrides). */
  get(): Promise<Record<string, unknown>>
  /** Set one key and persist. Backend broadcasts the change to all onConfigChange subscribers. */
  set(key: string, value: unknown): Promise<void>
  /** Subscribe to config changes (including those from other clients/tabs). Returns a disposable. */
  onConfigChange(handler: (config: Record<string, unknown>) => void): Disposable
}
```

Flow (`web/src/plugin-runtime/config.ts` + backend): `get()`/`set()` go through the `plugin.get_config` / `plugin.set_config` RPC entries; when a change lands, the backend broadcasts `web_plugin_config_changed` over WS, `usePluginRuntimeHost` calls `runtime.notifyPluginConfigChanged(pluginId, value)`, and `PluginConfigService` dispatches to that plugin's `onConfigChange` subscribers. The host settings panel does **not** use this API — it renders forms directly from the `plugin.get_config` schema.

## Example: permission-driven surface

```ts
import type { PluginContext, Disposable } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'ui'] as const,
  contributes: [],
} satisfies import('@xbot/plugin-api').PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  // OK — events declared
  const off = ctx.events.on('turn.ended', (ev) => {
    // OK — ui declared
    ctx.ui.showToast(`turn ${ev.turnID}: ${ev.outcome}`)
  })
  // ✗ compile error — 'rpc' not declared → ctx.rpc is never
  // void ctx.rpc.call('session.list', {})
  return () => off()
}
```
