import { describe, expect, it } from 'vitest'

import { filterAgentPanels, groupCloseTargets, tabLogicalKey, tabLogicalKeyFromParams } from './useTabManager'

function agentPanel(id: string) {
  return { id, params: { type: 'agent', closable: false }, contentComponent: 'agent' }
}
function workPanel(id: string, type = 'file') {
  return { id, params: { type }, contentComponent: type }
}

describe('filterAgentPanels', () => {
  it('returns layout unchanged when there is no agent panel', () => {
    const layout = {
      grid: { root: { type: 'leaf', data: { views: ['a'], activeView: 'a', id: 'g' } } },
      panels: { a: workPanel('a') },
    }
    expect(filterAgentPanels(layout)).toBe(layout)
  })

  it('removes agent panels from the panels map and leaf group views', () => {
    const layout = {
      grid: { root: { type: 'leaf', data: { views: ['agent', 'work'], activeView: 'work', id: 'g' } } },
      panels: { agent: agentPanel('agent'), work: workPanel('work') },
    }
    const out = filterAgentPanels(layout) as {
      grid: { root: { data: { views: string[] } } }
      panels: Record<string, unknown>
    }
    expect(out.grid.root.data.views).toEqual(['work'])
    expect(Object.keys(out.panels)).toEqual(['work'])
  })

  it('drops an empty group but keeps the root a branch (dockview fromJSON invariant)', () => {
    const layout = {
      grid: {
        root: {
          type: 'branch',
          data: [
            { type: 'leaf', data: { views: ['agent1'], activeView: 'agent1', id: 'g1' } },
            { type: 'leaf', data: { views: ['work'], activeView: 'work', id: 'g2' } },
          ],
        },
      },
      panels: { agent1: agentPanel('agent1'), work: workPanel('work') },
    }
    const out = filterAgentPanels(layout) as { grid: { root: { type: string; data: unknown[] } } }
    // dockview's fromJSON asserts "root must be of type branch" — the root may
    // NEVER be promoted to its single child (a leaf root crashes every restore).
    expect(out.grid.root.type).toBe('branch')
    expect(out.grid.root.data).toHaveLength(1)
    expect((out.grid.root.data[0] as { data: { views: string[] } }).data.views).toEqual(['work'])
  })

  it('keeps a nested single-child branch as a branch (no child promotion anywhere)', () => {
    const layout = {
      grid: {
        root: {
          type: 'branch',
          data: [
            {
              type: 'branch',
              data: [{ type: 'leaf', data: { views: ['agent1'], activeView: 'agent1', id: 'g1' } }],
            },
            { type: 'leaf', data: { views: ['work'], activeView: 'work', id: 'g2' } },
          ],
        },
      },
      panels: { agent1: agentPanel('agent1'), work: workPanel('work') },
    }
    const out = filterAgentPanels(layout) as {
      grid: { root: { type: string; data: { type: string; data: unknown[] }[] } }
    }
    // The emptied nested branch is removed; the surviving single leaf stays a
    // child of the root branch — structure is only ever pruned, never promoted.
    expect(out.grid.root.type).toBe('branch')
    expect(out.grid.root.data).toHaveLength(1)
    expect(out.grid.root.data[0].type).toBe('leaf')
  })

  it('returns null when every panel is filtered out', () => {
    const layout = {
      grid: {
        root: {
          type: 'branch',
          data: [{ type: 'leaf', data: { views: ['agent1'], activeView: 'agent1', id: 'g1' } }],
        },
      },
      panels: { agent1: agentPanel('agent1') },
    }
    // A layout whose grid tree is fully pruned has no restorable structure —
    // emitting `{ grid: { root: null } }` would crash fromJSON just like a leaf root.
    expect(filterAgentPanels(layout)).toBeNull()
  })

  it('keeps nested branch when multiple non-agent groups survive', () => {
    const layout = {
      grid: {
        root: {
          type: 'branch',
          data: [
            { type: 'leaf', data: { views: ['w1'], activeView: 'w1', id: 'g1' } },
            { type: 'leaf', data: { views: ['w2'], activeView: 'w2', id: 'g2' } },
          ],
        },
      },
      panels: { w1: workPanel('w1'), w2: workPanel('w2') },
    }
    const out = filterAgentPanels(layout) as { grid: { root: { type: string; data: unknown[] } } }
    expect(out.grid.root.type).toBe('branch')
    expect(out.grid.root.data).toHaveLength(2)
  })
})

