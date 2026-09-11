/**
 * AssistantMessage — renders one assistant message.
 *
 * Iterations are ALWAYS rendered individually (each with its own reasoning
 * line, text output and tool cards). There is deliberately no collapse level
 * and no "Processed N iterations · M tools" summary fold line.
 *
 * Streaming state: when a LIVE progress snapshot exists (phase != "done"), it
 * is the sole authority for the active turn — completed iterations come from
 * progress.iterationHistory, the in-flight one from LiveIteration (rendered by
 * TurnBody via liveProgress). When there is no live progress, the message's own
 * iteration history (DB) is authoritative.
 */
import { memo, useCallback } from 'react'
import { Copy, Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { MarkdownRenderer } from './MarkdownRenderer'
import { TurnBody } from './TurnBody'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, LiveProgress } from '@/types/agent'

interface AssistantMessageProps {
  message: ChatMessage
  /** Live progress for a streaming message; omitted for committed history. */
  progress?: LiveProgress | null
}

function AssistantMessageImpl({ message, progress }: AssistantMessageProps) {
  const { t } = useI18n()
  // ── Single source of truth ──────────────────────────────────────────
  // When live, prefer progress.iterationHistory (real-time SSE data). But if
  // it's empty (e.g. the store wasn't hydrated after a session switch), fall
  // back to message.iterations (DB data) so the user sees completed iterations
  // instead of an empty assistant message with only the live iteration.
  const hasLiveProgress = progress != null && progress.phase !== 'done'
  const progressIters = progress?.iterationHistory ?? []
  const dbIters = message.iterations ?? []
  const iterations = hasLiveProgress
    ? (progressIters.length > 0 ? progressIters : dbIters)
    : dbIters

  // LiveIteration renders the current in-flight iteration. It has its own
  // tool filtering (by iteration number) so it won't duplicate completed
  // iterations. Pass the real progress when live, null when done.
  const liveProgress: LiveProgress | null = hasLiveProgress ? progress : null
  const isStreaming = message.isPartial || hasLiveProgress
  // frozen（cancel）：live 行 isPartial=true 永远使 isStreaming=true → 内容由
  // TurnBody/LiveIteration 渲染，但 frozen 时 progress 的 streamContent 可能为空
  // → 'partial reply' 不渲染。frozen live 必须用 message.content（MessageStore
  // slot.live.content 保留的累积文本），保证"已渲染内容永不消失"。
  const isFrozenLive = message.isPartial && progress?.phase === 'frozen'

  const emptyResponse = isEmptyResponseContent(message.content)
  // "一个 iter 的内容只能渲染在 iter 内"：
  // - 行有迭代（iterations 非空）：内容由 TurnBody/IterationGroup 在迭代内渲染，
  //   message.content 是最终回复的权威副本（copy/actions/rewind 用）。
  // - 行无迭代：message.content 是唯一内容源 → 渲染（最终回复）。
  // - turn-live（isPartial）行：LiveIteration 在迭代内渲染 progress 内容。
  const hasIterations = iterations.length > 0
  const liveHasContent = hasLiveProgress && Boolean(progress?.streamContent || progress?.content)
  const finalContent = !emptyResponse && !hasIterations && !liveHasContent
    ? message.content
    : ''
  const emptyResponseWarning = emptyResponse ? t('agent.emptyResponseWarning') : ''

  // Copy markdown content to clipboard
  const handleCopy = useCallback(() => {
    void navigator.clipboard.writeText(message.content).then(() => {
      toast.success(t('agent.copyMarkdownDone'))
    })
  }, [message.content, t])

  // Action bar shown for completed (non-streaming) messages with content.
  // Use `message.content` (the authoritative final reply), NOT `finalContent`:
  // finalContent is empty when the content duplicates an iteration's thinking
  // (render dedup — same text on both paths). In that case the final reply is
  // still the user's content and MUST be copyable.
  const showActions = !isStreaming && !!message.content && !message.displayOnly

  return (
    <div className="group/msg px-1">
      <TurnBody
        iterations={iterations}
        liveProgress={liveProgress}
        turnID={message.turnID}
      />
      {(!isStreaming || isFrozenLive) && finalContent && (
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
      {isStreaming && liveProgress?.phase === 'compressing' && (
        <div className="mt-2 flex items-center gap-2 text-xs text-text-muted">
          <Loader2 className="size-3.5 animate-spin" />
          <span>{t('agent.compressing')}</span>
        </div>
      )}

      {showActions && <AssistantActions onCopy={handleCopy} t={t} />}
    </div>
  )
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
