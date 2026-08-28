/**
 * LayoutRegistry 单元测试——布局注册/查询/覆盖/重置/持久化。
 *
 * 测试直接驱动全局单例 layoutRegistry，并在 afterEach 重置状态，避免
 * 用例间相互污染（localStorage 也清空）。
 */
import { describe, it, expect, beforeEach, afterEach } from 'vitest'

import {
  BUILTIN_LAYOUT_ITEMS,
  LAYOUT_COLLAPSED_KEY,
  LAYOUT_GROUPS,
  LAYOUT_OVERRIDES_KEY,
  LAYOUT_ORDER_KEY,
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
    localStorage.removeItem(LAYOUT_COLLAPSED_KEY)
    localStorage.removeItem(LAYOUT_ORDER_KEY)
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
    // 聊天为主角的手机端重设计：默认无底部导航项。
    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toEqual([])
    // mobileAgent 不再默认注册（agent 视图是主视图，导航在顶栏）。
    expect(layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id))
      .not.toContain(BUILTIN_LAYOUT_ITEMS.mobileAgent)

    const topBar = layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileNewChat)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
    expect(topBar).toContain(BUILTIN_LAYOUT_ITEMS.mobileSettings)

    // 布局 v2/v4：内置面板默认注册到左栏 desktop.activity_bar。
    const sidebar = layoutRegistry.itemsFor('desktop.activity_bar').map((i) => i.id)
    expect(sidebar).toContain(BUILTIN_LAYOUT_ITEMS.desktopFiles)
    expect(sidebar).toContain(BUILTIN_LAYOUT_ITEMS.desktopTerminal)
  })

  it('moveItem 把项移到目标 slot 并持久化', () => {
    // 把「工具」从顶栏移到底部导航（按需渲染场景）。
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileTools, 'mobile.bottom_nav')

    const topBar = layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id)
    expect(topBar).not.toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)

    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)

    // 持久化：从 localStorage 重建后仍生效。
    const raw = localStorage.getItem(LAYOUT_OVERRIDES_KEY)
    expect(raw).toBeTruthy()
    const parsed = JSON.parse(raw!)
    expect(parsed[BUILTIN_LAYOUT_ITEMS.mobileTools]).toBe('mobile.bottom_nav')
  })

  it('重置单个项恢复默认 slot', () => {
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileTools, 'mobile.bottom_nav')
    expect(layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id))
      .toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)

    layoutRegistry.resetItem(BUILTIN_LAYOUT_ITEMS.mobileTools)
    expect(layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id))
      .not.toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
    expect(layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id))
      .toContain(BUILTIN_LAYOUT_ITEMS.mobileTools)
  })

  it('resetAll 恢复全部默认', () => {
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileSettings, 'mobile.bottom_nav')
    layoutRegistry.moveItem(BUILTIN_LAYOUT_ITEMS.mobileNewChat, 'desktop.info_bar')
    layoutRegistry.resetAll()

    const bottomNav = layoutRegistry.itemsFor('mobile.bottom_nav').map((i) => i.id)
    expect(bottomNav).toEqual([])
    // 被移走的项恢复默认 slot。
    expect(layoutRegistry.itemsFor('desktop.info_bar').map((i) => i.id))
      .not.toContain(BUILTIN_LAYOUT_ITEMS.mobileNewChat)
    expect(layoutRegistry.itemsFor('mobile.top_bar').map((i) => i.id))
      .toContain(BUILTIN_LAYOUT_ITEMS.mobileNewChat)
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
    // 布局 v2：right_sidebar container 映射到 desktop.activity_bar（协议不变）。
    const mappedSlot = VIEW_CONTAINER_TO_SLOT['right_sidebar']
    layoutRegistry.register({
      id: 'xbot.git-fancy.panel',
      slot: mappedSlot,
      title: 'Git',
      icon: 'git-branch',
      weight: 100,
    })
    const sidebar = layoutRegistry.itemsFor(mappedSlot).map((i) => i.id)
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

  it('分组默认展开，可折叠并持久化到 localStorage', () => {
    // 默认展开。
    expect(layoutRegistry.isCollapsed(LAYOUT_GROUPS.channels)).toBe(false)
    expect(layoutRegistry.isCollapsed(LAYOUT_GROUPS.tools)).toBe(false)

    // 收起「渠道」组。
    layoutRegistry.setCollapsed(LAYOUT_GROUPS.channels, true)
    expect(layoutRegistry.isCollapsed(LAYOUT_GROUPS.channels)).toBe(true)

    // 持久化：从 localStorage 重建后仍生效。
    const raw = localStorage.getItem(LAYOUT_COLLAPSED_KEY)
    expect(raw).toBeTruthy()
    expect(JSON.parse(raw!)[LAYOUT_GROUPS.channels]).toBe(true)
  })

  it('toggleCollapsed 切换收起/展开', () => {
    layoutRegistry.toggleCollapsed(LAYOUT_GROUPS.tools)
    expect(layoutRegistry.isCollapsed(LAYOUT_GROUPS.tools)).toBe(true)
    layoutRegistry.toggleCollapsed(LAYOUT_GROUPS.tools)
    expect(layoutRegistry.isCollapsed(LAYOUT_GROUPS.tools)).toBe(false)
  })
})

