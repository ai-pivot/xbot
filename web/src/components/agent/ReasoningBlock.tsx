/**
 * ReasoningBlock — renders the agent's reasoning/thinking text.
 *
 * Used as the content inside a FoldedLine. Renders only the Markdown body.
 * The "thinking…" placeholder is handled by ShimmerThinking at the
 * AssistantMessage level (shown when NO content/tools exist yet), NOT here.
 */
import { memo } from 'react'

import { MarkdownRenderer } from './MarkdownRenderer'

interface ReasoningBlockProps {
  content: string
  /** Number of source characters to reveal without reparsing Markdown. */
  visibleChars?: number
}

export const ReasoningBlock = memo(function ReasoningBlock({
  content,
  visibleChars,
}: ReasoningBlockProps) {
  if (!content) return null

  return (
    <div className="py-1">
      <MarkdownRenderer
        content={content}
        className="text-xs text-text-secondary"
        streaming={visibleChars !== undefined}
        visibleChars={visibleChars}
      />
    </div>
  )
})
