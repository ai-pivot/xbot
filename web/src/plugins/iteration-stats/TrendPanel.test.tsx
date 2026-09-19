/**
 * xbot.iteration-stats 趋势面板测试。
 *
 * 通过真实入口 activate(ctx) 注入能力（rpc / i18n / config / events），渲染
 * `xbot.iteration-stats.trend` 视图 —— 顺便验证"单入口双视图"契约。
 *
 * 覆盖：正常渲染 / 粒度切换 / 空态 / 单点降级 / 身份未就绪 / RPC 失败重试 /
 * hover tooltip / 未覆盖区（LIMIT 截断）/ i18n 走 ctx.i18n / turn.ended 自动刷新。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as React from 'react'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

import type { SessionUsageStats } from './bridge'
import type { UsageIterationRow } from '@/plugin-api'

type Entry = typeof import('./entry')

const TREND_VIEW_ID = 'xbot.iteration-stats.trend'
const MIN = 60_000

/** naive-UTC（服务端真实 `created_at` 形态）。 */
function naiveUtc(ts: number): string {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`
}

function row(tsOffsetMs: number, partial: Partial<UsageIterationRow> = {}): UsageIterationRow {
  return {
    turn_id: 1,
    iteration: 1,
    input_tokens: 1000,
    output_tokens: 50,
    cached_tokens: 800,
    ttft_ms: 1200,
    tpot_ms: 12,
    tokens_per_sec: 120,
    total_ms: 2000,
    model: 'test-model',
    created_at: naiveUtc(Date.now() + tsOffsetMs),
    ...partial,
  }
}

/** 跨小时桶的 3 条样本（小时级粒度下必须有 ≥2 个有样本桶才会画图）。 */
function hourSpread(): UsageIterationRow[] {
  return [row(-130 * MIN), row(-70 * MIN), row(-5 * MIN)]
}

function stats(rows: UsageIterationRow[]): SessionUsageStats {
  return {
    iteration_count: rows.length,
    turn_count: 1,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    llm_total_ms: 0,
    avg_ttft_ms: 0,
    avg_tpot_ms: 0,
    avg_tokens_per_sec: 0,
    first_iteration_at: '',
    last_iteration_at: '',
    last_prompt_tokens: 0,
    last_completion_tokens: 0,
    current_model: 'test-model',
    session_created_at: '',
    session_last_active: '',
    by_model: null,
    recent_iterations: rows,
  } as unknown as SessionUsageStats
}

interface Harness {
  entry: Entry
  rpcCall: ReturnType<typeof vi.fn>
  events: Map<string, (payload: unknown) => void>
}

async function mountPanel(options: {
  response?: unknown
  setup?: (rpcCall: ReturnType<typeof vi.fn>) => void
  identity?: { channel: string; chatID: string } | null
  i18n?: { locale: string; t: (k: string, f?: string) => string }
}): Promise<Harness> {
  vi.resetModules()
  vi.stubGlobal('React', React)
  if (options.identity === null) {
    vi.stubGlobal('__xbot_session__', undefined)
  } else {
    vi.stubGlobal('__xbot_session__', options.identity ?? { channel: 'web', chatID: 'chat_TEST' })
  }
  const rpcCall = vi.fn(async () => options.response ?? stats(hourSpread()))
  options.setup?.(rpcCall)
  const events = new Map<string, (payload: unknown) => void>()
  const entry = await import('./entry')
  entry.activate({
    rpc: { call: rpcCall },
    i18n: options.i18n ?? { locale: 'zh-CN', t: (_k: string, f?: string) => f ?? _k },
    config: { get: async () => ({ showTTFT: true }), onConfigChange: () => () => {} },
    events: {
      on: (event: string, handler: (payload: unknown) => void) => {
        events.set(event, handler)
        return () => events.delete(event)
      },
    },
  } as never)
  render(React.createElement(entry[TREND_VIEW_ID]))
  return { entry, rpcCall, events }
}

/** 让 chart surface 有真实几何（jsdom 里 getBoundingClientRect 全是 0）。 */
function stubSurfaceRect(testid: string, width = 600): HTMLElement {
  const el = screen.getByTestId(testid)
  el.getBoundingClientRect = () => ({
    left: 0,
    width,
    right: width,
    top: 0,
    bottom: 100,
    height: 100,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  })
  return el
}

describe('iteration-stats 趋势面板', () => {
  beforeEach(() => {
    cleanup()
  })

  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('渲染趋势图（小时级默认）：总览 chips + 两张图 + 覆盖范围标注', async () => {
    const { rpcCall } = await mountPanel({})
    expect(await screen.findByText('会话用量趋势')).toBeInTheDocument()
    expect(screen.getByTestId('iter-token-chart')).toBeInTheDocument()
    expect(screen.getByTestId('iter-perf-chart')).toBeInTheDocument()
    // 粒度默认小时级。
    expect(screen.getByTestId('iter-granularity').querySelector('[data-granularity="hour"]')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    // 覆盖范围诚实标注（count = 明细行数）。
    expect(screen.getByTestId('iter-stats-coverage').textContent).toContain('仅最近 3 次迭代')
    expect(screen.getByTestId('iter-stats-coverage').textContent).toContain('500')
    expect(rpcCall).toHaveBeenCalledWith('get_session_usage_stats', {
      channel: 'web',
      chat_id: 'chat_TEST',
      limit: 500,
    })
  })

  it('粒度切换：点「分钟」重新聚合（覆盖桶数 60、按钮态转移）', async () => {
    await mountPanel({})
    await screen.findByText('会话用量趋势')
    expect(screen.getByTestId('iter-stats-coverage').textContent).toContain('覆盖桶')
    fireEvent.click(screen.getByText('分钟'))
    await waitFor(() => {
      expect(screen.getByTestId('iter-granularity').querySelector('[data-granularity="minute"]')).toHaveAttribute(
        'aria-pressed',
        'true',
      )
    })
    // 分钟级窗口 = 60 桶（覆盖桶文案随之改变）。
    expect(screen.getByTestId('iter-stats-coverage').textContent).toContain('/ 60')
  })

  it('空数据（窗口内无样本）⇒ 空态卡，不画图', async () => {
    await mountPanel({ response: stats([]) })
    expect(await screen.findByTestId('iter-stats-empty')).toBeInTheDocument()
    expect(screen.queryByTestId('iter-token-chart')).not.toBeInTheDocument()
    expect(screen.getByText(/本会话还没有 LLM 迭代记录/)).toBeInTheDocument()
  })

  it('样本全在窗口外 ⇒ 空态提示"最近一次迭代在 …（试试更粗的粒度）"', async () => {
    // 50 小时前（默认小时级窗口 = 24 小时）⇒ 样本落在窗口外。
    await mountPanel({ response: stats([row(-3000 * MIN)]) })
    expect(await screen.findByTestId('iter-stats-empty')).toBeInTheDocument()
    expect(screen.getByText(/试试更粗的粒度/)).toBeInTheDocument()
  })

  it('单点降级：只有 1 个有样本的桶 ⇒ 单点卡（不画假曲线）', async () => {
    await mountPanel({ response: stats([row(-5 * MIN)]) })
    expect(await screen.findByTestId('iter-stats-single')).toBeInTheDocument()
    expect(screen.queryByTestId('iter-token-chart')).not.toBeInTheDocument()
    expect(screen.getByText(/数据不足以绘制趋势曲线/)).toBeInTheDocument()
  })

  it('会话身份未就绪 ⇒ 等待态，且不发 RPC（绝不伪造 chatID）', async () => {
    const { rpcCall } = await mountPanel({ identity: null })
    expect(await screen.findByTestId('iter-stats-waiting')).toBeInTheDocument()
    expect(rpcCall).not.toHaveBeenCalled()
  })

  it('RPC 失败 ⇒ 错误卡 + 重试按钮重新拉取', async () => {
    let fail = true
    const { rpcCall } = await mountPanel({
      setup: (call) => {
        call.mockImplementation(async () => {
          if (fail) throw new Error('boom')
          return stats(hourSpread())
        })
      },
    })
    expect(await screen.findByTestId('iter-stats-error')).toBeInTheDocument()
    expect(screen.getByText('boom')).toBeInTheDocument()
    fail = false
    fireEvent.click(screen.getByText('重试'))
    await waitFor(() => expect(screen.queryByTestId('iter-stats-error')).not.toBeInTheDocument())
    expect(rpcCall.mock.calls.length).toBeGreaterThan(1)
    expect(screen.getByTestId('iter-token-chart')).toBeInTheDocument()
  })

  it('hover ⇒ 十字线 + tooltip（时间桶标题 + 指标）；移出后消失', async () => {
    await mountPanel({ response: stats(hourSpread()) })
    await screen.findByTestId('iter-token-chart')
    const surface = stubSurfaceRect('iter-chart-surface')
    fireEvent.mouseMove(surface, { clientX: 0 })
    const tip = await screen.findByTestId('iter-stats-tooltip')
    expect(tip.textContent).toContain('缓存命中')
    expect(tip.textContent).toContain('TTFT')
    expect(screen.getByTestId('iter-hover-crosshair')).toBeInTheDocument()
    fireEvent.mouseLeave(surface)
    await waitFor(() => expect(screen.queryByTestId('iter-stats-tooltip')).not.toBeInTheDocument())
  })

  it('未覆盖区（LIMIT 截断）⇒ 斜纹区占位 + 图例说明，绝不画成 0 用量', async () => {
    // 样本只覆盖最近 ~2.2 小时 ⇒ 小时级窗口（24 桶）里更早的桶是"未知区"。
    await mountPanel({ response: stats(hourSpread()) })
    await screen.findByTestId('iter-token-chart')
    expect(screen.getAllByTestId('iter-stats-unknown-region').length).toBeGreaterThan(0)
    expect(screen.getByText(/未纳入统计，非 0 用量/)).toBeInTheDocument()
  })

  it('文案全部走 ctx.i18n（注入英文表即得英文界面）', async () => {
    const EN: Record<string, string> = {
      title: 'Session usage trend',
      'section.tokens': 'Token usage',
      'section.perf': 'Performance (TTFT / TPOT)',
      'coverage.range': 'Latest {{count}} iterations only: {{range}}',
    }
    await mountPanel({
      response: stats(hourSpread()),
      i18n: { locale: 'en', t: (k: string, f?: string) => EN[k] ?? f ?? k },
    })
    expect(await screen.findByText('Session usage trend')).toBeInTheDocument()
    expect(screen.getByText('Token usage')).toBeInTheDocument()
    expect(screen.getByText('Performance (TTFT / TPOT)')).toBeInTheDocument()
    // 宿主未提供的 key ⇒ 回退调用点的中文兜底（永不显示裸 key）。
    expect(screen.getAllByText(/峰值/).length).toBeGreaterThan(0)
    expect(screen.getByText('分钟')).toBeInTheDocument()
    expect(screen.getByTestId('iter-stats-coverage').textContent).toContain('Latest 3 iterations only')
  })

  it('turn.ended 事件 ⇒ 自动重拉（agent 跑完就刷新）', async () => {
    const { rpcCall, events } = await mountPanel({})
    await screen.findByTestId('iter-token-chart')
    const before = rpcCall.mock.calls.length
    expect(events.has('turn.ended')).toBe(true)
    await React.act(async () => {
      events.get('turn.ended')!({ turnID: 1, outcome: 'ok' })
    })
    await waitFor(() => expect(rpcCall.mock.calls.length).toBeGreaterThan(before))
  })

  it('手动刷新按钮触发重拉', async () => {
    const { rpcCall } = await mountPanel({})
    await screen.findByTestId('iter-token-chart')
    const before = rpcCall.mock.calls.length
    fireEvent.click(screen.getByTestId('iter-refresh'))
    await waitFor(() => expect(rpcCall.mock.calls.length).toBeGreaterThan(before))
  })
})
