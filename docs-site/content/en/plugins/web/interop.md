---
title: "Inter-plugin Interop"
weight: 9
---

Plugins expose public APIs as **module named exports** (everything except the reserved `manifest`/`activate`/`deactivate`). Consumers obtain them through `ctx.plugins`; types come from the `PluginExportsMap` declaration-merging table. Defined in `web/src/plugin-api/plugins.ts`.

## PluginExportsMap

```ts
/** Empty table; each plugin's published type package extends it (declaration merging). */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type -- legal empty table for declaration merging
export interface PluginExportsMap {}
```

A plugin publishing a public API ships a `.d.ts` package:

```ts
declare module '@xbot/plugin-api' {
  interface PluginExportsMap {
    'xbot.git-info': {
      refresh(): Promise<void>
      readonly branch: string | null
    }
  }
}
```

Consumers importing the package get typed `ctx.plugins.get('xbot.git-info')`.

## PluginsAPI

```ts
export interface PluginsAPI {
  /** Synchronously get an activated plugin's public API; unactivated/disabled/crashed → undefined. */
  get<K extends keyof PluginExportsMap>(id: K): PluginExportsMap[K] | undefined
  /** Asynchronously ensure a dependency plugin is activated, then return its API (lazy-load entry for optional deps). */
  require<K extends keyof PluginExportsMap>(id: K): Promise<PluginExportsMap[K]>
  /** Subscribe to a dependency's activation/deactivation (degrade/recover on dynamic reload). */
  onActivated<K extends keyof PluginExportsMap>(id: K, h: (api: PluginExportsMap[K]) => void): Disposable
  onDeactivated<K extends keyof PluginExportsMap>(id: K, h: () => void): Disposable
}
```

## Runtime implementation

`PluginInterop` (`web/src/plugin-runtime/plugins.ts`) wraps the `ContributionRegistry`:

- `get` reads `registry.getExports(id)` — the exports map recorded at activation.
- `require` falls back to an on-demand activation: the runtime injects an `ensureActive(id)` into the registry (constructor wiring in `web/src/plugin-runtime/index.ts`), which fetches the plugin declaration from the backend (`web_plugin_list`) and activates it. Throws if the plugin cannot be activated.
- `onActivated` fires immediately if the dependency is already active (subscriber receives the current API), then on each later activation; `onDeactivated` fires when the dependency unloads (hot reload) — consumers degrade/recover gracefully.
- `notifyActivated`/`notifyDeactivated` are invoked by `PluginRuntime.activateModule`/`deactivate`.

## Collecting exports

```ts
/** A plugin module's named exports = its public API (reserved keys excluded). */
function collectPluginExports(mod: PluginModule): Record<string, unknown> {
  const reserved = new Set(['manifest', 'activate', 'deactivate'])
  const out: Record<string, unknown> = {}
  for (const key of Object.keys(mod)) {
    if (!reserved.has(key)) out[key] = mod[key]
  }
  return out
}
```

## Activation dependencies

`manifest.activationDependencies` declares **hard** dependencies: ids that must be active before this plugin activates. The validation gate (`ContributionRegistry.validate`) rejects activation when any listed dependency is missing:

```ts
for (const dep of manifest.activationDependencies ?? []) {
  if (!this.plugins.has(dep)) {
    return { pluginId: manifest.id, message: `缺少强依赖插件: ${dep}` }
  }
}
```

`PluginRuntimeBootstrap` activates built-in plugins first, then third-party plugins — activation order follows the served list, and hard dependencies are enforced by the gate.

## Example

```ts
// provider plugin
export const manifest = {
  id: 'xbot.data-service',
  name: 'Data Service',
  version: '1.0.0',
  permissions: ['rpc'] as const,
  contributes: [],
} satisfies PluginManifest

export async function query(chatID: string) {
  return { rows: [] as readonly string[] }
}

// consumer plugin
export const manifest = {
  id: 'xbot.consumer',
  name: 'Consumer',
  version: '1.0.0',
  permissions: ['plugins', 'rpc'] as const,
  activationDependencies: ['xbot.data-service'],
  contributes: [],
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  // synchronous: data-service already active (hard dependency)
  const api = ctx.plugins.get('xbot.data-service')
  void api?.query('x')

  // asynchronous: ensures activation for OPTIONAL dependencies
  const off = ctx.plugins.onDeactivated('xbot.data-service', () => {
    ctx.ui.showToast('data service went offline', 'error')
  })
  return () => off()
}
```
