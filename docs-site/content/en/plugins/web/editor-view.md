---
title: "Editor View API"
weight: 13
---

The Editor View API gives plugins VSCode-style editor tabs: open a parameterized plugin view, a host file editor, or a native diff editor in the **main editor area** — with imperative control handles. Types in `web/src/plugin-api/ui.ts`; runtime in `web/src/plugin-runtime/editorTabs.ts` + `editorRegistry.ts`.

## The module-level bridge

PluginRuntime/PluginUI are plain TS instances **outside** the React tree, while `tabManager` lives in AppShell (inside the tree). A module-level mutable registrar decouples them:

```
AppShell (on mount)   → registerEditorTabOpener(tabManager.openTab)
PluginUI.openViewTab() → openEditorViewTab(options)
```

```ts
export type EditorTabOpener = (input: {
  type: 'plugin' | 'file' | 'diff'
  title: string
  icon?: string
  closable?: boolean
  data?: Record<string, unknown>
}) => string

/** AppShell registers tabManager.openTab on mount (passes null on unmount to clean up). */
export function registerEditorTabOpener(fn: EditorTabOpener | null): void
```

## openViewTab — parameterized dynamic views

```ts
export interface OpenViewTabOptions {
  /** Plugin view contribution id (must be declared; any container — editor tabs render full-width). */
  viewId: string
  /** Tab title (e.g. file path / commit short hash). */
  title: string
  /** Lucide icon name (optional). */
  icon?: string
  /** Dedup logical key: same key focuses the existing tab, different key opens a new tab. */
  key?: string
  /** Props passed to the view component (e.g. { path, commit }). */
  params?: Record<string, unknown>
}
```

Semantics (VSCode webviewPanel model):

- `viewId` must be a declared view contribution; declare `dynamic: true` for views that have no static entry (they are filtered from sidebars and the layout registry — openable only via `openViewTab`).
- `key` is the tab dedup key: same key focuses the existing tab, different keys open separate tabs (`PanelParams.viewKey` → `tabLogicalKey` → `plugin-view:${key}`).
- `params` are passed **as props** to the view component — `PluginView` spreads `panelParams.viewParams` onto the component (`<state.comp {...(viewParams ?? {})} />`).
- The dockview panel component must be the `viewId` (`openTab` sets `component: input.data.viewId ?? 'plugin'`) — `renderPluginView` looks up by `view.id === component`; a generic `'plugin'` component name never matches (historical blank-tab bug).

The wrapper in `editorTabs.ts`:

```ts
export function openEditorViewTab(options: OpenViewTabOptions): string {
  if (!opener) {
    console.warn('[plugin-runtime] openViewTab: editor tab opener 尚未注册（AppShell 未挂载）', options)
    return ''
  }
  return opener({
    type: 'plugin',
    title: options.title,
    icon: options.icon,
    closable: true,
    data: { viewId: options.viewId, viewKey: options.key, viewParams: options.params },
  })
}
```

## openFileTab — host file editor

```ts
export interface OpenFileTabOptions {
  /** Tab title (defaults to file name). */
  title?: string
  /** Dedup logical key (same key focuses the existing tab; defaults to path). */
  key?: string
  /** Jump to line after open (1-based, centered). */
  line?: number
  /** Highlight line range after open (start ≤ end). */
  highlight?: { startLine: number; endLine?: number }
  /** Override syntax-highlighting language (default: inferred from extension). */
  language?: string
  /** Override initial view (only markdown supports preview). Default by extension. */
  viewMode?: 'editor' | 'preview'
}

openFileTab(path: string, opts?: OpenFileTabOptions): EditorHandle
```

Returns an **EditorHandle** — imperative control that stays valid after the tab opens; methods become no-ops returning `false` once the tab closes:

```ts
export interface EditorHandle {
  readonly editorId: string
  revealLine(line: number, opts?: { center?: boolean }): boolean
  revealRange(startLine: number, endLine: number): boolean
  setSelection(startLine: number, startCol?: number, endLine?: number, endCol?: number): boolean
  setCursorPosition(line: number, column?: number): boolean
  highlightLines(startLine: number, endLine?: number, opts?: { className?: string }): boolean
  clearHighlights(): boolean
  getContent(): string | null          // content edits do NOT persist (same as manual editing)
  setContent(text: string): boolean
  setLanguage(language: string): boolean
  setTitle(title: string): boolean
  setViewMode(mode: 'editor' | 'preview'): boolean
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}
```

## openDiffTab — native Monaco diff editor

```ts
export interface OpenDiffTabOptions {
  title: string
  /** Old content (left/top). */
  original: string
  /** New content (right/bottom). */
  modified: string
  /** File path (host infers highlight language, optional). */
  path?: string
  /** Dedup logical key. */
  key?: string
  /** Scope label (e.g. "commit abc1234" / "workspace"). */
  scope?: string
}

export interface DiffHandle {
  readonly editorId: string
  nextDiff(): boolean
  prevDiff(): boolean
  setRenderSideBySide(sideBySide: boolean): boolean
  setTitle(title: string): boolean
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}
```

The plugin passes **only the two content sides** — zero rendering code. The host does language inference + Monaco rendering + diff navigation (syntax highlighting, line-level coloring, side-by-side/inline navigation).

## Deterministic editorIds

```ts
export function editorIdForFile(path: string): string { return `ed-file:${path}` }
export function editorIdForDiff(diffKey: string): string { return `ed-diff:${diffKey}` }
```

The id is derived deterministically — the same file / same diff key yields a constant id, so:

- Repeated `open` calls get the **same handle** (no session-level mapping).
- Layout-restored tabs (params carry the id after refresh) naturally re-attach to the same handle.

## Handle routing

```ts
/** FilePanel/DiffPanel mount registers a controller; returns detach. */
export function attachEditor(editorId: string, controller: EditorController | DiffController): () => void
```

Handles are **decoupled from instances**: every method looks up the registry at call time (`withEntry`). Tab closed (panel unmounted) → registry miss → no-op returning `false` — plugins never need to track editor lifecycle. `onClose` callbacks broadcast when the panel detaches (guarded so a replaced instance's detach does not disturb the new one).

## Reference implementation: xbot.git-fancy

`web/src/plugins/git-fancy/` is the canonical multi-entry editor-view plugin:

- `index.tsx` — main entry + sidebar panel (change list + commit history pagination). Clicks call `openDiffTab` / `openViewTab` for file diffs and commit details.
- `diff.tsx` / `commit.tsx` — separate view entries (must be declared as views with their own `entry`; built with esbuild `--splitting` so the shared `rpc`/`ui` singletons injected in `activate(ctx)` are visible to every entry).
- Permissions: the manifest **must include every capability actually used** — `openDiffTab` goes through `ctx.ui.openViewTab`, so omitting `"ui"` makes `ctx.ui` undefined (buildContext injects by permission) and clicks silently do nothing (no error, no log).

## Wiring in PluginUI

`web/src/plugin-runtime/ui.ts` `PluginUI.openViewTab/openFileTab/openDiffTab` prefer host-provided implementations (`UIServices.openViewTab?` etc., used as test doubles) and fall back to the `editorTabs` registrar.
