/**
 * LiveIteration — renders the in-flight iteration from a ProgressSnapshot.
 *
 * Streaming T (reasoning): FoldedLine wrapping ReasoningBlock with streaming
 *   indicator. Falls back to lastReasoning when streamContent is empty.
 * Streaming O (text): MarkdownRenderer with a streaming cursor indicator.
 * Streaming C (tools): ToolGroup — every tool from the snapshot rendered as its
 *   own expanded card.
 *
 * Render order: T → O → C (Spec A §2).
 */
import { memo, useEffect, useMemo } from 'react'

import { ThinkingLine } from './ThinkingLine'
import { ToolGroup } from './ToolGroup'
import { GenUICollapsiblePanel } from './GenUIPanel'

import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { ShimmerThinking } from './ShimmerThinking'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { SweepText } from './SweepText'
import { isToolInProgress } from './statusVisual'
import { useTypewriter } from '@/hooks/useTypewriter'
import { useI18n } from '@/providers/i18n'
import { dedupTools } from './progressStore'
import { IterationSlot, setGlobalLiveStats } from '@/plugin-runtime/iteration-render'
import type { ProgressSnapshot } from '@/types/shared'
import type { LiveStreamStats } from '@/plugin-api'

interface LiveIterationProps {
  progress: ProgressSnapshot
}

export const LiveIteration = memo(function LiveIteration({ progress }: LiveIterationProps) {
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
  // PERF note: liveSubAgents/hasSubAgents are memoized below (toolDeriv block) —
  // the original inline filter recomputed on every frame.
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

  // PERF：工具派生链 useMemo（字段级依赖）—— 此前每帧重算（progress prop 每帧
  // 新引用，memo 挡不住流式帧）。reduce 的 withTurn patch 是结构性共享：stream 帧
  // 只改 content/reasoning/streamingTools/genui 字段（`...prev` 引用透传），
  // activeTools/completedTools/iterationHistory 引用不变 → 字段级 useMemo 让
  // stream 帧跳过全部工具推导（3×filter + O(H×tools) Set 构建 + dedupTools +
  // Math.max spread）。输入相同 → 输出必然相同（纯函数），零行为差异。
  const liveSubAgents = useMemo(() =>
    progress.subAgents.filter(
      (n) => n.iteration === undefined || n.iteration === currentIter,
    ),
  [progress.subAgents, currentIter])
  const hasSubAgents = liveSubAgents.length > 0

  const toolDeriv = useMemo(() => {
    const maxCompletedIter = progress.iterationHistory.length > 0
      ? Math.max(...progress.iterationHistory.map((i) => i.iteration))
      : -1
    const currentActive = progress.activeTools.filter(
      (t) => t.iteration === undefined || t.iteration === null || t.iteration > maxCompletedIter,
    )
    const currentStreaming = progress.streamingTools.filter(
      (t) => t.iteration === undefined || t.iteration === null || t.iteration > maxCompletedIter,
    )
    const currentCompleted = progress.completedTools.filter(
      (t) => t.iteration === undefined || t.iteration === null || t.iteration > maxCompletedIter,
    )
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
      // 不排除会导致同一 genui 双渲染（hasGenUI + ToolGroup→ToolRender 各一个
      // GenUIPanel）→ 高度双倍 + DOM 反复出现/消失 + 虚拟列表高度跳变（busy 时最严重）。
    ]).filter((t) => !t.uiMode)
    const hasToolInProgress = allTools.some((tool) => isToolInProgress(tool.status))
    return { allTools, hasToolInProgress }
  }, [progress.activeTools, progress.streamingTools, progress.completedTools, progress.iterationHistory])
  const allTools = toolDeriv.allTools
  const hasTools = allTools.length > 0
  const hasToolInProgress = toolDeriv.hasToolInProgress
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
    // ⚠️ 第一迭代也必须渲染（切换会话新 turn 空白根治）：M4 架构下 turn_started
    // 立即创建 live 行（EMPTY_LIVE，无内容）→ MessageList 的 liveId 非 null 且
    // 最后一行是 live assistant（非 user）→ busy placeholder 的旧条件
    // （rows 最后是 user）不满足 → 两个指示器都不渲染 → 完全空白（用户报告：
    // "切换会话后新 agent turn 完全是空，不渲染思考中"）。busy placeholder 已
    // 收紧为 liveId===null（互斥），第一迭代窗口由本组件渲染。
    //
    // ⚠️ 即使 phase='tool_exec'（工具马上到达）这里也**照常渲染** ShimmerThinking。
    // 曾试图在此加 phase 守卫来消除"工具到达前一帧的思考中"（CR 建议），但它会让
    // live 行高度先掉到 0、再跳到工具卡片高度 —— `iteration-commit-flicker` E2E
    // 的 spikes() 判据（h[i] >= min(相邻帧) + 12）会判定为 double-render spike 并
    // 失败（CI 实测 frame 10: 178 vs plateau 150）。**行高稳定优先于消除这一帧**，
    // 该 E2E 正是为防此类抖动而存在的。
    if (progress.streaming) {
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
                text={t('agent.thinkingLive', { count: reasoningCount })}
                color="var(--text-muted)"
                className="text-[10px]"
              />
            : t('agent.thoughtChars', { count: reasoningCount })}
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

      {/* Streaming C — each tool its own expanded card */}
      {hasTools && <ToolGroup tools={allTools} />}
    </div>
  )
})
