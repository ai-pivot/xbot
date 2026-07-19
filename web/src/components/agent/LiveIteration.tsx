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
import { useTypewriter } from '@/hooks/useTypewriter'
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
  // Text output: prefer streaming (real-time), fall back to structured content
  // (snapshot from server — may arrive without preceding stream_content events)
  const textContent = progress.streamContent || progress.content || ''
  const hasStreamContent = Boolean(textContent)
  const hasSubAgents = progress.subAgents.length > 0

  // Typewriter: gradually reveal text using TUI's exponential catch-up algorithm.
  // `streaming` is the authoritative flag: set true by stream_content events,
  // set false by phase='done' / reset. Phase checks (thinking/tool) were a
  // fallback that caused streaming-content class to persist after the turn
  // ended (streaming=false but phase still 'thinking' from the last event).
  const isLive = progress.streaming
  const tw = useTypewriter(isLive ? textContent : '')
  const rw = useTypewriter(isLive ? reasoningContent : '')
  // MarkdownRenderer receives the complete source text. It parses only when
  // this source changes; the typewriter changes visibleChars and clips the
  // already-rendered text nodes instead of reparsing Markdown on every tick.
  const displayText = textContent
  const displayReasoning = reasoningContent

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
      {/* Streaming T — typewriter reveal + character count */}
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
          <div className={rw.isTyping ? 'typewriter-fade' : 'typewriter-done'}>
            <ReasoningBlock
              content={displayReasoning}
              visibleChars={isLive ? rw.visibleChars : undefined}
              streaming={false}
            />
          </div>
        </FoldedLine>
      )}

      {/* Streaming O — typewriter reveal + fade-in effect */}
      {hasStreamContent && (
        <div
          className={
            isLive
              ? `streaming-content ${tw.isTyping ? 'typewriter-fade' : 'typewriter-done'}`
              : undefined
          }
        >
          <MarkdownRenderer
            content={displayText}
            className="text-sm text-text-primary"
            streaming={isLive}
            visibleChars={isLive ? tw.visibleChars : undefined}
          />
        </div>
      )}

      {hasSubAgents && <SubAgentProgressTree nodes={progress.subAgents} />}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
