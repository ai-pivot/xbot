/**
 * tokenTrend 的纯函数单测 —— 三粒度 × 边界 / 跨日 / 空 / 单点 / 时间解析。
 *
 * 全部用**显式注入的 now 与时区偏移**驱动：不依赖进程 TZ（CI 与本机结果一致）。
 */
import { describe, expect, it } from 'vitest'
import type { UsageIterationRow } from '@/plugin-api'
import {
  browserTzOffsetMinutes,
  bucketizeUsageTrend,
  buildAxis,
  floorToBucket,
  formatBucketLabel,
  monotonePath,
  monotoneSegments,
  parseServerTimestamp,
  stackedAreaPath,
  summarizeTrend,
  TREND_GRANULARITIES,
  trendGranularitySpec,
  type TrendPoint,
} from './tokenTrend'

/** 一条 per-iteration 明细（只填本模块用到的字段）。 */
function row(createdAt: string, input: number, cached: number, output: number): UsageIterationRow {
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
    created_at: createdAt,
  }
}

/** 按桶起点取桶（比硬编码下标更抗误解 —— 窗口起始随粒度/时区变）。 */
function bucketAt<T extends { start: number }>(
  trend: { buckets: readonly T[] },
  iso: string,
): T | undefined {
  return trend.buckets.find((b) => b.start === Date.parse(iso))
}

const at = (iso: string) => Date.parse(iso)

describe('粒度规格', () => {
  it('三粒度：60 分钟 / 24 小时 / 30 天', () => {
    expect(trendGranularitySpec('minute')).toEqual({ granularity: 'minute', bucketMs: 60_000, windowBuckets: 60 })
    expect(trendGranularitySpec('hour')).toEqual({ granularity: 'hour', bucketMs: 3_600_000, windowBuckets: 24 })
    expect(trendGranularitySpec('day')).toEqual({ granularity: 'day', bucketMs: 86_400_000, windowBuckets: 30 })
    expect(Object.keys(TREND_GRANULARITIES)).toEqual(['minute', 'hour', 'day'])
  })
})

describe('parseServerTimestamp：服务端时间戳解析', () => {
  it('naive "YYYY-MM-DD HH:MM:SS"（SQLite CURRENT_TIMESTAMP）按 **UTC** 解析', () => {
    // ⛔ 关键：`new Date("2026-09-19 16:03:51")` 在东八区会按本地时间解析、整体偏 8 小时。
    expect(parseServerTimestamp('2026-09-19 16:03:51')).toBe(Date.UTC(2026, 8, 19, 16, 3, 51))
    expect(parseServerTimestamp('2026-09-19 16:03:51.250')).toBe(Date.UTC(2026, 8, 19, 16, 3, 51, 250))
  })

  it('RFC3339（Z / ±HH:MM）与 ISO-T 无时区都正确', () => {
    expect(parseServerTimestamp('2026-09-19T16:03:51Z')).toBe(at('2026-09-19T16:03:51Z'))
    expect(parseServerTimestamp('2026-09-19T16:03:51+08:00')).toBe(at('2026-09-19T08:03:51Z'))
    // 无时区标记的 ISO-T 与服务端契约一致按 UTC 解释
    expect(parseServerTimestamp('2026-09-19T16:03:51')).toBe(at('2026-09-19T16:03:51Z'))
  })

  it('epoch 秒 / 毫秒；空串与垃圾串 ⇒ null（绝不猜时间）', () => {
    expect(parseServerTimestamp('1758297831')).toBe(1758297831000)
    expect(parseServerTimestamp('1758297831000')).toBe(1758297831000)
    expect(parseServerTimestamp('')).toBeNull()
    expect(parseServerTimestamp('   ')).toBeNull()
    expect(parseServerTimestamp('not-a-time')).toBeNull()
  })
})

