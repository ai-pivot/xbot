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
  LAYOUT_OVERRIDES_KEY,
  type LayoutItem,
  type LayoutOverrides,
  type LayoutSlotId,
} from './layoutTypes'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'

/** view container → 默认布局 slot 映射（插件 view 自动注册用）。 */
export const VIEW_CONTAINER_TO_SLOT: Record<string, LayoutSlotId> = {
  right_sidebar: 'desktop.sidebar',
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
  private listeners = new Set<() => void>()

  constructor() {
    this.loadOverrides()
    // 后端已有覆盖时（换浏览器/设备，syncAndMigrateSettings 拉取 server →
    // localStorage），重新加载并通知订阅者。仅在浏览器环境（单例模块在
    // SSR/单测里可能无 window）。
    if (typeof window !== 'undefined') {
      window.addEventListener(SETTINGS_SYNCED_EVENT, this.onSettingsSynced)
    }
  }

  private onSettingsSynced = (): void => {
    this.loadOverrides()
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

  /** 查询某 slot 的实际项（默认 + 覆盖后，按 weight 升序）。 */
  itemsFor(slot: LayoutSlotId): LayoutItem[] {
    const out: LayoutItem[] = []
    for (const item of this.items.values()) {
      const effectiveSlot = this.overrides[item.id] ?? item.slot
      if (effectiveSlot === slot) out.push(item)
    }
    out.sort((a, b) => (a.weight ?? 0) - (b.weight ?? 0))
    return out
  }

  /** 移动一个项到目标 slot（持久化）。 */
  moveItem(id: string, targetSlot: LayoutSlotId): void {
    const item = this.items.get(id)
    if (!item) return
    this.overrides[id] = targetSlot
    this.saveOverrides()
    this.notify()
  }

  /** 把项的覆盖恢复为默认 slot。 */
  resetItem(id: string): void {
    if (!(id in this.overrides)) return
    delete this.overrides[id]
    this.saveOverrides()
    this.notify()
  }

  /** 全部恢复默认。 */
  resetAll(): void {
    this.overrides = {}
    this.saveOverrides()
    this.notify()
  }

  /** 当前覆盖配置（设置面板用）。 */
  getOverrides(): LayoutOverrides {
    return { ...this.overrides }
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
}

/** 全局单例。 */
export const layoutRegistry = new LayoutRegistryImpl()

/** 内置布局项默认注册（应用启动时调用一次）。 */
export function registerBuiltinLayoutItems(): void {
  layoutRegistry.registerAll([
    { id: BUILTIN_LAYOUT_ITEMS.mobileAgent, slot: 'mobile.bottom_nav', title: '会话', labelKey: 'sidebar.sessions', icon: 'bot', weight: 0 },
    { id: BUILTIN_LAYOUT_ITEMS.mobileTools, slot: 'mobile.bottom_nav', title: '工具', labelKey: 'agent.tools', icon: 'square-terminal', weight: 1 },
    { id: BUILTIN_LAYOUT_ITEMS.mobileNewChat, slot: 'mobile.top_bar', title: '新会话', labelKey: 'session.newSession', icon: 'plus', weight: 0 },
    { id: BUILTIN_LAYOUT_ITEMS.mobileSettings, slot: 'mobile.top_bar', title: '设置', labelKey: 'settings.title', icon: 'settings', weight: 1 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopSessions, slot: 'desktop.activity_bar', title: '会话', labelKey: 'sidebar.sessions', icon: 'panel-left', weight: 0 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopFiles, slot: 'desktop.sidebar', title: '文件', labelKey: 'sidebar.files', icon: 'files', weight: 0 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopSearch, slot: 'desktop.sidebar', title: '搜索', labelKey: 'sidebar.search', icon: 'search', weight: 1 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopInfo, slot: 'desktop.sidebar', title: '信息', labelKey: 'sidebar.info', icon: 'info', weight: 2 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopTasks, slot: 'desktop.sidebar', title: '任务', labelKey: 'sidebar.tasks', icon: 'list-checks', weight: 3 },
    { id: BUILTIN_LAYOUT_ITEMS.desktopTerminal, slot: 'desktop.sidebar', title: '终端', labelKey: 'sidebar.terminal', icon: 'square-terminal', weight: 4 },
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
  const resetItem = useCallback((id: string) => layoutRegistry.resetItem(id), [])
  const resetAll = useCallback(() => layoutRegistry.resetAll(), [])

  return {
    allItems: layoutRegistry.allItems(),
    overrides: layoutRegistry.getOverrides(),
    moveItem,
    resetItem,
    resetAll,
  }
}
