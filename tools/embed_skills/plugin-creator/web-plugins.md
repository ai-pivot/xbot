# Web Plugins (v2 — 类型即契约 / Type-as-Contract)

Frontend ESM plugin runtime. Web plugins are **compiled ESM modules** loaded and executed directly in the browser — the backend has **no JS VM**. The backend (Go / stdio process model) only does: manifest delivery + event bridge + RPC routing + static hosting.

Design doc: `docs/agent/web-plugin-system.md`.

## Core Principles

1. **无后端 VM（No server-side VM）** — frontend plugin = compiled ESM module loaded in-browser; backend plugins remain Go native / stdio process.
2. **无沙箱（No sandbox）** — plugins are trusted code (trust = install, like VSCode/browser extensions). The type system enforces **contract correctness**, not a security boundary.
3. **类型即契约（Type-as-Contract）** — a real type system (discriminated unions, capability-as-type, conditional-type refinement, declaration merging) constrains everything a plugin can do at **compile time**: contribution shapes, capability APIs, event payloads, RPC methods, renderer matcher↔param association.
4. **单一启动门控（Single gate）** — contribution-semantics validation happens ONLY in the frontend runtime (`registry.validate()`). The backend does NOT schema-validate contributions; it only does transport-level checks (`Web.Entry` non-empty, plugin ID valid, static-path safe).

## Backend Side (Go)

The manifest's `Web` field declares the frontend module (`plugin/plugin.go`):

```go
type PluginManifest struct {
    // ...
    Web *WebPluginDecl `json:"web,omitempty"`
}

type WebPluginDecl struct {
    Entry      string          `json:"entry"`       // frontend module path relative to web/ dir, e.g. "index.js"
    Contributes json.RawMessage `json:"contributes,omitempty"` // opaque, forwarded verbatim to frontend
}
```

Key backend facts:

- **`contributes` is an opaque JSON blob** passed verbatim to the frontend runtime. The frontend is the single authoritative gate for contribution semantics (shapes, permission↔capability correspondence, ID uniqueness). Do NOT add backend schema validation — it would diverge from the frontend rules (dual-gate drift, lesson from cordis).
- **Static hosting**: `web/` dir is served at `/plugins/<id>/web/*` (`channel/web/web.go` `handlePluginStatic`). Plugin ID must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` (`isValidPluginIDForServe`); the sub-path must resolve inside `<pluginDir>/<id>/web/` (path-traversal guard).
- **`web_plugin_list` RPC** (`serverapp/rpc_table.go`) returns all plugins with a non-nil `Web.Entry`, as `{id, name, version, state, enabled, permissions, entry, module_url, contributes}`.
- **`web_plugin_rpc` RPC**: frontend plugin calls a backend method (JSON-RPC 2.0). Method with a `pluginId.` prefix routes to that backend plugin (stdio RPC); otherwise it hits the core RPC table. Plugin-ID resolution uses **longest-prefix match over activated plugin IDs** — never `SplitN(".", ...)` because plugin IDs themselves can contain dots (`xbot.git-fancy`).

## Type Package: `@xbot/plugin-api`

Source: `web/src/plugin-api/`. This is the compile-time contract. Modules:

| Module | Contents |
|--------|----------|
| `manifest.ts` | `Permission`, `ViewContainer`, `Contribution` (8 variants), `PluginManifest`, `Disposable`, `ContributionAPI` |
| `context.ts` | `PluginContext<P>` — capability-as-type; `CommandsAPI` |
| `events.ts` | `EventMap` — event name → payload indexed access |
| `rpc.ts` | `BackendRPC` — method table; `RPCAPI` |
| `renderer.ts` | `MessageRendererContribution` — matcher refines render-param type; `ToolResultMap` |
| `components.ts` | `ComponentDecl` — declarative component props (type narrows with `type`) |
| `plugins.ts` | `PluginExportsMap` + `PluginsAPI` (inter-plugin interop) |
| `safe.ts` / `state.ts` / `ui.ts` / `progress.ts` | sanitized message types, state API, UI API, progress types |

## Contribution Types (8, all discriminate on `kind`)

```ts
type Contribution =
  | ViewContribution          // { kind:'view', id, container, title, icon?, entry? | component? }
  | CommandContribution       // { kind:'command', id, title, keybinding?, when? }
  | MessageRendererContribution // { kind:'messageRenderer', matches, priority, render }
  | ToolbarContribution       // { kind:'toolbar', id, title, icon?, command }
  | ContextMenuContribution   // { kind:'contextMenu', id, title, when?, command }
  | SettingContribution       // { kind:'setting', key, type, label, default?, options? }
  | EventHandlerContribution  // { kind:'eventHandler', event, entry, permission? }
  | ThemeContribution         // { kind:'theme', cssVars }
