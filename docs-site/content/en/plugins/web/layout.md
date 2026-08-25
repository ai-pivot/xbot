---
title: "Layout System"
weight: 12
---

The VSCode-style layout customization system lets users (and plugins) move UI elements between **named slots**. Types live in `web/src/plugin-runtime/layoutTypes.ts`; the registry in `web/src/plugin-runtime/layoutRegistry.ts`.

## Layout slots

```ts
export type LayoutSlotId =
  | 'mobile.bottom_nav'      // mobile bottom navigation
  | 'mobile.top_bar'         // mobile top bar right actions (+ / settings)
  | 'desktop.activity_bar'   // desktop left ActivityBar
  | 'desktop.sidebar'        // desktop right sidebar panel tabs
  | 'desktop.info_bar'       // desktop bottom InfoBar
  | 'desktop.main'           // desktop main editor area (Dockview editor tabs)
```

## Layout items

```ts
export interface LayoutItem {
  /** Unique id (built-ins like "mobile.view.agent", plugin views like "xbot.git-fancy.panel"). */
  id: string
  /** Default slot. */
  slot: LayoutSlotId
  /** Display title. */
  title: string
  /** i18n key (optional): overrides title for UI translation. */
  labelKey?: string
  /** Optional lucide icon name. */
  icon?: string
  /** Sort weight (ascending within a slot, default 0). */
  weight?: number
  /** Whether the user may move it (default true). */
  movable?: boolean
  /** Group id (optional): same-group items collapse together. */
  group?: string
}
```

## Plugin integration: views auto-register as items

A plugin view contribution automatically becomes a movable layout item — default slot mapped from its `container` (`VIEW_CONTAINER_TO_SLOT`):

```ts
export const VIEW_CONTAINER_TO_SLOT: Record<string, LayoutSlotId> = {
  right_sidebar: 'desktop.sidebar',
  bottom: 'desktop.info_bar',
  info_bar: 'desktop.info_bar',
  panel: 'desktop.sidebar',
  status_bar_right: 'desktop.info_bar',
  iteration: 'desktop.sidebar',
  main: 'desktop.main',
}
```

`PluginRuntimeBootstrap` syncs views → layout registry (weight 100, after built-ins; `dynamic` views skipped — they have no static entry). Unloaded/removed views are unregistered automatically. Users can then move plugin views into the bottom nav, top bar, activity bar, or the main editor area.

## User overrides & ordering

```ts
/** User overrides: itemId → target slot (unlisted items stay in their default slot). */
export type LayoutOverrides = Record<string, LayoutSlotId>

/** User ordering: slot → ordered itemId list. */
export type LayoutOrder = Partial<Record<LayoutSlotId, string[]>>

/** Group collapse state: groupId → collapsed (frontend-only persistence). */
export type LayoutCollapseState = Record<string, boolean>
```

Storage keys:

```ts
export const LAYOUT_OVERRIDES_KEY = 'xbot:layout:overrides'
export const LAYOUT_ORDER_KEY = 'xbot:layout:order'
export const LAYOUT_COLLAPSED_KEY = 'xbot:layout:collapsed'
```

`itemsFor(slot)` sorting rule: items listed in the slot's order array sort by array index first; unlisted items (newly installed plugins) append **after** the ordered ones, by weight — new plugins land stably at the tail without disturbing the user's arrangement.

## Registry operations

```ts
export class LayoutRegistryImpl {
  register(item: LayoutItem): void                 // idempotent: same id overwrites
  registerAll(items: LayoutItem[]): void
  unregister(id: string): void                     // plugin unload
  itemsFor(slot: LayoutSlotId): LayoutItem[]       // defaults + overrides, sorted
  moveItem(id: string, targetSlot: LayoutSlotId): void
  moveItemTo(id: string, targetSlot: LayoutSlotId, opts?: { beforeId?: string | null }): void
  setSlotOrder(slot: LayoutSlotId, orderedIds: string[]): void
  getSlotOrder(slot: LayoutSlotId): string[]
  resetItem(id: string): void                      // back to default slot
  resetAll(): void                                 // clear overrides + order
  getOverrides(): LayoutOverrides
  isCollapsed(groupId: string): boolean
  setCollapsed(groupId: string, collapsed: boolean): void
  toggleCollapsed(groupId: string): void
  allItems(): LayoutItem[]
  subscribe(listener: () => void): () => void
}
```

`moveItemTo` details (drag-reorder core API):

- Cross-slot move removes the id from the source slot's order array (one id lives in one slot).
- The order array is written as a **full-order snapshot at move time** (including unexplicitly-ordered items) — the moved item lands where the user actually sees it, not at an abstract "ordered items first" position.
- `setSlotOrder` dedups and keeps unknown ids (position memory — the item returns to its slot when it comes back).

## Persistence: localStorage + backend sync

```ts
private saveOverrides(): void {
  localStorage.setItem(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
  // Backend sync (web:ui:layout-overrides → user_settings table). localStorage is
  // the fast read path; the backend is the authoritative source — switching
  // browser/device pulls back the same layout via syncAndMigrateSettings.
  syncSettingToServer(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
}
```

- Backend stores only **slot assignment + ordering** (`web:ui:layout-overrides`, `web:ui:layout-order` in the user_settings table).
- Widths/heights/collapse state are frontend-only details (localStorage), never sent to the backend.
- `SETTINGS_SYNCED_EVENT` (fired when the backend copy is pulled to localStorage after a device switch) reloads overrides + order and notifies subscribers.

## React hooks

```ts
export function useLayoutItems(slot: LayoutSlotId): LayoutItem[]
export function useLayoutConfig(): {
  allItems: LayoutItem[]
  overrides: LayoutOverrides
  moveItem: (id: string, slot: LayoutSlotId) => void
  moveItemTo: (id: string, slot: LayoutSlotId, opts?: { beforeId?: string | null }) => void
  resetItem: (id: string) => void
  resetAll: () => void
}
export function useLayoutCollapse(): {
  isCollapsed: (groupId: string) => boolean
  setCollapsed: (groupId: string, collapsed: boolean) => void
  toggleCollapsed: (groupId: string) => void
}
```

## Built-in items

`registerBuiltinLayoutItems()` registers the default UI at app startup (`web/src/plugin-runtime/layoutRegistry.ts`): mobile top-bar items (new chat / tools / settings), desktop activity-bar sessions, and the five desktop sidebar tool panels (files / search / info / tasks / terminal), grouped via `LAYOUT_GROUPS` (`channels` / `tools` / `plugins`). Layout item text uses `labelKey` i18n keys (must be real i18n keys — a missing key renders the raw key string).

## Drag & drop

Both the desktop sidebars and the settings layout panel support HTML5 drag & drop reordering: dragStart records the source id in state (`dragOver` cannot read `dataTransfer`), pointer position relative to the target header decides before/after insertion, and `computeReorder` (`web/src/lib/reorder.ts`) returns `null` for a no-op (nothing persisted). In the settings panel, dropping onto an item inserts before it (`moveItemTo(beforeId)`), matching the real-UI semantics.
