/**
 * ToolCallBlock — renders the body of one tool call: args + output + summary
 * (Spec 4 §3.3, §3.5).
 *
 * In the new folding model this component is used as the *content* inside a
 * FoldedLine — it does NOT manage its own collapse state. The folding arrow
 * and toggle are handled by the parent FoldedLine / FoldedToolGroup.
 *
 * Accepts both the new WebToolProgress type and the legacy IterationTool /
 * ToolProgress shapes (structurally compatible).
 */
import { memo } from 'react'

import { useI18n } from '@/providers/i18n'
import type { IterationTool, ToolProgress } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'

/** Union of all tool-like shapes this component accepts. */
type ToolLike = WebToolProgress | IterationTool | ToolProgress

interface ToolCallBlockProps {
  tool: ToolLike
}

function summaryOf(t: ToolLike): string | undefined {
  if ('summary' in t && t.summary) return t.summary as string
  return undefined
}

function argsOf(t: ToolLike): string | undefined {
  if ('args' in t && t.args) return t.args as string
  return undefined
}

function detailOf(t: ToolLike): string | undefined {
  if ('detail' in t && t.detail) return t.detail as string
  return undefined
}

export const ToolCallBlock = memo(function ToolCallBlock({
  tool,
}: ToolCallBlockProps) {
  const { t } = useI18n()
  const args = argsOf(tool)
  const detail = detailOf(tool)
  const summary = summaryOf(tool)

  return (
    <div className="flex flex-col gap-2 py-1 text-xs">
      {args && (
        <div>
          <div className="mb-1 text-text-muted">{t('agent.args')}</div>
          <pre className="overflow-x-auto rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-primary">
            {args}
          </pre>
        </div>
      )}
      {detail && (
        <div>
          <div className="mb-1 text-text-muted">{t('agent.output')}</div>
          <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 font-mono text-[12px] text-text-secondary">
            {detail}
          </pre>
        </div>
      )}
      {!args && !detail && summary && (
        <pre className="whitespace-pre-wrap rounded bg-bg-tertiary/60 p-2 text-text-secondary">
          {summary}
        </pre>
      )}
      {!args && !detail && !summary && (
        <div className="text-text-muted">{t('agent.none')}</div>
      )}
    </div>
  )
})
