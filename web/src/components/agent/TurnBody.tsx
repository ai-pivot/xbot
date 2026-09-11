/**
 * TurnBody — renders all iterations after one User message.
 *
 * Every iteration renders individually, in order:
 *   reasoning (foldable line) → text output → tool calls (each its own card).
 *
 * There is deliberately NO cross-iteration tool merging and NO turn-level
 * collapse ("Processed N iterations · M tools"): the user requires that each
 * iteration be rendered on its own.
 *
 * When a live progress snapshot is present (streaming), appends a
 * LiveIteration at the end for the in-flight iteration.
 */
import { memo, useMemo } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { continuousIterations } from './progressStore'
import type { ProgressSnapshot, WebIteration, WebSubAgentProgress } from '@/types/shared'

interface TurnBodyProps {
  iterations: WebIteration[]
  /** Live progress for an in-flight turn; null for committed history. */
  liveProgress?: ProgressSnapshot | null
  /** TurnID for data-attribute debugging (data-turn-id on each block). */
  turnID?: number
}

/** Stable identity of a SubAgent node across the committed / live views. */
function subAgentKey(n: WebSubAgentProgress): string {
  return `${n.role}:${n.instance ?? ''}`
}

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
  // but committed iterations only change when history grows.
  const contiguous = useMemo(() => continuousIterations(iterations), [iterations])

  // SubAgent nodes already rendered by the LIVE area. A node whose `iteration`
  // is undefined is admitted by LiveIteration's filter (legacy / un-stamped
  // data), so the same node could ALSO sit in a committed iteration's frozen
  // `subAgents` — rendering it under both the iteration and the live area
  // (duplicate card + doubled row height in the virtual list).
  const liveSubAgentKeys = useMemo(
    () => new Set((liveProgress?.subAgents ?? []).map(subAgentKey)),
    [liveProgress],
  )

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
      {contiguous.map((iter, i) => {
        const frozenSubAgents = (iter.subAgents ?? []).filter(
          (n) => !liveSubAgentKeys.has(subAgentKey(n)),
        )
        return (
          <div key={iter.iteration ?? i} data-iter-id={iter.iteration} data-turn-id={turnID}>
            <IterationGroup iteration={iter} />
            {frozenSubAgents.length > 0 && <SubAgentProgressTree nodes={frozenSubAgents} />}
          </div>
        )
      })}
      {liveProgress && (
        <div
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