describe('bucketizeUsageTrend：分钟级', () => {
  const NOW = at('2026-09-19T12:00:30Z')

  it('60 个连续桶、按分钟对齐、行落进正确桶', () => {
    const trend = bucketizeUsageTrend(
      [row('2026-09-19 11:59:10', 100, 40, 10), row('2026-09-19 12:00:05', 200, 0, 20)],
      'minute',
      NOW,
      0,
    )
    expect(trend.buckets).toHaveLength(60)
    expect(trend.start).toBe(at('2026-09-19T11:01:00Z'))
    expect(trend.end).toBe(at('2026-09-19T12:01:00Z'))
    // 空桶是 0 值（连续序列，趋势图不跳桶）
    expect(trend.buckets[0]).toMatchObject({ calls: 0, input: 0, cached: 0, output: 0, total: 0, cacheRate: null })

    const b58 = trend.buckets[58]
    expect(b58.start).toBe(at('2026-09-19T11:59:00Z'))
    expect(b58).toMatchObject({ calls: 1, input: 100, cached: 40, uncached: 60, output: 10, total: 110 })
    expect(b58.cacheRate).toBeCloseTo(0.4, 6)

    const b59 = trend.buckets[59]
    expect(b59.start).toBe(at('2026-09-19T12:00:00Z'))
    expect(b59).toMatchObject({ calls: 1, input: 200, cached: 0, uncached: 200, output: 20, total: 220 })
    expect(b59.cacheRate).toBe(0)

    // 覆盖区间 = 最早明细所在桶 → 首次出现之前的桶是"未知区"（不是 0 用量）
    expect(trend.coverageStart).toBe(at('2026-09-19T11:59:00Z'))
    expect(trend.buckets[57].covered).toBe(false)
    const summary = summarizeTrend(trend)
    expect(summary.uncoveredBuckets).toBe(58)
    expect(summary.activeBuckets).toBe(2)
    expect(summary.idleBuckets).toBe(0)
    expect(summary.calls).toBe(2)
    expect(summary.total).toBe(330)
  })

  it('桶边界左闭右开：起点入桶、终点归下一桶、窗口左界外被计数', () => {
    const trend = bucketizeUsageTrend(
      [
        row('2026-09-19 12:00:00', 10, 0, 0), // 最后一个桶的起点 → 入最后一桶
        row('2026-09-19 11:01:00', 20, 0, 0), // 窗口左界 → 入第一桶
        row('2026-09-19 11:00:59', 999, 0, 0), // 窗口外
      ],
      'minute',
      NOW,
      0,
    )
    expect(trend.buckets[0].input).toBe(20)
    expect(trend.buckets[59].input).toBe(10)
    expect(trend.matchedCalls).toBe(2)
    expect(trend.skippedOutOfWindow).toBe(1)
  })

  it('自定义窗口桶数：越界裁剪到 [1, 360]', () => {
    expect(bucketizeUsageTrend([], 'minute', NOW, 0, 5).buckets).toHaveLength(5)
    expect(bucketizeUsageTrend([], 'minute', NOW, 0, 0).buckets).toHaveLength(1)
    expect(bucketizeUsageTrend([], 'minute', NOW, 0, 99999).buckets).toHaveLength(360)
  })
})

describe('bucketizeUsageTrend：小时级 + 跨日（东八区）', () => {
  it('24 桶按本地整点对齐；UTC 15:30 = 本地 23:30 → 落在本地 23:00 桶', () => {
    const now = at('2026-09-19T16:30:00Z') // 本地 2026-09-20 00:30
    const trend = bucketizeUsageTrend(
      [row('2026-09-19 15:30:00', 300, 100, 30), row('2026-09-19 16:10:00', 50, 0, 5)],
      'hour',
      now,
      480,
    )
    expect(trend.buckets).toHaveLength(24)
    expect(trend.buckets[23].start).toBe(at('2026-09-19T16:00:00Z')) // 本地 09-20 00:00
    expect(trend.buckets[0].start).toBe(at('2026-09-18T17:00:00Z')) // 本地 09-19 01:00
    expect(trend.buckets[22].start).toBe(at('2026-09-19T15:00:00Z')) // 本地 23:00
    expect(trend.buckets[22].input).toBe(300)
    expect(trend.buckets[23].input).toBe(50)
    // 标签是**本地墙钟**（跨日：最后一桶是次日 00:00）
    expect(formatBucketLabel(trend.buckets[23].start, 'hour', 480)).toEqual({ primary: '00:00', secondary: '09-20' })
    expect(formatBucketLabel(trend.buckets[22].start, 'hour', 480)).toEqual({ primary: '23:00', secondary: '09-19' })
  })
})

