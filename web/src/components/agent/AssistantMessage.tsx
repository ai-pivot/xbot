/**
 * AssistantMessage — renders one assistant message.
 *
 * 3-level collapse model:
 *   'all'     — only a summary fold line + final O. Click the summary to
 *               expand into a TurnBody rendered at 'minimal' level.
 *               If the last iteration has tools, those tools are also shown
 *               after the final text.
 *   'minimal' — full TurnBody: T folded, C merged (mergeTools), O shown.
 *   'none'    — full TurnBody: T folded, C individual, O shown.
 *
 * Streaming state: when `message.isPartial`, force 'minimal' level regardless
 * of user's collapse setting. "all" (complete fold) is only for completed
 * messages. A shimmer "thinking" indicator appears at the bottom during streaming.
 */
import { memo } from 'react'

import { FoldedLine } from './FoldedLine'
import { MarkdownRenderer } from './MarkdownRenderer'
import { TurnBody } from './TurnBody'
import { ShimmerThinking } from './ShimmerThinking'
import { dedupTools } from './progressStore'
import { isToolInProgress } from './statusVisual'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, CollapseLevel, LiveProgress } from '@/types/agent'
import type { WebIteration } from '@/types/shared'

interface AssistantMessageProps {
  message: ChatMessage
  /** Live progress for a streaming message; omitted for committed history. */
  progress?: LiveProgress | null
  /** Collapse level controlling default-open for iteration history. */
  collapseLevel: CollapseLevel
  /** Whether to merge consecutive tools. Default true. */
  mergeTools?: boolean
}

function AssistantMessageImpl({ message, progress, collapseLevel, mergeTools = true }: AssistantMessageProps) {
  const { t } = useI18n()
  // ── Single source of truth ──────────────────────────────────────────
  // When a LIVE progress snapshot exists (phase != "done"), the snapshot is
  // the sole authority for the active turn. We build a unified iteration list
  // by merging snapshot.iterationHistory (completed iterations) with a
  // synthetic current iteration from the snapshot's live fields. This
  // eliminates the dual-path (message.iterations vs progress) that caused
  // tools to vanish or duplicate across refresh.
  //
  // When no live progress exists (phase="done" or null), DB history's
  // message.iterations is authoritative — no transformation needed.
  const hasLiveProgress = progress != null && progress.phase !== 'done'

  // Unified iterations: snapshot history + DB fallback. When live, the
  // snapshot's completed iterations are the source. When not live, use DB.
  const baseIterations = hasLiveProgress
    ? (progress.iterationHistory ?? [])
    : (message.iterations ?? [])

  // Synthetic current iteration from the live snapshot: reasoning + text +
  // tools that are in-flight right now. This is what CLI renders via
  // liveIterationBlocks — the "live" part of the turn.
  const currentIteration: WebIteration | null = hasLiveProgress
    ? buildLiveIteration(progress)
    : null

  // The complete iteration list for rendering: base + current.
  // When currentIteration is null (no live tools/reasoning/text), this is
  // just baseIterations — no duplication possible.
  const allRenderedIterations = currentIteration
    ? [...baseIterations, currentIteration]
    : baseIterations

  const isStreaming = message.isPartial || hasLiveProgress
  const effectiveLevel: CollapseLevel = isStreaming ? 'minimal' : collapseLevel

  // LiveIteration is no longer rendered separately by TurnBody — the current
  // iteration is merged into allRenderedIterations. Pass null so TurnBody
  // uses its unified path only.
  const liveProgress: LiveProgress | null = null

  const hasReasoning = Boolean(progress?.reasoningStreamContent || progress?.lastReasoning)
  const hasToolInProgress = progress
    ? progress.streamingTools.some((tool) => isToolInProgress(tool.status)) ||
      progress.activeTools.some((tool) => isToolInProgress(tool.status)) ||
      progress.completedTools.some((tool) => isToolInProgress(tool.status))
    : false
  const showThinkingIndicator = isStreaming && !progress?.streamContent && !hasReasoning && !hasToolInProgress
  const emptyResponse = isEmptyResponseContent(message.content)
  const finalContent = !emptyResponse && shouldRenderFinalContent(message.content, allRenderedIterations)
    ? message.content
    : ''
  const emptyResponseWarning = emptyResponse ? t('agent.emptyResponseWarning') : ''

  // 'all' level + committed: fold all intermediate content (iterations' thinking/O),
  // show only the last TEXT output. Last TEXT = message.content, or fall back to
  // the last iteration's thinking when content is empty.
  if (effectiveLevel === 'all' && !isStreaming) {
    const totalTools = allRenderedIterations.reduce((sum, iter) => sum + iter.toolCount, 0)
    const showSummary = allRenderedIterations.length > 0
    const lastIteration = allRenderedIterations[allRenderedIterations.length - 1]
    const lastText = finalContent || lastIteration?.thinking || ''

    return (
      <div className="px-1">
        {showSummary && (
          <FoldedLine
            title={t('agent.processed', { iterations: allRenderedIterations.length, tools: totalTools })}
            defaultOpen={false}
          >
            <TurnBody iterations={allRenderedIterations} level="minimal" mergeTools={mergeTools} />
          </FoldedLine>
        )}
        {lastText ? (
          <MarkdownRenderer content={lastText} />
        ) : emptyResponseWarning ? (
          <LLMEmptyResponseWarning text={emptyResponseWarning} />
        ) : (
          !showSummary && (
            <span className="text-sm text-text-muted">{t('agent.emptyAssistant')}</span>
          )
        )}
        {message.displayOnly && (
          <span className="mt-1 inline-block rounded bg-bg-tertiary px-1.5 py-0.5 text-[11px] text-text-muted">
            {t('agent.displayOnly')}
          </span>
        )}
      </div>
    )
  }

  // 'minimal'/'none' level or streaming: render full TurnBody.
  return (
    <div className="px-1">
      <TurnBody
        iterations={allRenderedIterations}
        liveProgress={liveProgress}
        level={effectiveLevel}
        mergeTools={mergeTools}
      />
      {/* Final O: for committed messages, render message.content after iterations.
          For streaming, the streamContent is already in the synthetic iteration. */}
      {!isStreaming && finalContent && (
        <MarkdownRenderer content={finalContent} />
      )}
      {!isStreaming && emptyResponseWarning && (
        <LLMEmptyResponseWarning text={emptyResponseWarning} />
      )}
      {!isStreaming && !finalContent && !emptyResponseWarning && allRenderedIterations.length === 0 && !showProgress(progress) && (
        <span className="text-sm text-text-muted">{t('agent.emptyAssistant')}</span>
      )}
      {message.displayOnly && (
        <span className="mt-1 inline-block rounded bg-bg-tertiary px-1.5 py-0.5 text-[11px] text-text-muted">
          {t('agent.displayOnly')}
        </span>
      )}
      {/* Shimmer "thinking" indicator during streaming */}
      {showThinkingIndicator && <ShimmerThinking />}
    </div>
  )
}

