import { describe, expect, it, vi } from 'vitest'
import type { DockviewApi, DockviewGroupPanel } from 'dockview-core'

import {
  DEFAULT_LAYOUT_OPTIONS,
  LayoutEngine,
  computeMasterStack,
  isMasterGroup,
  type LayoutGroupInfo,
} from './layoutEngine'

// ── helpers ─────────────────────────────────────────────────────────────────

function info(id: string, isMaster: boolean): LayoutGroupInfo {
  return { id, isMaster }
}

/** 构造 mock group（isMasterGroup 走 panels[].params.type === 'agent'；
 * isTabGroup 走 panels[0].params.type !== 'panel'；locked 可写（分类制
 * header 策略设置）；element 是真实 div（classList 兼容） */
function mockGroup(id: string, types: string[]): DockviewGroupPanel {
  return {
    id,
    panels: types.map((type) => ({
      params: { type },
      // panelContentElement（applyGroupHeaderPolicy 顶角圆角 toggle）的
      // 访问路径：p.view.content.element——mock 真实 div 供 classList 断言
      view: { content: { element: document.createElement('div') } },
    })),
    api: { setSize: vi.fn(), location: { type: 'grid' } },
    model: { header: { hidden: false } },
    element: document.createElement('div'),
    locked: false as boolean | 'no-drop-target',
  } as unknown as DockviewGroupPanel
}

/** 构造 mock DockviewApi：Event 订阅收集 listener，手动 fire */
function mockApi(groups: DockviewGroupPanel[], width: number, height = 600) {
  const listeners = {
    addGroup: new Set<(g: DockviewGroupPanel) => void>(),
    removeGroup: new Set<(g: DockviewGroupPanel) => void>(),
    addPanel: new Set<(p: unknown) => void>(),
    removePanel: new Set<(p: unknown) => void>(),
    layoutChange: new Set<() => void>(),
  }
  const api = {
    groups,
    width,
    height,
    onDidAddGroup: (l: (g: DockviewGroupPanel) => void) => {
      listeners.addGroup.add(l)
      return { dispose: () => listeners.addGroup.delete(l) }
    },
    onDidRemoveGroup: (l: (g: DockviewGroupPanel) => void) => {
      listeners.removeGroup.add(l)
      return { dispose: () => listeners.removeGroup.delete(l) }
    },
    onDidAddPanel: (l: (p: unknown) => void) => {
      listeners.addPanel.add(l)
      return { dispose: () => listeners.addPanel.delete(l) }
    },
    onDidRemovePanel: (l: (p: unknown) => void) => {
      listeners.removePanel.add(l)
      return { dispose: () => listeners.removePanel.delete(l) }
    },
    onDidLayoutChange: (l: () => void) => {
      listeners.layoutChange.add(l)
      return { dispose: () => listeners.layoutChange.delete(l) }
    },
    fireAddGroup: (g: DockviewGroupPanel) => listeners.addGroup.forEach((l) => l(g)),
    fireAddPanel: () => listeners.addPanel.forEach((l) => l({})),
    fireRemovePanel: () => listeners.removePanel.forEach((l) => l({})),
    fireLayoutChange: () => listeners.layoutChange.forEach((l) => l()),
  }
  return api as unknown as DockviewApi & {
    fireAddGroup: (g: DockviewGroupPanel) => void
    fireAddPanel: () => void
    fireRemovePanel: () => void
    fireLayoutChange: () => void
  }
}

// ── computeMasterStack 纯计算 ───────────────────────────────────────────────

