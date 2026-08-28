/**
 * TurnBody — renders all iterations after one User message (Spec 4 §3.3).
 *
 * Flattens iterations into a sequence of content blocks (reasoning, text,
 * tools). Consecutive tool blocks across iterations are merged into a single
 * FoldedToolGroup so that "连续的工具调用都合并" (cross-iteration merge).
 * When a live progress snapshot is present (streaming), appends a
 * LiveIteration at the end for the in-flight iteration.
 */
import { memo } from 'react'

import { ThinkingLine } from './ThinkingLine'
import { IterationGroup } from './IterationHistory'
import { FoldedToolGroup } from './FoldedToolGroup'
import { LiveIteration } from './LiveIteration'
import { continuousIterations } from './progressStore'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { SubAgentProgressTree } from './SubAgentProgressTree'
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
  const contiguous = continuousIterations(iterations)

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

  // mergeTools on: flatten iterations into content blocks, merging consecutive tools.
  const blocks = flattenIterations(contiguous)

  return (
    <div className="flex flex-col gap-1" data-iter-range={contiguous.length > 0 ? `${contiguous[0].iteration}-${contiguous[contiguous.length - 1].iteration}` : undefined} data-iter-total={contiguous.length}>
      <div className="flex flex-col gap-1">
      {blocks.map((block, i) => {
        if (block.kind === 'reasoning') {
          return (
            <div key={`r-${i}`} data-iter-id={block.iteration} data-turn-id={turnID}>
              <ThinkingLine label={'思考 ' + (block.elapsedMs && block.elapsedMs > 0 ? (block.elapsedMs / 1000).toFixed(1) + 's' : Math.ceil(block.text.length / 4) + ' 字')}>
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
