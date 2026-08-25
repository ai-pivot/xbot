---
title: "Web Plugins"
weight: 10
---

Web plugins are TypeScript ESM modules loaded directly by the frontend plugin runtime — **no server-side VM, no sandbox**. The design is documented in `docs/agent/web-plugin-system.md` ("Type-as-Contract"); the type package lives in `web/src/plugin-api/`; the runtime in `web/src/plugin-runtime/`; production examples are `plugins/xbot-git-fancy/` and the built-in plugin manager (`web/src/plugins/manager/`).

## Architecture

```
┌─ Web frontend ──────────────────────────────┐
│  PluginRuntime: loader / registry / events  │
│  rpc / state / ui / ViewSlot / plugins      │
│  Plugin module imports the host React tree  │
│  Crash isolation = ErrorBoundary            │
└──────────┬──────────────────────────────────┘
           │ web_plugin_* messages (WS/SSE)
┌──────────▼──────────────────────────────────┐
│ Go backend: manifest push + EventBridge +   │
│ WebPluginRPC routing + static file serving  │
└─────────────────────────────────────────────┘
```

Trust model (same as VSCode extensions): installing a plugin = trusting it. The type system enforces **contract correctness**, not security.

## The manifest (type-checked at compile time)

```ts
import type { PluginManifest } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.git-fancy',
  name: 'Git Fancy',
  version: '0.3.2',
  permissions: ['rpc', 'ui', 'events'] as const,
  contributes: [
    { kind: 'view', id: 'xbot.git-fancy.panel', container: 'right_sidebar',
      title: 'Git', icon: 'git-branch', entry: 'index.js' },
    { kind: 'view', id: 'xbot.git-fancy.commit', container: 'main',
      title: 'Commit', icon: 'git-commit-horizontal', entry: 'commit.js',
      dynamic: true },
  ] as const,
} satisfies PluginManifest
```

The `Contribution` union (`web/src/plugin-api/manifest.ts`) covers `ViewContribution`, `CommandContribution`, `MessageRendererContribution`, `ToolbarContribution`, `ContextMenuContribution`, `SettingContribution`, `EventHandlerContribution`, `ThemeContribution`. `satisfies` gives compile-time shape checking while preserving literal types.

- **`container: 'right_sidebar'`** — panel tab in the right sidebar.
- **`container: 'main'`** — a main-area editor tab (VSCode editor semantics), full-width.
- **`dynamic: true`** — dynamic views are excluded from the activity bar/sidebar/layout registry; they open only via `ctx.ui.openViewTab(...)`.

## Capability-as-type: permissions shape the context

`PluginContext<P>` (`web/src/plugin-api/context.ts`) is a type function over the declared permissions:

```ts
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config'
export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & { readonly meta: PluginMeta; readonly contributes: ContributionAPI }
```

Accessing an undeclared capability is a **compile error** (`never`). At runtime `buildContext` injects strictly by declared permission — undeclared APIs are `undefined`.

## Core APIs

### RPC (typed method table)

`ctx.rpc.call(method, params)` is checked against `BackendRPC` (`web/src/plugin-api/rpc.ts`) — method names, params, and results:

```ts
const status = await ctx.rpc.call('git.status', { channel: 'web', chatID })
// status: { branch: string; changes: ...; is_repo: boolean }  ← inferred

ctx.rpc.notify('plugin.set_config', { id, key, value })  // fire-and-forget
```

Backend plugins extend the table via **declaration merging** — they publish a `.d.ts` that augments `BackendRPC`, and implement the methods in `web_plugin_rpc` (see [Stdio Plugins](../stdio-plugins/)).

### Events (typed by EventMap)

```ts
ctx.events.on('message.committed', (p) => { /* p: { turnID; message: SafeMessage } */ })
ctx.events.on('turn.started', (p) => { /* p: { turnID; trigger } */ })
ctx.events.once('session.switched', (p) => { /* p: { session: SessionSummary } */ })
```

Core `EventMap` (`web/src/plugin-api/events.ts`): `message.committed`, `message.streaming`, `turn.started`, `turn.ended`, `session.switched`, `progress.iteration`, `context.compressed`, `command.executed`. Extendable by declaration merging too.