describe('bucketizeUsageTrend：天级 + 跨日', () => {
  it('30 桶按本地午夜对齐；UTC 15:59 归本地 09-19，UTC 16:01 归本地 09-20', () => {
    const now = at('2026-09-19T16:30:00Z') // 本地 09-20 00:30
    const trend = bucketizeUsageTrend(
      [row('2026-09-19 15:59:00', 10, 0, 1), row('2026-09-19 16:01:00', 20, 0, 2)],
      'day',
      now,
      480,
    )
    expect(trend.buckets).toHaveLength(30)
    expect(trend.buckets[29].start).toBe(at('2026-09-19T16:00:00Z')) // 本地 09-20 00:00
    expect(trend.buckets[0].start).toBe(at('2026-08-21T16:00:00Z')) // 本地 08-22 00:00
    expect(trend.buckets[28].input).toBe(10)
    expect(trend.buckets[29].input).toBe(20)
    expect(formatBucketLabel(trend.buckets[28].start, 'day', 480)).toEqual({ primary: '09-19', secondary: '2026' })
    expect(formatBucketLabel(trend.buckets[29].start, 'day', 480)).toEqual({ primary: '09-20', secondary: '2026' })
  })

  it('UTC（偏移 0）与西八区（-480）边界一致地跨日', () => {
    // 西八区：UTC 2026-09-19 03:00 = 本地 09-18 19:00
    const now = at('2026-09-19T03:00:00Z')
    const trend = bucketizeUsageTrend([row('2026-09-19 02:30:00', 7, 0, 0)], 'day', now, -480)
    expect(formatBucketLabel(trend.buckets[29].start, 'day', -480)).toEqual({ primary: '09-18', secondary: '2026' })
    expect(trend.buckets[29].input).toBe(7)
    expect(floorToBucket(at('2026-09-19T02:30:00Z'), 86_400_000, -480)).toBe(at('2026-09-18T08:00:00Z'))
  })
})

describe('空数据 / 单点 / 脏数据', () => {
  it('空明细：全 0 桶、无覆盖、无峰值，且绝不谎报命中率', () => {
    const trend = bucketizeUsageTrend([], 'hour', at('2026-09-19T12:00:00Z'), 0)
    expect(trend.buckets).toHaveLength(24)
    expect(trend.buckets.every((b) => b.total === 0 && b.cacheRate === null)).toBe(true)
    expect(trend.coverageStart).toBeNull()
    expect(trend.sampleSize).toBe(0)
    const s = summarizeTrend(trend)
    expect(s).toMatchObject({ input: 0, output: 0, total: 0, calls: 0, cacheRate: null, peak: null, activeBuckets: 0 })
    expect(s.uncoveredBuckets).toBe(24)
  })

  it('单点：只有一个桶有数据，peak 指向它（图表降级用）', () => {
    const trend = bucketizeUsageTrend([row('2026-09-19 11:30:00', 500, 250, 100)], 'hour', at('2026-09-19T12:00:00Z'), 0)
    const s = summarizeTrend(trend)
    expect(s.activeBuckets).toBe(1)
    expect(s.calls).toBe(1)
    expect(s.peak?.total).toBe(600)
    expect(s.cacheRate).toBeCloseTo(0.5, 6)
    expect(trend.coverageStart).toBe(at('2026-09-19T11:00:00Z'))
    // 覆盖起点之前的 22 个小时桶是"未知区"（绝不当 0 用量渲染）
    expect(s.uncoveredBuckets).toBe(22)
    expect(bucketAt(trend, '2026-09-19T11:00:00Z')?.covered).toBe(true)
    expect(bucketAt(trend, '2026-09-19T10:00:00Z')?.covered).toBe(false)
  })

  it('窗口外行 / 不可解析行都被计数，且不污染覆盖区间', () => {
    const trend = bucketizeUsageTrend(
      [
        row('2026-09-18 10:00:00', 5, 0, 5), // 窗口外（比窗口更老）
        row('', 999, 0, 999), // 不可解析
        row('2026-09-19 11:45:00', 42, 0, 0), // 窗口内
      ],
      'hour',
      at('2026-09-19T12:00:00Z'),
      0,
    )
    expect(trend.sampleSize).toBe(3)
    expect(trend.matchedCalls).toBe(1)
    expect(trend.skippedOutOfWindow).toBe(1)
    expect(trend.unparsableRows).toBe(1)
    expect(trend.oldestSampleAt).toBe(at('2026-09-18T10:00:00Z'))
    expect(trend.newestSampleAt).toBe(at('2026-09-19T11:45:00Z'))
    // 明细延伸到了窗口左边（更老的行被跳过）⇒ 覆盖起点就是窗口起点
    expect(trend.coverageStart).toBe(trend.start)
    expect(trend.buckets[0].covered).toBe(true)
  })

  it('cached > input 被钳制；input=0 的桶命中率是 null（不是 0%）', () => {
    const trend = bucketizeUsageTrend(
      [row('2026-09-19 11:10:00', 100, 150, 10), row('2026-09-19 11:20:00', 0, 0, 80)],
      'hour',
      at('2026-09-19T12:00:00Z'),
      0,
    )
    const b = bucketAt(trend, '2026-09-19T11:00:00Z')!
    expect(b.cached).toBe(100)
    expect(b.uncached).toBe(0)
    expect(b.cacheRate).toBe(1)
    // 两个条目在同一小时桶 → 合计 input=100（第二行 input=0）⇒ 命中率仍是 1（有输入）
    expect(b.calls).toBe(2)
    expect(b.output).toBe(90)
    // 其它桶 input=0 ⇒ cacheRate null（不许拿 0 冒充"命中率 0%"）
    expect(bucketAt(trend, '2026-09-19T10:00:00Z')?.cacheRate).toBeNull()
  })
})

