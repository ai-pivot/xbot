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
import { memo, useCallback } from 'react'
import { Copy } from 'lucide-react'
import { toast } from 'sonner'

import { FoldedLine } from './FoldedLine'
import { GenUIBlock } from './GenUIBlock'
import { MarkdownRenderer } from './MarkdownRenderer'
import { TurnBody } from './TurnBody'
import { ShimmerThinking } from './ShimmerThinking'
import { isToolInProgress } from './statusVisual'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, CollapseLevel, LiveProgress } from '@/types/agent'
import type { WebIteration, WebToolProgress } from '@/types/shared'
import { parseArgs } from './ToolRender'

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
  // the sole authority for the active turn:
  //   - Completed iterations ← progress.iterationHistory (snapshot only,
  //     NEVER message.iterations from DB — those overlap with completedTools)
  //   - Current in-flight iteration ← LiveIteration (rendered by TurnBody
  //     via liveProgress, with SweepText animation + running indicator)
  //
  // When no live progress exists (phase="done" or null), DB history's
  // message.iterations is authoritative — no transformation needed.
  const hasLiveProgress = progress != null && progress.phase !== 'done'

  // Completed iterations: snapshot when live, DB when not.
  // CRITICAL: when live, use progress.iterationHistory exclusively — even
  // if empty. message.iterations from DB contains the active turn's tools
  // (incremental persistence), which overlap with LiveIteration's tools.
  const iterations = hasLiveProgress
    ? (progress.iterationHistory ?? [])
    : (message.iterations ?? [])

  // LiveIteration renders the current in-flight iteration. It has its own
  // tool filtering (by iteration number) so it won't duplicate completed
  // iterations. Pass the real progress when live, null when done.
  const liveProgress: LiveProgress | null = hasLiveProgress ? progress : null

  const isStreaming = message.isPartial || hasLiveProgress
  const effectiveLevel: CollapseLevel = isStreaming ? 'minimal' : collapseLevel

  const hasReasoning = Boolean(progress?.reasoningStreamContent || progress?.lastReasoning)
  const hasToolInProgress = progress
    ? progress.streamingTools.some((tool) => isToolInProgress(tool.status)) ||
      progress.activeTools.some((tool) => isToolInProgress(tool.status)) ||
      progress.completedTools.some((tool) => isToolInProgress(tool.status))
    : false
  const hasAnyTools = progress
    ? progress.streamingTools.length > 0 ||
      progress.activeTools.length > 0 ||
      progress.completedTools.length > 0
    : false
  // Shimmer only during pure thinking (no tools, no text, no reasoning).
  // Showing it between tool completions causes flicker.
  const showThinkingIndicator = isStreaming && !progress?.streamContent && !hasReasoning && !hasToolInProgress && !hasAnyTools
  const emptyResponse = isEmptyResponseContent(message.content)
  const finalContent = !emptyResponse && shouldRenderFinalContent(message.content, iterations)
    ? message.content
    : ''
  const emptyResponseWarning = emptyResponse ? t('agent.emptyResponseWarning') : ''

  // Copy markdown content to clipboard
  const handleCopy = useCallback(() => {
    void navigator.clipboard.writeText(message.content).then(() => {
      toast.success(t('agent.copyMarkdownDone'))
    })
  }, [message.content, t])

  // Action bar shown only for completed (non-streaming) messages with content
  const showActions = !isStreaming && (finalContent || message.content.trim()) && !message.displayOnly

  // 'all' level + committed: fold all intermediate content (iterations' thinking/O),
  // show only the last TEXT output. Last TEXT = message.content, or fall back to
  // the last iteration's thinking when content is empty.
  // GenUI (display_html) is extracted and rendered OUTSIDE the fold — it has
  // special status and should never be hidden.
  if (effectiveLevel === 'all' && !isStreaming) {
    const totalTools = iterations.reduce((sum, iter) => sum + iter.toolCount, 0)
    const showSummary = iterations.length > 0
    const lastIteration = iterations[iterations.length - 1]
    const lastText = finalContent || lastIteration?.thinking || ''

    // Extract GenUI tools from all iterations — render outside the fold
    const genuiTools: WebToolProgress[] = []
    for (const iter of iterations) {
      for (const tool of iter.tools) {
        if (tool.name === 'display_html') {
          genuiTools.push(tool)
        }
      }
    }

    return (
      <div className="group/msg px-1">
        {showSummary && (
          <FoldedLine
            title={t('agent.processed', { iterations: iterations.length, tools: totalTools })}
            defaultOpen={false}
          >
            <TurnBody iterations={iterations} level="minimal" mergeTools={mergeTools} />
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
        {/* GenUI: always visible, never folded */}
        {genuiTools.map((tool, i) => (
          <GenUIBlock key={`genui-${i}`} code={(parseArgs(tool)?.code as string) || ''} />
        ))}
        {message.displayOnly && (
          <span className="mt-1 inline-block rounded bg-bg-tertiary px-1.5 py-0.5 text-[11px] text-text-muted">
            {t('agent.displayOnly')}
          </span>
        )}
        {showActions && <AssistantActions onCopy={handleCopy} t={t} />}
      </div>
    )
  }

  // 'minimal'/'none' level or streaming: render full TurnBody.
  return (
    <div className="group/msg px-1">
      <TurnBody
        iterations={iterations}
        liveProgress={liveProgress}
        level={effectiveLevel}
        mergeTools={mergeTools}
      />
      {/* Final O: for committed messages, render message.content after iterations.
          For streaming, the streamContent is already in LiveIteration.
          noDebounce disables the 150ms delay so committed content renders
          immediately (no flicker at turn completion). */}
      {!isStreaming && finalContent && (
        <MarkdownRenderer content={finalContent} noDebounce />
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
      {showActions && <AssistantActions onCopy={handleCopy} t={t} />}
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

/** Copy-MD action bar shown at the bottom-left of assistant messages. */
function AssistantActions({ onCopy, t }: {
  onCopy: () => void
  t: (key: string) => string
}) {
  return (
    <div className="mt-1 flex items-center gap-0.5">
      <button
        type="button"
        onClick={onCopy}
        title={t('agent.copyMarkdown')}
        className="flex h-6 items-center gap-1 rounded px-1.5 text-text-muted transition-opacity hover:text-text-primary hover:bg-muted"
      >
        <Copy className="size-3.5" />
      </button>
    </div>
  )
}

export const AssistantMessage = memo(AssistantMessageImpl)
