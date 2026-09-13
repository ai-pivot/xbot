/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * 唯一渲染形态（用户要求，2026-09-12）：**每个迭代独立渲染**（T 折叠 / O 文本 /
 * C 工具 pills）。跨迭代合并工具（mergeTools）、折叠级别（CollapseLevel）、
 * 「已处理 N 次迭代」摘要行已【彻底删除】。
 *
 * PERF-1（Trace-20260912T100816）：已提交迭代的渲染抽进 <CommittedTurn>（memo 边界），
 * 流式帧只重渲染 LiveIteration —— **React 侧**代价与迭代数无关。
 *
 * PERF-2（2026-09-13「手机上 iter 多了还是很卡」）：交互成本 ∝ **DOM 规模**
 * （移动端 + CPU 4×：N=15 → 653 节点/样式失效 89ms/打开设置 307ms；
 * N=60 → 2348 节点/262ms/655ms；`contain: layout|paint` 三变体无差别）⇒ **迭代级
 * 窗口化**：远离视口的块只留外壳 + 固定高度，内容卸载；每屏真实挂载的迭代内容由
 * **视口**决定，与 N 无关（实测 N=15 → 2 块；N=60 → 5 块；节点 2348→~350）。
 *
 * ⚠️ 正确性铁律（两条都是真实事故的教训）：
 *   1. **高度缓存必须实例作用域**（2026-09-13「切换 session 后出现空 tool iter」）：
 *      key 只能是 `turnID:iteration`，而 turnID 每会话独立 → 模块级缓存会让
 *      会话 A 的 `1084:810` 与会话 B 的同一 key 撞车 → 切到 B 后 B 的块读到 A 的
 *      "已结算高度" → 立刻被判定可冻结 → 内容卸载 → **空块**。⇒ 每个
 *      CommittedTurn 实例各持一份 `createIterationHeightTracker()`。
 *   2. **只有 settled 的高度才允许冻结** + **冻结后一次性复核**（首版把瞬态测量
 *      当成可信高度 → 卸载后内容永久消失）。`iterationHeight.ts` 里 settle 语义
 *      要求同值连续两次测量（间隔 ≥200ms）；`scheduleSettleSample` 负责补第二次
 *      采样（ResizeObserver 只在尺寸变化时回调，不会自己再报一次）；冻结后
 *      `VERIFY_DELAY_MS` 再挂一帧实测，不符即解冻重稳。
 *   3. 只对迭代号可解析（Number.isFinite）的块窗口化；从未渲染过的块必须挂载
 *      （否则永远量不到高度）；jsdom / 无 IO+RO 环境退化为全量渲染。
 */
