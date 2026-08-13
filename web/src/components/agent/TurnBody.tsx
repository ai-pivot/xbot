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

import { IterationGroup } from './IterationHistory'
import { FoldedLine } from './FoldedLine'
import { FoldedToolGroup } from './FoldedToolGroup'
import { LiveIteration } from './LiveIteration'
import { continuousIterations } from './progressStore'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/agent'
import type { ProgressSnapshot, WebIteration, WebToolProgress } from '@/types/shared'

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
  | { kind: 'reasoning'; text: string; iteration: number }
  | { kind: 'text'; content: string; iteration: number }
  | { kind: 'tools'; tools: WebToolProgress[]; iterations: number[] }

/** Flatten iterations into content blocks, merging consecutive tool blocks. */
function flattenIterations(iterations: WebIteration[]): ContentBlock[] {
  const blocks: ContentBlock[] = []
  for (const iter of iterations) {
    const iterNum = iter.iteration
    if (iter.reasoning) {
      blocks.push({ kind: 'reasoning', text: iter.reasoning, iteration: iterNum })
    }
    if (iter.content) {
      blocks.push({ kind: 'text', content: iter.content, iteration: iterNum })
    }
    if (iter.tools.length > 0) {
      // Merge with previous block if it's also tools
      const last = blocks[blocks.length - 1]
      if (last && last.kind === 'tools') {
        last.tools.push(...iter.tools)
        last.iterations.push(iterNum)
      } else {
        blocks.push({ kind: 'tools', tools: [...iter.tools], iterations: [iterNum] })
      }
    }
  }
  return blocks
}

export const TurnBody = memo(function TurnBody({
  iterations,
  liveProgress,
  level,
  mergeTools = true,
  turnID,
}: TurnBodyProps) {
  const { t } = useI18n()

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
      {blocks.map((block, i) => {
        if (block.kind === 'reasoning') {
          return (
            <div key={`r-${i}`} data-iter-id={block.iteration} data-turn-id={turnID}>
              <FoldedLine
                title={t('agent.thinkingChars', { count: block.text.length })}
                defaultOpen={false}
              >
                <ReasoningBlock content={block.text} />
              </FoldedLine>
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
      {liveProgress && (
        <div data-iter-id="live" data-iter-num={liveProgress.iteration || undefined} data-turn-id={liveProgress.turnID || turnID}>
          <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />
        </div>
      )}
    </div>
  )
})
