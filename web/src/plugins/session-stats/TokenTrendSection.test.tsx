/**
 * TokenTrendSection / TokenTrendChart 的行为测试。
 *
 * 覆盖"fancy"部分的契约：粒度分段控件真的换窗（不是只换高亮）、hover 出十字线 +
 * 浮层明细、空数据 / 仅单点 / 未覆盖区（样本被 LIMIT 截断）三种降级都如实呈现。
 *
 * 时间全部基于显式固定的 NOW 构造（组件也从 props 拿窗口右端）⇒ 完全确定，
 * 不依赖进程时区与真实时钟。
 */
import { fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import type { UsageIterationRow } from '@/plugin-api'

import { TokenTrendSection } from './TokenTrendSection'
import { setPluginI18n } from './i18n'

/** 固定"现在"（UTC）—— 组件与用例共用同一个基准。 */
const NOW = Date.parse('2026-09-19T12:00:30Z')

function row(msAgo: number, input: number, cached: number, output: number, createdAt?: string): UsageIterationRow {
  return {
    turn_id: 1,
    iteration: 1,
    input_tokens: input,
    output_tokens: output,
    cached_tokens: cached,
    ttft_ms: 0,
    tpot_ms: 0,
    tokens_per_sec: 0,
    total_ms: 0,
    model: 'm',
    created_at: createdAt ?? new Date(NOW - msAgo).toISOString(),
  }
}

function renderSection(rows: readonly UsageIterationRow[]) {
  return render(<TokenTrendSection rows={rows} now={NOW} />)
}

/** jsdom 没有布局：给 hover 命中面一个确定的矩形，模拟真实宽度。 */
function stubSurfaceRect(width = 544, left = 0) {
  const surface = screen.getByTestId('trend-hover-surface')
  surface.getBoundingClientRect = () =>
    ({ left, top: 0, width, height: 150, right: left + width, bottom: 150, x: left, y: 0, toJSON: () => ({}) }) as DOMRect
  return surface
}

beforeEach(() => {
  // 未注入插件 i18n ⇒ 走中文 fallback（断言就按 fallback 文案）
  setPluginI18n(undefined)
})

describe('TokenTrendSection：粒度分段控件', () => {
  it('默认小时级；切到分钟级必须真的换窗（窗口描述变化）而不是只换高亮', () => {
    renderSection([row(5 * 60_000, 100, 50, 10)])
    expect(screen.getByTestId('trend-granularity-hour')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText(/最近 24 小时/)).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('trend-granularity-minute'))
    expect(screen.getByTestId('trend-granularity-minute')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByTestId('trend-granularity-hour')).toHaveAttribute('aria-pressed', 'false')
    expect(screen.getByText(/最近 60 分钟/)).toBeInTheDocument()

    fireEvent.click(screen.getByTestId('trend-granularity-day'))
    expect(screen.getByText(/最近 30 天/)).toBeInTheDocument()
  })
})

describe('TokenTrendChart：hover 交互', () => {
  it('hover 命中面 ⇒ 十字线 + 浮层（桶标签 / 本桶合计 / 命中率 / 调用次数）；移出即消失', () => {
    renderSection([row(3 * 60_000, 1000, 400, 200), row(2 * 60_000, 500, 500, 100)])
    expect(screen.queryByTestId('trend-tooltip')).toBeNull()

    const surface = stubSurfaceRect()
    // 靠右 hover ⇒ 命中最近的时间桶（左侧是未覆盖区，那是另一个用例）
    fireEvent.pointerMove(surface, { clientX: 540 })
    expect(screen.getByTestId('trend-crosshair')).toBeInTheDocument()
    const tip = screen.getByTestId('trend-tooltip')
    expect(tip).toHaveTextContent('本桶合计')
    expect(tip).toHaveTextContent('缓存命中率')
    expect(tip).toHaveTextContent('调用次数')

    fireEvent.pointerLeave(surface)
    expect(screen.queryByTestId('trend-tooltip')).toBeNull()
  })

  it('hover 到未覆盖的桶 ⇒ 明确说"不在覆盖范围内"，不显示 0 值明细', () => {
    renderSection([row(2 * 60_000, 100, 0, 10)])
    // 只有最近一小时有数据 ⇒ 左侧 23 个桶是未覆盖区
    expect(screen.getByTestId('trend-uncovered')).toBeInTheDocument()
    const surface = stubSurfaceRect()
    fireEvent.pointerMove(surface, { clientX: 2 })
    expect(screen.getByTestId('trend-tooltip')).toHaveTextContent('该区间不在明细覆盖范围内')
  })
})

describe('TokenTrendSection：降级', () => {
  it('无明细 ⇒ 空状态（说明本会话还没有明细）', () => {
    renderSection([])
    expect(screen.getByTestId('trend-empty')).toBeInTheDocument()
    expect(screen.getByText('本会话还没有落库的调用明细')).toBeInTheDocument()
  })

  it('有明细但都不在窗口内 ⇒ 空状态提示"换更粗的粒度"并报出样本条数', () => {
    const old = new Date(NOW - 10 * 24 * 3600_000).toISOString()
    renderSection([row(0, 100, 0, 10, old)])
    expect(screen.getByTestId('trend-empty')).toBeInTheDocument()
    expect(screen.getByText(/明细里有 1 条记录/)).toBeInTheDocument()
  })

  it('仅 1 个桶有数据 ⇒ 标记点 + 单点提示（面积图退化的优雅处理）', () => {
    renderSection([row(30_000, 100, 40, 10)])
    expect(screen.getByTestId('trend-single-point-marker')).toBeInTheDocument()
    expect(screen.getByTestId('trend-single-point-note')).toBeInTheDocument()
    // 单点也必须画出真实数值（不能因为只有一个点就什么都不显示）
    // 该行：input 100 + output 10 = 110 token（摘要 + 峰值区间各出现一次）
    expect(screen.getAllByText('110').length).toBeGreaterThan(0)
  })

  it('未覆盖区如实标注（样本被 LIMIT 截断 / 会话当时不存在）', () => {
    renderSection([row(2 * 60_000, 100, 0, 10)])
    expect(screen.getByTestId('trend-uncovered-note')).toHaveTextContent('未按 0 渲染')
    expect(screen.getByText(/明细 1 行/)).toBeInTheDocument()
  })

  it('时间戳解析失败的行被计数并提示（数据质量不静默）', () => {
    renderSection([row(60_000, 100, 0, 10), row(0, 0, 0, 0, 'garbage')])
    expect(screen.getByTestId('trend-unparsable-note')).toHaveTextContent('1 行时间戳无法解析')
  })
})

describe('TokenTrendSection：摘要', () => {
  it('摘要/图例给出真实合计与命中率（input=0 时命中率显示 —，不冒充 0%）', () => {
    renderSection([row(60_000, 1000, 250, 500), row(30_000, 1000, 750, 500)])
    const section = screen.getByTestId('trend-section')
    // 总量 = 2000 in + 1000 out = 3k；命中 1000/2000 = 50%
    expect(section).toHaveTextContent('3.0k')
    expect(section).toHaveTextContent('50.0%')
    expect(section).toHaveTextContent('2 次调用')
  })
})
