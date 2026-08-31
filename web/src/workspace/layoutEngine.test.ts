import { describe, expect, it, vi } from 'vitest'
import type { DockviewApi, DockviewGroupPanel } from 'dockview-core'

import {
  DEFAULT_LAYOUT_OPTIONS,
  LayoutEngine,
  computeWidths,
  isMasterGroup,
  type LayoutGroupInfo,
} from './layoutEngine'

// ── helpers ─────────────────────────────────────────────────────────────────

function info(id: string, isMaster: boolean): LayoutGroupInfo {
  return { id, isMaster }
}

/** 构造 mock group（isMasterGroup 走 panels[].params.type === 'agent'） */
function mockGroup(id: string, types: string[]): DockviewGroupPanel {
  return {
    id,
    panels: types.map((type) => ({ params: { type } })),
    api: { setSize: vi.fn() },
  } as unknown as DockviewGroupPanel
}

/** 构造 mock DockviewApi：Event 订阅收集 listener，手动 fire */
function mockApi(groups: DockviewGroupPanel[], width: number) {
  const listeners = {
    addGroup: new Set<(g: DockviewGroupPanel) => void>(),
    removeGroup: new Set<(g: DockviewGroupPanel) => void>(),
    layoutChange: new Set<() => void>(),
  }
  const api = {
    groups,
    width,
    onDidAddGroup: (l: (g: DockviewGroupPanel) => void) => {
      listeners.addGroup.add(l)
      return { dispose: () => listeners.addGroup.delete(l) }
    },
    onDidRemoveGroup: (l: (g: DockviewGroupPanel) => void) => {
      listeners.removeGroup.add(l)
      return { dispose: () => listeners.removeGroup.delete(l) }
    },
    onDidLayoutChange: (l: () => void) => {
      listeners.layoutChange.add(l)
      return { dispose: () => listeners.layoutChange.delete(l) }
    },
    fireAddGroup: (g: DockviewGroupPanel) => listeners.addGroup.forEach((l) => l(g)),
    fireLayoutChange: () => listeners.layoutChange.forEach((l) => l()),
  }
  return api as unknown as DockviewApi & {
    fireAddGroup: (g: DockviewGroupPanel) => void
    fireLayoutChange: () => void
  }
}

// ── computeWidths 纯计算 ────────────────────────────────────────────────────

