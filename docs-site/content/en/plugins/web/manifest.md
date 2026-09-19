---
title: "Manifest"
weight: 3
---

A Web plugin's manifest is the single source of truth: it is both the runtime data served by the backend and the type source for compile-time checking. Defined in `web/src/plugin-api/manifest.ts`.

## PluginManifest

```ts
export interface PluginManifest {
  /** Globally unique plugin id (e.g. "xbot.git-info"). */
  id: string
  name: string
  version: string
  description?: string
  /** Capability declarations — determine the ctx shape (type-as-contract §capability-as-type). */
  permissions?: readonly Permission[]
  /** Hard dependencies: plugin ids that must be activated BEFORE this plugin (topological activation). */
  activationDependencies?: readonly string[]
  /** Typed contribution declarations. */
  contributes: readonly Contribution[]
  /** Frontend entry module (ESM). Purely backend plugins have no such field. */
  entry?: string
}
```

## Permissions

```ts
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config'
```

Each permission unlocks one capability interface on `PluginContext` (see [PluginContext API](context-api.md)). The backend permission whitelist (`plugin/permissions.go`, `allPermissions`) must stay in sync with this list — adding a new frontend `Permission` value requires adding the matching constant there too.

## Contributions

`Contribution` is a discriminated union of eight members:

### View — `kind: 'view'`

```ts
export type ViewContainer = 'right_sidebar' | 'panel' | 'bottom' | 'info_bar' | 'status_bar_right' | 'iteration' | 'main'

export interface ViewContribution {
  kind: 'view'
  /** Globally unique: <pluginId>.<viewId>. */
  id: string
  /** Render container. */
  container: ViewContainer
  title: string
  icon?: string
  /** ESM module path (relative to plugin package root). The default export is the view. */
  entry?: string
  /** L1 declarative view: type + props (no entry needed). */
  component?: ComponentDecl
  /** Alignment inside the container: 'start' (default) or 'end'. */
  align?: 'start' | 'end'
  /** Parameterized dynamic view: no static entry; opened only via ctx.ui.openViewTab. */
  dynamic?: boolean
}
```

- A view's panel tab appears automatically in **both** desktop and mobile sidebars (`usePluginViewPanels`) — declare once, render everywhere.
- `dynamic: true` views are filtered out of sidebars and the layout registry; they are opened only via `ctx.ui.openViewTab({ viewId, params })` (VSCode webviewPanel semantics — see [Editor View API](editor-view.md)).
- `container: 'main'` maps to the desktop main editor area (`desktop.main` slot) — the view renders as a full-width editor tab.

### Command — `kind: 'command'`

```ts
export interface CommandContribution {
  kind: 'command'
  id: string
  title: string
  keybinding?: string
  /** Enable/disable condition expression (reserved for a future when-evaluator). */
  when?: string
}
```

The handler is resolved at runtime: first the manifest's optional `handlers[id]`, then the module's exported `commandHandlers[id]` (`registry.ts` `mount`).

### Message renderer — `kind: 'messageRenderer'`

Renders tool results / messages in the chat flow. Matching conditions refine the render parameter type. See [Message Renderer](message-renderer.md).

### Toolbar / Context menu

```ts
export interface ToolbarContribution {
  kind: 'toolbar'
  id: string
  title: string
  icon?: string
  /** Command id executed on click. */
  command: string
}

export interface ContextMenuContribution {
  kind: 'contextMenu'
  id: string
  title: string
  /** Matches message/file type (reserved). */
  when?: string
  command: string
}
```

### Setting — `kind: 'setting'`

```ts
export interface SettingContribution {
  kind: 'setting'
  key: string
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect'
  label: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string }>
  /** Group name: properties in the same section are grouped in the settings panel. */
  section?: string
  /** Sensitive value: masked input in the UI. */
  secret?: boolean
  placeholder?: string
  required?: boolean
}
```

### Event handler — `kind: 'eventHandler'`

```ts
export interface EventHandlerContribution<E extends keyof EventMap = keyof EventMap> {
  kind: 'eventHandler'
  event: E
  /** Handler module path. The module exports handler(payload: EventMap[E]). */
  entry: string
  /** Permission required for the subscription (defaults to plugin permissions). */
  permission?: string
}
```

### Theme — `kind: 'theme'`

```ts
export interface ThemeContribution {
  kind: 'theme'
  cssVars: Record<string, string>
}
```

## Runtime registration (ContributionAPI)

`activate` can dynamically register extra contribution points. Each registration returns a disposable, cleaned up automatically on unload:

```ts
export interface ContributionAPI {
  register(contribution: Contribution): Disposable
  registerAll(contributions: readonly Contribution[]): Disposable
}
```

## Disposable

```ts
/** Cleanup handle: calling it releases resources; idempotent. */
export type Disposable = () => void
```

## PluginMeta

```ts
/** Lifecycle metadata passed to activate. */
export interface PluginMeta {
  id: string
  version: string
}
```

## Validation (single gate)

The frontend `ContributionRegistry.validate()` is the only place that validates contribution semantics (`web/src/plugin-runtime/registry.ts`):

- `id`/`name`/`version` must be present; `contributes` must be an array.
- Each contribution must have a known `kind` and a non-empty `id`.
- Contribution ids must be unique **across plugins** — except `messageRenderer`, where duplicates are legal (priority chain).
- All `activationDependencies` must already be active.

The backend performs only transport-level checks (non-empty entry, valid plugin ID, safe static path). Never add a second semantic-validation layer on the backend — two gates drift.

### Localization (`web.i18n`)

Plugin copy ships **with the plugin** — never in the host's i18n files:

```json
{
  "web": {
    "entry": "index.js",
    "i18n": { "en": { "save": "Save" }, "zh-CN": { "save": "保存" }, "ja": { "save": "保存" } }
  }
}
```

`ctx.i18n.t(key, fallback)` resolves against the host's current locale with this chain:
exact locale → language prefix (`zh-TW`/`zh-HK` fall back to `zh-CN`) → `en` → first available
locale → your `fallback` argument → the key itself. `ctx.i18n` is available to **every** plugin
(no permission needed) and reads the locale at call time, so switching the host language updates
the plugin UI immediately. Either make the fallback argument the English string, or keep all three
tables complete — otherwise the plugin shows fallback text where the host shows another language.

> ⚠️ **Manifests are read once at server startup.** Editing `plugin.json`
> (`web.i18n` / `web.entry` / `permissions` / `version` …) does **not** change anything until you
> reload plugins (`config action=reload_plugins`, logged as `Plugin discovered plugin=<id>`) or
> restart the server. Plugin **web assets** (`/plugins/<id>/web/*.js`) are served from disk per
> request, so they update immediately — which makes "the new bundle runs but the manifest is old"
> a real trap (symptom: `ctx.i18n` silently falls back to your fallback strings).

### Containers (`container`)

`right_sidebar` · `panel` · `bottom` · `info_bar` · `status_bar_right` · `iteration` · `main`.

- `main` renders as a full-width editor tab in the desktop main area.
- `info_bar` renders in the **desktop bottom status bar** — the same row as the app's "check for
  updates" button, ideal for a compact status chip (e.g. the ssh-runner plugin shows the session's
  bound runner there and lets you switch it with a click).