describe('formatBucketLabel / buildAxis', () => {
  it('分钟级标签 = HH:MM + MM-DD', () => {
    expect(formatBucketLabel(at('2026-09-19T11:59:00Z'), 'minute', 0)).toEqual({
      primary: '11:59',
      secondary: '09-19',
    })
  })

  it('buildAxis：上限 ≥ 数据、刻度从 0 到 max、升序且数量可控', () => {
    const axis = buildAxis(12_345, 4)
    expect(axis.max).toBeGreaterThanOrEqual(12_345)
    expect(axis.ticks[0]).toBe(0)
    expect(axis.ticks[axis.ticks.length - 1]).toBe(axis.max)
    expect(axis.ticks.length).toBeGreaterThanOrEqual(2)
    expect(axis.ticks.length).toBeLessThanOrEqual(12)
    for (let i = 1; i < axis.ticks.length; i++) expect(axis.ticks[i]).toBeGreaterThan(axis.ticks[i - 1])

    expect(buildAxis(0)).toEqual({ max: 1, ticks: [0, 1] })
    expect(buildAxis(Number.NaN)).toEqual({ max: 1, ticks: [0, 1] })
    expect(buildAxis(7).max).toBeGreaterThanOrEqual(7)
  })
})

describe('曲线几何（平滑堆叠面积）', () => {
  const pts: TrendPoint[] = [
    { x: 0, y: 0 },
    { x: 10, y: 40 },
    { x: 20, y: 40 },
    { x: 30, y: 5 },
  ]

  it('monotoneSegments 不过冲（控制点落在数据带的 ±eps 内），且点数 <2 返回空', () => {
    expect(monotoneSegments([])).toEqual([])
    expect(monotoneSegments([{ x: 0, y: 5 }])).toEqual([])
    const segs = monotoneSegments(pts)
    expect(segs).toHaveLength(3)
    const ys = [0, 40, 40, 5]
    for (const s of segs) {
      for (const y of [s.y0, s.cy1, s.cy2, s.y1]) {
        expect(Number.isFinite(y)).toBe(true)
        expect(y).toBeGreaterThanOrEqual(Math.min(...ys) - 1e-6)
        expect(y).toBeLessThanOrEqual(Math.max(...ys) + 1e-6)
      }
    }
  })

  it('monotonePath：空 ⇒ ""；单点 ⇒ 只有 M；多点 ⇒ M + C 段数 = n-1', () => {
    expect(monotonePath([])).toBe('')
    expect(monotonePath([{ x: 3, y: 4 }])).toBe('M3,4')
    const path = monotonePath(pts)
    expect(path.startsWith('M0,0')).toBe(true)
    expect(path.match(/C/g)).toHaveLength(3)
    expect(path).not.toMatch(/NaN|Infinity/)
  })

  it('stackedAreaPath：闭合区域、上下边界都参与、长度不匹配按较短者截断', () => {
    const bottom: TrendPoint[] = [
      { x: 0, y: 0 },
      { x: 10, y: 0 },
      { x: 20, y: 0 },
      { x: 30, y: 0 },
    ]
    const area = stackedAreaPath(pts, bottom)
    expect(area.endsWith('Z')).toBe(true)
    expect(area.match(/C/g)).toHaveLength(6) // 上边界 3 段 + 下边界反向 3 段
    expect(area).toContain('L30,0') // 右侧落到下边界
    expect(area).not.toMatch(/NaN|Infinity/)

    expect(stackedAreaPath([], [])).toBe('')
    expect(stackedAreaPath([{ x: 1, y: 2 }], [])).toBe('')
    // 单点 ⇒ 退化成竖线（图表用标记点渲染）
    expect(stackedAreaPath([{ x: 5, y: 9 }], [{ x: 5, y: 0 }])).toBe('M5,9L5,0Z')
    // 长度不一致 ⇒ 取较短
    expect(stackedAreaPath(pts, bottom.slice(0, 2))).not.toMatch(/NaN/)
  })
})

describe('browserTzOffsetMinutes', () => {
  it('是 getTimezoneOffset 的反号（东为正）', () => {
    expect(browserTzOffsetMinutes()).toBe(-new Date().getTimezoneOffset())
  })
})
