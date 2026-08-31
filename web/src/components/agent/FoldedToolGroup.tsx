/**
 * FoldedToolGroup — tool call display with merged groups and status colors.
 *
 * 折叠行（pill 行）+ Popover 浮层（性能根治）：原地 AnimatedCollapse 展开会
 * 逐帧改变行高 → MessageList 虚拟列表 measureElement/ResizeObserver 逐帧
 * relayout，且展开瞬间挂载全部工具 fancy DOM —— 两者叠加导致展开/关闭卡死。
 * 改为 Popover 后：折叠行高度恒定（虚拟列表零 relayout），浮层经 Portal
 * 渲染在 document.body（脱离虚拟列表布局树），且浮层内容仅 open 时挂载。
 *
 * Folded row:  ▸ [pill] [pill] …（>8 工具时前 7 pill + "+N" 徽标）
 * Popover:     全量工具列表（状态图标 + name + label 单行 + 耗时），
 *              点击一条展开该工具完整 fancy 渲染（ToolCard，与原 AnimatedCollapse
 *              内渲染的同一组件）。
 *
 * 'none' collapse level 仍为独立展开 ToolCard（不折叠，语义不变）。
 * GenUI 工具（uiMode）永不折叠，直接渲染为顶层卡片。
 */
import { memo, useMemo, useState, type ReactNode } from 'react'

import { AnsiText } from './AnsiText'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { ArgsView } from './ToolCallBlock'
import { SweepText } from './SweepText'
import { ToolRender } from './ToolRender'
import { getToolIcon } from './toolIcons'
import { isToolInProgress } from './statusVisual'

import type { CollapseLevel } from '@/types/agent'
import { Check, X } from 'lucide-react'
import type { WebToolProgress } from '@/types/shared'

/** Max param preview length in folded row. */
const MAX_PARAM_LEN = 25

/** Inline pill cap: more than 8 tools → first 7 pills + "+N" badge. */
const PILL_INLINE_MAX = 8
const PILL_INLINE_HEAD = 7

/** 折叠行按钮样式（pill 行容器）。 */
const ROW_BUTTON_CLASS =
  'flex w-full flex-wrap items-center gap-2 px-0.5 py-1 text-left text-xs cursor-pointer text-text-secondary transition-colors hover:opacity-80'

/** 合并组 pill 行容器（div——行本身不是 trigger，pill 各自独立 Popover）。 */
const ROW_ROW_CLASS = 'flex w-full flex-wrap items-center gap-2 px-0.5 py-1 text-xs'

/** 浮层样式（设计稿 1:1）：固定深色玻璃底 + 大阴影；宽 430px、内部滚动。
 *  覆盖 ui/popover 默认的 w-72/rounded-md/bg-popover/p-4/shadow-md。 */
const POPOVER_CLASS =
  'w-[430px] max-w-[calc(100vw-2rem)] max-h-[min(60vh,480px)] overflow-y-auto rounded-xl border-border bg-sidebar-bg p-2 text-text-primary shadow-2xl backdrop-blur-md'

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

/** 工具 pill 三态（设计稿 1:1）：running=accent 椭圆+pulse 圆点+流光 / error=红椭圆+✗ / done=绿椭圆+✓。 */
function toolPill(tool: WebToolProgress, sweepRunning = true): ReactNode {
  const status = singleStatus(tool)
  const running = status === 'running'
  const failed = status === 'all-failed'
  const okC = 'var(--status-success, #22c55e)'
  const c = running ? 'var(--accent)' : failed ? 'var(--destructive)' : okC
  const bg = running
    ? 'color-mix(in srgb, var(--accent) 14%, transparent)'
    : failed
      ? 'color-mix(in srgb, var(--destructive) 12%, transparent)'
      : 'color-mix(in srgb, var(--status-success, #22c55e) 12%, transparent)'
  const name = displayName(tool)
  const param = toolParam(tool)
  const label = name + (param ? ' ' + truncate(param, MAX_PARAM_LEN) : '')
  const showSweep = running && sweepRunning && !isSubAgentTool(tool)
  return (
    <span
      className="inline-flex max-w-full items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium"
      style={{ color: c, background: bg }}
    >
      {running
        ? <span className="size-1.5 shrink-0 rounded-full" style={{ background: c, animation: 'pulse-blue 1.2s infinite' }} />
        : failed
          ? <X className="shrink-0" size={9} strokeWidth={3} style={{ color: c }} />
          : <Check className="shrink-0" size={9} strokeWidth={3} style={{ color: c }} />}
      {showSweep
        ? <SweepText text={label} color={c} className="truncate font-mono" />
        : <span className="truncate font-mono">{label}</span>}
    </span>
  )
}