### UI (VSCode-style semantic operations, no DOM)

```ts
ctx.ui.showToast('Done', 'success')                       // toast
ctx.ui.openPanel('right_sidebar')                         // open a container
ctx.ui.openViewTab({ viewId: 'xbot.git-fancy.commit',     // editor tab
  title: 'abc1234', key: 'commit-abc1234', params: { hash: 'abc1234' } })
const editor = ctx.ui.openFileTab('/path/to/file.go', { line: 42, highlight: { startLine: 40, endLine: 50 } })
editor.revealLine(100, { center: true }); editor.setSelection(1, 0, 1, 10)
const diff = ctx.ui.openDiffTab({ title: 'a.go', original: oldText, modified: newText, path: 'a.go' })
diff.nextDiff(); diff.setRenderSideBySide(false)
```

`OpenViewTabOptions.key` dedups tabs (same key focuses, different key opens a new tab); `params` becomes the view component's props.

### State / Config / Plugins / Commands

- `ctx.state` — key-value store (`StateAPI`, `web/src/plugin-api/state.ts`).
- `ctx.config` — read/write the plugin's own configuration (schema declared in `contributes.configuration`).
- `ctx.plugins` — inter-plugin registry.
- `ctx.commands` — `register(id, handler)`, `execute(id, args)`, `registerKeybinding(keybinding, commandId)`.

## Activation and hot reload

```ts
export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  const disposables: Disposable[] = []
  disposables.push(ctx.events.on('turn.ended', () => {}))
  return disposables  // run in REVERSE order on deactivate
}
```

Hot reload (`usePluginRuntimeHost.ts`): deactivate old instance (reverse-order disposables + contribution removal) → activate new. Third-party plugins load via versioned URL (`?v=<version>`); built-ins are statically imported.

## Message renderers

`MessageRendererContribution` declares how plugin tools render inside chat messages: the `PluginRuntime.renderTool` dispatcher matches `{tool}`/`{uiMode}`/`{role}`/`{}` with priority ordering. The built-in `builtinGenuiRenderer` matches `{uiMode:'genui'}`; `builtinLegacyDisplayHtmlRenderer` matches `{tool:'display_html'}` for old history.

## Manifest → backend

The backend `PluginManifest.Web *WebPluginDecl` (`plugin/plugin.go:131`) only carries `Entry` + an opaque `Contributes` JSON blob — **no semantic validation on the backend** (single gate = frontend `registry.validate()`; dual gating was the lesson from dsh/cordis). `web_plugin_list` RPC serves manifests with Web declarations; static files are served at `/plugins/<id>/web/*` with plugin-ID regex + path-clean guards.

## Gotchas (all from production bugs)

1. ⚠️ **Permissions must include every capability actually used.** Missing `"ui"` → `ctx.ui` undefined → `openViewTab` clicks fail silently. The Go whitelist (`plugin/permissions.go allPermissions`) must also contain the permission — it is compiled into the server binary.
2. ⚠️ **Built-in views must be statically imported** (`builtinViews` map), never dynamic `import()` — dynamic imports let rolldown bind React hooks to the wrong vendor chunk (React #311, black screen).
3. ⚠️ **Hooks after conditional early returns** — a plugin view with `useState` after a `return null` loading branch changes hook counts (React #310, whole panel crash). Put all hooks before any conditional return.
4. ⚠️ **Multi-entry plugins must build with `esbuild --splitting`** — each view entry gets its own module; without splitting, injected shared singletons (rpc/ui) are invisible across entries.
5. ⚠️ **Plugin view tabs must use `component: viewId`** in dockview — `renderPluginView` looks up by `view.id === component`; a generic component name never resolves.
6. ⚠️ **Mock `usePluginRuntime` in tests must return a stable reference** — a fresh object per render loops `AsyncPluginView`'s `useEffect [runtime]` forever (vitest hangs).
7. ⚠️ **`filterPanels` must keep the root a branch** — pruning that promotes a single-child branch to a leaf persists an illegal layout (`fromJSON` asserts "root must be of type branch") → CrashBoundary crash on every session switch back.
