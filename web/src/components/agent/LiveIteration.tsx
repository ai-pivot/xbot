/**
 * LiveIteration — renders the in-flight iteration from a ProgressSnapshot.
 *
 * Streaming T (reasoning): FoldedLine wrapping ReasoningBlock with streaming
 *   indicator. Falls back to lastReasoning when streamContent is empty.
 * Streaming O (text): MarkdownRenderer with a streaming cursor indicator.
 * Streaming C (tools): FoldedToolGroup with merged streaming/active/completed
 *   tools from the snapshot.
 *
 * Render order: T → O → C (Spec A §2).
 */
import { memo } from 'react'

import { FoldedLine } from './FoldedLine'
import { FoldedToolGroup } from './FoldedToolGroup'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { SweepText } from './SweepText'
import { useI18n } from '@/providers/i18n'
import { dedupTools } from './progressStore'
import { isToolInProgress } from './statusVisual'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot } from '@/types/shared'

interface LiveIterationProps {
  progress: ProgressSnapshot
  level: CollapseLevel
  mergeTools?: boolean
}

export const LiveIteration = memo(function LiveIteration({
  progress,
  level,
  mergeTools = true,
}: LiveIterationProps) {
  const { t } = useI18n()

  // Reasoning: prefer streaming value, fall back to structured (mirrors TUI)
  const reasoningContent = progress.reasoningStreamContent || progress.lastReasoning || ''
  const hasReasoning = Boolean(reasoningContent)
  const hasStreamContent = Boolean(progress.streamContent)
  const hasSubAgents = progress.subAgents.length > 0

  // Merge all tool groups, using the shared dedupTools (generating skips dedup)
  const allTools = dedupTools([
    ...progress.streamingTools,
    ...progress.activeTools,
    ...progress.completedTools,
  ])
  const hasTools = allTools.length > 0
  const hasToolInProgress = allTools.some((tool) => isToolInProgress(tool.status))
  const reasoningInProgress = progress.streaming && progress.phase === 'thinking' && !hasStreamContent && !hasToolInProgress

  if (!hasReasoning && !hasTools && !hasStreamContent && !hasSubAgents) return null

  return (
    <div className="flex flex-col gap-1">
      {/* Streaming T — show character count */}
      {hasReasoning && (
        <FoldedLine
          title={reasoningInProgress ? (
            <SweepText
              text={t('agent.thinkingChars', { count: reasoningContent.length })}
              color="var(--text-muted)"
              className="text-xs"
            />
          ) : t('agent.thinkingChars', { count: reasoningContent.length })}
          defaultOpen={false}
        >
          <ReasoningBlock
            content={reasoningContent}
            streaming={false}
          />
        </FoldedLine>
      )}

      {/* Streaming O — inline typewriter cursor via .streaming-content ::after */}
      {hasStreamContent && (
        <div className={progress.streaming ? 'streaming-content' : undefined}>
          <MarkdownRenderer
            content={progress.streamContent}
            className="text-sm text-text-primary"
          />
        </div>
      )}

      {hasSubAgents && <SubAgentProgressTree nodes={progress.subAgents} />}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
