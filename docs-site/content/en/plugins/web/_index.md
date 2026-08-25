---
title: "Web Plugin System"
weight: 1
---

xbot's Web plugin system (v2) lets plugins extend the Web UI arbitrarily. It is built on three principles:

1. **No server-side VM** — a Web plugin is a compiled ESM module loaded and executed directly in the frontend. The backend plugin model (Go native / stdio process) is unchanged.
2. **No sandbox** — plugins are trusted code. Installing a plugin means trusting it (same model as VSCode / browser extensions). The type system enforces **contract correctness**, not a security boundary.
3. **Type-as-Contract** — a real type system (discriminated unions, capability-as-type, conditional-type refinement, declaration merging) constrains at **compile time** everything a plugin can do: contribution shapes, capability APIs, event payloads, RPC methods, renderer match-parameter correlation.

## Architecture

```
┌─ xbot Web frontend (where plugins run) ────────────────────────┐
│                                                                │
│  PluginRuntime (web/src/plugin-runtime/)                       │
│  ├── loader.ts    ESM dynamic import (versioned URLs)          │
│  ├── registry.ts  typed contribution registry (views/…/themes) │
│  ├── events.ts    typed event bus (EventMap indexed access)    │
│  ├── rpc.ts       typed RPC bridge (BackendRPC method table)   │
│  ├── commands.ts  command registry + keybindings               │
│  ├── state.ts     read-only snapshots (structuredClone)        │
│  ├── config.ts    per-plugin config service                    │
│  ├── layoutRegistry.ts  VSCode-style layout slots              │
│  ├── editorTabs.ts      module-level editor-view opener        │
│  └── PluginView.tsx     ErrorBoundary-isolated view host       │
│                                                                │
│  Plugin modules are imported directly into the host React tree │
│  Crash isolation = React ErrorBoundary + per-contribution rollback
└──────────────┬─────────────────────────────────────────────────┘
               │ web_plugin_* messages (reuse existing WS/SSE)
┌──────────────▼─────────────────────────────────────────────────┐
│ xbot backend (Go)                                              │
│  ├── Plugin activation: serves manifests (contributions +     │
│  │     module URL + permissions)                               │
│  ├── EventBridge: agent lifecycle events → frontend plugin    │
│  │     events (web_plugin_event)                               │
│  └── WebPluginRPC: routes pluginId.method calls to backend     │
│        plugin processes (web_plugin_rpc)                       │
└────────────────────────────────────────────────────────────────┘
```

## Trust model (instead of a sandbox)

- Installing a plugin = trusting the plugin (VSCode extension model).
- The type system guarantees contract correctness (API shapes, payload types, parameter correlation).
- Runtime defenses are minimal: contribution ID collision detection, version checks, ErrorBoundary crash isolation, a per-plugin enable/disable switch.
- Ecosystem governance: marketplace review + publisher signatures + permission lists shown on the plugin page.
- LLM-generated dynamic code (GenUI) keeps iframe rendering isolation — that is visual isolation, not a security boundary.

## Key concepts

| Concept | File | What it does |
|---|---|---|
| `PluginManifest` | `web/src/plugin-api/manifest.ts` | Typed contribution declarations + permissions |
| `PluginContext<P>` | `web/src/plugin-api/context.ts` | Capability-as-type: permissions determine the ctx shape |
| `EventMap` | `web/src/plugin-api/events.ts` | Event name ↔ payload indexed access |
| `BackendRPC` | `web/src/plugin-api/rpc.ts` | Method-table-driven typed RPC |
| `MatchedMessage<M>` | `web/src/plugin-api/renderer.ts` | Matcher conditions refine render parameter types |
| `ComponentDecl` | `web/src/plugin-api/components.ts` | L1 declarative UI components |
| `PluginExportsMap` | `web/src/plugin-api/plugins.ts` | Declaration-merged inter-plugin exports |
| `LayoutSlotId` | `web/src/plugin-runtime/layoutTypes.ts` | Named layout slots (mobile bottom nav, desktop sidebar, …) |
| `ContributionRegistry` | `web/src/plugin-runtime/registry.ts` | Single activation gate + hot reload |

## Quick start

```ts
// plugin source (compiled to ESM, served at /plugins/<id>/web/)
import type { PluginContext, PluginManifest } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'rpc', 'ui'] as const,
  contributes: [
    { kind: 'view', id: 'demo.panel', container: 'right_sidebar',
      title: 'Demo', entry: './panel' },
  ] as const,
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  ctx.events.on('turn.started', (ev) => {
    void ctx.rpc.call('session.get', { chatID: 'x' })
  })
  return () => { /* cleanup on unload */ }
}
```

## Relationship with the backend plugin system

The backend plugin system (Go native / stdio / gRPC / script runtimes) is a **separate system** from the Web plugin v2 runtime. A single plugin package can ship **both**: a `plugin.json` backend manifest plus a `Web` declaration (`entry` + `contributes`) that the backend serves verbatim to the frontend. The backend performs **transport-level checks only** (non-empty entry, valid plugin ID, safe static serving path) — contribution-point semantic validation lives solely in the frontend `registry.validate()` (single gate), so rules never drift between two places.

- Frontend ↔ backend communication: `web_plugin_list` (fetch manifests), `web_plugin_init` / `web_plugin_deactivate` (hot load/unload), `web_plugin_event` (backend → frontend events), `web_plugin_config_changed` (config hot reload), `web_plugin_rpc` (frontend → backend plugin method routing).
- Static serving: plugin web artifacts are served under `/plugins/<id>/web/*` (plugin ID validated against `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`, path traversal protected).

## Document index

| Doc | Topic |
|---|---|
| [Type-as-Contract](type-as-contract.md) | The four type-system weapons |
| [Manifest](manifest.md) | Typed contribution declarations |
| [PluginContext API](context-api.md) | Capability-as-type |
| [Events](events.md) | Typed event bus |
| [RPC](rpc.md) | Method-table-driven RPC |
| [Message Renderer](message-renderer.md) | Matcher-refined rendering |
| [Declarative Components](components.md) | L1 component declarations |
| [Inter-plugin Interop](interop.md) | Exports API + activation dependencies |
| [ESM Module Format](module-format.md) | Build and loading contract |
| [Hot Reload](hot-reload.md) | Reload/unload lifecycle |
| [Layout System](layout.md) | VSCode-style layout slots |
| [Editor View API](editor-view.md) | webviewPanel-style editor tabs |
| [Plugin Manager](plugin-manager.md) | The bootstrap management panel |
