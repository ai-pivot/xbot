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
import { GenUIBlock } from './GenUIBlock'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { SweepText } from './SweepText'
import { isToolInProgress } from './statusVisual'
import { useI18n } from '@/providers/i18n'
import { useTypewriter } from '@/hooks/useTypewriter'
import { dedupTools } from './progressStore'
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
  // ── effectiveStreamContent: suppress streamContent that equals the last
  // completed iteration's thinking/content. After a session switch, the
  // hydrated snapshot carries the same final text in BOTH streamContent and
  // the last iteration's thinking/content — TurnBody renders the iteration
  // text, and LiveIteration would render streamContent again (duplicate).
  // Drop streamContent when it matches the last iteration's thinking.
  const lastIter = progress.iterationHistory.length > 0
    ? progress.iterationHistory[progress.iterationHistory.length - 1]
    : null
  const rawTextContent = progress.streamContent || progress.content || ''
  const textContent = (lastIter && rawTextContent && rawTextContent === lastIter.thinking)
    ? ''
    : rawTextContent
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

  // Merge all tool groups, using the shared dedupTools (generating skips dedup).
  // Filter activeTools AND completedTools by iteration number — only keep tools
  // from the current (in-flight) iteration. Tools from completed iterations are
  // already rendered by TurnBody via iterationHistory.
  //
  // We determine "completed" by comparing against the max iteration in
  // iterationHistory. Tools with iteration <= maxCompletedIter are already
  // rendered; tools with iteration > maxCompletedIter (or no iteration field
  // when iterationHistory is empty) are current.
  const maxCompletedIter = progress.iterationHistory.length > 0
    ? Math.max(...progress.iterationHistory.map((i) => i.iteration))
    : -1
  // Filter activeTools by iteration — stale activeTools from a completed
  // iteration persist because the backend clears ActiveTools (nil→omitted by
  // omitempty) so the frontend keeps the previous event's value.
  const currentActive = progress.activeTools.filter(
    (t) => t.iteration === undefined || t.iteration === null || t.iteration > maxCompletedIter,
  )
  const currentCompleted = progress.completedTools.filter(
    (t) => t.iteration === undefined || t.iteration === null || t.iteration > maxCompletedIter,
  )
  // Exclude stale completedTools already rendered in completed iterations
  // (by name+label). This filter does NOT apply to activeTools — a running
  // tool in the current iteration must NOT be filtered out just because a
  // tool with the same name+label exists in a completed iteration.
  const completedIterToolKeys = new Set<string>()
  for (const iter of progress.iterationHistory) {
    for (const tool of iter.tools) {
      completedIterToolKeys.add(`${tool.name}\x00${tool.label}`)
    }
  }
  const filteredCompleted = currentCompleted.filter(
    (t) => !completedIterToolKeys.has(`${t.name}\x00${t.label}`),
  )
  const allTools = dedupTools([
    ...progress.streamingTools,
    ...currentActive,
    ...filteredCompleted,
  ])
  const hasTools = allTools.length > 0
  const hasToolInProgress = allTools.some((tool) => isToolInProgress(tool.status))
  const reasoningInProgress = progress.streaming && progress.phase === 'thinking' && !hasStreamContent && !hasToolInProgress

  const hasGenUI = Boolean(progress.genuiContent)

  if (!hasReasoning && !hasTools && !hasStreamContent && !hasSubAgents && !hasGenUI) {
    // Iteration boundary / waiting for the next iteration's first delta: the
    // previous iteration just finished (lastIter >= 1) but the next iteration's
    // content hasn't arrived yet (slow SSE — the boundary clear is often a
    // phase:undefined stream delta with no iteration_history). Show a
    // "thinking…" placeholder so the user knows the agent is STILL WORKING
    // (not stuck) — user requirement: "iter x 结束后如果 iter x+1 还没有进度
    // 到达，就应该在 iter x+1 渲染思考中占位，防止用户以为卡死". The
    // pre-iteration phase (lastIter=0, turn just started) keeps the panel's own
    // busy placeholder instead.
    if (progress.streaming && progress.lastIter >= 1) {
      return (
        <div className="flex items-center gap-1.5 px-1 py-0.5 text-xs text-text-muted">
          <span className="animate-pulse">…</span>
          <span>{t('agent.reasoningStreaming')}</span>
        </div>
      )
    }
    return null
  }

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

      {/* Streaming GenUI — after content, before tools (GenUI is a tool product) */}
      {hasGenUI && (
        <GenUIBlock code={progress.genuiContent} streaming={progress.streaming} />
      )}

      {hasSubAgents && <SubAgentProgressTree nodes={progress.subAgents} />}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
