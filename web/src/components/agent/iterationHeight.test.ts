/**
 * iterationHeight 单元守护（迭代级窗口化的地基）。
 *
 * 契约（三次真实事故后确立）：
 *   1. **作用域 = 内容身份（会话 + 布局宽度），不是组件实例、也不是裸 key**
 *      （2026-09-13 两次事故各占一边）：
 *      - 裸 `turnID:iteration` 的模块级缓存 → 两个会话同 key 撞车 → 新会话读到旧会话的
 *        "已结算高度" → 立刻冻结 → **空块**（76b731de）；
 *      - 纯**实例**作用域 → 行重挂载（任何扰动布局的交互）即丢态 → 无实测高度 ⇒
 *        每个迭代块内容全部重新挂载 + markdown 全量重解析（开侧边栏 5-6 倍）。
 *      ⇒ `sharedIterationHeightTracker(scope)`：scope 含会话 ⇒ 会话间绝不串味；
 *        scope 是内容级 ⇒ 同一内容跨重挂载复用。本文件的三个隔离用例守护这条。
 *   2. **瞬态测量不得结算**：单次测量（哪怕 26.66px）永不 settled；同值连续两次、
 *      间隔 ≥ SETTLE_MS 才算；只有 settled 才允许冻结内容。
 *   3. **高度变化立即解冻**（值变了必须重新稳定），且**复核裁决一并作废**。
 *   4. 估算只用于「从未渲染过」的块显示占位，且随内容单调增长；非法值忽略。
 */
import { beforeEach, describe, expect, it } from 'vitest'

import {
  ITERATION_HEIGHT_SETTLE_MS,
  __resetSharedIterationHeightTrackers,
  createIterationHeightTracker,
  estimateIterationHeight,
  hasStableTurnKey,
  iterationHeightKey,
  sharedIterationHeightTracker,
  type IterationHeightTracker,
} from '@/components/agent/iterationHeight'
import type { WebIteration } from '@/types/shared'

const iter = (over: Partial<WebIteration>): WebIteration =>
  ({ iteration: 1, content: '', reasoning: '', tools: [], toolCount: 0, ...over }) as WebIteration

describe('estimateIterationHeight', () => {
  it('随内容量单调增长（不是常数占位）', () => {
    const small = estimateIterationHeight(iter({ content: 'x'.repeat(200) }))
    const big = estimateIterationHeight(iter({ content: 'x'.repeat(4000) }))
    expect(big).toBeGreaterThan(small * 2)
  })

  it('随 reasoning / 工具数增长', () => {
    const base = estimateIterationHeight(iter({ content: 'x'.repeat(500) }))
    const withReasoning = estimateIterationHeight(
      iter({ content: 'x'.repeat(500), reasoning: 'y'.repeat(2000) }),
    )
    const withTools = estimateIterationHeight(
      iter({
        content: 'x'.repeat(500),
        tools: Array.from({ length: 8 }, () => ({ name: 'Shell', status: 'done' })) as unknown as WebIteration['tools'],
      }),
    )
    expect(withReasoning).toBeGreaterThan(base)
    expect(withTools).toBeGreaterThan(base)
  })

  it('有下限与上限（极端输入不产生 0 或无穷）', () => {
    expect(estimateIterationHeight(iter({}))).toBeGreaterThanOrEqual(140)
    expect(estimateIterationHeight(iter({ content: 'x'.repeat(500000) }))).toBeLessThanOrEqual(6000)
  })
})

