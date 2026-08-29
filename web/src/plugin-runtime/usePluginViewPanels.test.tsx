/**
 * usePluginViewPanels shim（布局 v5 语义更新）测试。
 *
 * 守护点：徽章面板（bar 类容器贡献，def.location.zone 非 side）不再作为
 * 容器面板暴露——PluginPanelContainer(status_bar_right) 等旧直渲染点返回空，
 * 徽章由 rail 渲染点消费 def.badgeRender（迭代指标插件双渲染收敛的 shim 侧）。
 * side 面板（含合并场景的主面板——自身带 badgeRender）照常返回，返回形状
 * 不变（id/pluginId/title/container/view）。
 *
 * mock 模式仿 PluginView.test.tsx：vi.mock 工厂引用的外部变量必须经
 * vi.hoisted() 定义；runtime mock 返回【稳定引用】。
 */
import { renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { panelRegistry, buildPanelDefs } from './panelRegistry'
import { usePluginViewPanels } from './usePluginViewPanels'
import type { ViewContribution } from '@/plugin-api'

const { viewsFixture, runtimeMock } = vi.hoisted(() => {
  const viewsFixture: Array<{ pluginId: string; view: unknown }> = []
  // 稳定引用：shim 的 useEffect deps 含 runtime，每次渲染新对象会重触发 effect。
  const runtimeMock = {
    listAllViews: () => viewsFixture,
    subscribeViews: () => () => {},
  }
  return { viewsFixture, runtimeMock }
})

vi.mock('@/plugin-runtime', () => ({
  useOptionalPluginRuntime: () => runtimeMock,
}))

function makeView(id: string, container: ViewContribution['container']): ViewContribution {
  return { kind: 'view', id, container, title: id, icon: 'grid' }
}

/** 模拟 syncViews：view 贡献点经 buildPanelDefs 构建后注册进 panelRegistry。 */
function syncFixture(views: Array<{ pluginId: string; view: ViewContribution }>): void {
  viewsFixture.splice(0, viewsFixture.length, ...views)
  for (const { def } of buildPanelDefs(views, () => null)) {
    panelRegistry.registerPanel(def)
  }
}

describe('usePluginViewPanels (布局 v5 shim 语义)', () => {
  beforeEach(() => {
    for (const p of panelRegistry.listPanels()) panelRegistry.unregisterPanel(p.id)
    viewsFixture.splice(0, viewsFixture.length)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('status_bar_right 查询返回徽章 view（shim 直接从 runtime 获取，不经 panelRegistry——手机端 PluginPanelContainer 需要渲染徽章）', () => {
    syncFixture([{ pluginId: 'xbot.iteration-stats', view: makeView('xbot.iteration-stats.badge', 'status_bar_right') }])
    const { result } = renderHook(() => usePluginViewPanels('status_bar_right'))
    expect(result.current).toHaveLength(1)
    expect(result.current[0]).toMatchObject({ id: 'xbot.iteration-stats.badge', container: 'status_bar_right' })
  })

  it('right_sidebar 查询照常返回主面板，返回形状不变', () => {
    const view = makeView('xbot.git-fancy.panel', 'right_sidebar')
    syncFixture([{ pluginId: 'xbot.git-fancy', view }])
    const { result } = renderHook(() => usePluginViewPanels('right_sidebar'))
    expect(result.current).toHaveLength(1)
    expect(result.current[0]).toMatchObject({
      id: 'xbot.git-fancy.panel',
      pluginId: 'xbot.git-fancy',
      title: 'xbot.git-fancy.panel',
      container: 'right_sidebar',
    })
    expect(result.current[0].view).toBe(view)
  })

  it('合并场景：主面板 + 徽章 view 各自在对应容器查询返回（shim 直接从 runtime 获取，不经 panelRegistry 合并逻辑）', () => {
    syncFixture([
      { pluginId: 'x', view: makeView('x.panel', 'right_sidebar') },
      { pluginId: 'x', view: makeView('x.badge', 'status_bar_right') },
    ])
    const { result } = renderHook(() => usePluginViewPanels('right_sidebar'))
    expect(result.current.map((p) => p.id)).toEqual(['x.panel'])
    // shim 直接从 runtime.listAllViews() 获取——徽章 view 也在对应容器返回
    // （手机端 PluginPanelContainer 需要渲染徽章）。
    const barResult = renderHook(() => usePluginViewPanels('status_bar_right'))
    expect(barResult.result.current.map((p) => p.id)).toEqual(['x.badge'])
  })

  it('info_bar 查询返回对应 view（shim 直接从 runtime 获取）', () => {
    syncFixture([{ pluginId: 'b', view: makeView('b.item', 'info_bar') }])
    const { result } = renderHook(() => usePluginViewPanels('info_bar'))
    expect(result.current).toHaveLength(1)
    expect(result.current[0]).toMatchObject({ id: 'b.item', container: 'info_bar' })
  })
})
