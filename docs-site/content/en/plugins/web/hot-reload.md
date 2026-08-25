---
title: "Hot Reload"
weight: 11
---

Hot reload replaces a running plugin with its newest code without a page refresh. The lifecycle is driven by backend WS messages and enforced by three mechanisms: unload-then-activate, module-map cache busting, and per-plugin cleanup.

## Lifecycle flow

```
backend change detected
        │
        ▼
WS web_plugin_init (decl + module_url)
        │
        ▼
PluginRuntimeBootstrap handler
        ├─ bumpPluginLoadToken(decl.id)     ← cache bust (new import URL)
        ├─ toManifest(decl)                  ← build typed manifest
        └─ runtime.activate(manifest, module_url)
             ├─ registry.isActive(id)?  → deactivate(id)   ← unload old instance FIRST
             ├─ loadPluginModule(versionedUrl(url, version))
             ├─ registry.registerPlugin()    ← single-gate validate + mount
             │     └─ contribution mount failure → per-contribution ROLLBACK,
             │        plugin marked 'error', already-mounted disposers released
             ├─ buildContext(perms, services)
             ├─ mod.activate(ctx)            ← activate result fn pushed as disposer
             └─ plugins.notifyActivated(id)
```

Unload (`web_plugin_deactivate` WS message → `runtime.deactivate(id)`):

```ts
deactivate(pluginId: string): boolean {
  const removed = this.registry.unregisterPlugin(pluginId)   // disposables in REVERSE order
  if (removed) {
    this.events.unsubscribePlugin(pluginId)      // drop all event subscriptions
    this.commands.removePlugin(pluginId)         // drop commands + keybindings
    this.plugins.notifyDeactivated(pluginId)     // interop consumers degrade
    this.modules.delete(pluginId)
    // clear view cache (reload must re-import modules)
    for (const key of [...this.viewCache.keys()]) {
      if (key.startsWith(pluginId)) this.viewCache.delete(key)
    }
  }
  return removed
}
```

## Cache busting — the load-token mechanism

The browser ES module map keys by full URL. Without a URL change, a reloaded plugin keeps hitting the cached module (no network request at all) — the frontend runs old code forever no matter how many times the disk updates. `usePluginRuntimeHost.ts` keeps a per-plugin load token, bumped on every activate:

```ts
const pluginLoadTokens = new Map<string, string>()

function bumpPluginLoadToken(pluginId: string): void {
  pluginLoadTokens.set(pluginId, Date.now().toString(36))
}

// in loadPluginViewComponent:
const token = pluginLoadTokens.get(pluginId)
const bust = token ? `&_t=${token}` : ''
const mod = await import(/* @vite-ignore */ `${url}?view=${encodeURIComponent(view.id)}${bust}`)
```

The main module load uses `versionedUrl(url, manifest.version)` (`?v=<version>`). Together they guarantee: reload → URL changes → module map miss → network fetch → newest code on disk. Within a session without reload the token stays constant → URL stable → module map hit (no duplicate requests).

## Unload ordering guarantees

- **Disposables run in reverse registration order** (`unregisterPlugin` iterates `disposables.splice(0).reverse()`), and a throwing disposer does not abort the rest.
- **Event subscriptions** are bulk-removed by `unsubscribePlugin` (per-plugin attribution recorded at subscribe time).
- **Commands and keybindings** are removed by `removePlugin`.
- **Views** disappear from host sidebars via `notifyViewsChanged` → `usePluginViewPanels` recompute, and layout items are unregistered by the `PluginRuntimeBootstrap` sync effect.
- **Interop consumers** get `onDeactivated` notifications to degrade.

## Contribution-level rollback

If mounting one contribution throws mid-activation, the registry rolls back the **already-mounted** contributions of that plugin and marks it `error` — the host never sees a half-mounted plugin:

```ts
for (const c of manifest.contributes) {
  try {
    this.mount(manifest, c, disposables)
  } catch (error) {
    for (const d of disposables.splice(0).reverse()) {
      try { d() } catch { /* ignore */ }
    }
    this.plugins.delete(manifest.id)
    record.state.status = 'error'
    record.state.error = error instanceof Error ? error.message : String(error)
    this.hooks.onStateChange?.({ ...record.state })
    return { ok: false, error: String(error) }
  }
}
```

## Crash isolation at render time

`PluginView` wraps every plugin view in `PluginViewErrorBoundary` (`web/src/plugin-runtime/PluginView.tsx`): a render exception shows an error placeholder in the tab (message + componentStack rendered inline for screenshot diagnosis) — **never** unmounts the whole tree. Renderer dispatch (`renderTool`) similarly catches renderer crashes and falls back to default rendering.

## Built-in plugins

`activateBuiltin(manifest, mod)` shares the same gate/validate/activate path as third-party plugins — built-ins have no privileged difference (self-hosting discipline). The only difference is module origin: statically imported vs URL-loaded.

## Manual trigger points

- `PluginRuntimeBootstrap` startup: fetch `web_plugin_list` (with `rescan: true`) and activate enabled plugins. **Disabled plugins must be skipped** — otherwise a disabled frontend plugin still registers views and takes effect.
- WS `web_plugin_init`: reload a single plugin (bump token + activate).
- WS `web_plugin_deactivate`: unload by id.
- Plugin manager panel: enable/disable/reload actions re-sync the frontend runtime explicitly (the WS broadcast is unreliable; see [Plugin Manager](plugin-manager.md)).
