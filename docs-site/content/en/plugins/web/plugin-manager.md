---
title: "Plugin Manager"
weight: 14
---

The plugin manager panel (`xbot.plugin-manager`) is the bootstrap example of the whole system: **it is itself a plugin**, consuming only public APIs — a high-fidelity dogfooding of the capability model. Third parties can write a better panel and replace it.

## The plugin side

`web/src/plugins/manager/pluginManager.ts`:

```ts
export const manifest = {
  id: 'xbot.plugin-manager',
  name: 'Plugin Manager',
  version: '0.1.0',
  description: 'Manage plugins: view/enable/disable/uninstall/reload (self-hosting — itself a plugin)',
  permissions: ['rpc', 'plugins', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'xbot.plugin-manager.panel',
      container: 'right_sidebar',
      title: '插件',
      icon: 'blocks',
      // Built-in view marker: the host's loadViewComponent recognizes this and
      // returns the statically imported component directly.
      entry: 'builtin:xbot.plugin-manager.panel',
    },
  ],
} satisfies PluginManifest

export function activate(_ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  return () => {}   // no init side effects; the view is rendered by the contribution point
}
```

It is activated first at startup by `PluginRuntimeBootstrap` via `runtime.activateBuiltin(manifest, module)` — the same gate/validate/activate path as third-party plugins, just statically imported (no URL load).

## The panel

`PluginManagerPanel.tsx` renders inside `BuiltinView` (synchronous render — see [ESM Module Format](module-format.md) for why built-ins never go through async loading). Data flow:

| Action | Mechanism |
|---|---|
| List plugins | `runtime.rpc.call('plugin_status', { rescan: true })` — rescans the disk plugin dir and returns **backend Go plugin system** plugins (script/grpc runtimes) |
| Reload | `plugin_reload` RPC → refresh |
| Enable/disable | `plugin_set_enabled` RPC → **explicitly re-sync the frontend runtime** (below) |
| Install | `installPluginFile` upload (file input) |

The panel shows the **backend** plugin panorama (daily-jokes, dashboard, git-info, github, theme-party, …) — a separate system from the frontend Web plugin v2 runtime. It also lists frontend runtime plugin states (`PluginRuntimeState`: id/name/version/status/permissions/contributionIds) via `runtime.listPluginStates()` / `usePluginRuntime()`.

## Enable/disable: explicit frontend re-sync

Toggling a plugin re-syncs the frontend runtime **actively** — the WS `web_plugin_init` broadcast is not trusted as the sole path (broadcast links can be unreliable):

```ts
await runtime.rpc.call('plugin_set_enabled' as never, { id, enabled } as never)
// Pull the latest web_plugin_list, then activate / deactivate this plugin.
const res = await runtime.rpc.call('web_plugin_list' as never, {} as never) as { plugins?: WebPluginDecl[] }
const decl = res?.plugins?.find((p) => p.id === id)
if (decl) {
  if (enabled && decl.enabled) {
    await runtime.activate(toManifest(decl), decl.module_url)
  } else if (!enabled) {
    runtime.deactivate(id)
  }
}
```

## PluginRuntimeState

The runtime state the manager displays (`web/src/plugin-runtime/registry.ts`):

```ts
export interface PluginRuntimeState {
  id: string
  name: string          // from manifest.name
  version: string
  enabled: boolean
  status: 'active' | 'inactive' | 'error' | 'reloading'
  error?: string
  permissions: readonly Permission[]
  contributionIds: readonly string[]   // for panel display
}
```

State transitions are reported through `RegistryHooks.onStateChange` → `PluginRuntimeHost.onPluginStateChange` (manager panel data source).

## Disabled plugins must be skipped at bootstrap

`PluginRuntimeBootstrap` skips plugins with `enabled=false` when activating the initial list — a disabled frontend plugin must not register views and take effect:

```ts
for (const decl of res?.plugins ?? []) {
  if (!decl.enabled) {
    console.debug(`[plugin-runtime] 跳过已禁用的插件 ${decl.id}（enabled=false）`)
    continue
  }
  bumpPluginLoadToken(decl.id)
  const manifest = toManifest(decl)
  await runtime.activate(manifest, decl.module_url)
}
```

## Permission whitelist sync

The backend permission whitelist (`plugin/permissions.go` `allPermissions`) must list every frontend `Permission` value the runtime uses — the manager's `plugin_reload` re-validates manifests against it. Adding a new frontend permission requires the matching Go constant (`// Matches the frontend Permission 'rpc' (web/src/plugin-api/manifest.ts)` pattern). The whitelist lives in the Go binary — a reload using new permissions needs a server restart.

## Manifest reload

`/plugin reload-all` (agent command) or a server restart triggers backend reload; `WatchConfig` is **not** wired — editing `disabled_plugins` in config.json does not trigger a reload. stdio plugin binary updates need `/plugin reload` as well.