describe('IterationHeightTracker（实例作用域 + settle 语义）', () => {
  let t: IterationHeightTracker
  const key = iterationHeightKey(1084, 810)

  beforeEach(() => {
    t = createIterationHeightTracker()
  })

  it('⛔ 单次瞬态测量永不结算（内容因此不会被错误高度冻结）', () => {
    const first = t.record(key, 26.6562, 1000)
    expect(first.changed).toBe(true)
    expect(first.settled).toBe(false)
    expect(t.isSettled(key)).toBe(false)
    // 时间过去但只测过一次 → 仍不结算
    expect(t.record(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS * 5).settled).toBe(true)
  })

  it('同值两次且间隔 ≥ SETTLE_MS 才结算（这才是可信高度）', () => {
    t.record(key, 812, 1000)
    expect(t.record(key, 812, 1000 + ITERATION_HEIGHT_SETTLE_MS - 1).settled).toBe(false)
    expect(t.record(key, 812.5, 1000 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
    expect(t.isSettled(key)).toBe(true)
  })

  it('⚠️ 高度变化立即解冻（现场：先 26px 后变高）', () => {
    t.record(key, 26.6562, 1000)
    t.record(key, 26.6562, 1000 + ITERATION_HEIGHT_SETTLE_MS)
    expect(t.isSettled(key)).toBe(true)
    const res = t.record(key, 1434, 2000)
    expect(res.changed).toBe(true)
    expect(res.settled).toBe(false)
    expect(t.get(key)).toBe(1434)
  })

  it('显式解冻后必须重新稳定，不得立即再冻结', () => {
    t.record(key, 300, 0)
    t.record(key, 300, ITERATION_HEIGHT_SETTLE_MS)
    t.unsettle(key, 400)
    expect(t.isSettled(key)).toBe(false)
    expect(t.record(key, 300, 401).settled).toBe(false)
    expect(t.record(key, 300, 401 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
  })

  it('±2px 内视为同值；超过即变化（必须重新稳定）', () => {
    t.record(key, 300, 0)
    expect(t.record(key, 301.5, ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
    const t2 = createIterationHeightTracker()
    t2.record(key, 300, 0)
    expect(t2.record(key, 303, ITERATION_HEIGHT_SETTLE_MS).changed).toBe(true)
    expect(t2.isSettled(key)).toBe(false)
  })

  it('非法高度（0/负数/NaN/Infinity）被忽略', () => {
    for (const bad of [0, -5, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(t.record(key, bad, 0).changed).toBe(false)
    }
    expect(t.get(key)).toBeUndefined()
    expect(t.isSettled(key)).toBe(false)
  })

  it('⛔ 没有布局的测量（display:none 等）不得入账、不得结算、不得冒充结算', () => {
    // 面板被移动端外壳 display:none 时 RO 报 0 → 不是测量
    for (const bad of [0, 0.5, Number.NaN]) {
      const res = t.record(key, bad, 0, false)
      expect(res).toEqual({ changed: false, settled: false })
    }
    expect(t.get(key)).toBeUndefined()
    expect(t.isSettled(key)).toBe(false)

    // 已有可信高度时：无布局测量不得覆盖它，也不得把它当"刚结算"放行冻结
    t.record(key, 812, 0)
    t.record(key, 812, ITERATION_HEIGHT_SETTLE_MS)
    expect(t.isSettled(key)).toBe(true)
    expect(t.record(key, 0, 5000, false)).toEqual({ changed: false, settled: false })
    expect(t.get(key)).toBe(812) // 可信值保持
    // 可见后重新测量：新值才入账
    expect(t.record(key, 900, 6000, true).changed).toBe(true)
    expect(t.isSettled(key)).toBe(false)
  })

  it('⛔ 0 高度不得靠"同值两次"混进结算（首帧瞬态 0 不能成为冻结依据）', () => {
    t.record(key, 0, 0)
    t.record(key, 0, ITERATION_HEIGHT_SETTLE_MS * 3)
    expect(t.isSettled(key)).toBe(false)
    expect(t.get(key)).toBeUndefined()
    // 真正有布局后才可能结算
    t.record(key, 420, 10_000)
    expect(t.record(key, 420, 10_000 + ITERATION_HEIGHT_SETTLE_MS).settled).toBe(true)
  })

  it('⛔ 实例之间完全隔离（切换 session 的 key 撞车不会再冻结别人的高度）', () => {
    const sessionA = createIterationHeightTracker()
    sessionA.record(key, 1434, 0)
    sessionA.record(key, 1434, ITERATION_HEIGHT_SETTLE_MS)
    expect(sessionA.isSettled(key)).toBe(true)

    // 会话 B 的同名 key（turnID/iteration 相同但完全无关）必须"从未测量"
    const sessionB = createIterationHeightTracker()
    expect(sessionB.get(key)).toBeUndefined()
    expect(sessionB.isSettled(key)).toBe(false)
    // B 自己量到的才是 B 的
    sessionB.record(key, 320, 0)
    expect(sessionB.get(key)).toBe(320)
    expect(sessionA.get(key)).toBe(1434)
  })

  it('不同 turn / iteration 互不串味', () => {
    t.record(iterationHeightKey(1, 1), 100, 0)
    t.record(iterationHeightKey(2, 1), 200, 0)
    expect(t.get(iterationHeightKey(1, 1))).toBe(100)
    expect(t.get(iterationHeightKey(2, 1))).toBe(200)
  })

  it('⛔ 内容身份的前提：只有"稳定 turn 键"才允许共享作用域（legacy 行必须退化）', () => {
    // legacy 行（turnID 缺失/0）与 pending 行（MAX_SAFE_INTEGER）不唯一 ⇒ 不得共享
    for (const bad of [undefined, 0, -1, Number.NaN, Number.MAX_SAFE_INTEGER]) {
      expect(hasStableTurnKey(bad)).toBe(false)
    }
    expect(hasStableTurnKey(1)).toBe(true)
    expect(hasStableTurnKey(1084)).toBe(true)
    // 判据与 MessageList.getItemKey 的"稳定 turn 键"一致（同一常量语义）
    expect(hasStableTurnKey(Number.MAX_SAFE_INTEGER - 1)).toBe(true)
  })

  it('复核裁决：未复核不得冻结；通过后保持；高度一变裁决作废', () => {
    expect(t.isVerified(key)).toBe(false)
    t.markVerified(key)
    expect(t.isVerified(key)).toBe(true)
    // 高度变了 → 裁决一并作废（TurnBody 的冻结门槛要求"settled + verified"）
    t.record(key, 812, 0)
    expect(t.isVerified(key)).toBe(false)
  })

  it('复核失败 = 撤销裁决 + 解冻（必须重新稳定 + 重新复核）', () => {
    t.record(key, 812, 0)
    t.record(key, 812, ITERATION_HEIGHT_SETTLE_MS)
    t.markVerified(key)
    t.unverify(key, 5000)
    expect(t.isVerified(key)).toBe(false)
    expect(t.isSettled(key)).toBe(false)
    expect(t.get(key)).toBe(812) // 高度本身保留（复核失败不解冻高度值）
  })

  /**
   * ⛔ 「内容被裁剪（压扁）」裁决 —— 取代已被证伪的绝对高度下限 `MIN_FREEZE_HEIGHT`。
   *
   * 生产 trace 12.gz 实测：真实迭代块高度**中位数 54px / 最低 19px**，而压扁态 ~26px
   * ⇒ 高度阈值区间重叠、不可能区分（旧阈值 120px 让 `muted 7/2011`、DOM 44k）。
   * 正确判据是「内容有没有被 `max-height`/`overflow` 夹住」：夹住 ⇒ 高度不可信 ⇒
   * 既不能冻结、也不再排复核；内容一变（高度变化）⇒ 自动清除该裁决。
   */
  it('⛔ clipped 裁决：标记后不得冻结，且内容一变（高度变化）自动清除', () => {
    t.record(key, 26, 0)
    t.record(key, 26, ITERATION_HEIGHT_SETTLE_MS)
    t.markVerified(key)
    t.markClipped(key, 1000)
    expect(t.isClipped(key)).toBe(true)
    expect(t.isVerified(key)).toBe(false) // 撤销裁决 ⇒ TurnBody 不会冻结它
    expect(t.isSettled(key)).toBe(false)
    // 内容变化（压扁解除 → 真实高度）⇒ 清除 clipped ⇒ 重新走"稳定 + 复核"
    t.record(key, 812, 2000)
    expect(t.isClipped(key)).toBe(false)
    expect(t.isSettled(key)).toBe(false)
    t.record(key, 812, 2000 + ITERATION_HEIGHT_SETTLE_MS)
    expect(t.isSettled(key)).toBe(true)
    t.markVerified(key)
    expect(t.isVerified(key)).toBe(true)
  })

  it('clipped 与 clear() 一起复位（会话/作用域切换不得残留裁决）', () => {
    t.record(key, 26, 0)
    t.record(key, 26, ITERATION_HEIGHT_SETTLE_MS)
    t.markClipped(key, 500)
    expect(t.isClipped(key)).toBe(true)
    t.clear()
    expect(t.isClipped(key)).toBe(false)
    expect(t.get(key)).toBeUndefined()
    expect(t.isSettled(key)).toBe(false)
    expect(t.isVerified(key)).toBe(false)
  })
})

describe('sharedIterationHeightTracker（内容身份作用域，跨重挂载复用）', () => {
  const key = iterationHeightKey(1084, 810)

  beforeEach(() => {
    __resetSharedIterationHeightTrackers()
  })

  it('⛔ 重挂载（同一 scope）必须复用先前实测高度 + 复核裁决 —— 这是"开侧栏不再全量重解析"的地基', () => {
    const first = sharedIterationHeightTracker('chat-1|390')
    first.record(key, 1434, 0)
    first.record(key, 1434, ITERATION_HEIGHT_SETTLE_MS)
    first.markVerified(key)

    // 组件卸载又重挂（实例没了，scope 没变）→ 同一个 tracker，状态在
    const remounted = sharedIterationHeightTracker('chat-1|390')
    expect(remounted).toBe(first)
    expect(remounted.get(key)).toBe(1434)
    expect(remounted.isSettled(key)).toBe(true)
    expect(remounted.isVerified(key)).toBe(true)
  })

  it('⛔ 会话不同 = 作用域不同 → 绝不复用（76b731de 空块事故的回归守卫）', () => {
    const a = sharedIterationHeightTracker('chat-A|390')
    a.record(key, 1434, 0)
    a.record(key, 1434, ITERATION_HEIGHT_SETTLE_MS)
    a.markVerified(key)

    // 会话 B 的同名 key（turnID/iteration 相同但完全无关）必须"从未测量"
    const b = sharedIterationHeightTracker('chat-B|390')
    expect(b).not.toBe(a)
    expect(b.get(key)).toBeUndefined()
    expect(b.isSettled(key)).toBe(false)
    expect(b.isVerified(key)).toBe(false)
  })

  it('⛔ 布局宽度不同 = 作用域不同（高度不变性的前提被破坏 ⇒ 旧高度作废）', () => {
    const wide = sharedIterationHeightTracker('chat-1|390')
    wide.record(key, 1434, 0)
    wide.record(key, 1434, ITERATION_HEIGHT_SETTLE_MS)

    const narrow = sharedIterationHeightTracker('chat-1|230')
    expect(narrow).not.toBe(wide)
    expect(narrow.get(key)).toBeUndefined()
  })

  it('有界：scope 数量超过上限时淘汰最久未使用的（防无限增长）', () => {
    for (let i = 0; i < 12; i++) sharedIterationHeightTracker(`scope-${i}|390`)
    // 最早创建的已被淘汰 → 重新取到的是新实例（无缓存）
    const evicted = sharedIterationHeightTracker('scope-0|390')
    evicted.record(key, 500, 0)
    expect(evicted.get(key)).toBe(500)
    // 最近使用的仍在
    expect(sharedIterationHeightTracker('scope-11|390')).toBe(sharedIterationHeightTracker('scope-11|390'))
  })
})
