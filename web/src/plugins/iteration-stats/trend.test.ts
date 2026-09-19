/**
 * xbot.iteration-stats 趋势聚合单测 —— 纯函数，无 DOM。
 *
 * 覆盖：三粒度 × 空 / 边界 / 单点 / 跨日 / 时区 / 脏数据 / 覆盖区间语义，
 * 外加 SVG 几何与格式化（它们同样是纯函数，必须可确定性测试）。
 */
import { describe, expect, it } from 'vitest'
import type { UsageIterationRow } from '@/plugin-api'

import {
  aggregateTrend,
  browserTzOffsetMinutes,
  coveredBucketCount,
  filledBucketCount,
  formatBucketLabel,
  formatBucketTitle,
  formatCoverageRange,
  formatDuration,
  formatRate,
  formatStamp,
  formatTokens,
  floorToBucket,
  granularitySpec,
  hasUncoveredWindow,
  normalizeWindowBuckets,
  parseSampleTime,
  seriesPeak,
  seriesToPoints,
  smoothAreaPath,
  smoothLinePath,
  splitSegments,
} from './trend'

/** 东八区（测试统一用固定偏移，绝不依赖进程 TZ）。 */
const TZ = 480
const MIN = 60_000
const HOUR = 3_600_000
const DAY = 86_400_000

/** 构造一条明细；created_at 默认由 ts 生成 naive-UTC 串（服务端真实形态）。 */
function row(partial: Partial<UsageIterationRow> & { ts?: number }): UsageIterationRow {
  const { ts, ...rest } = partial
  return {
    turn_id: 1,
    iteration: 1,
    input_tokens: 0,
    output_tokens: 0,
    cached_tokens: 0,
    ttft_ms: 0,
    tpot_ms: 0,
    tokens_per_sec: 0,
    total_ms: 0,
    model: 'test-model',
    created_at: ts !== undefined ? naiveUtc(ts) : '2026-09-19 16:00:00',
    ...rest,
  }
}

