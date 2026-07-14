/**
 * IterationGroup — renders a single iteration: T → O → C order (Spec A §2).
 *
 * Each iteration renders:
 *   - T (reasoning): FoldedLine, always folded by default
 *   - O (text output): MarkdownRenderer, always shown
 *   - C (tools): FoldedToolGroup (handles both single and merged tool display)
 *
 * The component is used by TurnBody for committed iterations, and by
 * AssistantMessage for the "all" level summary expansion.
 */
import { memo } from 'react'

import { FoldedToolGroup } from './FoldedToolGroup'
import { FoldedLine } from './FoldedLine'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/agent'
import type { WebIteration } from '@/types/shared'

interface IterationGroupProps {
  iteration: WebIteration
  level: CollapseLevel
  mergeTools?: boolean
}

export const IterationGroup = memo(function IterationGroup({
  iteration,
  level,
  mergeTools = true,
}: IterationGroupProps) {
  const { t } = useI18n()

  return (
    <div className="flex flex-col gap-1">
      {/* T: reasoning (always folded by default) — show character count, not T0/T1 */}
      {iteration.reasoning && (
        <FoldedLine
          title={t('agent.thinkingChars', { count: iteration.reasoning.length })}
          defaultOpen={false}
        >
          <ReasoningBlock content={iteration.reasoning} />
        </FoldedLine>
      )}

      {/* O: text output (always shown) */}
      {iteration.thinking && (
        <MarkdownRenderer
          content={iteration.thinking}
          className="text-sm text-text-primary"
        />
      )}

      {/* C: tool calls (FoldedToolGroup handles both single and merged display) */}
      {iteration.tools.length > 0 && (
        <FoldedToolGroup tools={iteration.tools} level={level} mergeTools={mergeTools} />
      )}

      {/* Fallback: if nothing in this iteration, show a subtle hint */}
      {!iteration.reasoning && iteration.tools.length === 0 && !iteration.thinking && (
        <span className="text-xs text-text-muted">{t('agent.none')}</span>
      )}
    </div>
  )
})
