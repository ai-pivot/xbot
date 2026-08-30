/**
 * panelRegistry 单测——「一切皆面板」统一 Panel API（布局 v4）。
 *
 * 覆盖：注册/注销/列表/订阅通知（仿 view 订阅语义）、同 id 覆盖、
 * 订阅者异常不污染通知循环。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

import { panelRegistry, buildPanelDefs, mapContainerToLocation } from './panelRegistry'
import type { PanelDefinition } from '@/plugin-api'
import type { ViewContribution } from '@/plugin-api'

function makeDef(id: string, overrides: Partial<PanelDefinition> = {}): PanelDefinition {
  return {
    id,
    title: id,
    icon: 'grid',
    defaultSlot: 'left',
    defaultMode: 'docked',
    render: (): ReactNode => null,
    source: 'core',
    ...overrides,
  }
}

function makeView(id: string, container: ViewContribution['container'], overrides: Partial<ViewContribution> = {}): ViewContribution {
  return { kind: 'view', id, container, title: id, icon: 'grid', ...overrides }
}

describe('panelRegistry', () => {
  beforeEach(() => {
    for (const p of panelRegistry.listPanels()) panelRegistry.unregisterPanel(p.id)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('registerPanel / listPanels / unregisterPanel lifecycle', () => {
    expect(panelRegistry.listPanels()).toHaveLength(0)
    panelRegistry.registerPanel(makeDef('core.a'))
    panelRegistry.registerPanel(makeDef('core.b', { defaultMode: 'floating', source: 'xbot.demo' }))
    expect(panelRegistry.listPanels().map((p) => p.id)).toEqual(['core.a', 'core.b'])
    expect(panelRegistry.getPanel('core.b')?.defaultMode).toBe('floating')

    panelRegistry.unregisterPanel('core.a')
    expect(panelRegistry.listPanels().map((p) => p.id)).toEqual(['core.b'])
    // 注销不存在的 id 静默无害。
    panelRegistry.unregisterPanel('core.a')
    expect(panelRegistry.listPanels()).toHaveLength(1)
  })

  it('registerPanel with the same id overwrites the previous definition', () => {
    panelRegistry.registerPanel(makeDef('core.a', { title: 'v1' }))
    panelRegistry.registerPanel(makeDef('core.a', { title: 'v2' }))
    const panels = panelRegistry.listPanels()
    expect(panels).toHaveLength(1)
    expect(panels[0].title).toBe('v2')
  })

  it('subscribePanels notifies on register/unregister and returns an unsubscribe fn', () => {
    const listener = vi.fn()
    const unsub = panelRegistry.subscribePanels(listener)
    expect(listener).not.toHaveBeenCalled()

    panelRegistry.registerPanel(makeDef('core.a'))
    expect(listener).toHaveBeenCalledTimes(1)

    panelRegistry.unregisterPanel('core.a')
    expect(listener).toHaveBeenCalledTimes(2)

    // 退订后不再通知。
    unsub()
    panelRegistry.registerPanel(makeDef('core.c'))
    expect(listener).toHaveBeenCalledTimes(2)
  })

  it('a throwing subscriber does not break notification delivery to others', () => {
    const bad = vi.fn(() => {
      throw new Error('boom')
    })
    const good = vi.fn()
    panelRegistry.subscribePanels(bad)
    panelRegistry.subscribePanels(good)

    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    panelRegistry.registerPanel(makeDef('core.a'))
    expect(bad).toHaveBeenCalledTimes(1)
    expect(good).toHaveBeenCalledTimes(1)
    expect(errSpy).toHaveBeenCalled()
  })

  it('badgeRender is stored as-is on register (fallback is a consumer concern, not the registry)', () => {
    const badge = (): ReactNode => 'badge-node'
    panelRegistry.registerPanel(makeDef('core.badge', { badgeRender: badge }))
    expect(panelRegistry.getPanel('core.badge')?.badgeRender).toBe(badge)

    // 无 badgeRender 的 def 原样存储（undefined）——fallback 链由消费方处理。
    panelRegistry.registerPanel(makeDef('core.plain'))
    expect(panelRegistry.getPanel('core.plain')?.badgeRender).toBeUndefined()
  })
})

describe('mapContainerToLocation (container→zone 通用映射，v5.1)', () => {
  it('侧栏类容器 right_sidebar → zone side（默认钉选，h 220）', () => {
    expect(mapContainerToLocation('right_sidebar')).toEqual({ zone: 'side', h: 220, order: 0 })
  })

  it('bar 类容器 status_bar_right → 徽章 zone top / segment right', () => {
    expect(mapContainerToLocation('status_bar_right')).toEqual({
      zone: 'top',
      segment: 'right',
      order: 0,
    })
  })

  it('未知 container 兜底 zone chip（v5.1：底部 chips 启动器，不再 side）', () => {
    expect(mapContainerToLocation('nonexistent' as ViewContribution['container'])).toEqual({
      zone: 'chip',
      order: 0,
    })
  })

  it('底栏类容器（info_bar/bottom）→ 徽章 zone bottom', () => {
    expect(mapContainerToLocation('info_bar')).toEqual({ zone: 'bottom', order: 0 })
    expect(mapContainerToLocation('bottom')).toEqual({ zone: 'bottom', order: 0 })
  })
})

describe('buildPanelDefs (view 贡献点 → 面板定义，徽章合并)', () => {
  const renderView = vi.fn((_pluginId: string, _view: ViewContribution): ReactNode => 'view-node')

  beforeEach(() => {
    renderView.mockClear()
  })

  it('面板类容器 view → 主面板（location 用映射产物），render 走 renderView(pluginId, view)', () => {
    const view = makeView('t.panel', 'right_sidebar')
    const built = buildPanelDefs([{ pluginId: 't', view }], renderView)
    expect(built).toHaveLength(1)
    expect(built[0].def.id).toBe('t.panel')
    expect(built[0].def.location).toEqual({ zone: 'side', h: 220, order: 0 })
    expect(built[0].def.badgeRender).toBeUndefined()
    expect(built[0].def.render({} as never)).toBe('view-node')
    expect(renderView).toHaveBeenCalledWith('t', view)
  })

  it('未知容器 view → 主面板兜底 zone chip（contribution 默认位置被尊重）', () => {
    const view = makeView('t.panel', 'panel')
    const built = buildPanelDefs([{ pluginId: 't', view }], renderView)
    expect(built).toHaveLength(1)
    expect(built[0].def.location).toEqual({ zone: 'chip', order: 0 })
    expect(built[0].def.badgeRender).toBeUndefined()
    expect(built[0].def.render({} as never)).toBe('view-node')
  })

  it('bar 类容器 view 无主 view → 独立徽章面板：badge zone/segment + badgeRender + render 置 null', () => {
    const view = makeView('t.badge', 'status_bar_right', { align: 'end' })
    const built = buildPanelDefs([{ pluginId: 't', view }], renderView)
    expect(built).toHaveLength(1)
    const def = built[0].def
    expect(def.id).toBe('t.badge')
    expect(def.location).toEqual({ zone: 'top', segment: 'right', order: 0 })
    // 徽章面板无面板主体（主体即徽章）——旧面板引擎全量渲染时不产生可见残留。
    expect(def.render({} as never)).toBeNull()
    expect(def.badgeRender?.({} as never)).toBe('view-node')
    expect(renderView).toHaveBeenCalledWith('t', view)
  })

  it('同 pluginId 另有主 view → bar 贡献合并为主面板 badgeRender，不产生独立面板（同 panelId 合并）', () => {
    const main = makeView('t.panel', 'right_sidebar')
    const bar = makeView('t.badge', 'status_bar_right')
    const built = buildPanelDefs(
      [
        { pluginId: 't', view: main },
        { pluginId: 't', view: bar },
      ],
      renderView,
    )
    // 仅主面板一个 def；徽章 view id 无独立 def。
    expect(built).toHaveLength(1)
    expect(built[0].def.id).toBe('t.panel')
    expect(built[0].def.location).toEqual({ zone: 'side', h: 220, order: 0 })
    expect(built[0].def.badgeRender?.({} as never)).toBe('view-node')
    expect(renderView).toHaveBeenCalledWith('t', bar)
    expect(built.map((b) => b.def.id)).not.toContain('t.badge')
  })

  it('不同 pluginId 的徽章贡献不误合并：B 无主 view 时注册为独立徽章面板', () => {
    const main = makeView('a.panel', 'right_sidebar')
    const bar = makeView('b.badge', 'status_bar_right')
    const built = buildPanelDefs(
      [
        { pluginId: 'a', view: main },
        { pluginId: 'b', view: bar },
      ],
      renderView,
    )
    expect(built.map((b) => [b.def.id, b.def.location?.zone])).toEqual([
      ['a.panel', 'side'],
      ['b.badge', 'top'],
    ])
    expect(built[0].def.badgeRender).toBeUndefined()
  })

  it('同插件多个徽章贡献按声明序合并（Fragment 包裹）', () => {
    const main = makeView('t.panel', 'right_sidebar')
    const bar1 = makeView('t.badge1', 'status_bar_right')
    const bar2 = makeView('t.badge2', 'info_bar')
    const built = buildPanelDefs(
      [
        { pluginId: 't', view: main },
        { pluginId: 't', view: bar1 },
        { pluginId: 't', view: bar2 },
      ],
      renderView,
    )
    expect(built).toHaveLength(1)
    const badgeNode = built[0].def.badgeRender?.({} as never)
    expect(badgeNode).not.toBeNull()
    // 两个徽章贡献都经 renderView 渲染（声明序）。
    expect(renderView.mock.calls.map((c) => c[1].id)).toEqual(['t.badge1', 't.badge2'])
  })
})