function naiveUtc(ts: number): string {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}`
}

/** 基准"现在"：2026-09-19T16:32:20Z（东八区墙钟 2026-09-20 00:32:20）。 */
const NOW = Date.UTC(2026, 8, 19, 16, 32, 20)

describe('parseSampleTime — 时间戳形态兼容（服务端 naive UTC 是主形态）', () => {
  it('naive `YYYY-MM-DD HH:MM:SS` 按 UTC 解释（不是本地时间）', () => {
    expect(parseSampleTime('2026-09-19 16:00:00')).toBe(Date.UTC(2026, 8, 19, 16, 0, 0))
  })

  it('RFC3339 带 Z / 带 +08:00 / ISO-T 无时区 都解析到同一瞬时', () => {
    const expected = Date.UTC(2026, 8, 19, 16, 0, 0)
    expect(parseSampleTime('2026-09-19T16:00:00Z')).toBe(expected)
    expect(parseSampleTime('2026-09-20T00:00:00+08:00')).toBe(expected)
    expect(parseSampleTime('2026-09-19T16:00:00')).toBe(expected)
    expect(parseSampleTime('2026-09-19 16:00:00.250')).toBe(expected + 250)
  })

  it('epoch 秒 / 毫秒都支持；垃圾输入返回 null（绝不猜）', () => {
    expect(parseSampleTime('1758297600')).toBe(1_758_297_600_000)
    expect(parseSampleTime('1758297600000')).toBe(1_758_297_600_000)
    expect(parseSampleTime('')).toBeNull()
    expect(parseSampleTime('not-a-time')).toBeNull()
    expect(parseSampleTime(null)).toBeNull()
    expect(parseSampleTime(undefined)).toBeNull()
  })

  it('floorToBucket 在墙钟空间取整（东八区：24:00 本地 = 16:00Z 是分钟桶边界）', () => {
    const ts = Date.UTC(2026, 8, 19, 16, 32, 20)
    expect(floorToBucket(ts, MIN, TZ)).toBe(Date.UTC(2026, 8, 19, 16, 32, 0))
    expect(floorToBucket(ts, HOUR, TZ) % HOUR).toBe(0)
    // 天桶边界落在本地 00:00 = 16:00Z。
    const dayStart = floorToBucket(ts, DAY, TZ)
    expect(dayStart).toBe(Date.UTC(2026, 8, 19, 16, 0, 0))
  })

  it('normalizeWindowBuckets 裁剪到 [1, 360]', () => {
    expect(normalizeWindowBuckets(0)).toBe(1)
    expect(normalizeWindowBuckets(-5)).toBe(1)
    expect(normalizeWindowBuckets(10_000)).toBe(360)
    expect(normalizeWindowBuckets(Number.NaN)).toBe(granularitySpec('hour').windowBuckets)
  })
})

describe('aggregateTrend — 空 / 单点 / 三粒度边界', () => {
  it('空明细：连续窗口全为 0、coverageStart=null、covered 全 false（不许冒充"那段时间没用量"）', () => {
    const s = aggregateTrend([], 'minute', NOW, TZ)
    expect(s.buckets).toHaveLength(60)
    expect(s.buckets.every((b) => b.total === 0 && b.iterations === 0)).toBe(true)
    expect(s.coverageStart).toBeNull()
    expect(s.buckets.every((b) => b.covered === false)).toBe(true)
    expect(filledBucketCount(s)).toBe(0)
    expect(coveredBucketCount(s)).toBe(0)
    expect(hasUncoveredWindow(s)).toBe(true)
    expect(s.totals).toMatchObject({ iterations: 0, input: 0, cached: 0, output: 0, cacheRate: null })
    expect(s.sampleSize).toBe(0)
    expect(s.unparsableRows).toBe(0)
  })

  it('null / undefined 明细等同空（RPC 可能返回 null）', () => {
    expect(aggregateTrend(null, 'hour', NOW, TZ).sampleSize).toBe(0)
    expect(aggregateTrend(undefined, 'day', NOW, TZ).sampleSize).toBe(0)
  })

  it('分钟级同桶聚合：input / cached / uncached(派生) / output / 迭代数', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW - 5_000, input_tokens: 1000, cached_tokens: 800, output_tokens: 50 }),
        row({ ts: NOW - 15_000, input_tokens: 500, cached_tokens: 0, output_tokens: 25 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    const last = s.buckets[s.buckets.length - 1]!
    expect(last.iterations).toBe(2)
    expect(last.input).toBe(1500)
    expect(last.cached).toBe(800)
    expect(last.uncached).toBe(700)
    expect(last.output).toBe(75)
    expect(last.total).toBe(1575)
    expect(last.cacheRate).toBeCloseTo(800 / 1500)
    expect(last.covered).toBe(true)
    expect(filledBucketCount(s)).toBe(1)
  })

  it('分钟级桶边界：59.9s 与下一分钟的 0.0s 落不同桶（左闭右开）', () => {
    const base = Date.UTC(2026, 8, 19, 16, 30, 0)
    const s = aggregateTrend(
      [row({ ts: base + 59_900, output_tokens: 1 }), row({ ts: base + 60_000, output_tokens: 2 })],
      'minute',
      NOW,
      TZ,
    )
    const a = s.buckets.find((b) => b.start === base)!
    const b = s.buckets.find((x) => x.start === base + MIN)!
    expect(a.output).toBe(1)
    expect(b.output).toBe(2)
    expect(filledBucketCount(s)).toBe(2)
  })

  it('窗口外（更老）的明细被忽略计数，但它的存在证明整窗有覆盖', () => {
    const inWindow = NOW - 5 * MIN
    const outWindow = NOW - 200 * MIN
    const s = aggregateTrend(
      [
        row({ ts: outWindow, input_tokens: 999, output_tokens: 999 }),
        row({ ts: inWindow, input_tokens: 10, output_tokens: 1 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    expect(s.skippedOutOfWindow).toBe(1)
    expect(s.matchedIterations).toBe(1)
    expect(s.totals.input).toBe(10)
    // 最早样本比窗口还老 ⇒ LIMIT 截断点在窗口左侧 ⇒ 整窗都有覆盖。
    expect(s.coverageStart).toBe(s.start)
    expect(hasUncoveredWindow(s)).toBe(false)
  })

  it('样本都在窗口内且较新 ⇒ 之前的桶是未知区（留空），之后的空桶是真实的 0', () => {
    const inWindow = NOW - 5 * MIN
    const s = aggregateTrend([row({ ts: inWindow, input_tokens: 10, output_tokens: 1 })], 'minute', NOW, TZ)
    expect(s.coverageStart).toBe(floorToBucket(inWindow, MIN, TZ))
    expect(hasUncoveredWindow(s)).toBe(true)
    const unknown = s.buckets.filter((b) => !b.covered)
    expect(unknown).toHaveLength(54)
    // 覆盖起点之后、无样本的桶 = 有覆盖的真实 0（与未知区语义不同）。
    const newerEmpty = s.buckets.slice(-4, -1)
    expect(newerEmpty.every((b) => b.covered && b.total === 0)).toBe(true)
  })

  it('样本延伸到窗口左边界 ⇒ 整窗 covered（coverageStart = 窗口起点）', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW - 10 * DAY, output_tokens: 1 }),
        row({ ts: NOW - MIN, output_tokens: 2 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    expect(s.coverageStart).toBe(s.start)
    expect(hasUncoveredWindow(s)).toBe(false)
    expect(coveredBucketCount(s)).toBe(60)
  })

  it('小时级：24 桶、最后一个桶是当前小时、跨小时分层正确', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW - 2 * HOUR, input_tokens: 100 }),
        row({ ts: NOW - HOUR, input_tokens: 200 }),
        row({ ts: NOW - 60_000, input_tokens: 300 }),
      ],
      'hour',
      NOW,
      TZ,
    )
    expect(s.buckets).toHaveLength(24)
    expect(s.matchedIterations).toBe(3)
    expect(filledBucketCount(s)).toBe(3)
    expect(s.buckets[s.buckets.length - 1]!.input).toBe(300)
    expect(s.buckets[s.buckets.length - 2]!.input).toBe(200)
    expect(s.buckets[s.buckets.length - 3]!.input).toBe(100)
    expect(s.end - s.start).toBe(24 * HOUR)
  })

  it('天级跨日（东八区墙钟）：UTC 15:59 与 16:01 分属不同天桶', () => {
    const dayA = Date.UTC(2026, 8, 18, 15, 59, 0) // 本地 09-18 23:59
    const dayB = Date.UTC(2026, 8, 18, 16, 1, 0) // 本地 09-19 00:01
    const s = aggregateTrend(
      [row({ ts: dayA, output_tokens: 1 }), row({ ts: dayB, output_tokens: 2 })],
      'day',
      NOW,
      TZ,
    )
    expect(s.buckets).toHaveLength(30)
    const bucketA = s.buckets.find((b) => b.start === floorToBucket(dayA, DAY, TZ))!
    const bucketB = s.buckets.find((b) => b.start === floorToBucket(dayB, DAY, TZ))!
    expect(bucketA).not.toBe(bucketB)
    expect(bucketA.output).toBe(1)
    expect(bucketB.output).toBe(2)
    expect(bucketB.start - bucketA.start).toBe(DAY)
    expect(formatBucketLabel(bucketA.start, 'day', TZ)).toBe('09-18')
    expect(formatBucketLabel(bucketB.start, 'day', TZ)).toBe('09-19')
  })

  it('单点降级：窗口内只有一条明细 ⇒ filledBucketCount=1（图表走点渲染，不画假曲线）', () => {
    const s = aggregateTrend([row({ ts: NOW - 1000, input_tokens: 7, output_tokens: 3 })], 'minute', NOW, TZ)
    expect(filledBucketCount(s)).toBe(1)
    expect(s.matchedIterations).toBe(1)
    expect(s.totals).toMatchObject({ iterations: 1, input: 7, output: 3 })
  })

  it('乱序输入与正序输入结果完全一致（不依赖 RPC 顺序）', () => {
    const rows = [
      row({ ts: NOW - 3 * MIN, input_tokens: 30 }),
      row({ ts: NOW - 1 * MIN, input_tokens: 10 }),
      row({ ts: NOW - 2 * MIN, input_tokens: 20 }),
    ]
    const a = aggregateTrend(rows, 'minute', NOW, TZ)
    const b = aggregateTrend([...rows].reverse(), 'minute', NOW, TZ)
    expect(a.buckets).toEqual(b.buckets)
    expect(a.totals).toEqual(b.totals)
  })

  it('桶数可覆盖：windowBuckets 显式传入并裁剪', () => {
    const s = aggregateTrend([row({ ts: NOW, input_tokens: 1 })], 'minute', NOW, TZ, 5)
    expect(s.buckets).toHaveLength(5)
    expect(s.end - s.start).toBe(5 * MIN)
  })
})

describe('aggregateTrend — 脏数据 / 缺失指标', () => {
  it('cached > input 被 clamp（cached ≤ input，uncached 不为负）', () => {
    const s = aggregateTrend([row({ ts: NOW, input_tokens: 100, cached_tokens: 250 })], 'minute', NOW, TZ)
    const b = s.buckets[s.buckets.length - 1]!
    expect(b.cached).toBe(100)
    expect(b.uncached).toBe(0)
    expect(b.cacheRate).toBe(1)
  })

  it('无输入 token 的桶 cacheRate=null（不用 0 冒充命中率 0%）', () => {
    const s = aggregateTrend([row({ ts: NOW, output_tokens: 42 })], 'minute', NOW, TZ)
    const b = s.buckets[s.buckets.length - 1]!
    expect(b.cacheRate).toBeNull()
    expect(s.totals.cacheRate).toBeNull()
    expect(formatRate(b.cacheRate)).toBeNull()
  })

  it('性能指标是桶内均值，且只统计 >0 样本；无样本 ⇒ null', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW - 1000, ttft_ms: 1000, tpot_ms: 10, tokens_per_sec: 100 }),
        row({ ts: NOW - 2000, ttft_ms: 3000, tpot_ms: 30, tokens_per_sec: 200 }),
        row({ ts: NOW - 3000, ttft_ms: 0, tpot_ms: 0, tokens_per_sec: 0 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    const last = s.buckets[s.buckets.length - 1]!
    expect(last.ttftMs).toBe(2000)
    expect(last.tpotMs).toBe(20)
    expect(last.tokensPerSec).toBe(150)
    const empty = s.buckets[0]!
    expect(empty.ttftMs).toBeNull()
    expect(empty.tpotMs).toBeNull()
    expect(empty.tokensPerSec).toBeNull()
  })

  it('无法解析的 created_at 计入 unparsableRows 且不参与聚合（数据质量信号可见）', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW, input_tokens: 5 }),
        row({ created_at: '', input_tokens: 999 }),
        row({ created_at: 'garbage', input_tokens: 999 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    expect(s.unparsableRows).toBe(2)
    expect(s.matchedIterations).toBe(1)
    expect(s.totals.input).toBe(5)
    expect(s.sampleSize).toBe(3)
  })

  it('负值 / 非数字 token 归零（不产生负面积）', () => {
    const s = aggregateTrend(
      [row({ ts: NOW, input_tokens: -100, output_tokens: Number.NaN })],
      'minute',
      NOW,
      TZ,
    )
    const b = s.buckets[s.buckets.length - 1]!
    expect(b.input).toBe(0)
    expect(b.output).toBe(0)
    expect(b.total).toBe(0)
    expect(b.iterations).toBe(1)
  })
})

describe('派生判据 / 标签 / 格式化', () => {
  it('seriesPeak 取窗口内峰值（含性能指标）', () => {
    const s = aggregateTrend(
      [
        row({ ts: NOW - MIN, input_tokens: 100, output_tokens: 10, ttft_ms: 500 }),
        row({ ts: NOW, input_tokens: 300, output_tokens: 50, ttft_ms: 3200 }),
      ],
      'minute',
      NOW,
      TZ,
    )
    const peak = seriesPeak(s)
    expect(peak.input).toBe(300)
    expect(peak.output).toBe(50)
    expect(peak.total).toBe(350)
    expect(peak.ttftMs).toBe(3200)
  })

  it('formatBucketLabel / Title / Stamp 确定性（显式偏移，不依赖进程 TZ）', () => {
    const ts = Date.UTC(2026, 8, 19, 16, 32, 0)
    expect(formatBucketLabel(ts, 'minute', TZ)).toBe('00:32')
    expect(formatBucketLabel(ts, 'hour', TZ)).toBe('09-20 00:00')
    expect(formatBucketTitle(ts, 'hour', TZ)).toBe('2026-09-20 00:00–00:59')
    expect(formatBucketTitle(ts, 'day', TZ)).toBe('2026-09-20')
    expect(formatStamp(ts, TZ)).toBe('09-20 00:32')
    // 同一瞬时在 UTC（偏移 0）下是另一个墙钟 —— 证明偏移真的生效。
    expect(formatBucketLabel(ts, 'minute', 0)).toBe('16:32')
  })

  it('formatCoverageRange 用最早/最新样本给出可读区间', () => {
    const s = aggregateTrend([row({ ts: NOW - 5 * MIN }), row({ ts: NOW })], 'minute', NOW, TZ)
    expect(formatCoverageRange(s)).toBe('09-20 00:27 ~ 09-20 00:32')
    expect(formatCoverageRange(aggregateTrend([], 'minute', NOW, TZ))).toBeNull()
  })

  it('formatTokens / formatDuration / formatRate', () => {
    expect(formatTokens(999)).toBe('999')
    expect(formatTokens(1_234)).toBe('1.23k')
    expect(formatTokens(12_500)).toBe('12.5k')
    expect(formatTokens(1_234_567)).toBe('1.23M')
    expect(formatDuration(240)).toBe('240ms')
    expect(formatDuration(1200)).toBe('1.2s')
    expect(formatDuration(0)).toBeNull()
    expect(formatDuration(null)).toBeNull()
    expect(formatRate(0.934)).toBe('93%')
    expect(formatRate(null)).toBeNull()
  })

  it('browserTzOffsetMinutes 与 Date#getTimezoneOffset 反号（纯函数可注入）', () => {
    expect(browserTzOffsetMinutes()).toBe(-new Date().getTimezoneOffset())
  })
})

describe('SVG 几何纯函数', () => {
  it('seriesToPoints 跳过 null（缺指标断开，不画成跌到 0）', () => {
    const pts = seriesToPoints([0, 5, null, 10], 300, 100, 10)
    expect(pts[0]).toEqual({ x: 0, y: 100 })
    expect(pts[1]).toEqual({ x: 100, y: 50 })
    expect(pts[2]).toBeNull()
    expect(pts[3]).toEqual({ x: 300, y: 0 })
  })

  it('splitSegments 按 null 断开', () => {
    const segs = splitSegments([{ x: 0, y: 0 }, null, { x: 1, y: 1 }, { x: 2, y: 2 }])
    expect(segs).toHaveLength(2)
    expect(segs[0]).toHaveLength(1)
    expect(segs[1]).toHaveLength(2)
  })

  it('smoothLinePath：单点退化为零长段、两点为直线、三点起用三次贝塞尔', () => {
    expect(smoothLinePath([{ x: 1, y: 2 }])).toBe('M 1 2 L 1 2')
    expect(smoothLinePath([{ x: 0, y: 0 }, { x: 10, y: 10 }])).toBe('M 0 0 C 1.67 1.67, 8.33 8.33, 10 10')
    const three = smoothLinePath([
      { x: 0, y: 10 },
      { x: 10, y: 0 },
      { x: 20, y: 10 },
    ])
    expect(three).toContain('C')
    expect(three.match(/C/g)).toHaveLength(2)
    expect(smoothLinePath([])).toBe('')
  })

  it('smoothAreaPath 闭合到基线，且按断点分段', () => {
    const area = smoothAreaPath(
      [
        { x: 0, y: 10 },
        { x: 10, y: 0 },
      ],
      20,
    )
    expect(area).toMatch(/^M 0 10 .* L 10 20 L 0 20 Z$/)
    const two = smoothAreaPath([{ x: 0, y: 5 }, null, { x: 10, y: 5 }], 10)
    expect(two.match(/Z/g)).toHaveLength(2)
    expect(smoothAreaPath([], 10)).toBe('')
  })
})
