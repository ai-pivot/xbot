---
title: "Permissions"
weight: 17
---

Plugins declare the capabilities they need; xbot enforces them. The catalogue lives in `plugin/permissions.go` (backend) and `web/src/plugin-api/manifest.ts` (frontend). The permission model is **declarative**: a plugin can only access APIs for permissions it declared — on the frontend this is enforced at **compile time** (capability-as-type), on the backend by convention and registration paths.

## The backend catalogue

`plugin/permissions.go` constants:

| Permission | Grants |
|---|---|
| `tools.register` | Register tools |
| `tools.call` | Invoke tools |
| `hooks.subscribe` | Subscribe to lifecycle hooks |
| `context.enrich` | Register context enrichers |
| `storage.private` | Private key-value storage |
| `storage.shared` | Shared plugin storage |
| `network.outbound` | Outbound network requests |
| `bus.read` | Subscribe to the event bus |
| `bus.write` | Publish to the event bus |
| `bus.plugin` | Use the plugin-to-plugin event bus (requires `bus.read`/`bus.write` in addition) |
| `ui.contribute` | Contribute UI widgets |
| `ui.themes` | Contribute themes |
| `ui.overlay` | Register/control overlays |
| `channels.register` | Register custom Channel providers |
| `commands.register` | Register slash commands |
| `cron.schedule` | Schedule cron tasks |
| `notifications.send` | Send notifications and play sounds |
| `rpc` | Frontend view ↔ backend process RPC (`web_plugin_rpc`) — matches frontend `'rpc'` |
| `events` | Typed event bus (`ctx.events`) — matches frontend `'events'` |
| `commands` | Command registration/execution (`ctx.commands`) — matches frontend `'commands'` |
| `state` | Key-value state store (`ctx.state`) — matches frontend `'state'` |
| `ui` | UI capabilities: toast, panels, editor view tabs (`ctx.ui`) — matches frontend `'ui'` |
| `plugins` | Inter-plugin registry (`ctx.plugins`) — matches frontend `'plugins'` |

The frontend permission `'config'` (plugin's own configuration) exists in the type system (`web/src/plugin-api/context.ts Permission`), mirroring the backend config access granted by `Config()`.

## Checking permissions

```go
checker := plugin.NewPermissionChecker(manifest.Permissions)
checker.Has(plugin.PermToolsRegister)
checker.HasAll("bus.plugin", "bus.read")
checker.HasAny("ui.contribute", "ui.themes")
```

A `"*"` entry grants everything (wildcard). `IsValidPermission(p)` tests whether a string is a known permission.

## Declaring in the manifest

```json
{
  "permissions": ["tools.register", "hooks.subscribe", "context.enrich", "storage.private"]
}
```

Manifest validation rejects **unknown permission strings** at load time (`plugin/manifest.go validateManifest`) — a typo like `"tool.register"` fails discovery instead of silently granting nothing.

## Frontend: capability-as-type

```ts
permissions: ['rpc', 'events'] as const,

export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  ctx.rpc.call('plugin.list', {})   // ✅ 'rpc' declared
  ctx.ui.showToast('hi')            // ❌ compile error: 'ui' not in P → never
}
```

`PluginContext<P>` maps each `Permission` to its API only when declared (`web/src/plugin-api/context.ts`). At runtime `buildContext` injects strictly by declaration — undeclared APIs are `undefined`.

## The sync rule (critical)

⚠️ **The backend whitelist and the frontend `Permission` type must stay in sync.** `allPermissions` in `plugin/permissions.go` is compiled into the Go binary; a manifest declaring a new frontend permission that isn't in the whitelist is rejected at reload (`unknown permission "ui"` was a real incident). When adding a frontend `Permission` value, update `permissions.go` constants + `allPermissions` in the same change (follow the `PermRPC` comment pattern: `// Matches the frontend Permission 'rpc' (web/src/plugin-api/manifest.ts)`).

## Best practices

1. **Declare only what you use.** Permissions are surfaced to users in the plugin panel — a lean list builds trust. The git-fancy incident proves the inverse too: **declare everything you use** — an omitted `"ui"` makes `ctx.ui` undefined with silent no-op failures.
2. **Event bus needs three permissions** (`bus.plugin` + `bus.read` + `bus.write`) — one alone does nothing.
3. **Check `IsValidPermission` output in tests** — keep a unit test asserting your manifest's permission list parses (see the git-fancy `TestManifestPermissions` pattern in `plugins/xbot-git-fancy/main_test.go`).
4. `"*"` is available but discouraged outside debugging.
