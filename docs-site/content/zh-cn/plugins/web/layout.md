---
title: "布局定制系统"
weight: 12
---

VSCode 式布局定制系统让用户（与插件）在**命名槽位**之间移动 UI 元素。类型在 `web/src/plugin-runtime/layoutTypes.ts`；注册表在 `web/src/plugin-runtime/layoutRegistry.ts`。

## 布局槽位

```ts
export type LayoutSlotId =
  | 'mobile.bottom_nav'      // 手机底部导航条
  | 'mobile.top_bar'         // 手机顶栏右侧操作区（+ / 设置等）
  | 'desktop.activity_bar'   // 桌面左侧 ActivityBar
  | 'desktop.sidebar'        // 桌面右侧边栏面板 tab
  | 'desktop.info_bar'       // 桌面底部 InfoBar
  | 'desktop.main'           // 桌面主编辑区（Dockview 的 editor tab）
```

## 布局项

```ts
export interface LayoutItem {
  /** 唯一 id（内置项如 "mobile.view.agent"，插件 view 如 "xbot.git-fancy.panel"）。 */
  id: string
  /** 默认 slot。 */
  slot: LayoutSlotId
  /** 显示标题。 */
  title: string
  /** i18n key（可选）：优先于 title 做界面翻译。 */
  labelKey?: string
  /** 可选 lucide 图标名。 */
  icon?: string
  /** 排序权重（同 slot 内升序，默认 0）。 */
  weight?: number
  /** 是否允许用户移动（默认 true）。 */
  movable?: boolean
  /** 分组 id（可选）。同组项折叠在一起。 */
  group?: string
}
```

## 插件接入：view 自动注册为布局项

插件 view 贡献点自动成为可移动布局项——默认 slot 由 `container` 映射（`VIEW_CONTAINER_TO_SLOT`）：

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

`PluginRuntimeBootstrap` 同步 view → 布局注册表（weight 100，排在内置项之后；`dynamic` 视图跳过——无静态入口）。卸载/移除的 view 自动注销。用户随后可把插件 view 移入底部导航、顶栏、ActivityBar 或主编辑区。

## 用户覆盖与排序

```ts
/** 用户覆盖：itemId → 目标 slot（未列出的项留在默认 slot）。 */
export type LayoutOverrides = Record<string, LayoutSlotId>

/** 用户排序：slot → 有序 itemId 列表。 */
export type LayoutOrder = Partial<Record<LayoutSlotId, string[]>>

/** 分组折叠状态：groupId → 是否收起（纯前端持久化）。 */
export type LayoutCollapseState = Record<string, boolean>
```

存储键：

```ts
export const LAYOUT_OVERRIDES_KEY = 'xbot:layout:overrides'
export const LAYOUT_ORDER_KEY = 'xbot:layout:order'
export const LAYOUT_COLLAPSED_KEY = 'xbot:layout:collapsed'
```

`itemsFor(slot)` 排序规则：slot 的 order 数组里的项按数组索引优先；未记录的项（新装插件）**追加在已排序项之后**、按 weight 兜底——新插件稳定落尾，不打乱用户已排的顺序。

## 注册表操作

```ts
export class LayoutRegistryImpl {
  register(item: LayoutItem): void                 // 幂等：同 id 覆盖
  registerAll(items: LayoutItem[]): void
  unregister(id: string): void                     // 插件卸载
  itemsFor(slot: LayoutSlotId): LayoutItem[]       // 默认 + 覆盖后，已排序
  moveItem(id: string, targetSlot: LayoutSlotId): void
  moveItemTo(id: string, targetSlot: LayoutSlotId, opts?: { beforeId?: string | null }): void
  setSlotOrder(slot: LayoutSlotId, orderedIds: string[]): void
  getSlotOrder(slot: LayoutSlotId): string[]
  resetItem(id: string): void                      // 恢复默认 slot
  resetAll(): void                                 // 清空覆盖 + 排序
  getOverrides(): LayoutOverrides
  isCollapsed(groupId: string): boolean
  setCollapsed(groupId: string, collapsed: boolean): void
  toggleCollapsed(groupId: string): void
  allItems(): LayoutItem[]
  subscribe(listener: () => void): () => void
}
```

`moveItemTo` 细节（拖拽重排核心 API）：

- 跨 slot 移动自动从原 slot 的 order 数组移除该 id（一个 id 只在一个 slot）。
- order 数组按**移动时的完整顺序快照**写入（含未显式排序的项）——被移项落在用户看到的真实位置，而不是"已排序项优先"的抽象位置。
- `setSlotOrder` 去重并保留未知 id（位置记忆——项回来时恢复原位）。

## 持久化：localStorage + 后端同步

```ts
private saveOverrides(): void {
  localStorage.setItem(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
  // 后端同步（web:ui:layout-overrides → user_settings 表）。localStorage 是
  // 秒回的读路径，后端是权威源 —— 换浏览器/设备后 syncAndMigrateSettings
  // 拉回同一份布局覆盖。
  syncSettingToServer(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
}
```

- 后端只存**槽位归属 + 顺序**（user_settings 表的 `web:ui:layout-overrides`、`web:ui:layout-order`）。
- 宽度/高度/折叠状态是纯前端细节（localStorage），不上后端。
- `SETTINGS_SYNCED_EVENT`（换设备后后端副本拉回 localStorage 时触发）重新加载覆盖 + 排序并通知订阅者。

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

## 内置项

`registerBuiltinLayoutItems()` 在应用启动时注册默认 UI（`web/src/plugin-runtime/layoutRegistry.ts`）：手机顶栏项（新会话 / 工具 / 设置）、桌面 ActivityBar 会话、桌面侧栏五个工具面板（文件 / 搜索 / 信息 / 任务 / 终端），经 `LAYOUT_GROUPS`（`channels` / `tools` / `plugins`）分组。布局项文本走 `labelKey` i18n key（必须是真实 i18n key——不存在的 key 会显示原始 key 字符串）。

## 拖拽

桌面侧栏与布局设置面板都支持 HTML5 拖拽重排：dragStart 把源 id 记入 state（`dragOver` 读不到 `dataTransfer`）、指针相对目标 header 中线判 before/after 插入、`computeReorder`（`web/src/lib/reorder.ts`）no-op 返回 null（不持久化）。设置面板拖到项上 = 插到该项前（`moveItemTo(beforeId)`），与真实 UI 语义一致。
