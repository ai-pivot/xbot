import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as React from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

type Entry = typeof import('./entry')

function makeLive() {
  const listeners = new Set<() => void>()
  let snapshot: Record<string, unknown> = { tokensPerSec: 100, ttftMs: 1200 }
  return {
    listeners,
    getGlobalLiveStats: vi.fn(() => snapshot),
    subscribeGlobalLiveStats: vi.fn((cb: () => void) => {
      listeners.add(cb)
      return () => listeners.delete(cb)
    }),
    setLive: (s: Record<string, unknown>) => {
      snapshot = s
      listeners.forEach((f) => f())
    },
  }
}

describe('iteration-stats entry', () => {
  let live: ReturnType<typeof makeLive>
  let configListeners: Set<(c: Record<string, unknown>) => void>
  let configSnapshot: Record<string, unknown>
  let ctx: { config: { get: ReturnType<typeof vi.fn>; onConfigChange: ReturnType<typeof vi.fn> } }
  let entry: Entry

  beforeEach(async () => {
    vi.resetModules()
    live = makeLive()
    vi.stubGlobal('React', React)
    vi.stubGlobal('__xbot_iteration__', {
      getGlobalLiveStats: live.getGlobalLiveStats,
      subscribeGlobalLiveStats: live.subscribeGlobalLiveStats,
    })
    configListeners = new Set()
    configSnapshot = { showTTFT: true }
    ctx = {
      config: {
        get: vi.fn(async () => configSnapshot),
        onConfigChange: vi.fn((cb: (c: Record<string, unknown>) => void) => {
          configListeners.add(cb)
          return () => configListeners.delete(cb)
        }),
      },
    }
    entry = await import('./entry')
    entry.activate(ctx as never)
    await vi.waitFor(() => expect(ctx.config.get).toHaveBeenCalled())
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('showTTFT=true 时渲染 TTFT（桌面版含 ttft 前缀）', async () => {
    render(React.createElement(entry.default))
    expect(await screen.findByText('100 tok/s · ttft 1.2s')).toBeInTheDocument()
  })

  it('onConfigChange 改 showTTFT=false 后实时隐藏 TTFT', async () => {
    render(React.createElement(entry.default))
    await screen.findByText('100 tok/s · ttft 1.2s')
    act(() => {
      configListeners.forEach((cb) => cb({ showTTFT: false }))
    })
    await waitFor(() => {
      expect(screen.queryByText('100 tok/s · ttft 1.2s')).not.toBeInTheDocument()
      expect(screen.getByText('100 tok/s')).toBeInTheDocument()
    })
  })

  it('手机版（sm 以下）走紧凑文本：去 ttft 前缀词', async () => {
    render(React.createElement(entry.default))
    // 桌面版含 "ttft" 前缀；手机版是 "100t/s · 1.2s"（无 "ttft" 词）。
    expect(await screen.findByText('100t/s · 1.2s')).toBeInTheDocument()
  })

  it('streaming 停止（tok/s=0）时整体隐藏', async () => {
    render(React.createElement(entry.default))
    await screen.findByText('100 tok/s · ttft 1.2s')
    act(() => {
      live.setLive({ tokensPerSec: 0 })
    })
    await waitFor(() => {
      expect(screen.queryByText(/tok\/s/)).not.toBeInTheDocument()
    })
  })
})
