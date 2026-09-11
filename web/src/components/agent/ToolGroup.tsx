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
import { createElement, memo } from 'react'

import { SweepText } from './SweepText'
import { ToolRender } from './ToolRender'
import { getToolIcon } from './toolIcons'
import { isToolInProgress } from './statusVisual'
import { toolStatusKind } from '@/types/agent'

import type { WebToolProgress } from '@/types/shared'

/** 单个工具的显示态（三态）。 */
type ToolVisualState = 'normal' | 'error' | 'running'

/** 状态对应的 CSS 变量（与 statusVisual.ts 共用同一组 --status-* token）。 */
function statusColorVar(state: ToolVisualState): string {
  switch (state) {
    case 'error':
      return 'var(--status-error)'
    case 'running':
      return 'var(--status-running)'
    default:
      return 'var(--status-idle)'
  }
}

/** Display name / param preview truncation（按字符，非按行）。 */
const MAX_PARAM_LEN = 25

/** Extract a short parameter hint from the tool label (text after ": "). */
function toolParam(tool: WebToolProgress): string {
  const label = tool.label || ''
  const idx = label.indexOf(': ')
  return idx >= 0 ? label.slice(idx + 2) : ''
}

/** Truncate to N characters with an ellipsis. */
function truncateChars(text: string, max: number): string {
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

/** 单个工具的显示态（error 优先于 in-progress）。 */
function toolVisualState(tool: WebToolProgress): ToolVisualState {
  if (toolStatusKind(tool.status) === 'error') return 'error'
  return isToolInProgress(tool.status) ? 'running' : 'normal'
}

/** 耗时格式化（卡片右上角）。 */
function formatElapsed(ms: number): string {
  return ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : `${Math.round(ms)}ms`
}

/** Render a single Lucide tool icon at 16px with status color.
 *  Uses createElement rather than `<Icon/>`: picking a component TYPE during
 *  render trips `react-hooks/static-components` (the compiler can't hoist a
 *  component chosen at runtime). createElement is equivalent and rule-clean. */
function ToolIcon({ name, state }: { name: string; state: ToolVisualState }) {
  return createElement(getToolIcon(name), {
    className: 'tool-icon-single shrink-0',
    style: { color: statusColorVar(state) },
  })
}

/** A single tool card: [icon] name [elapsed] + full render (args + output). */
export const ToolCard = memo(function ToolCard({ tool }: { tool: WebToolProgress }) {
  // GenUI tool: no card chrome, just render the GenUI panel directly.
  if (tool.uiMode) {
    return <ToolRender tool={tool} />
  }

  const state = toolVisualState(tool)
  const color = statusColorVar(state)
  const dn = displayName(tool)
  const param = toolParam(tool)
  // Header: displayName + a short param preview.
  const headerLabel = param ? `${dn} ${truncateChars(param, MAX_PARAM_LEN)}` : dn
  const showSweep = state === 'running' && !isSubAgentToolName(tool.name)

  return (
    <div
      data-testid="tool-card"
      data-tool-name={tool.name}
      className="rounded-md border border-border/50 bg-bg-tertiary/30 p-2"
    >
      {/* Card header: icon + name (+ param) + elapsed */}
      <div className="mb-1.5 flex items-center gap-1.5" style={{ color }}>
        <ToolIcon name={tool.name || 'tool'} state={state} />
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
        // Key is name+index: the tool list within one iteration is append-only,
        // and tool.label CHANGES while streaming ("思考中…" → "Read: path").
        // Including label would remount the card and restart its animations —
        // exactly the name flicker this component avoids.
        <ToolCard key={`${tool.name}-${i}`} tool={tool} />
      ))}
    </div>
  )
})
