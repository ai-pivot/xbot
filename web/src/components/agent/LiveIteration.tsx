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
import { memo, useEffect } from 'react'

import { ThinkingLine } from './ThinkingLine'
import { FoldedToolGroup } from './FoldedToolGroup'
import { GenUICollapsiblePanel } from './GenUIPanel'

import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { ShimmerThinking } from './ShimmerThinking'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { SweepText } from './SweepText'
import { isToolInProgress } from './statusVisual'
import { useTypewriter } from '@/hooks/useTypewriter'
import { dedupTools } from './progressStore'
import { IterationSlot, setGlobalLiveStats } from '@/plugin-runtime/iteration-render'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot } from '@/types/shared'
import type { LiveStreamStats } from '@/plugin-api'

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
  // effectiveStreamContent: suppress streamContent that equals the last
  // completed iteration's content — the same final text arrives in BOTH
  // streamContent (streaming push) and the completed iteration's content
  // (snapshot); TurnBody renders the iteration text, so LiveIteration must
  // not render streamContent again. content field is the text output
  // (thinking 已彻底删除，无字符串比较 —— 直接取字段)。
  const textContent = (lastIter && rawTextContent && rawTextContent === lastIter.content)
    ? ''
    : rawTextContent
  const hasStreamContent = Boolean(textContent)
  // Top-level subAgents: only nodes belonging to the CURRENT iteration (or
  // untagged legacy data). Nodes stamped with an older iteration are rendered
  // by TurnBody under their original iteration — the live area must not show
  // them ("后台 subagent 污染最新迭代" 的前端兜底).
  const currentIter = progress.iteration
  const liveSubAgents = progress.subAgents.filter(
    (n) => n.iteration === undefined || n.iteration === currentIter,
  )
  const hasSubAgents = liveSubAgents.length > 0
  // Streaming GenUI（display_html 参数流式累积）——在工具调用的 iteration 位置
  // 实时渲染面板，而非 stream 结束后才出现。
  const hasGenUI = Boolean(progress.genuiContent)

  // Typewriter: gradually reveal text using TUI's exponential catch-up algorithm.
  // `streaming` is the authoritative flag: set true by stream_content events,
  // set false by phase='done' / reset. Phase checks (thinking/tool) were a
  // fallback that caused streaming-content class to persist after the turn
  // ended (streaming=false but phase still 'thinking' from the last event).
  const isLive = progress.streaming
  // reasoning 与 content 用同一个稳定的 isLive 标志（与 content typewriter 完全
  // 一致）。此前用 `isLive && phase === 'thinking'`，phase 在 thinking/tool_exec/
  // content 之间振荡，导致 reasoningStreaming 反复 true/false：每次 false 都让
  // typewriter 从 0 重置、MarkdownRenderer streaming 标志横跳 → 全量 re-parse
  // （推理展开时『一卡一卡』的根因）。改用 isLive 后，reasoning 停止增长时
  // typewriter 以 gap/3 自然追平并静止，无需 phase 判断。
  const reasoningStreaming = isLive
  const tw = useTypewriter(isLive ? textContent : '')
  const rw = useTypewriter(isLive ? reasoningContent : '')

  // 更新全局实时生成指标 store（ModelStatusBar / status 插件读，解耦 IterationSlot
  // Context 只能在 LiveIteration 内有效的问题）。LiveIteration 卸载（streaming 结束 /
  // committed）时清空 → ModelStatusBar 回到 idle 态。⚠️ 必须在所有条件 return 之前。
  useEffect(() => {
    setGlobalLiveStats({
      tokensPerSec: progress.streamStats?.tokensPerSec,
      ttftMs: progress.streamStats?.ttftMs,
      completionTokens: progress.tokenUsage?.completionTokens,
    } as LiveStreamStats)
    return () => setGlobalLiveStats({} as LiveStreamStats)
  }, [progress.streamStats, progress.tokenUsage])
  // MarkdownRenderer receives the complete source text. It parses only when
  // this source changes; the typewriter changes visibleChars and clips the
  // already-rendered text nodes instead of reparsing Markdown on every tick.
  const displayText = textContent
  const displayReasoning = reasoningContent

  // 折叠标题显示的思考字符数：reasoning 流式时用 typewriter 追赶值
  // rw.visibleChars（gap/3 per 50ms 平滑增长，与 content typer 同源），避免
  // reasoningContent.length 随 SSE chunk 直接跳变导致「一卡一卡」。reasoning
  // 完成后静止，显示完整长度。
  const reasoningCount = reasoningStreaming ? rw.visibleChars : reasoningContent.length

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
  // Same filter for streamingTools — a stale generating tool from a COMPLETED
  // iteration (catchup gap residue: the backend's streamState.StreamingTools is
  // merged into get_active_progress snapshots and can carry the previous
  // iteration's generating tool) must NOT render on the current iteration.
  // Previously streamingTools bypassed this filter entirely, so the old tool
  // showed until the current iteration's real tool replaced it (user report:
  // "过去的 generating 状态错误的在最新迭代上渲染").
  const currentStreaming = progress.streamingTools.filter(
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
    ...currentStreaming,
    ...currentActive,
    ...filteredCompleted,
    // 排除 genui 工具（uiMode）—— 它们由 hasGenUI 的 <GenUIPanel> 唯一渲染。
    // 不排除会导致同一 genui 双渲染（hasGenUI + FoldedToolGroup→ToolRender 各一个
    // GenUIPanel）→ 高度双倍 + DOM 反复出现/消失 + 虚拟列表高度跳变（busy 时最严重）。
  ]).filter((t) => !t.uiMode)
  const hasTools = allTools.length > 0
  const hasToolInProgress = allTools.some((tool) => isToolInProgress(tool.status))
  const reasoningInProgress = progress.streaming && progress.phase === 'thinking' && !hasStreamContent && !hasToolInProgress

  if (!hasReasoning && !hasTools && !hasStreamContent && !hasSubAgents && !hasGenUI) {
    // Iteration boundary / waiting for the next iteration's first delta: the
    // previous iteration just finished (lastIter >= 1) but the next iteration's
    // content hasn't arrived yet (slow SSE). liveMessage is non-null here, so
    // MessageList's busy placeholder ("思考中…") is suppressed — without this
    // the boundary shows a BLANK current-iteration area (user: "之前那个思考中
    // 有些情况没显示"). Reuse the SAME ShimmerThinking ("思考中…") component —
    // NOT a second indicator: it shows here when the live row exists, and in
    // the busy placeholder when the live row doesn't (mutually exclusive).
    // The FIRST iteration is special (user): iterationHistory is EMPTY (no
    // predecessor) — the busy placeholder already covers the pre-first-iter /
    // before-first-SSE window, so requiring iterationHistory.length > 0 here
    // keeps exactly ONE thinking indicator in every state.
    if (progress.streaming && progress.lastIter >= 1 && progress.iterationHistory.length > 0) {
      return <ShimmerThinking />
    }
    return null
  }

  return (
    <div className="flex flex-col gap-1">
      {/* 迭代指标（插件注入点）：把 live tokens/s 传给插件。 */}
      <IterationSlot
        data={{
          live: {
            tokensPerSec: progress.streamStats?.tokensPerSec,
            ttftMs: progress.streamStats?.ttftMs,
            completionTokens: progress.tokenUsage?.completionTokens,
          },
        }}
      />

      {/* Streaming T — typewriter reveal + character count */}
      {hasReasoning && (
        <ThinkingLine
          label={reasoningInProgress
            ? <SweepText
                text={'思考中… ' + reasoningCount + ' 字'}
                color="var(--text-muted)"
                className="text-[10px]"
              />
            : '思考 ' + reasoningCount + ' 字'}
        >
          <div className={rw.isTyping ? 'typewriter-fade' : 'typewriter-done'}>
            <ReasoningBlock
              content={displayReasoning}
              streaming={isLive}
              visibleChars={isLive ? rw.visibleChars : undefined}
            />
          </div>
        </ThinkingLine>
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

      {hasSubAgents && <SubAgentProgressTree nodes={liveSubAgents} />}

      {/* Streaming GenUI — 在工具调用位置实时渲染面板（streaming 状态） */}
      {hasGenUI && (
        <GenUICollapsiblePanel code={progress.genuiContent} streaming={isLive} />
      )}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
