/**
 * FoldedToolGroup — tool call display with merged groups and status colors.
 *
 * Single tool folded row:  [icon] ToolName param15chars
 *   - Icon color reflects status: gray=normal, light-red=all-failed, light-yellow=partial-fail
 *   - Click expands into a card: [icon] ToolName + input + output
 *
 * Merged row (any consecutive tools):  [icon1]×2 [icon2]×1
 *   - Icons grouped by tool name with count
 *   - Click expands to show each tool as an independent card
 *   - Icon color reflects aggregate status (gray=all-ok, red=all-fail, yellow=partial)
 */
import { memo, useState, type ReactNode } from 'react'

import { ToolRender } from './ToolRender'
import { getToolIcon } from './toolIcons'
import type { CollapseLevel } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'

/** Max param preview length in folded row. */
const MAX_PARAM_LEN = 15

interface FoldedToolGroupProps {
  tools: WebToolProgress[]
  level: CollapseLevel
  /** Merge consecutive tools into one row. Ignored at 'none' level. */
  mergeTools?: boolean
}

/** Extract a short parameter hint from the tool label (text after ": "). */
function toolParam(tool: WebToolProgress): string {
  const label = tool.label || ''
  const idx = label.indexOf(': ')
  return idx >= 0 ? label.slice(idx + 2) : ''
}

/** Truncate to N chars with ellipsis. */
function truncate(text: string, max: number): string {
  if (text.length <= max) return text
  return text.slice(0, max) + '…'
}

/** Determine the tool status for color purposes. */
type ToolStatusColor = 'normal' | 'all-failed' | 'partial-fail' | 'running'

/** Check if a tool's status indicates failure. */
function isFailed(status: string): boolean {
  return status === 'error'
}

/** Check if a tool's status indicates it's still running. */
function isRunning(status: string): boolean {
  return status === 'running' || status === 'generating'
}

/** Aggregate status across multiple tools. */
function aggregateStatus(tools: WebToolProgress[]): ToolStatusColor {
  const anyRunning = tools.some((t) => isRunning(t.status))
  if (anyRunning) return 'running'
  const failedCount = tools.filter((t) => isFailed(t.status)).length
  if (failedCount === 0) return 'normal'
  if (failedCount === tools.length) return 'all-failed'
  return 'partial-fail'
}

/** CSS color for a status color. */
function statusColorVar(status: ToolStatusColor): string {
  switch (status) {
    case 'all-failed':
      return 'var(--destructive)'
    case 'partial-fail':
      return '#e6a700' // light amber/yellow
    case 'running':
      return 'var(--accent)'
    default:
      return 'var(--text-muted)' // gray = normal
  }
}

/** Render a single Lucide tool icon at 16px with status color. */
function ToolIcon({ name, status }: { name: string; status: ToolStatusColor }) {
  const Icon = getToolIcon(name) as React.ComponentType<{ className?: string; style?: React.CSSProperties }>
  return <Icon className="tool-icon-single shrink-0" style={{ color: statusColorVar(status) }} />
}

/** Format a single tool's folded title: [icon] name param15 */
function formatToolTitle(tool: WebToolProgress): ReactNode {
  const name = tool.name || 'tool'
  const label = tool.label || name
  const param = toolParam(tool)
  const displayName = label.includes(': ') ? label.slice(0, label.indexOf(': ')) : name
  const status: ToolStatusColor = isFailed(tool.status) ? 'all-failed' : isRunning(tool.status) ? 'running' : 'normal'

  return (
    <span className="flex items-center gap-1.5 min-w-0">
      <ToolIcon name={name} status={status} />
      <span className="font-mono shrink-0">{displayName}</span>
      {param && (
        <span className="font-mono text-text-muted truncate">{truncate(param, MAX_PARAM_LEN)}</span>
      )}
    </span>
  )
}

/** Build merged title: [icon1]×2 [icon2]×1 */
function formatMergedTitle(tools: WebToolProgress[]): ReactNode {
  const status = aggregateStatus(tools)
  // Group by tool name, preserving first-occurrence order
  const groups: { name: string; count: number }[] = []
  const seen = new Map<string, number>()
  for (const tool of tools) {
    const idx = seen.get(tool.name)
    if (idx !== undefined) {
      groups[idx].count++
    } else {
      seen.set(tool.name, groups.length)
      groups.push({ name: tool.name, count: 1 })
    }
  }

  return (
    <span className="flex items-center gap-1.5">
      {groups.map((g, i) => (
        <span key={`${g.name}-${i}`} className="flex items-center gap-0.5">
          <ToolIcon name={g.name} status={status} />
          {g.count > 1 && (
            <span className="text-[11px] text-text-muted shrink-0">×{g.count}</span>
          )}
        </span>
      ))}
    </span>
  )
}

/** Expanded tool card: [icon] name + input + output */
function ToolCard({ tool }: { tool: WebToolProgress }) {
  const name = tool.name || 'tool'
  const label = tool.label || name
  const displayName = label.includes(': ') ? label.slice(0, label.indexOf(': ')) : name
  const status: ToolStatusColor = isFailed(tool.status) ? 'all-failed' : isRunning(tool.status) ? 'running' : 'normal'

  return (
    <div className="rounded-md border border-border/50 bg-bg-tertiary/30 p-2">
      {/* Card header: icon + name */}
      <div className="mb-1.5 flex items-center gap-1.5">
        <ToolIcon name={name} status={status} />
        <span className="font-mono text-xs font-medium">{displayName}</span>
      </div>
      {/* Tool input + output */}
      <ToolRender tool={tool} />
    </div>
  )
}

export const FoldedToolGroup = memo(function FoldedToolGroup({
  tools,
  level,
  mergeTools = true,
}: FoldedToolGroupProps) {
  const [expanded, setExpanded] = useState(false)

  if (!tools.length) return null

  // 'none' level: always expanded, each tool as independent card
  if (level === 'none') {
    return (
      <div className="flex flex-col gap-1.5">
        {tools.map((tool, i) => (
          <ToolCard key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
        ))}
      </div>
    )
  }

  // Single tool: folded row that expands to a card
  if (tools.length === 1 || !mergeTools) {
    if (tools.length === 1) {
      return (
        <div>
          <button
            type="button"
            onClick={() => setExpanded(!expanded)}
            aria-expanded={expanded}
            className="flex items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
          >
            <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
            {formatToolTitle(tools[0])}
          </button>
          {expanded && (
            <div className="ml-4 mt-1">
              <ToolCard tool={tools[0]} />
            </div>
          )}
        </div>
      )
    }

    // mergeTools disabled: each tool is an independent foldable row
    return (
      <div className="flex flex-col">
        {tools.map((tool, i) => (
          <SingleToolFold key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
        ))}
      </div>
    )
  }

  // Merged: any 2+ consecutive tools → [icon]×N [icon]×M
  return (
    <div>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        className="flex items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
      >
        <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
        {formatMergedTitle(tools)}
      </button>
      {expanded && (
        <div className="ml-4 mt-1 flex flex-col gap-1.5">
          {tools.map((tool, i) => (
            <ToolCard key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
          ))}
        </div>
      )}
    </div>
  )
})

/** Single tool as an independent foldable row with card expansion. */
const SingleToolFold = memo(function SingleToolFold({ tool }: { tool: WebToolProgress }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <div>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        className="flex items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
      >
        <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
        {formatToolTitle(tool)}
      </button>
      {expanded && (
        <div className="ml-4 mt-1">
          <ToolCard tool={tool} />
        </div>
      )}
    </div>
  )
})
