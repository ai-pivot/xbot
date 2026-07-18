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
}

/** A flattened content block extracted from iterations. */
type ContentBlock =
  | { kind: 'reasoning'; text: string }
  | { kind: 'text'; content: string }
  | { kind: 'tools'; tools: WebToolProgress[] }

/** Flatten iterations into content blocks, merging consecutive tool blocks. */
function flattenIterations(iterations: WebIteration[]): ContentBlock[] {
  const blocks: ContentBlock[] = []
  for (const iter of iterations) {
    if (iter.reasoning) {
      blocks.push({ kind: 'reasoning', text: iter.reasoning })
    }
    if (iter.thinking) {
      blocks.push({ kind: 'text', content: iter.thinking })
    }
    if (iter.tools.length > 0) {
      // Merge with previous block if it's also tools
      const last = blocks[blocks.length - 1]
      if (last && last.kind === 'tools') {
        last.tools.push(...iter.tools)
      } else {
        blocks.push({ kind: 'tools', tools: [...iter.tools] })
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
}: TurnBodyProps) {
  const { t } = useI18n()

  // Fast path: if mergeTools is off, use the original per-iteration rendering.
  if (!mergeTools) {
    return (
      <div className="flex flex-col gap-1">
        {iterations.map((iter, i) => (
          <IterationGroup
            key={iter.iteration ?? i}
            iteration={iter}
            level={level}
            mergeTools={mergeTools}
          />
        ))}
        {liveProgress && <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />}
      </div>
    )
  }

  // mergeTools on: flatten iterations into content blocks, merging consecutive tools.
  const blocks = flattenIterations(iterations)

  return (
    <div className="flex flex-col gap-1">
      {blocks.map((block, i) => {
        if (block.kind === 'reasoning') {
          return (
            <FoldedLine
              key={`r-${i}`}
              title={t('agent.thinkingChars', { count: block.text.length })}
              defaultOpen={false}
            >
              <ReasoningBlock content={block.text} />
            </FoldedLine>
          )
        }
        if (block.kind === 'text') {
          return (
            <MarkdownRenderer
              key={`t-${i}`}
              content={block.content}
              className="text-sm text-text-primary"
            />
          )
        }
        // tools block
        return (
          <FoldedToolGroup
            key={`c-${i}`}
            tools={block.tools}
            level={level}
            mergeTools={mergeTools}
          />
        )
      })}
      {liveProgress && <LiveIteration progress={liveProgress} level={level} mergeTools={mergeTools} />}
    </div>
  )
})
