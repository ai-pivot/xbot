/**
 * FoldedToolGroup — consecutive tool calls merged into a borderless fold (Spec 4 §3.3).
 *
 * 'none' level or single tool: each tool renders as its own FoldedLine.
 * 'minimal'/'all' level with 2+ tools: merged into one line
 *   "▸ C1 · C2 (N 个工具)" — expand to reveal individual FoldedLine per tool.
 *
 * Tool status indicators:
 *   generating: ◒ (spinner)
 *   running:    ◑ (in-progress)
 *   done:       ✓ (completed)
 *   error:      ✗ (failed)
 */
import { memo, useState, type ReactNode } from 'react'

import { FoldedLine } from './FoldedLine'
import { ToolRender } from './ToolRender'
import { useI18n } from '@/providers/i18n'
import type { CollapseLevel } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'

interface FoldedToolGroupProps {
  tools: WebToolProgress[]
  level: CollapseLevel
}

/** Extract display name from a tool (prefers label over name). */
function toolName(tool: WebToolProgress): string {
  return tool.label || tool.name || 'tool'
}

/** Status icon for a single tool. */
function statusIcon(status: string): string {
  switch (status) {
    case 'generating':
      return '◒'
    case 'running':
      return '◑'
    case 'done':
      return '✓'
    case 'error':
      return '✗'
    default:
      return '·'
  }
}

/** Status color CSS variable for a tool status. */
function statusColor(status: string): string {
  switch (status) {
    case 'error':
      return 'var(--status-error)'
    case 'running':
    case 'generating':
      return 'var(--status-running)'
    default:
      return 'var(--text-muted)'
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
  const icon = statusIcon(tool.status)
  const color = statusColor(tool.status)
  let suffix = ''
  if (tool.status === 'generating') suffix = t('agent.toolGenerating')
  else if (tool.status === 'running') suffix = t('agent.statusRunning')
  const elapsed = formatElapsed(tool.elapsedMs)
  return (
    <span className="flex items-center gap-1.5">
      <span style={{ color }}>{icon}</span>
      <span className="font-mono">{name}</span>
      {suffix && <span className="text-text-muted">{suffix}</span>}
      {elapsed && !suffix && <span className="text-text-muted tabular-nums">{elapsed}</span>}
    </span>
  )
}

/** Build the merged title: "C1 · C2 (N 个工具)". */
function formatMergedTitle(
  tools: WebToolProgress[],
  t: (key: string, params?: Record<string, string | number>) => string,
): ReactNode {
  const names = tools.map(toolName)
  const joined = names.join(' · ')
  return (
    <span className="flex items-center gap-1.5">
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
}: FoldedToolGroupProps) {
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)

  if (!tools.length) return null

  // 'none' level or single tool: each tool is an independent FoldedLine.
  if (level === 'none' || tools.length === 1) {
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

  // 'minimal'/'all' level with 2+ tools: merged into one foldable line.
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
