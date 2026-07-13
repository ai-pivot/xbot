/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * Groups output by iteration. Each iteration renders T → O → C via
 * IterationGroup. When a live progress snapshot is present (streaming),
 * appends a LiveIteration at the end for the in-flight iteration.
 */
import { memo } from 'react'

import { IterationGroup } from './IterationHistory'
import { LiveIteration } from './LiveIteration'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot, WebIteration } from '@/types/shared'

interface TurnBodyProps {
  iterations: WebIteration[]
  /** Live progress for an in-flight turn; null for committed history. */
  liveProgress?: ProgressSnapshot | null
  level: CollapseLevel
  mergeTools?: boolean
}

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  level,
  mergeTools = true,
}: TurnBodyProps) {
  return (
    <div className="flex flex-col gap-3">
      {iterations.map((iter, i) => (
        <IterationGroup
          key={iter.iteration ?? i}
          iteration={iter}
          level={level}
          mergeTools={mergeTools}
        />
      ))}
      {liveProgress && <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
