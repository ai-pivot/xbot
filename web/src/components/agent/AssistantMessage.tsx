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
import { memo, useState } from 'react'

import { FoldedLine } from './FoldedLine'
import { FoldedToolGroup } from './FoldedToolGroup'
import { MarkdownRenderer } from './MarkdownRenderer'
import { TurnBody } from './TurnBody'
import { ShimmerThinking } from './ShimmerThinking'
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
  const [summaryExpanded, setSummaryExpanded] = useState(false)

  // Source iterations: prefer committed message.iterations, fall back to live progress.
  const iterations = message.iterations?.length > 0
    ? message.iterations
    : progress?.iterationHistory ?? []

  const isStreaming = message.isPartial
  // During streaming, always use 'minimal' level (detailed fold).
  // 'all' (complete fold) is only for completed messages.
  const effectiveLevel: CollapseLevel = isStreaming ? 'minimal' : collapseLevel
  const liveProgress = isStreaming ? progress : null
  const showThinkingIndicator = isStreaming && !progress?.streamContent
  const emptyResponse = isEmptyResponseContent(message.content)
  const finalContent = !emptyResponse && shouldRenderFinalContent(message.content, iterations)
    ? message.content
    : ''
  const emptyResponseWarning = emptyResponse ? t('agent.emptyResponseWarning') : ''

  // 'all' level + committed: fold all intermediate content (iterations' thinking/O),
  // show only the last TEXT output. Last TEXT = message.content, or fall back to
  // the last iteration's thinking when content is empty.
  // If the last iteration has tools, those tools are also shown after the text.
  if (effectiveLevel === 'all' && !isStreaming) {
    const totalTools = iterations.reduce((sum, iter) => sum + iter.toolCount, 0)
    const showSummary = iterations.length > 0
    const lastIteration = iterations[iterations.length - 1]
    const lastText = finalContent || lastIteration?.thinking || ''
    const lastIterationTools = lastIteration?.tools ?? []

    return (
      <div className="agent-msg-card px-1">
        {showSummary && (
          <FoldedLine
            title={t('agent.processed', { iterations: iterations.length, tools: totalTools })}
            defaultOpen={false}
            onToggle={(open) => setSummaryExpanded(open)}
          >
            {summaryExpanded && (
              <TurnBody iterations={iterations} level="minimal" mergeTools={mergeTools} />
            )}
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
        {/* Show the last iteration's tools if they exist */}
        {lastIterationTools.length > 0 && (
          <FoldedToolGroup tools={lastIterationTools} level="minimal" mergeTools={mergeTools} />
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
    <div className="agent-msg-card px-1">
      <TurnBody
        iterations={iterations}
        liveProgress={liveProgress}
        level={effectiveLevel}
        mergeTools={mergeTools}
      />
      {/* Final O: for committed messages, render message.content after iterations.
          For streaming, the streamContent is already in LiveIteration. */}
      {!isStreaming && finalContent && (
        <MarkdownRenderer content={finalContent} />
      )}
      {!isStreaming && emptyResponseWarning && (
        <LLMEmptyResponseWarning text={emptyResponseWarning} />
      )}
      {!isStreaming && !finalContent && !emptyResponseWarning && iterations.length === 0 && !showProgress(progress) && (
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
