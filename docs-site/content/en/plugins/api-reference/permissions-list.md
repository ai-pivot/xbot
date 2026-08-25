---
title: "Permissions List"
weight: 7
---

Complete list of permission strings (`plugin/permissions.go`). Plugins declare them in `plugin.json` under `permissions`. The wildcard `"*"` (Go side only) grants everything; unknown permissions fail manifest validation.

## Backend Permissions (Go)

| Constant | String | Grants |
|----------|--------|--------|
| `PermToolsRegister` | `tools.register` | Register tools and middleware (`RegisterTool`, `RegisterTools`, `UseMiddleware`). |
| `PermToolsCall` | `tools.call` | Invoke tools. |
| `PermHooksSubscribe` | `hooks.subscribe` | Subscribe to lifecycle hooks (`OnPreToolUse` etc.) and register the plugin error callback. |
| `PermContextEnrich` | `context.enrich` | Register context enrichers (`EnrichContext`). |
| `PermStoragePrivate` | `storage.private` | Access the plugin's private key-value storage (`Storage()` and typed helpers). |
| `PermStorageShared` | `storage.shared` | Access shared plugin storage. |
| `PermNetworkOutbound` | `network.outbound` | Make outbound network requests. |
| `PermBusRead` | `bus.read` | Read from the event bus (with `bus.plugin`). |
| `PermBusWrite` | `bus.write` | Publish to the event bus (with `bus.plugin`). |
| `PermBusPlugin` | `bus.plugin` | Use the plugin-to-plugin event bus. `Subscribe` needs `bus.plugin`+`bus.read`; `Publish` needs `bus.plugin`+`bus.write`. |
| `PermUIContribute` | `ui.contribute` | Contribute UI widgets (`ContributeUI`). Also required by `contributes.ui` in the manifest. |
| `PermChannelsRegister` | `channels.register` | Register custom Channel providers. |
| `PermCommandsRegister` | `commands.register` | Register slash commands. |
| `PermCronSchedule` | `cron.schedule` | Schedule and cancel cron tasks. |
| `PermUIThemes` | `ui.themes` | Contribute themes (`ContributeTheme`). |
| `PermUIOverlay` | `ui.overlay` | Register and control overlays (`RegisterOverlay`, `ShowOverlay`, `HideOverlay`). |
| `PermNotificationsSend` | `notifications.send` | Send notifications and play sounds (`Notify`, `PlaySound`). |

## Frontend Permissions (Web Plugin v2)

These match `web/src/plugin-api/manifest.ts` `Permission` type and are also registered in the Go backend whitelist (`allPermissions`) — the two lists MUST stay in sync (a mismatch causes `unknown permission "ui"` reload failures):

| Permission | Grants (frontend `PluginContext<P>` capability) |
|-----------|--------------------------------------------------|
| `events` | Typed event bus access (`ctx.events.on/once`). |
| `commands` | Command registration/execution (`ctx.commands.register/execute/registerKeybinding`). |
| `rpc` | Backend RPC calls (`ctx.rpc.call/notify`). |
| `state` | Key-value state store (`ctx.state`). |
| `ui` | UI capabilities: toast, panel open/close, editor view tabs (`ctx.ui.openViewTab/openFileTab`). |
| `plugins` | Inter-plugin registry (`ctx.plugins`). |
| `config` | Plugin configuration API (`ctx.config`). |

## Enforcement

- **Go**: `PermissionChecker` (`NewPermissionChecker`, `Has`, `HasAll`, `HasAny`) — `pluginContextImpl` checks permissions on every capability call and returns `*PermissionError` (or silently warns for notifications).
- **Web**: capabilities are types — `PluginContext<P>` only exposes capability interfaces whose permission is in `P`; accessing undeclared capabilities is a compile-time error.
