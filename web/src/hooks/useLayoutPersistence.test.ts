import { afterEach, describe, expect, it } from 'vitest'

import { loadLayout } from './useLayoutPersistence'

const KEY = 'xbot-layout:chat-1'

function validLayout() {
  return {
    grid: {
      root: {
        type: 'branch',
        data: [{ type: 'leaf', data: { views: ['p1'], activeView: 'p1', id: 'g1' } }],
      },
    },
    panels: { p1: { params: { type: 'file', closable: true } } },
  }
}

afterEach(() => {
  localStorage.removeItem(KEY)
})

describe('loadLayout', () => {
  it('returns the persisted state for a valid branch-root layout', () => {
    localStorage.setItem(KEY, JSON.stringify({ layout: validLayout(), activeKey: 'file:/a' }))
    const state = loadLayout('chat-1')
    expect(state?.activeKey).toBe('file:/a')
    expect((state?.layout as { grid: { root: { type: string } } }).grid.root.type).toBe('branch')
  })

  it('discards a corrupted layout whose root is a leaf (historical bug data)', () => {
    // filterPanels used to promote a single-child root branch to its leaf child;
    // such layouts were persisted and crash dockview fromJSON with
    // "root must be of type branch" on EVERY restore. They must be discarded.
    const leafRoot = {
      grid: { root: { type: 'leaf', data: { views: ['p1'], activeView: 'p1', id: 'g1' } } },
      panels: { p1: { params: { type: 'file', closable: true } } },
    }
    localStorage.setItem(KEY, JSON.stringify({ layout: leafRoot, activeKey: null }))
    expect(loadLayout('chat-1')).toBeNull()
  })

  it('discards a layout with a null root (fully pruned tree)', () => {
    localStorage.setItem(KEY, JSON.stringify({ layout: { grid: { root: null }, panels: {} } }))
    expect(loadLayout('chat-1')).toBeNull()
  })

  it('discards a layout without a grid', () => {
    localStorage.setItem(KEY, JSON.stringify({ layout: { panels: {} }, activeKey: null }))
    expect(loadLayout('chat-1')).toBeNull()
  })

  it('returns null for malformed JSON', () => {
    localStorage.setItem(KEY, '{not json')
    expect(loadLayout('chat-1')).toBeNull()
  })
})
