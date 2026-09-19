/**
 * xbot.iteration-stats 入口测试 —— 徽章视图 + 单入口双视图契约。
 *
 * 关键回归守护：入口**故意不导出 default** —— 宿主 `loadViewComponent` 的解析
 * 顺序是 `mod[view.id]` → `mod.default`，有 default 时趋势面板会渲染成徽章组件。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as React from 'react'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

type Entry = typeof import('./entry')

const BADGE_VIEW_ID = 'xbot.iteration-stats.badge'
const TREND_VIEW_ID = 'xbot.iteration-stats.trend'

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
  let ctx: {
    config: { get: ReturnType<typeof vi.fn>; onConfigChange: ReturnType<typeof vi.fn> }
    i18n: { locale: string; t: (k: string, f?: string) => string }
    events: { on: ReturnType<typeof vi.fn> }
    rpc: { call: ReturnType<typeof vi.fn> }
  }
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
      i18n: { locale: 'zh-CN', t: (_k: string, f?: string) => f ?? _k },
      events: { on: vi.fn(() => () => {}) },
      rpc: { call: vi.fn(async () => ({ recent_iterations: [] })) },
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
    render(React.createElement(entry[BADGE_VIEW_ID]))
    expect(await screen.findByText('100 tok/s · ttft 1.2s')).toBeInTheDocument()
  })

  it('onConfigChange 改 showTTFT=false 后实时隐藏 TTFT', async () => {
    render(React.createElement(entry[BADGE_VIEW_ID]))
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
    render(React.createElement(entry[BADGE_VIEW_ID]))
    // 桌面版含 "ttft" 前缀；手机版是 "100t/s · 1.2s"（无 "ttft" 词）。
    expect(await screen.findByText('100t/s · 1.2s')).toBeInTheDocument()
  })

  it('streaming 停止（tok/s=0）时整体隐藏', async () => {
    render(React.createElement(entry[BADGE_VIEW_ID]))
    await screen.findByText('100 tok/s · ttft 1.2s')
    act(() => {
      live.setLive({ tokensPerSec: 0 })
    })
    await waitFor(() => {
      expect(screen.queryByText(/tok\/s/)).not.toBeInTheDocument()
    })
  })

  it('两个视图都按 view.id 命名导出，且【没有 default】（有 default 时趋势面板会渲染成徽章）', () => {
    expect(typeof entry[BADGE_VIEW_ID]).toBe('function')
    expect(typeof entry[TREND_VIEW_ID]).toBe('function')
    expect(entry[BADGE_VIEW_ID]).not.toBe(entry[TREND_VIEW_ID])
    expect(Object.prototype.hasOwnProperty.call(entry, 'default')).toBe(false)
  })

  it('activate 注册 turn.ended / session.switched 刷新事件（面板自动刷新）', () => {
    expect(ctx.events.on).toHaveBeenCalledWith('turn.ended', expect.any(Function))
    expect(ctx.events.on).toHaveBeenCalledWith('session.switched', expect.any(Function))
  })

  it('宿主未注入实时桥时不崩（徽章静默不渲染）', async () => {
    vi.resetModules()
    vi.unstubAllGlobals()
    vi.stubGlobal('React', React)
    const freshEntry = await import('./entry')
    freshEntry.activate(ctx as never)
    const { container } = render(React.createElement(freshEntry[BADGE_VIEW_ID]))
    expect(container).toBeEmptyDOMElement()
  })
})