```

`ViewContainer` = `'right_sidebar' | 'panel' | 'bottom' | 'info_bar' | 'status_bar_right' | 'iteration'`.

`Permission` = `'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins'`.

Manifest uses `satisfies PluginManifest` so the compiler checks the shape AND preserves literal types (for `PluginContext<typeof manifest.permissions>`):

```ts
import type { PluginManifest } from '@xbot/plugin-api'
export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'commands', 'rpc'] as const,
  contributes: [
    { kind: 'view', id: 'demo.panel', container: 'right_sidebar', title: 'Demo', entry: './panel' },
    { kind: 'command', id: 'demo.hello', title: 'Hello', keybinding: 'ctrl+shift+h' },
  ] as const,
} satisfies PluginManifest
```

## Capability-as-Type: `PluginContext<P>`

`permissions` determines which capability APIs exist on `ctx`. Undeclared capability = **`never` at the type level** — accessing it is a compile error (no runtime Proxy whitelist):

```ts
export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & {
  readonly meta: PluginMeta
  readonly contributes: ContributionAPI // dynamic contribution registration (all plugins)
}

export function activate(ctx: PluginContext<typeof manifest.permissions>): Disposable | void {
  ctx.events.on('message.committed', ev => { /* OK: declared events */ })
  // ctx.state.getSession()  // ✗ compile error — 'state' not declared
  return () => { /* cleanup */ }
}
```

- `PermissionAPI` maps `events`→`EventsAPI`, `commands`→`CommandsAPI`, `rpc`→`RPCAPI`, `state`→`StateAPI`, `ui`→`UIAPI`, `plugins`→`PluginsAPI`.
- `PluginContext<P>` is a **type function**, not runtime guarding. The compiler replaces runtime interception.

## Typed Event Bus / RPC / Renderer

- **Events** — `EventMap` keyed access: `ctx.events.on('message.committed', e => …)` infers `e` as `{turnID, message: SafeMessage}` with zero casts.
- **RPC** — `ctx.rpc.call('agent.send', {…})` returns `Promise<BackendRPC['agent.send']['result']>`; param shapes are compile-checked.
- **Renderer** — `matches` **refines the render-param type** (dependent-type-style):

```ts
{ kind:'messageRenderer', id:'x.viewer', priority:10, matches: { tool:'display_html' },
  render: msg => <CodeViewer code={msg.tool.result.code} /> }  // msg typed with tool.result.code
