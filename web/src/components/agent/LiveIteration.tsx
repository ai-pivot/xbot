/**
 * LiveIteration — renders the in-flight iteration from a ProgressSnapshot.
 *
 * Streaming T (reasoning): ThinkingLine wrapping ReasoningBlock with streaming
 *   indicator. Falls back to lastReasoning when streamContent is empty.
 * Streaming O (text): MarkdownRenderer with a streaming cursor indicator.
 * Streaming C (tools): FoldedToolGroup with merged streaming/active/completed
 *   tools from the snapshot.
 *
 * Render order: T → O → C (Spec A §2).
 */
import { memo, useEffect, useMemo } from 'react'

import { ThinkingLine } from './ThinkingLine'
import { reasoningKey } from './reasoningOpenState'
import { FoldedToolGroup } from './FoldedToolGroup'
import { GenUICollapsiblePanel } from './GenUIPanel'

import { MarkdownRenderer } from './MarkdownRenderer'
import { CopyTarget } from './MessageActions'
import { ReasoningBlock } from './ReasoningBlock'
import { ShimmerThinking } from './ShimmerThinking'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { SweepText } from './SweepText'
import { isToolInProgress } from './statusVisual'
import { useTypewriter } from '@/hooks/useTypewriter'
import { useI18n } from '@/providers/i18n'
import { dedupTools, liveIterationInFlight } from './progressStore'
import { IterationSlot, setGlobalLiveStats } from '@/plugin-runtime/iteration-render'
import type { ProgressSnapshot } from '@/types/shared'
import type { LiveStreamStats } from '@/plugin-api'

interface LiveIterationProps {
  progress: ProgressSnapshot
}

