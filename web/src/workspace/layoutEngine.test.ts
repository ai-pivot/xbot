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

/** 构造 mock group（isMasterGroup 走 panels[].params.type === 'agent'） */
function mockGroup(id: string, types: string[]): DockviewGroupPanel {
  return {
    id,
    panels: types.map((type) => ({ params: { type } })),
    api: { setSize: vi.fn() },
  } as unknown as DockviewGroupPanel
}

/** 构造 mock DockviewApi：Event 订阅收集 listener，手动 fire */
function mockApi(groups: DockviewGroupPanel[], width: number, height = 600) {
  const listeners = {
    addGroup: new Set<(g: DockviewGroupPanel) => void>(),
    removeGroup: new Set<(g: DockviewGroupPanel) => void>(),
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
})
