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
import { Copy, Loader2 } from 'lucide-react'
import { toast } from 'sonner'

import { FoldedLine } from './FoldedLine'
import { MarkdownRenderer } from './MarkdownRenderer'
import { TurnBody } from './TurnBody'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage, CollapseLevel, LiveProgress } from '@/types/agent'

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
  // When live, prefer progress.iterationHistory (real-time SSE data). But
  // if it's empty (e.g. turnCommittedRef blocked initialProgress hydration
  // after session switch — store wasn't hydrated), fall back to
  // message.iterations (DB data) so the user sees completed iterations
  // instead of an empty assistant message with only the live iteration.
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
  // frozen（cancel）：live 行 isPartial=true 永远使 isStreaming=true → content
  // 永远走 TurnBody/LiveIteration（progress），但 progress 在 frozen 时
  // streamContent 可能为空 → 'partial reply' 不渲染（"已渲染内容永不消失"
  // 被破坏，用户报告：cancel 后 live 内容消失）。frozen live 必须用
  // message.content（MessageStore slot.live.content 保留的累积文本）。
  const isFrozenLive = message.isPartial && progress?.phase === 'frozen'
  // Do NOT change collapseLevel based on streaming state. The old code used
  // `isStreaming ? 'minimal' : collapseLevel` — this caused a height jump
  // when the turn completed (streaming→committed switched from 'minimal' to
  // 'all', folding all iterations into a summary line). The user sees their
  // content suddenly collapse — "人机对抗". Always use the user's preferred
  // collapseLevel for both streaming and committed messages.
  const effectiveLevel: CollapseLevel = collapseLevel

  // "思考中"占位符的唯一渲染点是 LiveIteration（TurnBody 内部，live 行）：
  // 第一迭代（无 iterationHistory 前置）+ 无可见内容 + streaming 时渲染。
  // 本组件【不再】渲染第二个 ShimmerThinking —— 双渲染根治（用户报告
  // "切换会话后渲染两个思考中"）：LiveIteration 的空内容分支条件与这里的
  // showThinkingIndicator 几乎完全重叠，修复 LiveIteration 首迭代渲染后两者
  // 同时渲染（.sweep-text ×2）。phase guard（isThinkingPhase）的差异留给
  // LiveIteration 内部处理（tool_exec + 无内容 + streaming 短暂显示占位符
  // 比完全空白好 —— 用户报的"切换会话后 agent 消息空白"形态）。
  const emptyResponse = isEmptyResponseContent(message.content)
  // "一个 iter 的内容只能渲染在 iter 内"（禁止任何字符串比较/内容判断 hack）：
  // - 行有迭代（iterations 非空，结构判断）：内容（含最终输出的 content）由
  //   TurnBody/IterationGroup 在迭代内渲染，message.content 是最终回复的权威
  //   副本（copy/actions/rewind 用），迭代块外不重复渲染 —— 无论迭代 content
  //   是否与 message.content 相同，都只在迭代内渲染一次。
  // - 行无迭代：message.content 是唯一内容源 → 渲染（最终回复）。
  // - turn-live（isPartial）行：LiveIteration 在迭代内渲染 progress 内容
  //   （liveHasContent）→ 不渲染；progress 空（frozen 后 reset）→ message.content
  //   渲染（"已渲染内容永不消失"—— MessageStore slot.live.content 保留的累积文本）。
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
  // still the user's content and MUST be copyable — a copy button that
  // "appears then disappears" when an iteration's thinking catches up to the
  // reply (user report) is a regression. `message.content` non-empty is the
  // correct condition.
  const showActions = !isStreaming && !!message.content && !message.displayOnly

  // ── Unified render (P2 Step2): ONE stable skeleton so the turn content never
  //    remounts across streaming→committed. GenUI is rendered by a SINGLE
  //    <SandboxedUI key={turnID}> in a fixed position; genui tools are excluded
  //    from the iteration/tool blocks (flattenIterations) so exactly one instance.
  const isAllCommitted = effectiveLevel === 'all' && !isStreaming
  const totalTools = iterations.reduce((sum, iter) => sum + iter.toolCount, 0)
  const showSummary = iterations.length > 0
  const lastIteration = iterations[iterations.length - 1]
  // finalContent 在有迭代时为空（内容由 TurnBody 迭代内渲染）—— 'all' 折叠
  // 模式显示"最后文本"必须 fallback 到最后迭代的 content（最终输出），再退
  // thinking，否则折叠后只显示推理摘要、丢失最终回复。
  const lastText = finalContent || lastIteration?.content || lastIteration?.reasoning || ''

  return (
    <div className="group/msg px-1">
      {isAllCommitted ? (
        <>
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
          {message.displayOnly && (
            <span className="mt-1 inline-block rounded bg-bg-tertiary px-1.5 py-0.5 text-xs text-text-muted md:text-[11px]">
              {t('agent.displayOnly')}
            </span>
          )}
        </>
      ) : (
        <>
          <TurnBody
            iterations={iterations}
            liveProgress={liveProgress}
            level={effectiveLevel}
            mergeTools={mergeTools}
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
        </>
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