export const LiveIteration = memo(function LiveIteration({
  progress,
}: LiveIterationProps) {
  const { t } = useI18n()
  // Reasoning: prefer streaming value, fall back to structured (mirrors TUI)
  const reasoningContent = progress.reasoningStreamContent || progress.lastReasoning || ''
  // hasReasoning 在 lastIter 之后计算：需要与"最后一个已完成迭代"的 reasoning
  // 对比后才能判定（见下方 effectiveReasoning）。
  // Text output: prefer streaming (real-time), fall back to structured content
  // (snapshot from server — may arrive without preceding stream_content events)
  // ── effectiveStreamContent: suppress streamContent that equals the last
  // completed iteration's thinking/content. After a session switch, the
  // hydrated snapshot carries the same final text in BOTH streamContent and
  // the last iteration's thinking/content — TurnBody renders the iteration
  // text, and LiveIteration would render streamContent again (duplicate).
  // Drop streamContent when it matches the last iteration's thinking.
  // ⛔ 抑制「已作为历史渲染过的内容」的 live 副本（2026-10-02 P0 用户截图实证：
  // 「一个 iter 重复渲染两次，第一次渲染没 tool」——同一段文本在历史块与 live 块
  // 各渲染一份，live 副本没有 pill）。
  // 旧判据只对比**最后一个**已完成迭代（lastIter）——迟到 / 重放 / 乱序的流式帧
  //（stream 事件是无状态合帧、无 seq gate）会让 live 持有**更早的**已提交迭代
  //（不是最后一个）的内容 ⇒ 比较失败 ⇒ 重复渲染（截图：同一段 bench24 文本两次）。
  // 判据扩展为「与**任意**已完成迭代的对应内容相同 ⇒ 抑制」——**内容匹配**而非
  // 迭代号比较（号可能滞后于内容：turnBodyLiveDedup 用例 2 钉死了「号同、内容真新」
  // 必须渲染的反例）。状态机零改动 ⇒ 线性一致性不受影响。
  // PERF：Set 按 iterationHistory **引用** useMemo（结构化共享：stream 帧不改该引用）
  // ⇒ 每帧 O(1) 查找，代价与 turn 长度无关（流式帧性能铁律）。
  const historyTextIndex = useMemo(() => {
    const contents = new Set<string>()
    const reasonings = new Set<string>()
    for (const it of progress.iterationHistory) {
      if (it.content) contents.add(it.content)
      if (it.reasoning) reasonings.add(it.reasoning)
    }
    return { contents, reasonings }
  }, [progress.iterationHistory])
  // effectiveReasoning —— live 的 reasoning 与**任意**已完成迭代相同 ⇒ 抑制
  //（该内容已在历史块渲染；AskUser 暂停等场景下 live 号仍停在旧迭代、持有同一段
  //  reasoning —— 现场实证：`data-iter-id="24"` 与 live 各一个 Thought 384 chars）。
  const effectiveReasoning =
    reasoningContent && historyTextIndex.reasonings.has(reasoningContent) ? '' : reasoningContent
  const hasReasoning = Boolean(effectiveReasoning)
  const rawTextContent = progress.streamContent || progress.content || ''
  // effectiveStreamContent —— 同源抑制：live 的 streamContent 与**任意**已完成迭代
  // 的 content 相同 ⇒ 抑制（content 字段即文本输出；thinking 已彻底删除，无字符串比较）。
  const textContent = rawTextContent && historyTextIndex.contents.has(rawTextContent)
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
  const displayReasoning = effectiveReasoning

  // 折叠标题显示的思考字符数：reasoning 流式时用 typewriter 追赶值
  // rw.visibleChars（gap/3 per 50ms 平滑增长，与 content typer 同源），避免
  // reasoningContent.length 随 SSE chunk 直接跳变导致「一卡一卡」。reasoning
  // 完成后静止，显示完整长度。
  const reasoningCount = reasoningStreaming ? rw.visibleChars : effectiveReasoning.length

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
      // streaming（未提交）工具标记：pill 据此渲染 sweep 标签
      ...currentStreaming.map((t) => ({ ...t, streaming: true })),
      ...currentActive.map((t) => ({ ...t, streaming: true })),
      ...filteredCompleted,
      // 排除 genui 工具（uiMode）—— 它们由 hasGenUI 的 <GenUIPanel> 唯一渲染。
      // 不排除会导致同一 genui 双渲染（hasGenUI + FoldedToolGroup→ToolRender 各一个
      // GenUIPanel）→ 高度双倍 + DOM 反复出现/消失 + 虚拟列表高度跳变（busy 时最严重）。
    ]).filter((t) => !t.uiMode)
    const hasToolInProgress = allTools.some((tool) => isToolInProgress(tool.status))
    return { allTools, hasToolInProgress }
  }, [progress.activeTools, progress.streamingTools, progress.completedTools, progress.iterationHistory])
  const allTools = toolDeriv.allTools
  const hasTools = allTools.length > 0
  const hasToolInProgress = toolDeriv.hasToolInProgress
  const reasoningInProgress = progress.streaming && progress.phase === 'thinking' && !hasStreamContent && !hasToolInProgress

  // ⛔ 进行中迭代号已作为历史渲染过 ⇒ LiveIteration **整块**不渲染
  //（2026-10-02 P0 用户截图实证：「一个 iter 重复渲染两次，第一次渲染没 tool」）。
  // ⚠️ 不能用「迭代号 ≤ 历史最大号」判据 —— turnBodyLiveDedup 用例 2 钉死了反例：
  // live 的 iteration 号可能**滞后于内容**（新迭代内容已流式到达、号未推进）⇒ 号比较
  // 会误杀「号同、内容真新」的合法渲染。改由下方**内容匹配判据**承担（对比**全部**
  // 已提交迭代而非只对比最后一个）。
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
    // 压缩期间【不】渲染思考占位符：phase='compressing' 时状态指示器归压缩
    // 指示器（AssistantMessage / MessageList 的 agent.compressing）—— 不变量
    // 「每个状态下有且只有一个状态指示器」（用户报告截图：`thinking…` 与
    // `Compressing context…` 同时渲染，看起来像 bug）。
    // ⛔ 「在飞迭代已被渲染成历史块」时不得再显示占位符（用户 2026-09-20 报告：
    // 「思考中和思考 stream 明显不可能同时存在才对」——截图里已完成的
    // 「思考 15175 字」下方又冒出「思考中…」）。判据与 MessageList 的 busy 占位符
    // **共用** `liveIterationInFlight`（两者互斥 ⇒ 同一状态下恰好一个指示器）。
    if (progress.streaming && progress.phase !== 'compressing' && liveIterationInFlight(progress)) {
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
          stateKey={reasoningKey(progress.turnID, progress.iteration)}
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
        <CopyTarget kind="iteration"
          iteration={{ iteration: currentIter, content: displayText, reasoning: '', tools: [], toolCount: 0 }}
          annotationSource={progress.turnID > 0 ? { turnID: progress.turnID, iteration: currentIter } : undefined}>
          <div
            data-annotation-body=""
            data-annotation-live={isLive ? '' : undefined}
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
        </CopyTarget>
      )}

      {hasSubAgents && <SubAgentProgressTree nodes={liveSubAgents} />}

      {/* Streaming GenUI — 在工具调用位置实时渲染面板（streaming 状态） */}
      {hasGenUI && (
        <GenUICollapsiblePanel code={progress.genuiContent} streaming={isLive} />
      )}

      {/* Streaming C */}
      {hasTools && <FoldedToolGroup tools={allTools} />}
    </div>
  )
})
