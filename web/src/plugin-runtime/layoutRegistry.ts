/**
 * LayoutRegistry —— 布局 slot 注册表。
 *
 * - 内置项注册：BUILTIN_LAYOUT_ITEMS 在应用启动时注册到默认 slot。
 * - 插件 view 自动注册：PluginRuntime 激活插件时把 view 贡献点注册为布局项。
 * - 用户覆盖：moveItem(id, targetSlot) 写 localStorage，itemsFor(slot) 返回
 *   该 slot 的实际项（默认 + 被移入的 - 被移走的，按 weight 排序）。
 * - 订阅：subscribe(listener) 让 UI（MobileAppShell/AppShell/设置面板）响应式。
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import {
  BUILTIN_LAYOUT_ITEMS,
  LAYOUT_COLLAPSED_KEY,
  LAYOUT_GROUPS,
  LAYOUT_OVERRIDES_KEY,
  LAYOUT_ORDER_KEY,
  type LayoutCollapseState,
  type LayoutItem,
  type LayoutOrder,
  type LayoutOverrides,
  type LayoutSlotId,
} from './layoutTypes'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'

/** view container → 默认布局 slot 映射（插件 view 自动注册用）。 */
export const VIEW_CONTAINER_TO_SLOT: Record<string, LayoutSlotId> = {
  // 布局 v2：右栏已删——插件声明的 right_sidebar container（协议不变）映射到
  // 左栏 activity_bar slot（SidebarSectionStack 的独立 section）。
  right_sidebar: 'desktop.activity_bar',
  bottom: 'desktop.info_bar',
  info_bar: 'desktop.info_bar',
  panel: 'desktop.sidebar',
  status_bar_right: 'desktop.info_bar',
  iteration: 'desktop.sidebar',
  main: 'desktop.main',
}

class LayoutRegistryImpl {
  private items = new Map<string, LayoutItem>()
  private overrides: LayoutOverrides = {}
  private order: LayoutOrder = {}
  private collapsed: LayoutCollapseState = {}
  private listeners = new Set<() => void>()

  constructor() {
    this.loadOverrides()
    this.loadOrder()
    this.loadCollapsed()
    // 后端已有覆盖时（换浏览器/设备，syncAndMigrateSettings 拉取 server →
    // localStorage），重新加载并通知订阅者。仅在浏览器环境（单例模块在
    // SSR/单测里可能无 window）。
    if (typeof window !== 'undefined') {
      window.addEventListener(SETTINGS_SYNCED_EVENT, this.onSettingsSynced)
    }
  }

  private onSettingsSynced = (): void => {
    this.loadOverrides()
    this.loadOrder()
    this.notify()
  }

  /** 注册一个布局项（幂等：同 id 覆盖）。 */
  register(item: LayoutItem): void {
    this.items.set(item.id, item)
    this.notify()
  }

  /** 批量注册。 */
  registerAll(items: LayoutItem[]): void {
    for (const it of items) this.items.set(it.id, it)
    this.notify()
  }

  /** 注销一个布局项（插件卸载时）。 */
  unregister(id: string): void {
    if (this.items.delete(id)) this.notify()
  }

  /** 查询某 slot 的实际项（默认 + 覆盖后）。排序：order 数组索引优先，未记录的项按 weight 升序追加在后（新装插件稳定落尾，不打乱用户已排的顺序）。 */
  itemsFor(slot: LayoutSlotId): LayoutItem[] {
    const out: LayoutItem[] = []
    for (const item of this.items.values()) {
      const effectiveSlot = this.overrides[item.id] ?? item.slot
      if (effectiveSlot === slot) out.push(item)
    }
    const orderedIds = this.order[slot] ?? []
    const idx = new Map<string, number>()
    for (let i = 0; i < orderedIds.length; i++) {
      if (!idx.has(orderedIds[i])) idx.set(orderedIds[i], i) // 去重：首个位置生效
    }
    out.sort((a, b) => {
      const ia = idx.get(a.id)
      const ib = idx.get(b.id)
      if (ia !== undefined && ib !== undefined) return ia - ib
      if (ia !== undefined) return -1
      if (ib !== undefined) return 1
      return (a.weight ?? 0) - (b.weight ?? 0)
    })
    return out
  }

