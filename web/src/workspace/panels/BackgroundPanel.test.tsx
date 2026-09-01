/**
 * BackgroundPanel output rendering tests.
 *
 * Reproduces the user report: "打开对应 task 的页面后看不到虚拟 terminal 上的
 * 输出" — the EXISTING output snapshot (produced before the panel was opened)
 * never reached the xterm terminal.
 *
 * Root cause (effect ordering): the first poll completes → setTask + setLoading
 * land in one commit → the container div renders → its callback ref fires
 * setContainer → xterm is created in the NEXT commit. The output effect
 * (deps [output]) runs on the FIRST commit — termRef.current is still null →
 * early return → the snapshot output is dropped. output never changes again
 * → the effect never re-runs → the existing output is permanently lost. Only
 * SSE deltas (bg-task-output) written directly via termRef after mount are
 * visible.
 *
 * Fix: the output effect's deps must include container + theme so it re-runs
 * after the terminal is (re)created, writing the snapshot in full (xterm
 * cleanup resets lastLenRef to 0).
 */
import { render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// Collect every string written to ANY xterm instance — the assertion target.
const xtermWrites = vi.hoisted(() => [] as string[])

vi.mock('@xterm/xterm', () => ({
  // class (not arrow fn) — BackgroundPanel calls `new Terminal(...)`;
  // vitest rejects vi.fn() with arrow implementations used as constructors.
  Terminal: class {
    write = vi.fn((s: string) => { xtermWrites.push(s) })
    reset = vi.fn()
    scrollToBottom = vi.fn()
    loadAddon = vi.fn()
    open = vi.fn()
    dispose = vi.fn()
    onScroll = vi.fn(() => ({ dispose: vi.fn() }))
    buffer = { active: { baseY: 0, cursorY: 0, length: 0 } }
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit = vi.fn()
    dispose = vi.fn()
  },
}))
vi.mock('@/workspace/types', () => ({
  useDockviewContext: () => ({
    ws: { rpc: vi.fn(), connected: true },
    theme: { theme: 'dark', setTheme: vi.fn() },
  }),
}))

import { BackgroundPanel } from './BackgroundPanel'
import type { PanelProps } from './types'

// jsdom has no ResizeObserver (BackgroundPanel uses one for fit-on-resize).
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = ResizeObserverStub

const EXISTING_OUTPUT = 'line1\nline2\nline3\n'

function mockTasksFetch(output: string, status = 'running') {
  const fetchMock = vi.fn(async () => new Response(JSON.stringify({
    ok: true,
    data: {
      background_tasks: [{
        id: 'bg123',
        command: 'echo hi',
        status,
        started_at: '2026-08-31T00:00:00Z',
        output,
      }],
    },
    error: null,
  }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderPanel() {
  const params = {
    taskID: 'bg123',
    taskChannel: 'web',
    taskChatID: 'chat-1',
    command: 'echo hi',
  } as PanelProps['params']
  return render(
    <BackgroundPanel params={params} api={{} as PanelProps['api']} containerApi={{} as PanelProps['containerApi']} />,
  )
}

describe('BackgroundPanel output rendering', () => {
  beforeEach(() => {
    xtermWrites.length = 0
    vi.unstubAllGlobals()
  })

  it('writes the EXISTING output snapshot to xterm after the panel opens', async () => {
    mockTasksFetch(EXISTING_OUTPUT)
    renderPanel()

    // The task snapshot's output must reach the terminal. Before the fix the
    // output effect ran BEFORE xterm was mounted (termRef.current === null →
    // early return) and never re-ran — the panel showed an empty terminal.
    await waitFor(() => {
      expect(xtermWrites.join('')).toContain('line1')
    }, { timeout: 3000 })
    expect(xtermWrites.join('')).toContain('line3')
  })
})
