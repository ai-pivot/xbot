/**
 * LiveIteration — renders the in-flight iteration from a ProgressSnapshot.
 *
 * Streaming T (reasoning): FoldedLine wrapping ReasoningBlock with streaming
 *   indicator. Falls back to lastReasoning when streamContent is empty.
 * Streaming C (tools): FoldedToolGroup with merged streaming/active/completed
 *   tools from the snapshot.
 * Streaming O (text): MarkdownRenderer with a streaming cursor indicator.
 */
import { memo } from 'react'

import { FoldedLine } from './FoldedLine'
import { FoldedToolGroup } from './FoldedToolGroup'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { useI18n } from '@/providers/i18n'
import { dedupTools } from './progressStore'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot } from '@/types/shared'

interface LiveIterationProps {
  progress: ProgressSnapshot
  level: CollapseLevel
}

export const LiveIteration = memo(function LiveIteration({
  progress,
  level,
}: LiveIterationProps) {
  const { t } = useI18n()

  // Reasoning: prefer streaming value, fall back to structured (mirrors TUI)
  const reasoningContent = progress.reasoningStreamContent || progress.lastReasoning || ''
  const hasReasoning = Boolean(reasoningContent)
  const hasTools =
    progress.streamingTools.length > 0 ||
    progress.activeTools.length > 0 ||
    progress.completedTools.length > 0
  const hasStreamContent = Boolean(progress.streamContent)
  const hasSubAgents = progress.subAgents.length > 0

  if (!hasReasoning && !hasTools && !hasStreamContent && !hasSubAgents) return null

  // Merge all tool groups, using the shared dedupTools (generating skips dedup)
  const allTools = dedupTools([
    ...progress.streamingTools,
    ...progress.activeTools,
    ...progress.completedTools,
  ])

  return (
    <div className="flex flex-col gap-1">
      {/* Streaming T — show character count */}
      {hasReasoning && (
        <FoldedLine
          title={t('agent.thinkingChars', { count: reasoningContent.length })}
          defaultOpen={false}
        >
          <ReasoningBlock
            content={reasoningContent}
            streaming={progress.streaming && !hasStreamContent}
          />
        </FoldedLine>
      )}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} level={level} />}

      {hasSubAgents && <SubAgentProgressTree nodes={progress.subAgents} />}

      {/* Streaming O */}
      {hasStreamContent && (
        <div>
          <MarkdownRenderer
            content={progress.streamContent}
            className="text-sm text-text-primary"
          />
          {progress.streaming && (
            <span
              className="inline-block ml-0.5 h-4 w-1.5 animate-pulse bg-text-primary align-middle"
              aria-hidden
            />
          )}
        </div>
      )}
    </div>
  )
})
