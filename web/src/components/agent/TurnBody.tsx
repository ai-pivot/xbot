/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * 唯一渲染形态（用户要求，2026-09-12）：**每个迭代独立渲染** —— IterationGroup
 * 逐迭代输出（T 折叠 / O 文本 / C 工具 pills）。跨迭代合并工具（mergeTools）、
 * 折叠级别（CollapseLevel）、「已处理 N 次迭代」摘要行已【彻底删除】，不允许
 * 再出现任何"气泡之外"的形态。
 *
 * PERF-1（Trace-20260912T100816）：已提交迭代的渲染被抽进 <CommittedTurn>
 * （memo 边界），流式帧只重渲染 LiveIteration —— 保证 **React 侧**代价与
 * turn 迭代数无关。
 *
 * PERF-2（2026-09-13 用户报告「手机上 iter 多了还是很卡，点什么交互都要等几秒」）：
 * 移动端（390×844 + CPU 4×）实测 —— 交互成本与 **DOM 规模**成正比：
 *
 *   N      节点    全局样式失效   打开设置面板
 *   15     653     89ms          307ms
 *   60     2348    262ms(×2.9)   655ms(×2.1)
 *
 * 且 `contain: layout/paint` 三变体实测**无差别**（274/267/265ms）—— containment
 * 只限定失效范围，节点仍在 DOM 里，任何触碰样式（Radix 面板给 body 加
 * pointer-events、主题/CSS 变量）都会横扫全部节点。⇒ **迭代级窗口化**：
 *
 *   - 每块的**外壳**始终保留（`data-iter-id` / `data-turn-id` / 总高度都不变，
 *     滚动稳定性与 E2E 结构断言不受影响）；
 *   - 远离视口（IntersectionObserver rootMargin 120%）且**已有实测高度**的块，
 *     卸载其内容、用固定高度占位 ⇒ 每屏真实挂载的迭代数由**视口**决定，与 N 无关；
 *   - 从未渲染过的块必须保持挂载（才能被 ResizeObserver 量出高度），一旦量到就
 *     可以卸载 —— 高度精确 ⇒ 滚动容器总高恒定（不会再有"鬼打墙"）。
 *
 * ⚠️ 占位高度绝不允许用常数（曾用 `contain-intrinsic-size: auto 320px` 导致
 * 向上滚动鬼打墙：真实块远高于占位值 → 总高边滚边涨 → 滚动锚定把内容顶回去，
 * 实测 14,704 → 31,609）。高度只允许来自 `iterationHeightCache`（实测）或
 * `estimateIterationHeight`（内容估算，±20% 量级）。
 */
import { memo, useCallback, useEffect, useMemo, useReducer, useRef } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { reasoningKey } from './reasoningOpenState'
import { continuousIterations } from './progressStore'
import {
  getCachedIterationHeight,
  iterationHeightKey,
  setCachedIterationHeight,
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

/** 窗口化可用的环境判定（jsdom / 老浏览器没有 IO/RO → 退化为全量渲染）。 */
const canWindow = (): boolean =>
  typeof IntersectionObserver !== 'undefined' && typeof ResizeObserver !== 'undefined'

/**
 * CommittedTurn — 已提交迭代的唯一渲染点（memo 边界 + 迭代级窗口化）。
 *
 * ⚠️ 性能不变量：
 *   1. props（contiguous/turnID）在**流式帧之间必须引用稳定**，否则每个流式帧
 *      重渲染全部已提交迭代（turn_perf.test.tsx 守护）；
 *   2. 远离视口的块**只保留外壳 + 固定高度**，内容卸载 —— 每屏挂载的迭代内容
 *      数量由视口决定，与 turn 迭代总数无关（MobileAppShell 真机上"点什么都要等
 *      几秒"的根治点）。
 */
const CommittedTurn = memo(function CommittedTurn({ contiguous, turnID }: CommittedTurnProps) {
  const [, bumpTick] = useReducer((n: number) => n + 1, 0)
  const nearRef = useRef<Set<number>>(new Set())
  const roRef = useRef<ResizeObserver | null>(null)
  const ioRef = useRef<IntersectionObserver | null>(null)

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
        if (setCachedIterationHeight(key, e.contentRect.height)) changed = true
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
    }
  }, [])

  // 每个外壳注册到 RO（量高）与 IO（窗口判定）。
  const observe = useCallback((el: HTMLDivElement | null) => {
    if (!el) return
    roRef.current?.observe(el)
    ioRef.current?.observe(el)
  }, [])

  return (
    <>
      {contiguous.map((iter, i) => {
        const key = iter.iteration ?? i
        const hKey = iterationHeightKey(turnID, iter.iteration)
        const cached = getCachedIterationHeight(hKey)
        const near = iter.iteration !== undefined && nearRef.current.has(iter.iteration)
        // 从未量到高度的块必须保持挂载（否则永远量不到）——量到后即可按窗口卸载。
        const mountContent = cached === undefined || near
        return (
          <div
            key={key}
            ref={observe}
            className="iter-block"
            data-iter-id={iter.iteration}
            data-turn-id={turnID}
            data-height-key={hKey}
            data-window-muted={mountContent ? undefined : 'true'}
            style={mountContent ? undefined : { height: cached, overflow: 'hidden' }}
          >
            {mountContent && (
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
      })}
    </>
  )
})

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  turnID,
}: TurnBodyProps) {
  // Linear-consistency guard: only render the CONTIGUOUS prefix of iterations.
  // On a weak network a middle iteration's delta may be dropped before
  // restoreActiveProgress backfills it — rendering iteration 3 while 2 is
  // missing would show a non-contiguous sequence (1, 3). Rendering the
  // contiguous prefix keeps the visible history linear.
  //
  // PERF: memoized on `iterations` — TurnBody re-renders every streaming frame
  // (its liveProgress prop changes identity per frame, memo can't block it),
  // but committed iterations only change when history grows. useMemo lets those
  // frames skip the O(N) contiguous-prefix scan and keeps CommittedTurn's props
  // reference-stable so React skips the entire committed subtree
  // (turn_perf.test.tsx 守护）。
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
