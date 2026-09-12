/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * 唯一渲染形态（用户要求，2026-09-12）：**每个迭代独立渲染** —— IterationGroup
 * 逐迭代输出（T 折叠 / O 文本 / C 工具 pills）。跨迭代合并工具（mergeTools）、
 * 折叠级别（CollapseLevel）、「已处理 N 次迭代」摘要行已【彻底删除】，不允许
 * 再出现任何"气泡之外"的形态。
 *
 * 流式时在末尾追加 LiveIteration 渲染进行中迭代。
 *
 * PERF（Trace-20260912T100816，用户要求"前端性能与 turn 长度完全无关"）：
 * 已提交迭代的渲染被抽进 <CommittedTurn>（memo 边界）。TurnBody 每个流式帧
 * 都会被重渲染（liveProgress 引用每帧变化），但 CommittedTurn 的 props
 * （contiguous/turnID）在流式帧之间引用稳定 → React 直接跳过整个已提交子树。
 * 流式帧的代价因此与 turn 迭代数无关（只渲染 LiveIteration）。
 *
 * 不变量：`iterations` 引用不变 ⇒ 流式帧不得重渲染任何已提交迭代。
 * 守护测试：turn_perf.test.tsx。
 */
import { memo, useMemo } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { continuousIterations } from './progressStore'
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

/**
 * CommittedTurn — 已提交迭代的唯一渲染点（memo 边界）。
 *
 * ⚠️ 性能不变量：本组件的 props 在**流式帧之间必须引用稳定**，否则
 * 每个流式帧会重渲染全部已提交迭代（trace 实测：6.2 万 DOM 节点、
 * 每帧 ~88ms、lucide/button/i18n 各占数个百分点）。
 * 调用方（TurnBody）负责用 useMemo 保持 contiguous 的引用。
 */
const CommittedTurn = memo(function CommittedTurn({ contiguous, turnID }: CommittedTurnProps) {
  return (
    <>
      {contiguous.map((iter, i) => (
        <div key={iter.iteration ?? i} data-iter-id={iter.iteration} data-turn-id={turnID}>
          <IterationGroup iteration={iter} />
          {iter.subAgents && iter.subAgents.length > 0 && (
            <SubAgentProgressTree nodes={iter.subAgents} />
          )}
        </div>
      ))}
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
      className="flex flex-col gap-1"
      data-iter-range={
        contiguous.length > 0
          ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}`
          : undefined
      }
      data-iter-total={contiguous.length}
    >
      <CommittedTurn contiguous={contiguous} turnID={turnID} />
      {liveProgress && (
        <div data-iter-id="live" data-iter-num={liveProgress.iteration || undefined} data-turn-id={liveProgress.turnID || turnID}>
          <LiveIteration progress={liveProgress} />
        </div>
      )}
    </div>
  )
})