/**
 * Build a synthetic WebIteration from the live progress snapshot's current
 * state — the in-flight iteration that hasn't been promoted to
 * iterationHistory yet. This merges reasoning, streaming text, and tools
 * (streaming + active + completed) into one iteration, mirroring CLI's
 * liveIterationBlocks.
 *
 * Returns null when there's no visible live content (no reasoning, no text,
 * no tools) — the turn hasn't produced anything for the current iteration.
 */
function buildLiveIteration(progress: LiveProgress): WebIteration | null {
  const reasoning = progress.reasoningStreamContent || progress.lastReasoning || ''
  const text = progress.streamContent || progress.content || ''
  const tools = dedupTools([
    ...progress.streamingTools,
    ...progress.activeTools,
    ...progress.completedTools,
  ])

  if (!reasoning && !text && tools.length === 0 && progress.subAgents.length === 0) {
    return null
  }

  return {
    iteration: progress.iteration || 0,
    thinking: text,
    reasoning,
    tools,
    toolCount: tools.length,
  }
}

function shouldRenderFinalContent(content: string, iterations: WebIteration[]): boolean {
  const finalText = content.trim()
  if (!finalText) return false
  return !iterations.some((iter) => (iter.thinking || '').trim() === finalText)
}

function isEmptyResponseContent(content: string): boolean {
  return content.trim() === '(empty response)'
}

function LLMEmptyResponseWarning({ text }: { text: string }) {
  return (
    <div className="rounded border border-status-error/40 bg-status-error/10 px-2 py-1 text-sm text-status-error">
      {text}
    </div>
  )
}

/** Check if a progress snapshot has any visible content. */
function showProgress(progress?: LiveProgress | null): boolean {
  if (!progress) return false
  return Boolean(
    progress.streaming ||
      progress.activeTools.length ||
      progress.completedTools.length ||
      progress.subAgents.length ||
      progress.reasoningStreamContent ||
      progress.iteration
  )
}

export const AssistantMessage = memo(AssistantMessageImpl)