import { memo, useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { reasoningKey } from './reasoningOpenState'
import { continuousIterations } from './progressStore'
import {
  ITERATION_HEIGHT_SETTLE_MS,
  createIterationHeightTracker,
  iterationHeightKey,
  type IterationHeightTracker,
} from './iterationHeight'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

interface TurnBodyProps {
  iterations: WebIteration[]
  /** Live progress for an in-flight turn; null for committed history. */
  liveProgress?: ProgressSnapshot | null
  /** TurnID for data-attribute debugging (data-turn-id on each block). */
  turnID?: number
}

interface CommittedTurnProps {
  /** 连续前缀迭代（useMemo 于 iterations —— 流式帧引用稳定）。 */
  contiguous: WebIteration[]
  turnID?: number
}

/** 窗口化可用环境判定（jsdom / 老浏览器没有 IO/RO → 退化为全量渲染）。 */
const canWindow = (): boolean =>
  typeof IntersectionObserver !== 'undefined' && typeof ResizeObserver !== 'undefined'

/** 冻结后的复核延迟：足够覆盖字体/异步 markdown 的定形时间。 */
const VERIFY_DELAY_MS = 400

interface IterationBlockProps {
  iter: WebIteration
  turnID?: number
  /** true = 窗口化卸载（只留外壳 + `height` 固定高度）；false = 渲染内容。 */
  muted: boolean
  /** muted 时的占位高度（**必须**是实测值，绝不允许常数）。 */
  mutedHeight?: number
  register: (hKey: string, el: HTMLDivElement | null) => void
}

/**
 * IterationBlock — 单个迭代块（外壳 + 内容/占位）。
 *
 * memo 的 props 只有 `iter`（contiguous 内引用稳定）、`turnID`、`muted`/`mutedHeight`
 * （窗口化决策，仅跨越视口边界时翻转）与稳定的 `register` —— 流式帧不会重渲染已提交
 * 迭代的内容（turn_perf.test.tsx 守护）。
 */
const IterationBlock = memo(function IterationBlock({
  iter,
  turnID,
  muted,
  mutedHeight,
  register,
}: IterationBlockProps) {
  const elRef = useRef<HTMLDivElement | null>(null)
  const hKey = iterationHeightKey(turnID, iter.iteration)
  const setRef = useCallback(
    (el: HTMLDivElement | null) => {
      elRef.current = el
      register(hKey, el)
    },
    [hKey, register],
  )

  return (
    <div
      ref={setRef}
      className="iter-block"
      data-iter-id={iter.iteration}
      data-turn-id={turnID}
      data-height-key={hKey}
      data-window-muted={muted ? 'true' : undefined}
      style={muted ? { height: mutedHeight, overflow: 'hidden' } : undefined}
    >
      {!muted && (
        <>
          <IterationGroup
            iteration={iter}
            reasoningStateKey={reasoningKey(turnID, iter.iteration ?? 0)}
          />
          {iter.subAgents && iter.subAgents.length > 0 && (
            <SubAgentProgressTree nodes={iter.subAgents} />
          )}
        </>
      )}
    </div>
  )
})

/**
 * CommittedTurn — 已提交迭代的唯一渲染点（memo 边界 + 迭代级窗口化 + 冻结复核）。
 */
const CommittedTurn = memo(function CommittedTurn({ contiguous, turnID }: CommittedTurnProps) {
  const [, bumpTick] = useReducer((n: number) => n + 1, 0)
  const nearRef = useRef<Set<number>>(new Set())
  const elements = useRef<Map<string, HTMLDivElement>>(new Map())
  const roRef = useRef<ResizeObserver | null>(null)
  const ioRef = useRef<IntersectionObserver | null>(null)
  /** 高度/结算状态：**实例作用域**（跨会话 key 撞车会造出空块，见文件头）。 */
  const trackerRef = useRef<IterationHeightTracker | null>(null)
  if (trackerRef.current === null) trackerRef.current = createIterationHeightTracker()
  const tracker = trackerRef.current
  /** 已复核过的 key（复核通过后才允许继续冻结；高度变化时清除）。 */
  const verified = useRef<Set<string>>(new Set())
  const verifyTimers = useRef<Map<string, number>>(new Map())
  /** 正在复核（临时重新挂载内容）的 key。 */
  const [verifying, setVerifying] = useState<ReadonlySet<string>>(() => new Set())
  /** 待补充的"第二次一致采样"定时器（RO 仅在尺寸变化时回调，需主动补一次）。 */
  const settleTimers = useRef<Map<string, number>>(new Map())

  /**
   * 安排一次 settle 复核采样：`ITERATION_HEIGHT_SETTLE_MS` 后重新量一次。
   * 这是"同值二次测量"的来源 —— 没有它，RO 只报一次尺寸，永远无法结算
   * （2026-09-13 实测：窗口化因此完全失效，mountedContents 15/15、60/60）。
   */
  const scheduleSettleSample = useCallback(
    (hKey: string) => {
      if (settleTimers.current.has(hKey)) return
      const timer = window.setTimeout(() => {
        settleTimers.current.delete(hKey)
        const el = elements.current.get(hKey)
        if (!el) return
        const res = tracker.record(hKey, el.getBoundingClientRect().height, performance.now())
        if (res.settled || res.changed) bumpTick()
      }, ITERATION_HEIGHT_SETTLE_MS + 50)
      settleTimers.current.set(hKey, timer)
    },
    [tracker],
  )

  // 观察器只建一次（整组块共用一个 RO / 一个 IO）。
  useEffect(() => {
    if (!canWindow()) return
    const io = new IntersectionObserver(
      (entries) => {
        let changed = false
        for (const e of entries) {
          const n = Number((e.target as HTMLElement).dataset.iterId)
          if (!Number.isFinite(n)) continue
          const was = nearRef.current.has(n)
          if (e.isIntersecting && !was) {
            nearRef.current.add(n)
            changed = true
          } else if (!e.isIntersecting && was) {
            nearRef.current.delete(n)
            changed = true
          }
        }
        if (changed) bumpTick()
      },
      // 视口上下各扩 1.2 屏 —— 滚动时下一批块已挂载好，避免"滚到才渲染"的白屏。
      { rootMargin: '120% 0px 120% 0px' },
    )
    const ro = new ResizeObserver((entries) => {
      let changed = false
      for (const e of entries) {
        const el = e.target as HTMLElement
        const key = el.dataset.heightKey
        if (!key) continue
        const res = tracker.record(key, e.contentRect.height, performance.now())
        if (res.changed) {
          // 高度变了 → 之前的复核作废，必须重新稳定 + 重新复核
          verified.current.delete(key)
          changed = true
          scheduleSettleSample(key)
        } else if (res.settled) {
          changed = true // 刚结算 → 允许冻结（需要一次渲染把 muted 决策落下）
        }
      }
      if (changed) bumpTick()
    })
    ioRef.current = io
    roRef.current = ro
    return () => {
      io.disconnect()
      ro.disconnect()
      ioRef.current = null
      roRef.current = null
      for (const t of verifyTimers.current.values()) window.clearTimeout(t)
      verifyTimers.current.clear()
      for (const t of settleTimers.current.values()) window.clearTimeout(t)
      settleTimers.current.clear()
    }
  }, [scheduleSettleSample, tracker])

  const register = useCallback((hKey: string, el: HTMLDivElement | null) => {
    const prev = elements.current.get(hKey)
    if (prev && prev !== el) {
      roRef.current?.unobserve(prev)
      ioRef.current?.unobserve(prev)
      elements.current.delete(hKey)
    }
    if (el) {
      elements.current.set(hKey, el)
      roRef.current?.observe(el)
      ioRef.current?.observe(el)
    }
  }, [])

  // 本帧的窗口化决策（muted = 已稳定 + 不在视口附近 + 迭代号可解析）。
  const decisions = contiguous.map((iter, i) => {
    const hKey = iterationHeightKey(turnID, iter.iteration)
    const height = tracker.get(hKey)
    const settled = tracker.isSettled(hKey)
    const near = iter.iteration !== undefined && nearRef.current.has(iter.iteration)
    const finite = Number.isFinite(iter.iteration)
    const wantsMute = canWindow() && height !== undefined && settled && !near && finite
    const muted = wantsMute && !verifying.has(hKey)
    return { iter, i, hKey, muted, wantsMute, height }
  })

  // 冻结复核：刚被冻结的块，延迟一帧重新挂载内容实测；不符则解冻（record 会清除
  // settled），相符则标记 verified（不再重复复核）。
  useEffect(() => {
    if (!canWindow()) return
    for (const d of decisions) {
      const { hKey, muted, wantsMute } = d
      if (!wantsMute || !muted) continue
      if (verified.current.has(hKey) || verifyTimers.current.has(hKey)) continue
      const timer = window.setTimeout(() => {
        verifyTimers.current.delete(hKey)
        const el = elements.current.get(hKey)
        if (!el) return
        setVerifying((prev) => {
          const next = new Set(prev)
          next.add(hKey)
          return next
        })
        requestAnimationFrame(() => {
          const measured = el.getBoundingClientRect().height
          const res = tracker.record(hKey, measured, performance.now())
          if (!res.changed) verified.current.add(hKey) // 复核通过：高度可信
          setVerifying((prev) => {
            const next = new Set(prev)
            next.delete(hKey)
            return next
          })
          bumpTick()
        })
      }, VERIFY_DELAY_MS)
      verifyTimers.current.set(hKey, timer)
    }
  })

  const registerStable = register
  return (
    <>
      {decisions.map(({ iter, i, muted, height }) => (
        <IterationBlock
          key={iter.iteration ?? i}
          iter={iter}
          turnID={turnID}
          muted={muted}
          mutedHeight={height}
          register={registerStable}
        />
      ))}
    </>
  )
})

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  turnID,
}: TurnBodyProps) {
  // Linear-consistency guard: 只渲染**连续前缀**（弱网丢中间迭代时不能出现 1,3）。
  // PERF: memoized on `iterations`，让流式帧保持 CommittedTurn 的 props 引用稳定
  // （turn_perf.test.tsx 守护：每帧已提交迭代渲染数 = 0）。
  const contiguous = useMemo(() => continuousIterations(iterations), [iterations])

  return (
    <div
      className="iter-blocks"
      data-iter-range={
        contiguous.length > 0
          ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}`
          : undefined
      }
      data-iter-total={contiguous.length}
    >
      <CommittedTurn contiguous={contiguous} turnID={turnID} />
      {liveProgress && (
        <div
          className="iter-block"
          data-iter-id="live"
          data-iter-num={liveProgress.iteration || undefined}
          data-turn-id={liveProgress.turnID || turnID}
        >
          <LiveIteration progress={liveProgress} />
        </div>
      )}
    </div>
  )
})
