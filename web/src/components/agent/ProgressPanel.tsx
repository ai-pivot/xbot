/**
 * ProgressPanel — renders the live process surface for an in-flight agent turn
 * (Spec 4 §3.5, §3.6).
 *
 * In the new folding model, this delegates to TurnBody + LiveIteration which
 * handle iteration grouping, tool merging, and streaming rendering. Kept as a
 * standalone component for callers that need just the process surface without
 * the final message content.
 */
import { memo } from 'react'

import { TurnBody } from './TurnBody'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot } from '@/types/shared'

interface ProgressPanelProps {
  progress: ProgressSnapshot
  level?: CollapseLevel
}

export const ProgressPanel = memo(function ProgressPanel({
  progress,
  level = 'minimal',
}: ProgressPanelProps) {
  const hasHistory = progress.iterationHistory.length > 0
  const hasLive =
    progress.streaming ||
    progress.activeTools.length > 0 ||
    progress.completedTools.length > 0 ||
    Boolean(progress.reasoningStreamContent) ||
    Boolean(progress.streamContent)

  if (!hasHistory && !hasLive) return null

  return (
    <TurnBody
      iterations={progress.iterationHistory}
      liveProgress={progress}
      level={level}
    />
  )
})
