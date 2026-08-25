---
title: "API Reference"
weight: 1
geekdocCollapseSection: true
---

This section is the complete API reference for xbot plugin development, generated from the actual source code. It covers both the Go plugin SDK (`plugin/` package) and the web frontend plugin API (`web/src/plugin-api/`).

## What's Covered

| Document | Description |
|----------|-------------|
| [Manifest Schema](manifest-schema/) | Complete `plugin.json` schema with every field, validation rule, and contribution type |
| [PluginContext API](plugin-context-api/) | The permission-filtered API surface available to plugins during activation |
| [PluginTool API](plugin-tool-api/) | Tool definition, execution, result types, and the fluent result builder |
| [Hook Events](hook-events/) | All 13 lifecycle hook events and the `HookPayload` field reference |
| [Environment Variables](environment-variables/) | `XBOT_*` variables injected into script plugin processes |
| [Permissions List](permissions-list/) | All 23 permission strings, their meaning, and which APIs they gate |
| [Trigger Events](trigger-events/) | Activation events and widget trigger matcher formats |
| [Widget Zones](widget-zones/) | UI slot names where widgets can render |
| [Component Types](component-types/) | Declarative L1 component types for web views |
| [RPC Methods](rpc-methods/) | Backend RPC method table (host RPC + frontend `ctx.rpc`) |
| [Event Types](event-types/) | Lifecycle events, typed event bus (`EventMap`), and notifier types |

## Two Plugin Surfaces

xbot plugins have two distinct API surfaces:

1. **Go SDK** (`plugin/` package) — in-process native plugins and the host side of stdio plugins. Types: `Plugin`, `PluginContext`, `PluginTool`, `HookPayload`, etc.
2. **Web Plugin API** (`web/src/plugin-api/`, package `@xbot/plugin-api`) — type-safe ESM frontend plugins. Types: `PluginManifest`, `PluginContext<P>`, `EventMap`, `BackendRPC`, etc. Capabilities are types: the `permissions` array in the manifest determines which capability interfaces exist on the context at compile time.

## Permission Model

Every capability a plugin uses must be declared in `plugin.json` under `permissions`. The `PermissionChecker` (see [Permissions List](permissions-list/)) enforces this at runtime; the web API enforces it at compile time via `PluginContext<P>`.

The wildcard `"*"` grants all permissions (Go side only). Invalid permissions fail manifest validation at load time.