describe('computeMasterStack', () => {
  it('单张 secondary（堆叠列只有一张）：列宽 20%，master 80%', () => {
    const plan = computeMasterStack(
      [info('sec', false), info('master', true)],
      1000,
      600,
    )
    // sec 直接在 root 层：设列宽 200；master 设宽 800
    expect(plan?.get('sec')).toEqual({ width: 200 })
    expect(plan?.get('master')).toEqual({ width: 800 })
  })

  it('多张 secondary：堆叠列内上下均分高度，最后一张省略（吸收误差）', () => {
    const plan = computeMasterStack(
      [info('sec1', false), info('sec2', false), info('sec3', false), info('master', true)],
      1000,
      600,
    )
    // 列内三张：各 200 高，最后一张省略
    expect(plan?.get('sec1')).toEqual({ height: 200 })
    expect(plan?.get('sec2')).toEqual({ height: 200 })
    expect(plan?.has('sec3')).toBe(false)
    // master 设宽 800（列宽 200 由 delta 吸收）
    expect(plan?.get('master')).toEqual({ width: 800 })
  })

  it('堆叠列高度不足时单张卡片不低于最小高度下限', () => {
    // 600 高、6 张 secondary → 均分 100 < minSecondaryHeight(120) → 每张 120
    const secs = Array.from({ length: 6 }, (_, i) => info(`sec${i}`, false))
    const plan = computeMasterStack([...secs, info('master', true)], 1000, 600)
    expect(plan?.get('sec0')).toEqual({ height: DEFAULT_LAYOUT_OPTIONS.minSecondaryHeight })
    // 最后一张（sec5）省略
    expect(plan?.has('sec5')).toBe(false)
  })

  it('master 保底：容器太窄时列宽压缩但 master 不低于其下限', () => {
    // 500px：理想列宽 100 < 下限 200 → 抬到 200；master 保底 380 → 列被压到 120
    const plan = computeMasterStack(
      [info('sec', false), info('master', true)],
      500,
      600,
    )
    expect(plan?.get('sec')).toEqual({ width: 120 })
    expect(plan?.get('master')).toEqual({ width: 380 })
  })

  it('多个 master 平分 80% 区域', () => {
    const plan = computeMasterStack(
      [info('master1', true), info('master2', true), info('sec', false)],
      1000,
      600,
    )
    expect(plan?.get('sec')).toEqual({ width: 200 })
    expect(plan?.get('master1')).toEqual({ width: 400 })
    expect(plan?.get('master2')).toEqual({ width: 400 })
  })

  it('不干预：卡片少于 2 个', () => {
    expect(computeMasterStack([info('master', true)], 1000, 600)).toBeNull()
  })

  it('不干预：无 master（Agent 卡片不存在）', () => {
    expect(computeMasterStack([info('a', false), info('b', false)], 1000, 600)).toBeNull()
  })

  it('不干预：无 secondary（只有 master 卡片）', () => {
    expect(computeMasterStack([info('m1', true), info('m2', true)], 1000, 600)).toBeNull()
  })

  it('不干预：容器宽度未就绪（0）', () => {
    expect(
      computeMasterStack([info('sec', false), info('master', true)], 0, 600),
    ).toBeNull()
  })

  it('不干预：容器高度未就绪（0）', () => {
    expect(
      computeMasterStack([info('sec', false), info('master', true)], 1000, 0),
    ).toBeNull()
  })
})

// ── isMasterGroup ───────────────────────────────────────────────────────────

describe('isMasterGroup', () => {
  it('含 agent tab 的 group 是 master', () => {
    expect(isMasterGroup(mockGroup('g1', ['agent']))).toBe(true)
  })

  it('sidebar panel group 不是 master', () => {
    expect(isMasterGroup(mockGroup('g2', ['panel']))).toBe(false)
  })

  it('tab 与 agent 混合的 group 是 master', () => {
    expect(isMasterGroup(mockGroup('g3', ['file', 'agent']))).toBe(true)
  })
})

// ── LayoutEngine 集中管线 ───────────────────────────────────────────────────

