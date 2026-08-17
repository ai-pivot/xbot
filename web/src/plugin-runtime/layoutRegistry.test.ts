/**
 * LayoutRegistry 单元测试——布局注册/查询/覆盖/重置/持久化。
 *
 * 测试直接驱动全局单例 layoutRegistry，并在 afterEach 重置状态，避免
 * 用例间相互污染（localStorage 也清空）。
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'

import {
  BUILTIN_LAYOUT_ITEMS,
  LAYOUT_OVERRIDES_KEY,
  type LayoutItem,
} from './layoutTypes'
import {
  layoutRegistry,
  registerBuiltinLayoutItems,
  VIEW_CONTAINER_TO_SLOT,
} from './layoutRegistry'

function resetRegistry() {
  // 清空所有已注册项 + 覆盖（单例无公开 reset API，逐个注销内置项）。
  for (const id of Object.values(BUILTIN_LAYOUT_ITEMS)) {
    layoutRegistry.unregister(id)
  }
  layoutRegistry.resetAll()
  try {
    localStorage.removeItem(LAYOUT_OVERRIDES_KEY)
  } catch { /* noop */ }
}

describe('layoutRegistry', () => {
  beforeEach(() => {
    resetRegistry()
    registerBuiltinLayoutItems()
  })

  afterEach(() => {
    resetRegistry()
  })

  it('内置项注册到正确默认 slot', () => {
    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toContain(BUILTIN_LAYOUT_ITEMS.mobileAgent)
    expect(bottomNav).toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
    // 会话在工具之前（weight 0 < 1）。
    expect(bottomNav.indexOf(BUILTIN_LAYOUT_ITEMS.mobileAgent))
      .toBeLessThan(bottomNav.indexOf(BUILTIN_LAYOUT_ITEMS.mobileTools))

    const topBar = layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileNewChat)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileSettings)

    const sidebar = layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id)
    expect(sidebar).toContain(BUILTIN_LAYOUT_ITEMS.desktopFiles)
    expect(sidebar).toContain(BUILTIN_LAYOUT_ITEMS.desktopTerminal)
  })

  it('moveItem 把项移到目标 slot 并持久化', () => {
    // 把「会话」从底部导航移到顶栏。
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileAgent, 'mobile.top_bar')

    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).not.toContain(BUILTIN_LAYOUT_ITEMS.mobileAgent)

    const topBar = layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileAgent)

    // 持久化：从 localStorage 重建后仍生效。
    const raw = localStorage.getItem(LAYOUT_OVERRIDES_KEY)
    expect(raw).toBeTruthy()
    const parsed = JSON.parse(raw!)
    expect(parsed[BUILTIN_LAYOUT_ITEMS.mobileAgent]).toBe('mobile.top_bar')
  })

  it('重置单个项恢复默认 slot', () => {
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileTools, 'mobile.top_bar')
    expect(layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id))
      .toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)

    layoutRegistry.resetItem(BUILTIN_LAYOUT_ITEMS.mobileTools)
    expect(layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id))
      .not.toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
    expect(layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id))
      .toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
  })

  it('resetAll 恢复全部默认', () => {
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileAgent, 'mobile.top_bar')
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileSettings, 'mobile.bottom_nav')
    layoutRegistry.resetAll()

    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toContain(BUILTIN_LAYOUT_ITEMS.mobileAgent)
    expect(bottomNav).not.toContain(BUILTIN_LAYOUT_ITEMS.mobileSettings)
    expect(localStorage.getItem(LAYOUT_OVERRIDES_KEY)).toBe('{}')
  })

  it('订阅：register/unregister/move 触发 listener', () => {
    const seen: string[] = []
    const unsub = layoutRegistry.subscribe(() => seen.push('changed'))

    layoutRegistry.register({ id: 'test.item', slot: 'desktop.sidebar', title: 'Test' })
    layoutRegistry.moveItem('test.item', 'desktop.info_bar')
    layoutRegistry.unregister('test.item')
    unsub()

    expect(seen.length).toBe(3)
  })

  it('插件 view 注册后出现在默认 slot（container 映射）', () => {
    layoutRegistry.register({
      id: 'xbot.git-fancy.panel',
      slot: VIEW_CONTAINER_TO_SLOT['right_sidebar'],
      title: 'Git',
      icon: 'git-branch',
      weight: 100,
    })
    const sidebar = layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id)
    expect(sidebar).toContain('xbot.git-fancy.panel')
    // weight 100 → 排在内置项之后。
    expect(sidebar.indexOf('xbot.git-fancy.panel'))
      .toBeGreaterThan(sidebar.indexOf(BUILTIN_LAYOUT_ITEMS.desktopFiles))
  })

  it('插件 view 可被用户移到其他 slot（如手机底部导航）', () => {
    layoutRegistry.register({
      id: 'xbot.git-fancy.panel',
      slot: 'desktop.sidebar',
      title: 'Git',
      icon: 'git-branch',
      weight: 100,
    })
    layoutRegistry.moveItem('xbot.git-fancy.panel', 'mobile.bottom_nav')

    expect(layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id))
      .not.toContain('xbot.git-fancy.panel')
    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toContain('xbot.git-fancy.panel')
    // 插件项排在内置「会话」「工具」之后（weight 100）。
    expect(bottomNav.indexOf('xbot.git-fancy.panel'))
      .toBeGreaterThan(bottomNav.indexOf(BUILTIN_LAYOUT_ITEMS.mobileTools))
  })

  it('unregister 移除项（插件卸载时不再出现在任何 slot）', () => {
    layoutRegistry.register({ id: 'xbot.git-fancy.panel', slot: 'desktop.sidebar', title: 'Git' })
    layoutRegistry.unregister('xbot.git-fancy.panel')
    expect(layoutRegistry.itemsFor('desktop.sidebar').map((i) => i.id))
      .not.toContain('xbot.git-fancy.panel')
  })

  it('moveItem 未知项 no-op（不抛错）', () => {
    expect(() => layoutRegistry.moveItem('nonexistent', 'mobile.top_bar')).not.toThrow()
  })

  it('LayoutItem 类型约束：slot 必须合法', () => {
    const item: LayoutItem = { id: 'x', slot: 'desktop.info_bar', title: 'X' }
    expect(item.slot).toBe('desktop.info_bar')
  })
})