```

`matches: { tool:'shell' }` cannot access `display_html` fields; `matches: { role:'assistant' }` gets `SafeAssistantMessage`.

## Interop: Declaration Merging, Exports API, Activation Order

- **Declaration merging** (the legal kind — type-space extension, no runtime magic): a backend plugin publishes a `.d.ts` that merges into `EventMap` / `BackendRPC` / `ToolResultMap` / `PluginExportsMap`. Frontend plugins `import '@xbot/plugin-xbot-git/types'` to close cross-plugin contracts at compile time.
- **Exports API** (same-process, zero serialization): a plugin's public API = its **named module exports** (except reserved `manifest`/`activate`/`deactivate`). Consume via `ctx.plugins.get('xbot.git')` / `require` / `onActivated` / `onDeactivated`. `PluginExportsMap` starts as `{}` (declaration-merged extension point).
- **Activation dependencies**: `activationDependencies: ['xbot.git']` = strong deps, topologically sorted (Kahn, like Go's `DependencyResolver`). Cycle → skip with error; missing → plugin doesn't activate and reports it. Optional deps use `ctx.plugins.require` lazily.

Interop matrix:

| Direction | Mechanism | Type guarantee |
|-----------|-----------|----------------|
| frontend → frontend | `ctx.plugins.get/require` (same-process call) | `PluginExportsMap` merge |
| frontend → backend | `ctx.rpc.call('pluginId.method', …)` | `BackendRPC` merge |
| backend → frontend | `web_plugin_event` push | `EventMap` merge |
| backend → backend | existing `Dependencies` + tools/hooks/services | Go types |

## Frontend Runtime

Source: `web/src/plugin-runtime/`. Key pieces:

- `loader.ts` — ESM dynamic import. `activate` (URL import of third-party `/plugins/<id>/web/<entry>`) and `activateBuiltin` (static import of builtin views) share `activateModule`. Hot-reload = same ID `deactivate` (run disposables in reverse + remove contributions) then activate new.
- `registry.ts` — typed contribution registry; `validate()` is the **single gate** for contribution semantics (shapes, permission↔capability, ID uniqueness).
- `events.ts` / `commands.ts` / `rpc.ts` / `context.ts` / `state.ts` / `ui.ts` / `plugins.ts` / `sanitize.ts` — runtime implementations of the `@xbot/plugin-api` interfaces.
- `ViewSlot.tsx` / `PluginView.tsx` — view rendering + `PluginViewErrorBoundary` (a crashing plugin view collapses only its tab, never the whole app).
- `usePluginRuntimeHost.ts` — runtime host bootstrap; synths `runtime.listAllViews()` into layout registry on activation/unload/hot-reload.
- `usePluginViewPanels.ts` — `usePluginViewPanels(container)` returns panels for a container (desktop + mobile sidebars auto-appear from view contributions — **no hardcoded `case 'xxx'` in RightSidebar/MobileAppShell**).

## Layout Customization System

Source: `web/src/plugin-runtime/layoutTypes.ts` + `layoutRegistry.ts`. VSCode-style slots + movable items.

- **Slots** (`LayoutSlotId`): `mobile.bottom_nav`, `mobile.top_bar`, `desktop.activity_bar`, `desktop.sidebar`, `desktop.info_bar`.
- **LayoutItem**: `{id, slot, title, labelKey?, icon?, weight?, movable?}` registered to `layoutRegistry` (singleton). `itemsFor(slot)` returns actual items (defaults + user overrides, weight ascending); `useLayoutItems(slot)` subscribes.
- **User overrides** persist to `localStorage['xbot:layout:overrides']` (`itemId → target slot`); `moveItem` / `resetItem` / `resetAll`.
- **Builtin items** registered in `App.tsx` via `registerBuiltinLayoutItems()`. **Plugin views auto-register** via container→slot map (`VIEW_CONTAINER_TO_SLOT`, weight 100 after builtins); unload/hot-reload diffs to unregister.
- **Settings UI**: `SettingsLayout` (SettingsDialog → 「布局」 category, i18n `settings.nav.layout`) lists all items + target-slot select + per-item/global reset.
- ⚠️ Layout-item text goes through `labelKey` i18n (priority over `title`); builtin `labelKey`s MUST be real i18n keys (e.g. `sidebar.sessions`, `agent.tools`) — a nonexistent key renders raw.

## Builtin Plugins

`web/src/plugins/`:

- `manager/` — self-hosting plugin manager panel (`xbot.plugin-manager`), lists plugins (state/deps/permissions), enable/disable/uninstall/reload with instant effect; built only on the public API.
- `git-info/` — `GitStatusPanel.tsx` (fancy view).
- `git-fancy/` — `entry.tsx`.
- `iteration-stats/` — `IterationStatsPanel.tsx`.

Builtin views use `builtin:` prefix marking; `builtinViews` map (key = view.id) does a **static import** of components. ⚠️ **Builtin views MUST be static-imported** — never dynamic `import()`. Dynamic import splits them into separate rolldown chunks and hoists `useState`/`useEffect`/`useCallback` onto the wrong `vendor-framer-motion` export → React error #311 (hooks mismatch) → full frontend black screen. Only third-party `/plugins/<id>/web/` modules use dynamic import.

## Development Workflow (add a Web plugin)

1. Decide: **pure-frontend** view/command vs **frontend + backend** (backend for RPC/events/tools via stdio).
2. Author the frontend module against `@xbot/plugin-api` (`web/src/plugin-api/`), export `manifest` + `activate`.
3. Build the ESM chunk into `<plugin>/web/` (e.g. `web/index.js`).
4. Declare in `plugin.json`: `"web": { "entry": "index.js", "contributes": { … } }`. Directory name must match the plugin ID.
5. Place under `~/.xbot/plugins/<id>/` (user) or `<project>/.xbot/plugins/<id>/`.
6. Reload plugins, then fetch `web_plugin_list` → frontend `activate` imports the versioned module URL.

## Gotchas

- **Single gate**: never add backend JSON-Schema validation of `contributes` — frontend `registry.validate()` is authoritative.
- **Plugin ID may contain dots** (`xbot.git-fancy`) — RPC routing uses longest-prefix match over plugin IDs, NOT `SplitN(".", …)`.
- **Builtin views must be static-imported** (React #311 black screen otherwise). `builtinViews` map, `builtin:` prefix.
- **`createPortal` is from `react-dom`**, not `react` (React 19 removed it from the `react` package).
- **L1 declarative components**: component props narrow by the `type` discriminant — wrong props fail compile.
- **`pluginIcon(name)`** maps view `icon` strings to lucide icons (in `pluginIcons.ts`).
- **Layout `labelKey` must be a real i18n key.**
- **`PluginPanelContainer` + `WidgetZone`** split: InfoBar renders both `PluginPanelContainer(container="info_bar")` and `WidgetZone(zone="infoBar", excludePrefixes=["git:"])` — avoid double-rendering git status.
- **Manifest `satisfies`**, not annotation, to preserve literal types for `PluginContext<typeof manifest.permissions>`.