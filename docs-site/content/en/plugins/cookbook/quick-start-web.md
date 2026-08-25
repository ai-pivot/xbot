---
title: "Quick Start: Web Plugin"
weight: 5
---

Build a frontend view panel in TypeScript that appears in the web UI sidebar. This recipe is based on the built-in `xbot.git-fancy` plugin (`plugins/xbot-git-fancy/plugin.json`) and the `@xbot/plugin-api` type package (`web/src/plugin-api/`).

Web plugins are compiled **ESM modules** loaded directly by the frontend plugin runtime. There is no server-side JS VM — the backend only serves the static files and routes RPC. Type safety is the contract: the manifest is checked at compile time against `@xbot/plugin-api`.

## Step 1: Write the frontend module

`~/.xbot/plugins/demo-web/web/index.ts`:

```ts
// Built with esbuild:  esbuild index.ts --bundle --format=esm --outdir=dist
import type { PluginContext, PluginManifest, Disposable } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo-web',
  name: 'Demo Web',
  version: '0.1.0',
  permissions: ['rpc', 'events', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'demo.panel',
      container: 'right_sidebar',
      title: 'Demo',
      icon: 'sparkles',
      entry: 'index.js',
    },
  ] as const,
} satisfies PluginManifest   // ❌ compile error if any field is wrong

export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  // ctx is typed by the declared permissions:
  ctx.rpc.call('plugin.list', {}).then((plugins) => {
    ctx.ui.showToast(`Loaded ${plugins.length} plugins`, 'info')
  })
  const sub = ctx.events.on('message.committed', (p) => {
    ctx.ui.showToast(`turn ${p.turnID} committed`, 'success')
  })
  return sub // returned disposables are cleaned up on deactivate
}
```

Key points:

- **`manifest` is the single source of truth** — `satisfies PluginManifest` makes wrong `kind`/`container`/field shapes compile errors.
- **`permissions` shape the context**: `PluginContext<P>` maps each declared permission to an API; accessing an undeclared one is type `never` (`web/src/plugin-api/context.ts`).
- **`activate(ctx)` returns disposables** — unregistered on hot-reload/deactivate.
- **`ctx.rpc.call('plugin.list', {})`** is fully typed by the `BackendRPC` method table (`web/src/plugin-api/rpc.ts`) — method names, params, and results are all checked.

## Step 2: Write the backend manifest

```json
{
  "id": "xbot.demo-web",
  "name": "Demo Web",
  "version": "0.1.0",
  "description": "A demo web plugin",
  "runtime": "native",
  "activationEvents": ["onStart"],
  "web": {
    "entry": "index.js",
    "contributes": [
      {
        "kind": "view",
        "id": "demo.panel",
        "container": "right_sidebar",
        "title": "Demo",
        "icon": "sparkles",
        "entry": "index.js"
      }
    ]
  }
}
```

Notes:

- `runtime` can be anything — the frontend part is independent. `xbot.git-fancy` uses `"stdio"` with a Go backend process; pure-UI plugins can use `"native"` with no backend code at all.
- `web.entry` is the module path relative to the plugin's `web/` directory, served at `/plugins/<id>/web/<entry>`.
- `web.contributes` is an **opaque JSON blob passed verbatim to the frontend runtime** — the backend does no semantic validation (`plugin/plugin.go:131 WebPluginDecl`). The frontend `registry.validate()` is the single authoritative gate.

## Step 3: Build and serve

Place the built module at `~/.xbot/plugins/demo-web/web/index.js`. The backend serves it statically (`channel/web/web.go handlePluginStatic`, guarded by the plugin ID regex `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`). Restart xbot and the "Demo" panel appears in the right sidebar.

## Built-in vs third-party loading

The runtime distinguishes two activation paths (`web/src/plugin-runtime/`):

- **`activateBuiltin`** — built-in plugins are statically imported into the main bundle. ⚠️ Built-in views must be statically imported, never dynamically `import()`'d — dynamic imports let the bundler bind React hooks to the wrong vendor chunk (React #311, full black screen).
- **`activate`** — third-party plugins are loaded via versioned URL (`?v=<version>`) to bypass browser module caching.

Hot reload = deactivate (run disposables in reverse, remove contributions) then activate the new instance.

## Adding a backend data source

If the view needs backend data, pair it with a stdio backend and call it through the typed RPC bridge:

```json
// plugin.json — permissions must include every capability actually used
{ "permissions": ["rpc", "ui"] }
```

```go
// backend: plugins/xbot-git-fancy/main.go pattern
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) *protocol.WebPluginRPCResult {
	switch p.Method {
	case "git.status":
		return rpcOK(gitStatus(cwd))
	// ...
	}
	return rpcErr("unknown method")
}
```

```ts
// frontend: typed via declaration merging in BackendRPC
const status = await ctx.rpc.call('git.status', { channel: 'web', chatID })
```

⚠️ **Permissions must match reality**: `buildContext` injects APIs strictly by declared permission — omit `"ui"` and `ctx.ui` is `undefined` at runtime, so `openViewTab` clicks fail **silently** (no error, no log). The backend whitelist (`plugin/permissions.go allPermissions`) must also contain every permission used — it is compiled into the Go binary.

Next: [Web Plugins](../web-plugins/) for the full runtime guide.