describe('LayoutEngine', () => {
  it('bindApi 后 relayout：master 设宽 80%，单张 secondary 设列宽 20%', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    expect(sec.api.setSize).toHaveBeenCalledWith({ width: 200 })
    expect(master.api.setSize).toHaveBeenCalledWith({ width: 800 })
    engine.dispose()
  })

  it('多张 secondary 时按高度分配（堆叠列），master 设宽', () => {
    const sec1 = mockGroup('sec1', ['panel'])
    const sec2 = mockGroup('sec2', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec1, sec2, master], 1000, 600)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    expect(sec1.api.setSize).toHaveBeenCalledWith({ height: 300 })
    expect(sec2.api.setSize).not.toHaveBeenCalled() // 最后一张省略（吸收误差）
    expect(master.api.setSize).toHaveBeenCalledWith({ width: 800 })
    engine.dispose()
  })

  it('api.width 为 0（播种时机）时保持 pending，onDidLayoutChange 首次触发时应用', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 0, 0)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    // bindApi 时不主动 relayout（等事件）
    expect(sec.api.setSize).not.toHaveBeenCalled()

    api.fireAddGroup(sec) // 播种 addPanel → onDidAddGroup → relayout → width=0 → pending
    expect(sec.api.setSize).not.toHaveBeenCalled()

    // autoResize ResizeObserver 完成 layout → api.width 有值 → onDidLayoutChange → 应用
    ;(api as unknown as { width: number; height: number }).width = 1000
    ;(api as unknown as { height: number }).height = 600
    api.fireLayoutChange()
    expect(sec.api.setSize).toHaveBeenCalledWith({ width: 200 })
    engine.dispose()
  })

  it('onDidRemoveGroup 后剩余卡片重算', () => {
    const sec1 = mockGroup('sec1', ['panel'])
    const sec2 = mockGroup('sec2', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec1, sec2, master], 1000, 600)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    api.fireAddGroup(sec1) // 结构变化 → 重算：两张各 300 高，sec2 省略
    expect(sec1.api.setSize).toHaveBeenCalledWith({ height: 300 })

    // 移除 sec1 → 剩余 [sec2, master] → 重算：sec2 直接占列（单张）设宽 200
    ;(sec2.api.setSize as ReturnType<typeof vi.fn>).mockClear()
    ;(api as unknown as { groups: DockviewGroupPanel[] }).groups = [sec2, master]
    api.fireLayoutChange() // 结构变化 → 重算
    expect(sec2.api.setSize).toHaveBeenCalledWith({ width: 200 })
    engine.dispose()
  })

  it('纯 sash 拖拽（onDidLayoutChange、结构未变）不覆盖用户手动比例', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    api.fireAddGroup(sec) // 应用一次：sec 宽 200
    expect(sec.api.setSize).toHaveBeenCalledWith({ width: 200 })
    ;(sec.api.setSize as ReturnType<typeof vi.fn>).mockClear()

    // 用户手动拖 sash → onDidLayoutChange（结构未变、已应用过）→ 不重算
    api.fireLayoutChange()
    expect(sec.api.setSize).not.toHaveBeenCalled()
    engine.dispose()
  })

  it('无 master 时 relayout 不调用 setSize', () => {
    const a = mockGroup('a', ['panel'])
    const b = mockGroup('b', ['panel'])
    const api = mockApi([a, b], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    api.fireAddGroup(a)
    expect(a.api.setSize).not.toHaveBeenCalled()
    engine.dispose()
  })

  it('dispose 后不再响应事件', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    // 清掉 bindApi 主动 relayout 的那次调用
    ;(sec.api.setSize as ReturnType<typeof vi.fn>).mockClear()
    engine.dispose()
    api.fireAddGroup(sec)
    expect(sec.api.setSize).not.toHaveBeenCalled()
  })

  // ── header 策略（卡片分类制：Tab 卡 / 非 Tab 卡） ──────────────────────────
  // Tab 卡（type≠panel）：tab 栏常驻（Header 融合）+ locked=false 可拖入；
  // 非 Tab 卡（type=panel）：无 tab 栏组件 + locked='no-drop-target' 禁拖。

  it('Tab 卡 tab 栏常驻 + 可拖入；非 Tab 卡无 tab 栏 + locked 禁拖入；所有卡片注入右上角 grip 把手', () => {
    const master = mockGroup('master', ['agent']) // Tab 卡（主卡）
    const file = mockGroup('file', ['file']) // Tab 卡（拖出的 file 卡）
    const panel = mockGroup('panel', ['panel']) // 非 Tab 卡（sidebar 面板）
    const api = mockApi([master, file, panel], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    // Tab 卡：tab 栏常驻（Header=Tab 列表融合）+ Tab 可互相拖入
    expect(master.model.header.hidden).toBe(false)
    expect(master.locked).toBe(false)
    expect(file.model.header.hidden).toBe(false)
    expect(file.locked).toBe(false)
    // 非 Tab 卡：无 tab 栏组件（功能条由面板内容自带）+ locked 禁拖入
    expect(panel.model.header.hidden).toBe(true)
    expect(panel.locked).toBe('no-drop-target')
    // 所有卡片（Tab/非 Tab）注入右上角拖动把手（.card-drag-handle）
    expect(master.element.querySelector('.card-drag-handle')).not.toBeNull()
    expect(file.element.querySelector('.card-drag-handle')).not.toBeNull()
    expect(panel.element.querySelector('.card-drag-handle')).not.toBeNull()
    // 内容根顶部圆角（非 Tab 卡专属——overlay 只保底两角，非 Tab 卡
    // tab 栏 hidden 时 overlay 占满整卡，顶部两角由内容根补齐）：
    // Tab 卡的 panel 内容根无顶角圆角（tab 栏/内容交界平直一体）
    const panelContent = (panel.panels[0] as unknown as { view: { content: { element: HTMLElement } } }).view.content.element
    const masterContent = (master.panels[0] as unknown as { view: { content: { element: HTMLElement } } }).view.content.element
    expect(panelContent.classList.contains('card-content-top-round')).toBe(true)
    expect(masterContent.classList.contains('card-content-top-round')).toBe(false)
    // grip 幂等：策略重算（tab 增删等触发 applyGroupHeaderPolicy）不重复注入
    api.fireAddPanel()
    expect(master.element.querySelectorAll('.card-drag-handle').length).toBe(1)
    expect(panel.element.querySelectorAll('.card-drag-handle').length).toBe(1)
    engine.dispose()
  })

  it('非 Tab 卡 tab 增删后策略重算保持（hidden/locked 恒定）', () => {
    const panel = mockGroup('panel', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([panel, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)

    // panel 卡内 tab 增删（onDidAddPanel/onDidRemovePanel → 策略重算）：
    // 分类按 panels[0].params.type 判定——panel 卡恒为非 Tab（hidden+locked）
    ;(panel as unknown as { panels: Array<{ params: { type: string } }> }).panels.push({
      params: { type: 'panel' },
    })
    api.fireAddPanel()
    expect(panel.model.header.hidden).toBe(true)
    expect(panel.locked).toBe('no-drop-target')

    ;(panel as unknown as { panels: Array<{ params: { type: string } }> }).panels.pop()
    api.fireRemovePanel()
    expect(panel.model.header.hidden).toBe(true)
    expect(panel.locked).toBe('no-drop-target')
    engine.dispose()
  })

  it('header 策略不依赖容器尺寸（width=0 时 bindApi 即应用）', () => {
    const panel = mockGroup('panel', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([panel, master], 0, 0) // 容器未就绪（播种时机）
    const engine = new LayoutEngine()
    engine.bindApi(api)
    // 尺寸分配 pending，但 header 策略立即可用（不依赖 api.width）
    expect(panel.api.setSize).not.toHaveBeenCalled()
    expect(panel.model.header.hidden).toBe(true)
    expect(panel.locked).toBe('no-drop-target')
    engine.dispose()
  })
})