describe('computeWidths', () => {
  it('master + 1 secondary：80/20，secondary 拿 20%', () => {
    const plan = computeWidths(
      [info('sec', false), info('master', true)],
      1000,
    )
    // sec = floor(1000 * 0.2) = 200；master 800 省略（吸收误差）
    expect(plan?.get('sec')).toBe(200)
    expect(plan?.has('master')).toBe(false)
  })

  it('master + 2 secondary：secondary 平分 20%，但每个不低于最小宽度下限', () => {
    const plan = computeWidths(
      [info('sec1', false), info('sec2', false), info('master', true)],
      1200,
    )
    // floor(1200*0.2/2)=120 < minSecondaryWidth(200) → 每个抬到 200，master=800
    expect(plan?.get('sec1')).toBe(200)
    expect(plan?.get('sec2')).toBe(200)
    expect(plan?.has('master')).toBe(false) // 最后一个是 master，省略
  })

  it('容器足够宽时 secondary 严格按 20% 均分', () => {
    const plan = computeWidths(
      [info('sec1', false), info('sec2', false), info('master', true)],
      2000,
    )
    expect(plan?.get('sec1')).toBe(200) // floor(2000*0.2/2)
    expect(plan?.get('sec2')).toBe(200)
    expect(plan?.has('master')).toBe(false)
  })

  it('master 在前 secondary 在后：master 拿 80%，最后一个（secondary）省略', () => {
    const plan = computeWidths(
      [info('master', true), info('sec', false)],
      1000,
    )
    expect(plan?.get('master')).toBe(800)
    expect(plan?.has('sec')).toBe(false)
  })

  it('容器太窄时 secondary 压到最小宽度下限', () => {
    // 600px：理想 sec = floor(600*0.2)=120 < minSecondaryWidth(200) → 200
    const plan = computeWidths(
      [info('sec', false), info('master', true)],
      600,
    )
    expect(plan?.get('sec')).toBe(DEFAULT_LAYOUT_OPTIONS.minSecondaryWidth)
  })

  it('master 保底：必要时压缩 secondary 但不低于其下限', () => {
    // 500px：minMaster(380) + minSec(200) = 580 > 500 → sec 压到下限 200，master 300
    const plan = computeWidths(
      [info('sec', false), info('master', true)],
      500,
    )
    expect(plan?.get('sec')).toBe(DEFAULT_LAYOUT_OPTIONS.minSecondaryWidth)
  })

  it('多个 master 平分 80% 区域', () => {
    const plan = computeWidths(
      [info('master1', true), info('master2', true), info('sec', false)],
      1000,
    )
    // sec=200（min 下限），master 各 (1000-200)/2=400；最后一个是 sec，省略
    expect(plan?.get('master1')).toBe(400)
    expect(plan?.get('master2')).toBe(400)
    expect(plan?.has('sec')).toBe(false)
  })

  it('不干预：卡片少于 2 个', () => {
    expect(computeWidths([info('master', true)], 1000)).toBeNull()
  })

  it('不干预：无 master（Agent 卡片不存在）', () => {
    expect(computeWidths([info('a', false), info('b', false)], 1000)).toBeNull()
  })

  it('不干预：无 secondary（只有 master 卡片）', () => {
    expect(computeWidths([info('m1', true), info('m2', true)], 1000)).toBeNull()
  })

  it('不干预：容器宽度未就绪（0）', () => {
    expect(
      computeWidths([info('sec', false), info('master', true)], 0),
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
  it('bindApi 后 relayout：按 80/20 应用 setSize', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    expect(sec.api.setSize).toHaveBeenCalledWith({ width: 200 })
    // master 省略（最后一个，吸收误差）
    expect(master.api.setSize).not.toHaveBeenCalled()
    engine.dispose()
  })

  it('api.width 为 0（播种时机）时保持 pending，onDidLayoutChange 首次触发时应用', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 0)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    // bindApi 时不主动 relayout（等事件）
    expect(sec.api.setSize).not.toHaveBeenCalled()

    api.fireAddGroup(sec) // 播种 addPanel → onDidAddGroup → relayout → width=0 → pending
    expect(sec.api.setSize).not.toHaveBeenCalled()

    // autoResize ResizeObserver 完成 layout → api.width 有值 → onDidLayoutChange → 应用
    ;(api as unknown as { width: number }).width = 1000
    api.fireLayoutChange()
    expect(sec.api.setSize).toHaveBeenCalledWith({ width: 200 })
    engine.dispose()
  })

  it('onDidRemoveGroup 后剩余卡片重算', () => {
    const sec1 = mockGroup('sec1', ['panel'])
    const sec2 = mockGroup('sec2', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec1, sec2, master], 1200)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    api.fireAddGroup(sec1) // 结构变化 → 重算：每个 secondary 抬到 min 200，master=800
    expect(sec1.api.setSize).toHaveBeenCalledWith({ width: 200 })
    expect(sec2.api.setSize).toHaveBeenCalledWith({ width: 200 })

    // 移除 sec1 → 剩余 [sec2, master] → 重算：sec2 = 240（1200*0.2）
    ;(sec2.api.setSize as ReturnType<typeof vi.fn>).mockClear()
    ;(api as unknown as { groups: DockviewGroupPanel[] }).groups = [sec2, master]
    api.fireLayoutChange() // 结构变化 → 重算
    expect(sec2.api.setSize).toHaveBeenCalledWith({ width: 240 })
    engine.dispose()
  })

  it('纯 sash 拖拽（onDidLayoutChange、结构未变）不覆盖用户手动比例', () => {
    const sec = mockGroup('sec', ['panel'])
    const master = mockGroup('master', ['agent'])
    const api = mockApi([sec, master], 1000)
    const engine = new LayoutEngine()
    engine.bindApi(api)
    api.fireAddGroup(sec) // 应用一次：sec=200
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
})