/** 耗时格式化（浮层行右侧）。 */
function formatElapsed(ms: number): string {
  return ms >= 1000 ? (ms / 1000).toFixed(1) + 's' : `${Math.round(ms)}ms`
}

/**
 * 单工具浮窗内容（用户要求信息齐全）：状态 header + summary + 统一参数块 + 完整渲染。
 * 参数由 ArgsView（hljs JSON 高亮）统一渲染——专用渲染器（Shell/Read 等）不展示
 * args JSON，fallback ToolCallBlock 由 hideArgs 抑制，全工具恰好一份参数块。
 */
function ToolPopoverDetail({ tool }: { tool: WebToolProgress }) {
  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const running = status === 'running'
  const failed = status === 'all-failed'
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2 text-xs">
        {running
          ? <span className="size-1.5 shrink-0 rounded-full" style={{ background: color, animation: 'pulse-blue 1.2s infinite' }} />
          : failed
            ? <X className="shrink-0" size={11} strokeWidth={3} style={{ color }} />
            : <Check className="shrink-0" size={11} strokeWidth={3} style={{ color }} />}
        <span className="font-mono text-[11px] font-medium" style={{ color }}>{displayName(tool)}</span>
        {tool.elapsedMs > 0 && (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-text-muted">{formatElapsed(tool.elapsedMs)}</span>
        )}
      </div>
      {/* summary 与 detail 输出同文时不重复显示（如 task_kill 的确认文本）。
          ANSI 渲染：Shell 等工具的 summary 取自命令输出首行，携带 SGR 颜色码
          （vitest/ls 等）——用 AnsiText 渲染成彩色，而非 raw 转义序列泄漏。 */}
      {tool.summary && tool.summary !== tool.detail ? <p className="text-[11.5px] leading-relaxed text-text-secondary"><AnsiText text={tool.summary} /></p> : null}
      {tool.args ? (
        <div>
          <div className="mb-1 text-[9px] font-semibold uppercase tracking-wider text-text-muted">参数</div>
          <div className="max-h-[150px] overflow-y-auto rounded-md border border-border">
            <ArgsView args={tool.args} />
          </div>
        </div>
      ) : null}
      <ToolRender tool={tool} hideArgs />
    </div>
  )
}

/** 折叠行 pill 列表：≤8 全量；>8 显示前 7 pill + "+N" 徽标。
 *  点哪个 pill 弹哪个工具的浮窗（summary + 参数 + 渲染）——互不混叠；
 *  "+N" 弹溢出工具的全量列表。 */
const MergedPills = memo(function MergedPills({ tools, sweepRunning = true }: { tools: WebToolProgress[]; sweepRunning?: boolean }) {
  const overflow = tools.length > PILL_INLINE_MAX
  const shown = overflow ? tools.slice(0, PILL_INLINE_HEAD) : tools
  return (
    <span className="flex flex-wrap items-center gap-1.5">
      {shown.map((tool, i) => (
        <Popover key={`${tool.name}-${i}`}>
          <PopoverTrigger asChild>
            <span data-testid="tool-pill" className="inline-flex cursor-pointer items-center transition-opacity hover:opacity-85">{toolPill(tool, sweepRunning)}</span>
          </PopoverTrigger>
          <PopoverContent align="start" className={POPOVER_CLASS}>
            <ToolPopoverDetail tool={tool} />
          </PopoverContent>
        </Popover>
      ))}
      {overflow && <OverflowPillsMenu tools={tools} />}
    </span>
  )
})

/** "+N" 溢出菜单：被收纳工具的全量列表（点击条目展开该工具卡片）。 */
function OverflowPillsMenu({ tools }: { tools: WebToolProgress[] }) {
  const hidden = tools.slice(PILL_INLINE_HEAD)
  return (
    <Popover>
      <PopoverTrigger asChild>
        <span data-testid="tool-pill-more" className="inline-flex shrink-0 cursor-pointer items-center rounded-full bg-surface-bg px-2 py-0.5 text-[11px] font-medium text-text-muted transition-opacity hover:opacity-85">
          +{hidden.length}
        </span>
      </PopoverTrigger>
      <PopoverContent align="start" className={POPOVER_CLASS}>
        <ToolPopoverContent tools={hidden} />
      </PopoverContent>
    </Popover>
  )
}

/**
 * 浮层内容：全量工具列表。每条 = 状态图标 + name + label（单行 truncate）+ 耗时；
 * 点击一条展开该工具完整 fancy 渲染（ToolCard —— 与原地展开版同一组件）。
 * 浮层在 Portal 内，内部展开的行高变化不进入虚拟列表布局树（零 relayout）。
 */
