/**
 * FoldedToolGroup — tool call display with merged groups and status colors.
 *
 * Single tool folded row:  [icon] ToolName param25chars
 *   - Icon + text color reflects status: gray=normal, light-red=all-failed, light-yellow=partial-fail
 *   - Click expands into a card: [icon] ToolName + input + output
 *
 * Merged row (any consecutive tools):  [icon] Name ×N [icon] Name ×M
 *   - Icons with names and counts, grouped by tool name
 *   - Click expands to show each tool as an independent card
 *   - Icon + text color reflects aggregate status
 *
 * All fold/unfold transitions use CSS grid animation (fold-container).
 */
import { memo, useState, type ReactNode } from 'react'

import { AnimatedCollapse } from '@/components/ui/animated-collapse'
import { SweepText } from './SweepText'
import { ToolRender } from './ToolRender'
import { getToolIcon } from './toolIcons'
import { isToolInProgress } from './statusVisual'
import type { CollapseLevel } from '@/types/agent'
import type { WebToolProgress } from '@/types/shared'

/** Max param preview length in folded row. */
const MAX_PARAM_LEN = 25

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

/** Aggregate status across multiple tools. */
function aggregateStatus(tools: WebToolProgress[]): ToolStatusColor {
  const anyRunning = tools.some((t) => isToolInProgress(t.status))
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

function isSubAgentTool(tool: WebToolProgress): boolean {
  return isSubAgentToolName(tool.name)
}

/** Get single tool status color. */
function singleStatus(tool: WebToolProgress): ToolStatusColor {
  return isFailed(tool.status) ? 'all-failed' : isToolInProgress(tool.status) ? 'running' : 'normal'
}

/** Render a single Lucide tool icon at 16px with status color. */
function ToolIcon({ name, status }: { name: string; status: ToolStatusColor }) {
  const Icon = getToolIcon(name) as React.ComponentType<{ className?: string; style?: React.CSSProperties }>
  return <Icon className="tool-icon-single shrink-0" style={{ color: statusColorVar(status) }} />
}

/** Format a single tool's folded title: [icon] name param25 */
function formatToolTitle(tool: WebToolProgress, sweepRunning = true): ReactNode {
  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const param = toolParam(tool)
  const name = displayName(tool)
  const showSweep = status === 'running' && sweepRunning && !isSubAgentTool(tool)

  return (
    <span className="flex items-center gap-1.5 min-w-0" style={{ color }}>
      <ToolIcon name={tool.name || 'tool'} status={status} />
      {showSweep
        ? <SweepText text={name} color={color} className="shrink-0 font-mono" />
        : <span className="shrink-0 font-mono">{name}</span>}
      {param && (
        <span className="font-mono truncate" style={{ color: 'var(--text-muted)' }}>{truncate(param, MAX_PARAM_LEN)}</span>
      )}
    </span>
  )
}

/** Build merged title: [icon] Name ×N [icon] Name ×M */
function formatMergedTitle(tools: WebToolProgress[], sweepRunning = true): ReactNode {
  const status = aggregateStatus(tools)
  const color = statusColorVar(status)
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

  const animatedText = groups
    .filter((group) => !isSubAgentToolName(group.name))
    .map((group) => `${group.name}${group.count > 1 ? ` ×${group.count}` : ''}`)
    .join('  ')
  const staticText = groups
    .filter((group) => isSubAgentToolName(group.name))
    .map((group) => `${group.name}${group.count > 1 ? ` ×${group.count}` : ''}`)
    .join('  ')

  return (
    <span className="flex flex-wrap items-center gap-1.5" style={{ color }}>
      {status === 'running' && sweepRunning && animatedText ? (
        <>
          <span className="flex items-center gap-0.5">
            {groups.map((group, index) => (
              <ToolIcon key={`${group.name}-${index}`} name={group.name} status={status} />
            ))}
          </span>
          <SweepText
            text={animatedText}
            color={color}
            className="shrink-0 font-mono text-xs"
          />
          {staticText && <span className="shrink-0 font-mono text-xs">{staticText}</span>}
        </>
      ) : groups.map((g, i) => (
        <span key={`${g.name}-${i}`} className="flex items-center gap-0.5">
          <ToolIcon name={g.name} status={status} />
          <span className="shrink-0 font-mono text-xs">{g.name}</span>
          {g.count > 1 ? (
            <span className="shrink-0 text-[11px]" style={{ color }}>×{g.count}</span>
          ) : null}
        </span>
      ))}
    </span>
  )
}

/** Expanded tool card: [icon] name + input + output */
function ToolCard({ tool }: { tool: WebToolProgress }) {
  const name = tool.name || 'tool'

  // display_html: no card chrome, just render the GenUI directly
  if (name === 'display_html') {
    return <ToolRender tool={tool} />
  }

  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const dn = displayName(tool)
  const showSweep = status === 'running' && !isSubAgentTool(tool)

  return (
    <div className="rounded-md border border-border/50 bg-bg-tertiary/30 p-2">
      {/* Card header: icon + name */}
      <div className="mb-1.5 flex items-center gap-1.5" style={{ color }}>
        <ToolIcon name={name} status={status} />
        {showSweep
          ? <SweepText text={dn} color={color} className="font-mono text-xs font-medium" />
          : <span className="font-mono text-xs font-medium">{dn}</span>}
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

  // display_html tools have special status: always visible, never folded.
  // Split them out and render before the folded tool group.
  const genuiTools = tools.filter((t) => t.name === 'display_html')
  const otherTools = tools.filter((t) => t.name !== 'display_html')

  const genuiElements = genuiTools.map((tool, i) => (
    <ToolCard key={`genui-${tool.label}-${i}`} tool={tool} />
  ))

  // If only GenUI tools, just render them
  if (otherTools.length === 0) {
    return (
      <div className="flex flex-col gap-1.5">
        {genuiElements}
      </div>
    )
  }

  // Render GenUI first, then fold the remaining tools
  const toolGroup = otherTools.length > 0 ? renderToolGroup(otherTools, level, mergeTools, expanded, setExpanded) : null

  return (
    <div className="flex flex-col gap-1.5">
      {genuiElements}
      {toolGroup}
    </div>
  )
})

/** Render a group of non-GenUI tools with folding logic. */
function renderToolGroup(
  tools: WebToolProgress[],
  level: CollapseLevel,
  mergeTools: boolean,
  expanded: boolean,
  setExpanded: (v: boolean) => void,
): ReactNode {
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
            className="flex flex-wrap items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
          >
            <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
            {formatToolTitle(tools[0], !expanded)}
          </button>
          <AnimatedCollapse open={expanded} lazy>
            <div className="ml-4 mt-1">
              <ToolCard tool={tools[0]} />
            </div>
          </AnimatedCollapse>
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

  // Merged: any 2+ consecutive tools → [icon] Name ×N [icon] Name ×M
  return (
    <div>
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        className="flex items-center gap-1 border-none bg-transparent px-0 py-1 text-left text-xs cursor-pointer text-text-secondary hover:text-text-primary transition-colors"
      >
        <span className="shrink-0 text-text-muted select-none">{expanded ? '▾' : '▸'}</span>
        {formatMergedTitle(tools, !expanded)}
      </button>
      <AnimatedCollapse open={expanded} lazy>
        <div className="ml-4 mt-1 flex flex-col gap-1.5">
          {tools.map((tool, i) => (
            <ToolCard key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
          ))}
        </div>
      </AnimatedCollapse>
    </div>
  )
}

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
        {formatToolTitle(tool, !expanded)}
      </button>
      <AnimatedCollapse open={expanded} lazy>
        <div className="ml-4 mt-1">
          <ToolCard tool={tool} />
        </div>
      </AnimatedCollapse>
    </div>
  )
})
