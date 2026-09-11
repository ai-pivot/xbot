/**
 * ToolGroup — renders the tool calls of ONE iteration.
 *
 * Every tool renders as an individually expanded card (icon + name + elapsed
 * + full args/output render). There is deliberately:
 *   - NO folding into a summary row ("Processed N iterations · M tools"),
 *   - NO merging of consecutive tools across iterations,
 *   - NO collapse level.
 *
 * Each iteration renders its own tools, in order, fully visible.
 */
import { memo, type ComponentType, type CSSProperties } from 'react'

import { SweepText } from './SweepText'
import { ToolRender } from './ToolRender'
import { getToolIcon } from './toolIcons'
import { isToolInProgress } from './statusVisual'

import type { WebToolProgress } from '@/types/shared'

/** Determine the tool status for color purposes. */
type ToolStatusColor = 'normal' | 'all-failed' | 'running'

/** Check if a tool's status indicates failure. */
function isFailed(status: string): boolean {
  return status === 'error'
}

/** CSS color for a status color. */
function statusColorVar(status: ToolStatusColor): string {
  switch (status) {
    case 'all-failed':
      return 'var(--destructive)'
    case 'running':
      return 'var(--accent)'
    default:
      return 'var(--text-muted)' // gray = normal
  }
}

/** Max param preview length in the tool card header. */
const MAX_PARAM_LEN = 25

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

/** Get display name from tool label.
 *  For generating tools, always use tool.name — the label is still streaming
 *  (e.g. "思考中…" placeholder) and parsing it would cause name flicker. */
function displayName(tool: WebToolProgress): string {
  const name = tool.name || 'tool'
  if (tool.status === 'generating') return name
  const label = tool.label || name
  return label.includes(': ') ? label.slice(0, label.indexOf(': ')) : name
}

/** SubAgent progress is rendered by SubAgentProgressTree as its own card. */
function isSubAgentToolName(name: string): boolean {
  const normalized = name.trim().toLowerCase().replaceAll('_', '')
  return normalized === 'subagent'
}

/** Get single tool status color. */
function singleStatus(tool: WebToolProgress): ToolStatusColor {
  return isFailed(tool.status) ? 'all-failed' : isToolInProgress(tool.status) ? 'running' : 'normal'
}

/** 耗时格式化（卡片右上角）。 */
function formatElapsed(ms: number): string {
  return ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : `${Math.round(ms)}ms`
}

/** Render a single Lucide tool icon at 16px with status color. */
function ToolIcon({ name, status }: { name: string; status: ToolStatusColor }) {
  const Icon = getToolIcon(name) as ComponentType<{ className?: string; style?: CSSProperties }>
  return <Icon className="tool-icon-single shrink-0" style={{ color: statusColorVar(status) }} />
}

/** A single tool card: [icon] name [elapsed] + full render (args + output). */
export const ToolCard = memo(function ToolCard({ tool }: { tool: WebToolProgress }) {
  // GenUI tool: no card chrome, just render the GenUI panel directly.
  if (tool.uiMode) {
    return <ToolRender tool={tool} />
  }

  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const dn = displayName(tool)
  const param = toolParam(tool)
  // Header shows the same information the old tool pill did: name + short param.
  const headerLabel = param ? `${dn} ${truncate(param, MAX_PARAM_LEN)}` : dn
  const showSweep = status === 'running' && !isSubAgentToolName(tool.name)

  return (
    <div data-testid="tool-card" className="rounded-md border border-border/50 bg-bg-tertiary/30 p-2">
      {/* Card header: icon + name (+ param) + elapsed */}
      <div className="mb-1.5 flex items-center gap-1.5" style={{ color }}>
        <ToolIcon name={tool.name || 'tool'} status={status} />
        {showSweep
          ? <SweepText text={headerLabel} color={color} className="font-mono text-xs font-medium" />
          : <span className="font-mono text-xs font-medium">{headerLabel}</span>}
        {tool.elapsedMs > 0 && (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-text-muted">
            {formatElapsed(tool.elapsedMs)}
          </span>
        )}
      </div>
      {/* Tool input + output — always visible */}
      <ToolRender tool={tool} />
    </div>
  )
})

/** Renders every tool of one iteration as its own expanded card. */
export const ToolGroup = memo(function ToolGroup({ tools }: { tools: WebToolProgress[] }) {
  if (!tools.length) return null
  return (
    <div className="flex flex-col gap-1.5">
      {tools.map((tool, i) => (
        <ToolCard key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
      ))}
    </div>
  )
})
