---
title: "Type-as-Contract"
weight: 2
---

The Web plugin system's central idea: **the type system is the contract**. Everything a plugin can do is expressed as compiler-checkable signatures — no runtime `any` magic, no implicit scopes, no borrowed PL vocabulary (`effect`/`fiber`/`epoch`). A disposer is called a `Disposable`.

Four type-system weapons do the work:

1. **Discriminated unions** — contribution shapes (`Contribution` in `web/src/plugin-api/manifest.ts`).
2. **Capability-as-type** — `PluginContext<P>` maps declared permissions to available capability interfaces; undeclared capabilities are `never` (`web/src/plugin-api/context.ts`).
3. **Conditional-type refinement** — a renderer's `matches` condition refines the message type its `render` receives (`web/src/plugin-api/renderer.ts`).
4. **Declaration merging** — `EventMap`, `BackendRPC`, `ToolResultMap`, `PluginExportsMap` are extension points that backend plugins and other packages augment by publishing `.d.ts` type packages.

## 1. Discriminated unions + `satisfies`

Every contribution point is a member of a discriminated union. Plugins use `satisfies PluginManifest` so the compiler validates the shape **and** preserves literal types (needed for later inference of `PluginContext<typeof manifest.permissions>`):

```ts
// web/src/plugin-api/manifest.ts
export type Contribution =
  | ViewContribution
  | CommandContribution
  | MessageRendererContribution
  | ToolbarContribution
  | ContextMenuContribution
  | SettingContribution
  | EventHandlerContribution
  | ThemeContribution

export interface PluginManifest {
  id: string
  name: string
  version: string
  description?: string
  permissions?: readonly Permission[]
  activationDependencies?: readonly string[]
  contributes: readonly Contribution[]
  entry?: string
}
```

```ts
export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'commands', 'rpc'] as const,
  contributes: [
    { kind: 'view', id: 'demo.panel', container: 'right_sidebar',
      title: 'Demo', entry: './panel' },
    { kind: 'command', id: 'demo.hello', title: 'Hello', keybinding: 'ctrl+shift+h' },
  ] as const,
} satisfies PluginManifest   // wrong kind / missing field → compile error
```

- **Exhaustiveness**: each element of `contributes` must match one member of the union; `switch (c.kind)` over contributions warns on unhandled kinds — type-enforced synchronization as the system evolves.
- **Runtime**: the manifest is serialized to JSON and served by the backend, but the **authoritative types are compile-time**.

## 2. Capability-as-type — permissions determine the ctx shape

```ts
// web/src/plugin-api/context.ts
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
  readonly meta: PluginMeta
  readonly contributes: ContributionAPI
}
```

`PluginContext<P>` is a type function: for each capability `K`, the interface is available **only when `K ∈ P`** — otherwise it is `never`, so accessing an undeclared capability is a compile error, not a runtime error:

```ts
export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  ctx.events.on('message.committed', ev => { /* OK: events declared */ })
  ctx.rpc.call('session.get', { chatID: 'x' })   // OK: rpc declared

  // ✗ compile error: 'state' not in permissions → ctx.state is never
  // ctx.state.getSession()
  // ✗ compile error: 'session.get' params must be { chatID: string }
  // void ctx.rpc.call('session.get', { chatID: 42 })
}
```

The runtime mirrors this: `buildContext(permissions, svc)` in `web/src/plugin-runtime/context.ts` only injects declared capabilities — undeclared ones are absent on the object (defense in depth, authority at compile time).

Why this is a "real type design" (and not the cordis-style pretense):

- Permission → capability mapping is an **explicit type function**, not a runtime Proxy whitelist.
- Plugin authors **cannot** use an undeclared capability — not "it throws", but "it does not compile".
- Every capability method has a full signature (params, return, payload) with no `any` leakage.

## 3. Conditional-type refinement — match what you can handle

```ts
// web/src/plugin-api/renderer.ts
export type MatchedMessage<M extends Matcher> =
  M extends { tool: infer T extends keyof ToolResultMap }
    ? SafeMessage & { tool: { name: T; result: ToolResultMap[T] } }
    : M extends { uiMode: string }
      ? SafeMessage & { tool: { name: string; uiMode: string; result: unknown } }
      : M extends { role: 'assistant' }
        ? SafeAssistantMessage
        : M extends { role: 'user' }
          ? SafeUserMessage
          : SafeMessage

export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  priority: number
  matches: M
  render: (msg: MatchedMessage<M>, ctx: RenderContext) => ReactNode | null
}
```

The `matches` condition is both the runtime filter and the type source: "what you match is what you can safely handle". A renderer declaring `matches: { tool: 'shell' }` receives a message whose `tool.result` is typed as `{ command: string; output: string; exitCode: number }` — with zero casts. See [Message Renderer](message-renderer.md) for the full dispatch chain.

## 4. Declaration merging — extension points

Four empty/decoratable interfaces are the ecosystem's extension points. Backend plugins publish `.d.ts` type packages that merge into them; frontend plugins importing the package automatically gain precise types:

| Interface | Extended by | Used by |
|---|---|---|
| `EventMap` | backend plugins, host | `ctx.events.on(name, …)` payload inference |
| `BackendRPC` | backend plugins (e.g. `xbot.git-fancy` adds `git.status` etc.) | `ctx.rpc.call(method, …)` param/result inference |
| `ToolResultMap` | backend plugins with custom tools | message-renderer matcher refinement |
| `PluginExportsMap` | any plugin publishing public APIs | `ctx.plugins.get(id)` return type |

Example (from `web/src/plugin-api/rpc.ts` — the real table already includes the git-fancy extension):

```ts
export interface BackendRPC {
  'session.get': { params: { chatID: string }; result: SessionDetail }
  // …
  // ---- xbot.git-fancy: fancy Git plugin data source ----
  'git.status': {
    params: { channel: string; chatID: string }
    result: { branch: string; repo_name: string; changes: Array<{ path: string; status: string; added: number; deleted: number }>; ahead: number; behind: number; commit_hash: string; commit_msg: string; is_repo: boolean }
  }
}
```

Empty interfaces (e.g. `PluginExportsMap {}`) are legal declaration-merging extension points — `eslint-disable no-empty-object-type` marks them as such.
