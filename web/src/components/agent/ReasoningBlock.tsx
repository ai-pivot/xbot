/**
 * ReasoningBlock — renders the agent's reasoning/thinking text (Spec 4 §3.3, §3.5).
 *
 * In the new folding model this component is used as the *content* inside a
 * FoldedLine — it renders the Markdown body only. The folding arrow and toggle
 * are handled by the parent FoldedLine. When `streaming` is true, a shimmer
 * indicator is appended to the content.
 */
import { memo } from 'react'

import { MarkdownRenderer } from './MarkdownRenderer'
import { SweepText } from './SweepText'
import { useI18n } from '@/providers/i18n'

interface ReasoningBlockProps {
  content: string
  /** Number of source characters to reveal without reparsing Markdown. */
  visibleChars?: number
  /** True while the reasoning is still being streamed (shows indicator). */
  streaming?: boolean
}

export const ReasoningBlock = memo(function ReasoningBlock({
  content,
  visibleChars,
  streaming = false,
}: ReasoningBlockProps) {
  const { t } = useI18n()
  if (!content) return null

  return (
    <div className="py-1">
      <MarkdownRenderer
        content={content}
        className="text-xs text-text-secondary"
        streaming={visibleChars !== undefined}
        visibleChars={visibleChars}
      />
      {streaming && (
        <SweepText
          text={t('agent.reasoningStreaming')}
          color="var(--text-muted)"
          className="mt-1 text-[11px]"
        />
      )}
    </div>
  )
})
