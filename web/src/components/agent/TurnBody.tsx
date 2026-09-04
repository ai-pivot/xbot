/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * Flattens iterations into a sequence of content blocks (reasoning, text,
 * tools). Consecutive tool blocks across iterations are merged into a single
 * FoldedToolGroup so that "连续的工具调用都合并" (cross-iteration merge).
 * When a live progress snapshot is present (streaming), appends a
 * LiveIteration at the end for the in-flight iteration.
 */
import { memo, useMemo } from 'react'

import { ThinkingLine } from './ThinkingLine'
import { IterationGroup } from './IterationHistory'
import { FoldedToolGroup } from './FoldedToolGroup'
import { LiveIteration } from './LiveIteration'
import { continuousIterations } from './progressStore'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { SubAgentProgressTree } from './SubAgentProgressTree'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot, WebIteration, WebSubAgentProgress, WebToolProgress } from '@/types/shared'

interface TurnBodyProps {
  iterations: WebIteration[]
  /** Live progress for an in-flight turn; null for committed history. */
  liveProgress?: ProgressSnapshot | null
  level: CollapseLevel
  mergeTools?: boolean
  /** TurnID for data-attribute debugging (data-turn-id on each block). */
  turnID?: number
}

/** A flattened content block extracted from iterations. */
type ContentBlock =
  | { kind: 'reasoning'; text: string; iteration: number; elapsedMs?: number }
  | { kind: 'text'; content: string; iteration: number }
  | { kind: 'tools'; tools: WebToolProgress[]; iterations: number[] }
  | { kind: 'subagents'; nodes: WebSubAgentProgress[]; iteration: number }

/** Flatten iterations into content blocks, merging consecutive tool blocks. */
function flattenIterations(iterations: WebIteration[]): ContentBlock[] {
  const blocks: ContentBlock[] = []
  for (const iter of iterations) {
    const iterNum = iter.iteration
    if (iter.reasoning) {
      blocks.push({ kind: 'reasoning', text: iter.reasoning, iteration: iterNum, elapsedMs: iter.elapsedMs })
    }
    if (iter.content) {
      blocks.push({ kind: 'text', content: iter.content, iteration: iterNum })
    }
    if (iter.tools.length > 0) {
      // genui 工具（uiMode）保留在 tools 块 —— 位置在其被调用的 iteration 内
      // （由 ToolRender 渲染成 GenUIPanel 顶层面板），不再抽到消息底部。
      const tools = iter.tools
      if (tools.length > 0) {
        const last = blocks[blocks.length - 1]
        if (last && last.kind === 'tools') {
          last.tools.push(...tools)
          last.iterations.push(iterNum)
        } else {
          blocks.push({ kind: 'tools', tools: [...tools], iterations: [iterNum] })
        }
      }
    }
    // SubAgent tree frozen at this iteration's boundary — background subagent
    // progress renders under its ORIGINAL iteration, never the newest one.
    if (iter.subAgents && iter.subAgents.length > 0) {
      blocks.push({ kind: 'subagents', nodes: iter.subAgents, iteration: iterNum })
    }
  }
  return blocks
}

/** 卡片顶部状态条（设计稿 v2 B2）：live turn 进行中=accent 渐变，committed 完成=ok 低透明度。 */

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  level,
  mergeTools = true,
  turnID,
}: TurnBodyProps) {
  // Linear-consistency guard: only render the CONTIGUOUS prefix of iterations.
  // On a weak network a middle iteration's delta may be dropped before
  // restoreActiveProgress backfills it — rendering iteration 3 while 2 is
  // missing would show a non-contiguous sequence (1, 3). Rendering the
  // contiguous prefix keeps the visible history linear.
  //
  // PERF: both derivations memoized on `iterations` — TurnBody re-renders every
  // streaming frame (its liveProgress prop changes identity per frame, memo
  // can't block it), but committed iterations only change when history grows.
  // useMemo lets those frames skip the O(N) contiguous-prefix scan and the
  // O(N×blocks) flatten. Pure computation, same inputs → same output.
  const contiguous = useMemo(() => continuousIterations(iterations), [iterations])
  const blocks = useMemo(() => flattenIterations(contiguous), [contiguous])
  const { t } = useI18n()

  // Fast path: if mergeTools is off, use the original per-iteration rendering.
  if (!mergeTools) {
    return (
      <div className="flex flex-col gap-1" data-iter-range={contiguous.length > 0 ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}` : undefined} data-iter-total={contiguous.length}>
        {contiguous.map((iter, i) => (
          <div key={iter.iteration ?? i} data-iter-id={iter.iteration} data-turn-id={turnID}>
            <IterationGroup
              iteration={iter}
              level={level}
              mergeTools={mergeTools}
            />
            {iter.subAgents && iter.subAgents.length > 0 && (
              <SubAgentProgressTree nodes={iter.subAgents} />
            )}
          </div>
        ))}
        {liveProgress && (
          <div data-iter-id="live" data-iter-num={liveProgress.iteration || undefined} data-turn-id={liveProgress.turnID || turnID}>
            <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-1" data-iter-range={contiguous.length > 0 ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}` : undefined} data-iter-total={contiguous.length}>
      <div className="flex flex-col gap-1">
      {blocks.map((block, i) => {
        if (block.kind === 'reasoning') {
          return (
            <div key={`r-${i}`} data-iter-id={block.iteration} data-turn-id={turnID}>
              {/* REPRO（committed 后"思考 N 字"数字不对）：旧代码 Math.ceil(block.text.length / 4)
                  是【估算】（670 字符显示"思考 167 字"）——违反"永远显示真实字符数"。
                  与 IterationHistory 路径（t('agent.thinkingChars', { count: iteration.reasoning.length })）
                  语义统一：真实 block.text.length + i18n。elapsedMs 时长分支删除
                  （用户要求：永远显示正确的 char 数）。 */}
              <ThinkingLine label={t('agent.thinkingChars', { count: block.text.length })}>
                <ReasoningBlock content={block.text} />
              </ThinkingLine>
            </div>
          )
        }
        if (block.kind === 'text') {
          return (
            <div key={`t-${i}`} data-iter-id={block.iteration} data-turn-id={turnID}>
              <MarkdownRenderer
                content={block.content}
                className="text-sm text-text-primary"
              />
            </div>
          )
        }
        // SubAgent tree frozen at the iteration boundary — renders under the
        // ORIGINAL iteration that spawned it (background subagent attribution).
        if (block.kind === 'subagents') {
          return (
            <div key={`sa-${i}`} data-iter-id={block.iteration} data-turn-id={turnID}>
              <SubAgentProgressTree nodes={block.nodes} />
            </div>
          )
        }
        // tools block — may span multiple iterations (merged). Show all iter IDs.
        const iterIds = block.iterations.join(',')
        return (
          <div key={`c-${i}`} data-iter-id={iterIds} data-turn-id={turnID}>
            <FoldedToolGroup
              tools={block.tools}
              level={level}
              mergeTools={mergeTools}
            />
          </div>
        )
      })}
      </div>
      {liveProgress && (
        <div data-iter-id="live" data-iter-num={liveProgress.iteration || undefined} data-turn-id={liveProgress.turnID || turnID}>
          <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />
        </div>
      )}
    </div>
  )
})
