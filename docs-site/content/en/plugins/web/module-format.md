---
title: "ESM Module Format"
weight: 10
---

A Web plugin is a compiled ESM module served under `/plugins/<id>/web/` and loaded via dynamic `import()`. The loading contract is defined in `web/src/plugin-runtime/loader.ts`.

## Module shape

```ts
/** Plugin module export shape (consistent with the type-package plugin contract). */
export interface PluginModule {
  manifest: PluginManifest
  activate?: (ctx: unknown) => void | Promise<void> | (() => void)
  deactivate?: () => void
  /** Exports API: named exports are the public API (interop). */
  [key: string]: unknown
}
```

- `manifest` — the module's exported manifest **overrides** the backend-served declaration ("declaration is contract"; `activateModule` uses `mod.manifest ?? manifest`).
- `activate(ctx)` — called once after the single-gate validation and contribution mounting. May return a cleanup function (a `Disposable`).
- `deactivate` — reserved; unloading is handled by the runtime's disposable chain.
- Named exports — everything except `manifest`/`activate`/`deactivate` becomes the public API (see [Interop](interop.md)).
- `commandHandlers` — a special named export: `{ [commandId]: (args) => void }` resolving contribution-declared commands.

## Build contract

Plugins are built as ESM bundles. The reference implementation (`xbot.git-fancy`, see `web/src/plugins/git-fancy/index.tsx`) uses:

```
esbuild --bundle --splitting --format=esm --jsx=transform   (React external)
```

Constraints:

- **React from `window`** — plugin modules must not import host internal modules; React is obtained from the window global (the host React runtime instance). The git-fancy plugin declares `import { React } from './shared'` where `shared.tsx` reads `window.React`.
- **Multi-entry plugins must use `--splitting`** — `activate(ctx)` runs in the **main entry** and injects `rpc`/`ui` singletons into the shared chunk. Without splitting, each view entry bundles its own copy of the shared module — the injection is invisible to other views (single-entry bundles give each view an independent instance). Shared chunk URLs carry no query → the ESM cache serves one instance.
- **Hooks discipline** — never place hooks after a conditional early return in view components (loading/error branches): switching loading → loaded changes the hook count (7→8) and triggers React #310 ("Rendered fewer hooks than expected").

## Versioned URLs

```ts
/** Versioned URL: /plugins/<id>/web/... + ?v=<hash>. */
export function versionedUrl(baseUrl: string, version: string, hash?: string): string {
  const sep = baseUrl.includes('?') ? '&' : '?'
  const v = hash ?? version.replace(/[^\w.-]/g, '_')
  return `${baseUrl}${sep}v=${encodeURIComponent(v)}`
}

/** Load a plugin module (dynamic import). Throws on syntax/network errors. */
export async function loadPluginModule(entryUrl: string): Promise<PluginModule> {
  const mod = await import(/* @vite-ignore */ entryUrl)
  return mod as PluginModule
}
```

The browser ES module map keys by full URL — `?v=` busting is what makes hot reload actually work (see [Hot Reload](hot-reload.md)).

## View entry resolution

A view's `entry` is resolved relative to `/plugins/<pluginId>/web`:

```ts
const base = `/plugins/${pluginId}/web`
const url = view.entry.startsWith('/') ? view.entry : `${base}/${view.entry}`
```

Resolution priority in `PluginRuntime.loadViewComponent`:

1. **Named export from the active module** — `mod[view.id]` (multi-view single-module is the singleton-optimal layout).
2. **Main module default export** — when `view.entry == null || view.entry === manifest.entry`, `mod.default` serves the view. Reusing the already-activated module instance matters: `activate()` was called on it and injected `ctx.rpc` etc.; re-importing under a different URL (`?view=`) creates a **second module instance** whose module-level state (like `rpc`) is uninitialized — the view would show "plugin not initialized".
3. **Host-side import** — other entries of multi-entry plugins (e.g. git-fancy's `diff.js`/`commit.js`) go through `host.loadViewComponent` (`usePluginRuntimeHost.ts`), which dynamic-imports `?view=<viewId>` + the reload token.

Only function components or objects with a valid `$$typeof` (memo/forwardRef) are accepted — a bare object would make React throw "Element type is invalid… got: object".

## `builtin:` views

Views whose `entry` starts with `builtin:` resolve to statically-imported components shipped in the main bundle (`builtinViews` map in `usePluginRuntimeHost.ts`):

```ts
builtinViews.set('xbot.plugin-manager.panel', PluginManagerPanel)
builtinViews.set('git-info.status', GitStatusPanel)
```

Built-in views are imported **statically**, never via dynamic `import()` — dynamic imports make rolldown split them into separate chunks and mis-bind their `useState`/`useEffect` symbols to `vendor-framer-motion` exports, causing React #311 and a full black screen. Static imports keep them in the main bundle.

## Serving & security

Backend static serving (`channel/web/web.go` `handlePluginStatic`):

- Plugin ID must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` (`isValidPluginIDForServe`).
- Sub-paths are cleaned and must stay inside `<pluginDir>/<id>/web/` (path traversal protection).
- `SetPluginDirs` injects `plugin.DefaultPluginDirs(config.XbotHome())`.