function ToolPopoverContent({ tools }: { tools: WebToolProgress[] }) {
  const [sel, setSel] = useState<number | null>(null)
  return (
    <div className="flex flex-col">
      {tools.map((tool, i) => {
        const status = singleStatus(tool)
        const running = status === 'running'
        const failed = status === 'all-failed'
        const c = running
          ? 'var(--accent)'
          : failed
            ? 'var(--destructive)'
            : 'var(--status-success, #22c55e)'
        const active = sel === i
        return (
          <div key={`${tool.name}-${tool.label}-${i}`}>
            <button
              type="button"
              data-testid="tool-row"
              aria-expanded={active}
              onClick={() => setSel(active ? null : i)}
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-xs transition-colors hover:bg-surface-bg"
            >
              {running
                ? <span className="size-1.5 shrink-0 rounded-full" style={{ background: c, animation: 'pulse-blue 1.2s infinite' }} />
                : failed
                  ? <X className="shrink-0" size={11} strokeWidth={3} style={{ color: c }} />
                  : <Check className="shrink-0" size={11} strokeWidth={3} style={{ color: c }} />}
              <span className="shrink-0 font-mono text-[11px] font-medium" style={{ color: c }}>{displayName(tool)}</span>
              <span className="min-w-0 flex-1 truncate text-[11px] text-text-muted">{tool.label}</span>
              {tool.elapsedMs > 0 && (
                <span className="shrink-0 text-[10px] tabular-nums text-text-muted">{formatElapsed(tool.elapsedMs)}</span>
              )}
            </button>
            {active && (
              <div className="px-2 pb-2">
                <ToolCard tool={tool} />
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

/** Expanded tool card: [icon] name + input + output */
function ToolCard({ tool }: { tool: WebToolProgress }) {
  const name = tool.name || 'tool'

  // GenUI tool: no card chrome, just render the GenUI directly.
  if (tool.uiMode) {
    return <ToolRender tool={tool} />
  }

  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const dn = displayName(tool)
  const showSweep = status === 'running' && !isSubAgentTool(tool)

  return (
    <div className="rounded-md border border-border/50 bg-surface-bg/30 p-2">
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
  // GenUI 工具永不折叠（metadata 驱动）；non-GenUI 才进入折叠行/浮层。
  // useMemo 必须在 early return 之前（hooks 规则）；tools 为空时结果为空数组，
  // 随后 return null。
  const { genuiTools, otherTools } = useMemo(
    () => ({ genuiTools: tools.filter((t) => t.uiMode), otherTools: tools.filter((t) => !t.uiMode) }),
    [tools],
  )
  // pill 行 JSX：依赖 tools 引用（otherTools 由上方 useMemo 派生，引用稳定）——
  // tools 不变时折叠行 re-render 零重建（pill 浮窗开合由 radix 内部管理）。
  const pillsRow = useMemo(() => <MergedPills tools={otherTools} />, [otherTools])

  if (!tools.length) return null

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

  // 'none' level: always expanded, each tool as independent card (no folding)
  if (level === 'none') {
    return (
      <div className="flex flex-col gap-1.5">
        {genuiElements}
        {otherTools.map((tool, i) => (
          <ToolCard key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
        ))}
      </div>
    )
  }

  // 多行非合并：每个工具独立折叠行 → 各自 Popover（浮层 = 单工具卡片）
  if (!mergeTools && tools.length > 1) {
    return (
      <div className="flex flex-col gap-1.5">
        {genuiElements}
        <div className="flex flex-col">
          {otherTools.map((tool, i) => (
            <SingleToolFold key={`${tool.name}-${tool.label}-${i}`} tool={tool} />
          ))}
        </div>
      </div>
    )
  }

  // 单工具 / 合并组：pill 行——每个 pill 独立浮窗（该工具 summary+参数+渲染），
  // +N 徽标弹溢出列表。行本身不再是 trigger（无 ▸ 箭头，用户要求）。
  return (
    <div className="flex flex-col gap-1.5">
      {genuiElements}
      <div className={ROW_ROW_CLASS}>{pillsRow}</div>
    </div>
  )
})

/** 多行非合并模式：单工具独立折叠行 → Popover 浮层（该工具 summary+参数+渲染）。 */
const SingleToolFold = memo(function SingleToolFold({ tool }: { tool: WebToolProgress }) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" aria-expanded={open} className={ROW_BUTTON_CLASS}>
          <span className="inline-flex items-center">{toolPill(tool)}</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className={POPOVER_CLASS}>
        <ToolPopoverDetail tool={tool} />
      </PopoverContent>
    </Popover>
  )
})
