/**
 * IterationGroup — renders a single iteration: T → O → C order.
 *
 * Each iteration renders:
 *   - T (reasoning): FoldedLine, folded by default (click to expand)
 *   - O (text output): MarkdownRenderer, always shown
 *   - C (tools): every tool as its own expanded card (ToolGroup)
 *
 * An iteration NEVER merges its tools with another iteration's, and is never
 * collapsed into a summary row.
 */
import { memo } from 'react'

import { FoldedLine } from './FoldedLine'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { ToolGroup } from './ToolGroup'
import { useI18n } from '@/providers/i18n'
import { IterationSlot } from '@/plugin-runtime/iteration-render'
import type { WebIteration } from '@/types/shared'

interface IterationGroupProps {
  iteration: WebIteration
}

export const IterationGroup = memo(function IterationGroup({ iteration }: IterationGroupProps) {
  const { t } = useI18n()

  return (
    <div className="flex flex-col gap-1">
      {/* 迭代指标（插件注入点）：把该迭代的 token/TTFT/tool 耗时传给插件。 */}
      <IterationSlot
        data={{
          stats: {
            iteration: iteration.iteration,
            tokens: iteration.tokens,
            ttftMs: iteration.ttftMs,
            tokensPerSec: iteration.tokensPerSec,
            toolMs: iteration.toolMs,
          },
        }}
      />

      {/* T: reasoning (folded by default) — show character count, not T0/T1 */}
      {iteration.reasoning && (
        <FoldedLine
          title={t('agent.thinkingChars', { count: iteration.reasoning.length })}
          defaultOpen={false}
        >
          <ReasoningBlock content={iteration.reasoning} />
        </FoldedLine>
      )}

      {/* O: text output (always shown) */}
      {iteration.content && (
        <MarkdownRenderer
          content={iteration.content}
          className="text-sm text-text-primary"
        />
      )}

      {/* C: tool calls — each tool its own expanded card */}
      {iteration.tools.length > 0 && <ToolGroup tools={iteration.tools} />}

      {/* Fallback: if nothing in this iteration, show a subtle hint */}
      {!iteration.reasoning && iteration.tools.length === 0 && !iteration.content && (
        <span className="text-xs text-text-muted">{t('agent.none')}</span>
      )}
    </div>
  )
})
