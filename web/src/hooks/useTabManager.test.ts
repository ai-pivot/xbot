import { describe, expect, it } from 'vitest'

import { filterAgentPanels } from './useTabManager'

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

  it('drops an empty group that contained only agent panels', () => {
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
    const out = filterAgentPanels(layout) as { grid: { root: unknown } }
    // single surviving leaf is collapsed up from the branch
    expect((out.grid.root as { type: string }).type).toBe('leaf')
    expect((out.grid.root as { data: { views: string[] } }).data.views).toEqual(['work'])
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