  /** 移动一个项到目标 slot（追加到该 slot 末尾，持久化）。 */
  moveItem(id: string, targetSlot: LayoutSlotId): void {
    this.moveItemTo(id, targetSlot)
  }

  /**
   * 移动一个项到目标 slot 的指定位置（VSCode 式拖拽重排的核心 API）。
   * - opts.beforeId：插入到该 id 之前；省略/null 落到该 slot 的真实末尾。
   * - 跨 slot 移动时自动从原 slot 的 order 数组移除（一个 id 只在一个 slot）。
   * - order 数组按移动时的完整顺序快照写入（含未显式排序的项）——
   *   被移项落在用户看到的真实位置，而不是“已排序项优先”的抽象位置。
   */
  moveItemTo(id: string, targetSlot: LayoutSlotId, opts?: { beforeId?: string | null }): void {
    const item = this.items.get(id)
    if (!item) return
    this.overrides[id] = targetSlot
    for (const key of Object.keys(this.order) as LayoutSlotId[]) {
      if (key !== targetSlot) {
        const prev = this.order[key]
        if (prev?.includes(id)) {
          this.order[key] = prev.filter((x) => x !== id)
        }
      }
    }
    // 当前顺序快照（已排序项按 order，其余按 weight 跟随），移除自身后插入。
    const arr = this.itemsFor(targetSlot).map((it) => it.id).filter((x) => x !== id)
    if (opts?.beforeId) {
      const i = arr.indexOf(opts.beforeId)
      if (i === -1) arr.push(id)
      else arr.splice(i, 0, id)
    } else {
      arr.push(id)
    }
    this.order[targetSlot] = arr
    this.saveOverrides()
    this.saveOrder()
    this.notify()
  }

  /** 整槽重设排序（同 slot 拖拽重排）。自动去重；保留未知 id（位置记忆，项回来时恢复原位）。 */
  setSlotOrder(slot: LayoutSlotId, orderedIds: string[]): void {
    const seen = new Set<string>()
    const arr: string[] = []
    for (const id of orderedIds) {
      if (!seen.has(id)) {
        seen.add(id)
        arr.push(id)
      }
    }
    this.order[slot] = arr
    this.saveOrder()
    this.notify()
  }

  /** 某槽的用户排序（未排序的部分不在返回值里，设置面板用）。 */
  getSlotOrder(slot: LayoutSlotId): string[] {
    return [...(this.order[slot] ?? [])]
  }

  /** 把项的覆盖恢复为默认 slot（同时从所有 order 数组移除该 id）。 */
  resetItem(id: string): void {
    let changed = false
    if (id in this.overrides) {
      delete this.overrides[id]
      this.saveOverrides()
      changed = true
    }
    for (const key of Object.keys(this.order) as LayoutSlotId[]) {
      const prev = this.order[key]
      if (prev?.includes(id)) {
        this.order[key] = prev.filter((x) => x !== id)
        changed = true
      }
    }
    if (changed) {
      this.saveOrder()
      this.notify()
    }
  }

  /** 全部恢复默认（覆盖 + 排序一起清空；折叠状态是纯前端细节，不动）。 */
  resetAll(): void {
    this.overrides = {}
    this.order = {}
    this.saveOverrides()
    this.saveOrder()
    this.notify()
  }

  /** 当前覆盖配置（设置面板用）。 */
  getOverrides(): LayoutOverrides {
    return { ...this.overrides }
  }

  /** 分组是否已收起（默认展开）。 */
  isCollapsed(groupId: string): boolean {
    return this.collapsed[groupId] === true
  }

  /** 设置分组收起/展开状态（纯前端持久化）。 */
  setCollapsed(groupId: string, collapsed: boolean): void {
    if (collapsed) {
      this.collapsed[groupId] = true
    } else {
      delete this.collapsed[groupId]
    }
    this.saveCollapsed()
    this.notify()
  }

