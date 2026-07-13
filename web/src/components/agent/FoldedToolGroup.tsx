/**
 * FoldedToolGroup — consecutive tool calls merged into a borderless fold (Spec A §3).
 *
 * Tool display is controlled by two orthogonal parameters:
 *   - `level` (CollapseLevel): 'all'/'minimal' → folded rows; 'none' → expanded
 *   - `mergeTools` (boolean): when true AND level is 'all'/'minimal' AND 2+ tools,
 *     consecutive tools merge into one line "▸ C1 · C2 (N 个工具)".
 *     When false, each tool shows its own title row (still folded).
 *     At 'none' level mergeTools is ignored — tools always expand.
 *
 * Tool status indicators use CSS status dot classes:
 *   generating: .tool-status-generating (blue blink)
 *   running:    .tool-status-running (blue pulse)
 *   done:       .tool-status-done (gray static)
 *   error:      .tool-status-error (red static)
 */
import { memo, useState, type ReactNode } from 'react'

import { FoldedLine } from './FoldedLine'
import { ToolRender } from './ToolRender'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'
import { cn } from '@/lib/utils'

interface FoldedToolGroupProps {
  tools: WebToolProgress[]
  level: CollapseLevel
  /** Merge consecutive tools into one row. Ignored at 'none' level. */
  mergeTools?: boolean
}

/** Extract display name from a tool (prefers label over name). */
function toolName(tool: WebToolProgress): string {
  return tool.label || tool.name || 'tool'
}

/** CSS class for a tool's status dot. */
function statusDotClass(status: string): string {
  switch (status) {
    case 'generating':
      return 'tool-status-generating'
    case 'running':
      return 'tool-status-running'
    case 'done':
      return 'tool-status-done'
    case 'error':
      return 'tool-status-error'
    default:
      return 'tool-status-pending'
  }
}

/** Format elapsed milliseconds into a compact human-readable string. */
function formatElapsed(ms: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${ms}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  const rem = Math.round(s % 60)
  return `${m}m${rem}s`
}

/** Format a single tool's title for its FoldedLine. */
function formatToolTitle(
  tool: WebToolProgress,
  t: (key: string, params?: Record<string, string | number>) => string,
): ReactNode {
  const name = toolName(tool)
  const dotClass = statusDotClass(tool.status)
  let suffix = ''
  if (tool.status === 'generating') suffix = t('agent.toolGenerating')
  else if (tool.status === 'running') suffix = t('agent.statusRunning')
  const elapsed = formatElapsed(tool.elapsedMs)
  return (
    <span className="flex items-center gap-1.5">
      <span className={cn('tool-status-dot', dotClass)} aria-hidden />
      <span className="font-mono">{name}</span>
      {suffix && <span className="text-text-muted">{suffix}</span>}
      {elapsed && !suffix && <span className="text-text-muted tabular-nums">{elapsed}</span>}
    </span>
  )
}

/** Build the merged title: "C1 · C2 (N 个工具)" with status dots. */
function formatMergedTitle(
  tools: WebToolProgress[],
  t: (key: string, params?: Record<string, string | number>) => string,
): ReactNode {
  // Use the worst status among all tools for the merged dot
  const worstStatus = tools.some((t) => t.status === 'error')
    ? 'error'
    : tools.some((t) => t.status === 'running' || t.status === 'generating')
      ? 'running'
      : 'done'
  const names = tools.map(toolName)
  const joined = names.join(' · ')
  return (
    <span className="flex items-center gap-1.5">
      <span className={cn('tool-status-dot', statusDotClass(worstStatus))} aria-hidden />
      <span className="font-mono text-text-secondary">{joined}</span>
      <span className="text-text-muted">
        ({t('agent.toolGroup', { count: tools.length })})
      </span>
    </span>
  )
}

export const FoldedToolGroup = memo(function FoldedToolGroup({
  tools,
  level,
  mergeTools = true,
}: FoldedToolGroupProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)

  if (!tools.length) return null

  // 'none' level or single tool or mergeTools disabled: each tool is an independent FoldedLine.
  const shouldMerge = level !== 'none' && mergeTools && tools.length > 1

  if (!shouldMerge) {
    return (
      <div className="flex flex-col">
        {tools.map((tool, i) => (
          <FoldedLine
            key={`${tool.name}-${tool.label}-${i}`}
            title={formatToolTitle(tool, t)}
            defaultOpen={false}
          >
            <ToolRender tool={tool} />
          </FoldedLine>
        ))}
      </div>
    )
  }

  // 'minimal'/'all' level with mergeTools=true and 2+ tools: merged into one foldable line.
  return (
    <div>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        className="flex items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
      >
        <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
        {formatMergedTitle(tools, t)}
      </button>
      {expanded && (
        <div className="ml-4 flex flex-col">
          {tools.map((tool, i) => (
            <FoldedLine
              key={`${tool.name}-${tool.label}-${i}`}
              title={formatToolTitle(tool, t)}
              defaultOpen={false}
            >
              <ToolRender tool={tool} />
            </FoldedLine>
          ))}
        </div>
      )}
    </div>
  )
})
