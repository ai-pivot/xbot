/**
 * ToolCallBlock — renders the body of one tool call: args + output + summary
 * (Spec 4 §3.3, §3.5).
 *
 * In the new folding model this component is used as the *content* inside a
 * FoldedLine — it does NOT manage its own collapse state. The folding arrow
 * and toggle are handled by the parent FoldedLine / FoldedToolGroup.
 *
 * This is the DEFAULT renderer (tools without a dedicated view in ToolRender):
 * args are pretty-printed as syntax-highlighted JSON; detail/output renders in
 * a bordered code panel.
 *
 * Accepts both the new WebToolProgress type and the legacy IterationTool /
 * ToolProgress shapes (structurally compatible).
 */
import { memo, useEffect, useMemo } from 'react'

import { useI18n } from '@/providers/i18n'
import { ensureHljsLoaded, highlightSync, useHljsReady } from './highlight'
import { AnsiText } from './AnsiText'
import type { IterationTool, ToolProgress } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'

/** Union of all tool-like shapes this component accepts. */
type ToolLike = WebToolProgress | IterationTool | ToolProgress

interface ToolCallBlockProps {
  tool: ToolLike
  /** 隐藏 args 块（统一参数渲染：宿主浮窗已自行渲染 ArgsView 时使用）。 */
  hideArgs?: boolean
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

/** Pretty-print the args JSON (falls back to the raw string when unparsable). */
function prettyArgs(args: string): string {
  try {
    return JSON.stringify(JSON.parse(args), null, 2)
  } catch {
    return args
  }
}

/** Highlighted args JSON — CodeBlock pattern (sync highlight + lazy load). */
export function ArgsView({ args }: { args: string }) {
  const hljsReady = useHljsReady()
  const pretty = useMemo(() => prettyArgs(args), [args])
  const html = useMemo(() => highlightSync(pretty, 'json'), [pretty, hljsReady])
  useEffect(() => {
    ensureHljsLoaded()
  }, [])
  return (
    <pre
      className="overflow-x-auto whitespace-pre rounded-md px-2.5 py-1.5 font-mono text-[12px] leading-5"
      style={{
        backgroundColor: 'var(--app-bg)',
        border: '1px solid var(--border)',
        color: 'var(--text-primary)',
      }}
      {...(html != null ? { dangerouslySetInnerHTML: { __html: html } } : { children: pretty })}
    />
  )
}

export const ToolCallBlock = memo(function ToolCallBlock({
  tool,
  hideArgs = false,
}: ToolCallBlockProps) {
  const { t } = useI18n()
  const args = argsOf(tool)
  const detail = detailOf(tool)
  const summary = summaryOf(tool)

  return (
    <div className="flex flex-col gap-2 py-1 text-xs">
      {args && !hideArgs && (
        <div>
          <div className="mb-1 text-text-muted">{t('agent.args')}</div>
          <ArgsView args={args} />
        </div>
      )}
      {detail && (
        <div>
          <div className="mb-1 text-text-muted">{t('agent.output')}</div>
          <pre
            className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md px-2.5 py-1.5 font-mono text-[12px] leading-5 text-text-secondary"
            style={{ backgroundColor: 'var(--app-bg)', border: '1px solid var(--border)' }}
          >
            <AnsiText text={detail} />
          </pre>
        </div>
      )}
      {!args && !detail && summary && (
        <pre
          className="max-h-60 overflow-auto whitespace-pre-wrap rounded-md px-2.5 py-1.5 text-text-secondary"
          style={{ backgroundColor: 'var(--sidebar-bg)' }}
        >
          <AnsiText text={summary} />
        </pre>
      )}
      {!args && !detail && !summary && (
        <div className="text-text-muted">{t('agent.none')}</div>
      )}
    </div>
  )
})