  /** 切换分组收起/展开。 */
  toggleCollapsed(groupId: string): void {
    this.setCollapsed(groupId, !this.isCollapsed(groupId))
  }

  /** 全部项（设置面板用）。 */
  allItems(): LayoutItem[] {
    return [...this.items.values()].sort((a, b) => (a.weight ?? 0) - (b.weight ?? 0))
  }

  /** 订阅变化。返回退订函数。 */
  subscribe(listener: () => void): () => void {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  private notify(): void {
    for (const l of this.listeners) l()
  }

  private loadOverrides(): void {
    try {
      const raw = localStorage.getItem(LAYOUT_OVERRIDES_KEY)
      if (raw) this.overrides = JSON.parse(raw) as LayoutOverrides
    } catch {
      this.overrides = {}
    }
  }

  private loadOrder(): void {
    try {
      const raw = localStorage.getItem(LAYOUT_ORDER_KEY)
      if (!raw) return
      const parsed = JSON.parse(raw) as LayoutOrder
      // 形状校验：只接受 slot → string[]（脏数据整体丢弃，回退 weight 排序）。
      if (parsed && typeof parsed === 'object') {
        const clean: LayoutOrder = {}
        for (const key of Object.keys(parsed)) {
          const v = parsed[key as LayoutSlotId]
          if (Array.isArray(v) && v.every((x) => typeof x === 'string')) {
            clean[key as LayoutSlotId] = v
          }
        }
        this.order = clean
      }
    } catch {
      this.order = {}
    }
  }

  private saveOrder(): void {
    try {
      localStorage.setItem(LAYOUT_ORDER_KEY, JSON.stringify(this.order))
    } catch {
      /* storage full / disabled — non-fatal */
    }
    // 后端同步（web:ui:layout-order）。后端只存「slot 归属 + 顺序」这类基础
    // 布局；宽度/高度/折叠等细节纯前端（localStorage），不占用后端存储。
    syncSettingToServer(LAYOUT_ORDER_KEY, JSON.stringify(this.order))
  }

  private saveOverrides(): void {
    try {
      localStorage.setItem(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
    } catch {
      /* storage full / disabled — non-fatal */
    }
    // 后端同步（web:ui:layout-overrides → user_settings 表）。localStorage 是
    // 秒回的读路径，后端是权威源 —— 换浏览器/设备后 syncAndMigrateSettings
    // 拉回同一份布局覆盖。
    syncSettingToServer(LAYOUT_OVERRIDES_KEY, JSON.stringify(this.overrides))
  }

  private loadCollapsed(): void {
    try {
      const raw = localStorage.getItem(LAYOUT_COLLAPSED_KEY)
      if (raw) this.collapsed = JSON.parse(raw) as LayoutCollapseState
    } catch {
      this.collapsed = {}
    }
  }

  private saveCollapsed(): void {
    try {
      localStorage.setItem(LAYOUT_COLLAPSED_KEY, JSON.stringify(this.collapsed))
    } catch {
      /* storage full / disabled — non-fatal */
    }
  }
}

/** 全局单例。 */
export const layoutRegistry = new LayoutRegistryImpl()

/** 内置布局项默认注册（应用启动时调用一次）。 */
export function registerBuiltinLayoutItems(): void {
  layoutRegistry.registerAll([
    // mobileAgent（会话导航项）不再默认注册：聊天是手机端主视图，导航由
    // 顶栏（☰ 抽屉 + 返回按钮）承担。id 常量保留供旧 overrides 无害引用。
    { id: BUILTIN_LAYOUT_ITEMS.mobileTools, slot: 'mobile.top_bar', title: '工具', labelKey: 'agent.tools', icon: 'wrench', weight: 1 },
    { id: BUILTIN_LAYOUT_ITEMS.mobileNewChat, slot: 'mobile.top_bar', title: '新会话', labelKey: 'session.newSession', icon: 'plus', weight: 0 },
    { id: BUILTIN_LAYOUT_ITEMS.mobileSettings, slot: 'mobile.top_bar', title: '设置', labelKey: 'settings.title', icon: 'settings', weight: 2 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopSessions, slot: 'desktop.activity_bar', title: '会话', labelKey: 'sidebar.sessions', icon: 'panel-left', weight: 0, group: LAYOUT_GROUPS.channels },
    { id: BUILTIN_LAYOUT_ITEMS.desktopFiles, slot: 'desktop.activity_bar', title: '文件', labelKey: 'sidebar.files', icon: 'files', weight: 0, group: LAYOUT_GROUPS.tools },
    { id: BUILTIN_LAYOUT_ITEMS.desktopSearch, slot: 'desktop.activity_bar', title: '搜索', labelKey: 'sidebar.search', icon: 'search', weight: 1, group: LAYOUT_GROUPS.tools },
    { id: BUILTIN_LAYOUT_ITEMS.desktopInfo, slot: 'desktop.activity_bar', title: '信息', labelKey: 'sidebar.info', icon: 'info', weight: 2, group: LAYOUT_GROUPS.tools },
    { id: BUILTIN_LAYOUT_ITEMS.desktopTasks, slot: 'desktop.activity_bar', title: '任务', labelKey: 'sidebar.tasks', icon: 'list-checks', weight: 3, group: LAYOUT_GROUPS.tools },
    { id: BUILTIN_LAYOUT_ITEMS.desktopTerminal, slot: 'desktop.activity_bar', title: '终端', labelKey: 'sidebar.terminal', icon: 'square-terminal', weight: 4, group: LAYOUT_GROUPS.tools },
  ])
}

/**
 * React hook：订阅某 slot 的布局项，slot 变化/项增删/覆盖变化时刷新。
 */
export function useLayoutItems(slot: LayoutSlotId): LayoutItem[] {
  const [items, setItems] = useState<LayoutItem[]>(() => layoutRegistry.itemsFor(slot))
  const slotRef = useRef(slot)
  slotRef.current = slot

  useEffect(() => {
    const recompute = () => setItems(layoutRegistry.itemsFor(slotRef.current))
    recompute()
    return layoutRegistry.subscribe(recompute)
  }, [])

  return items
}

/** React hook：读取全部项 + 覆盖 + 移动操作（布局设置面板用）。 */
export function useLayoutConfig() {
  const [version, setVersion] = useState(0)
  useEffect(() => layoutRegistry.subscribe(() => setVersion((v) => v + 1)), [])
  void version

  const moveItem = useCallback((id: string, slot: LayoutSlotId) => {
    layoutRegistry.moveItem(id, slot)
  }, [])
  const moveItemTo = useCallback(
    (id: string, slot: LayoutSlotId, opts?: { beforeId?: string | null }) => {
      layoutRegistry.moveItemTo(id, slot, opts)
    },
    [],
  )
  const resetItem = useCallback((id: string) => layoutRegistry.resetItem(id), [])
  const resetAll = useCallback(() => layoutRegistry.resetAll(), [])

  return {
    allItems: layoutRegistry.allItems(),
    overrides: layoutRegistry.getOverrides(),
    moveItem,
    moveItemTo,
    resetItem,
    resetAll,
  }
}

/** React hook：订阅分组折叠状态（isCollapsed/setCollapsed/toggleCollapsed）。 */
export function useLayoutCollapse() {
  const [version, setVersion] = useState(0)
  useEffect(() => layoutRegistry.subscribe(() => setVersion((v) => v + 1)), [])
  void version

  const isCollapsed = useCallback((groupId: string) => layoutRegistry.isCollapsed(groupId), [])
  const setCollapsed = useCallback((groupId: string, collapsed: boolean) => {
    layoutRegistry.setCollapsed(groupId, collapsed)
  }, [])
  const toggleCollapsed = useCallback((groupId: string) => {
    layoutRegistry.toggleCollapsed(groupId)
  }, [])

  return { isCollapsed, setCollapsed, toggleCollapsed }
}