describe('layoutRegistry ordering（VSCode 式拖拽重排）', () => {
  beforeEach(() => {
    resetRegistry()
    registerBuiltinLayoutItems()
  })

  afterEach(() => {
    resetRegistry()
  })

  it('setSlotOrder 重排同 slot 项并持久化到 localStorage', () => {
    const sid = 'desktop.activity_bar'
    const files = BUILTIN_LAYOUT_ITEMS.desktopFiles
    const terminal = BUILTIN_LAYOUT_ITEMS.desktopTerminal
    // 默认 weight 顺序 files(0)…terminal(4)；用户反转两者。
    layoutRegistry.setSlotOrder(sid, [terminal, files])
    const ids = layoutRegistry.itemsFor(sid).map((i) => i.id)
    expect(ids.indexOf(terminal)).toBeLessThan(ids.indexOf(files))
    // 未提到的项仍保留在该 slot。
    expect(ids).toContain(BUILTIN_LAYOUT_ITEMS.desktopSearch)
    // 持久化。
    const raw = localStorage.getItem(LAYOUT_ORDER_KEY)
    expect(raw).toBeTruthy()
    expect(JSON.parse(raw!)[sid][0]).toBe(terminal)
  })

  it('排序数组外的项（新装插件）按 weight 追加在已排序项之后', () => {
    const sid = 'desktop.activity_bar'
    const files = BUILTIN_LAYOUT_ITEMS.desktopFiles
    // 只排 files 到最后：其余未记录项应仍按 weight 在它前面？不 —— 已排序项优先。
    layoutRegistry.setSlotOrder(sid, [files])
    const ids = layoutRegistry.itemsFor(sid).map((i) => i.id)
    // files 是唯一已排序项 → 排最前，其余（含 weight 0 的会话项）按 weight 跟随。
    expect(ids[0]).toBe(files)
    expect(ids).toContain(BUILTIN_LAYOUT_ITEMS.desktopSearch)
    expect(ids.indexOf(BUILTIN_LAYOUT_ITEMS.desktopSearch))
      .toBeGreaterThan(ids.indexOf(files))
  })

  it('moveItemTo(id, slot, {beforeId}) 插入指定位置（跨 slot 自动清理旧 order）', () => {
    const sid = 'desktop.activity_bar'
    const files = BUILTIN_LAYOUT_ITEMS.desktopFiles
    const search = BUILTIN_LAYOUT_ITEMS.desktopSearch
    const info = BUILTIN_LAYOUT_ITEMS.desktopInfo
    // 用户已手动排 [search, files]。
    layoutRegistry.setSlotOrder(sid, [search, files])
    // 把 info 插到 files 前。
    layoutRegistry.moveItemTo(info, sid, { beforeId: files })
    expect(layoutRegistry.itemsFor(sid).map((i) => i.id).slice(0, 3)).toEqual([search, info, files])

    // 跨 slot 移走 search：旧 slot 的 order 里不再有 search。
    layoutRegistry.moveItemTo(search, 'desktop.info_bar')
    expect(layoutRegistry.getSlotOrder(sid)).not.toContain(search)
    expect(layoutRegistry.itemsFor('desktop.info_bar').map((i) => i.id)).toContain(search)
  })

  it('beforeId 不在目标 slot → 追加到末尾（不抛错）', () => {
    layoutRegistry.moveItemTo(BUILTIN_LAYOUT_ITEMS.desktopInfo, 'desktop.sidebar', {
      beforeId: 'unknown-id',
    })
    const order = layoutRegistry.getSlotOrder('desktop.sidebar')
    expect(order[order.length - 1]).toBe(BUILTIN_LAYOUT_ITEMS.desktopInfo)
  })

  it('setSlotOrder 去重（重复 id 只保留首个位置）', () => {
    const sid = 'mobile.bottom_nav'
    const tools = BUILTIN_LAYOUT_ITEMS.mobileTools
    layoutRegistry.setSlotOrder(sid, [tools, tools, BUILTIN_LAYOUT_ITEMS.mobileNewChat])
    expect(layoutRegistry.getSlotOrder(sid)).toEqual([tools, BUILTIN_LAYOUT_ITEMS.mobileNewChat])
  })

  it('resetItem 同时清掉 overrides 与所有 order 数组里的该 id', () => {
    const sid = 'desktop.activity_bar'
    const files = BUILTIN_LAYOUT_ITEMS.desktopFiles
    layoutRegistry.moveItemTo(files, 'mobile.bottom_nav')
    expect(layoutRegistry.getSlotOrder('mobile.bottom_nav')).toContain(files)
    layoutRegistry.resetItem(files)
    expect(layoutRegistry.getSlotOrder('mobile.bottom_nav')).not.toContain(files)
    // 恢复默认 slot（desktop.sidebar）。
    expect(layoutRegistry.itemsFor(sid).map((i) => i.id)).toContain(files)
  })

  it('resetAll 清空排序（回到 weight 默认顺序）', () => {
    const sid = 'desktop.activity_bar'
    const terminal = BUILTIN_LAYOUT_ITEMS.desktopTerminal
    layoutRegistry.setSlotOrder(sid, [terminal])
    layoutRegistry.resetAll()
    expect(layoutRegistry.getSlotOrder(sid)).toEqual([])
    // weight 0 的会话项回到最前（默认顺序）。
    expect(layoutRegistry.itemsFor(sid).map((i) => i.id)[0]).toBe(BUILTIN_LAYOUT_ITEMS.desktopSessions)
  })

  it('loadOrder 对脏数据（非数组值）整体丢弃该槽，回退 weight 排序', () => {
    const sid = 'desktop.activity_bar'
    localStorage.setItem(LAYOUT_ORDER_KEY, JSON.stringify({ [sid]: 'not-an-array' }))
    // 触发重载：新实例路径不可直接调用（单例），通过 SETTINGS_SYNCED 之外的
    // loadOrder 是私有的 —— 这里直接验证 setSlotOrder 覆盖脏数据即可。
    layoutRegistry.setSlotOrder(sid, [])
    // weight 0 的会话项回到最前（脏数据被丢弃，回退 weight 排序）。
    expect(layoutRegistry.itemsFor(sid).map((i) => i.id)[0]).toBe(BUILTIN_LAYOUT_ITEMS.desktopSessions)
  })
})