describe('tabLogicalKey: plugin view tabs', () => {
  it('dynamic instances (openViewTab with key) dedup by viewKey — same view id opens MULTIPLE tabs', () => {
    // 两个不同文件各开一个 diff tab（同一 viewId，不同 key）
    expect(
      tabLogicalKey({
        type: 'plugin',
        data: { viewId: 'xbot.git-fancy.diff', viewKey: 'git-diff:worktree:src/a.go' },
      }),
    ).toBe('plugin-view:git-diff:worktree:src/a.go')
    expect(
      tabLogicalKey({
        type: 'plugin',
        data: { viewId: 'xbot.git-fancy.diff', viewKey: 'git-diff:worktree:src/b.go' },
      }),
    ).toBe('plugin-view:git-diff:worktree:src/b.go')
  })

  it('static views (no viewKey) dedup by viewId', () => {
    expect(
      tabLogicalKey({ type: 'plugin', data: { viewId: 'xbot.git-fancy.panel' } }),
    ).toBe('plugin:xbot.git-fancy.panel')
  })

  it('params mirror: tabLogicalKeyFromParams reads viewKey first', () => {
    expect(
      tabLogicalKeyFromParams({ type: 'plugin', viewId: 'v', viewKey: 'k', tabId: 't', title: '', closable: true }),
    ).toBe('plugin-view:k')
    expect(
      tabLogicalKeyFromParams({ type: 'plugin', viewId: 'v', tabId: 't', title: '', closable: true }),
    ).toBe('plugin:v')
  })
})

describe('groupCloseTargets (tab 右键菜单批量关闭目标)', () => {
  // [A(不可关), B, C(self), D, E(不可关)] —— A/E 模拟常驻 tab（closable=false）。
  const tabs = [
    { tabId: 'A', closable: false },
    { tabId: 'B', closable: true },
    { tabId: 'C', closable: true },
    { tabId: 'D', closable: true },
    { tabId: 'E', closable: false },
  ]

  it('left：self 之前的可关 tab（常驻 tab 跳过）', () => {
    expect(groupCloseTargets(tabs, 'C', 'left')).toEqual(['B'])
  })

  it('right：self 之后的可关 tab（常驻 tab 跳过）', () => {
    expect(groupCloseTargets(tabs, 'C', 'right')).toEqual(['D'])
  })

  it('others：除 self 外全部可关 tab', () => {
    expect(groupCloseTargets(tabs, 'C', 'others')).toEqual(['B', 'D'])
  })

  it('all：全部可关 tab（含 self；常驻 tab 永不关闭）', () => {
    expect(groupCloseTargets(tabs, 'C', 'all')).toEqual(['B', 'C', 'D'])
  })

  it('边界：self 是第一个/最后一个 → left/right 为空；常驻 tab 全组 → 全空', () => {
    expect(groupCloseTargets(tabs, 'A', 'left')).toEqual([])
    expect(groupCloseTargets(tabs, 'E', 'right')).toEqual([])
    const allPinned = [
      { tabId: 'X', closable: false },
      { tabId: 'Y', closable: false },
    ]
    expect(groupCloseTargets(allPinned, 'X', 'all')).toEqual([])
    expect(groupCloseTargets(allPinned, 'X', 'others')).toEqual([])
  })

  it('未知 tabId（不在组内）→ 空结果', () => {
    expect(groupCloseTargets(tabs, 'Z', 'all')).toEqual([])
  })
})