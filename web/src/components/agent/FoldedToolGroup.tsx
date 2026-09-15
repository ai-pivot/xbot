/**
 * FoldedToolGroup — tool call display with merged groups and status colors.
 *
 * 折叠行（pill 行）+ Popover 浮层（性能根治）：原地 AnimatedCollapse 展开会
 * 逐帧改变行高 → MessageList 虚拟列表 measureElement/ResizeObserver 逐帧
 * relayout，且展开瞬间挂载全部工具 fancy DOM —— 两者叠加导致展开/关闭卡死。
 * 改为 Popover 后：折叠行高度恒定（虚拟列表零 relayout），浮层经 Portal
 * 渲染在 document.body（脱离虚拟列表布局树），且浮层内容仅 open 时挂载。
 *
 * Folded row:  [pill] [pill] …（>8 工具时前 7 pill + "+N" 徽标；每个 pill 独立
 *              浮窗 = 该工具详情）。行本身不是 trigger（无 ▸ 箭头）。
 * Popover:     单工具详情（summary + 参数 + fancy 渲染 ToolCard）。
 *
 * 唯一形态（2026-09-12）：每个工具一个 pill。折叠级别 / 合并工具 / 独立展开行
 * 等"气泡之外"的形态已彻底删除。
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
import { syntheticShortName, syntheticSubject } from './SyntheticToolCard'
import { useI18n } from '@/providers/i18n'
import { syntheticKindOf } from './SyntheticToolCard'
import { CATEGORY_COLOR, syntheticKindBadge, syntheticKindColor, toolCategory } from './toolVisuals'

import { Check, Minus, X } from 'lucide-react'
import type { WebToolProgress } from '@/types/shared'

/** Max param preview length in folded row. */
const MAX_PARAM_LEN = 25

/** Inline pill cap: more than 8 tools → first 7 pills + "+N" badge. */
const PILL_INLINE_MAX = 8
const PILL_INLINE_HEAD = 7

/** 合并组 pill 行容器（div——行本身不是 trigger，pill 各自独立 Popover）。 */
const ROW_ROW_CLASS = 'flex w-full flex-wrap items-center gap-2 px-0.5 py-1 text-xs'

/** 浮层样式（设计稿 1:1）：固定深色玻璃底 + 大阴影；宽 430px、内部滚动。
 *  覆盖 ui/popover 默认的 w-72/rounded-md/bg-popover/p-4/shadow-md。 */
const POPOVER_CLASS =
  'w-[430px] max-w-[calc(100vw-2rem)] max-h-[min(60vh,480px)] overflow-y-auto rounded-xl border-border bg-bg-secondary p-2 text-text-primary shadow-2xl backdrop-blur-md'

interface FoldedToolGroupProps {
  tools: WebToolProgress[]
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

/** i18n translate signature (subset of I18nContextValue['t']). */
type T = (key: string, params?: Record<string, string | number>) => string

/** Get display name from tool label.
 *  For generating tools, always use tool.name — the label is still streaming
 *  (e.g. "思考中…" placeholder) and parsing it would cause name flicker.
 *  Injected (synthetic) notification tools NEVER show their internal snake_case
 *  name (`bg_subagent_completed`) — a localized short name is used instead. */
function displayName(tool: WebToolProgress, t?: T): string {
  const name = tool.name || 'tool'
  const synthetic = syntheticShortName(tool, t)
  if (synthetic) return synthetic
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

/**
 * 工具 pill 视觉语言（用户 2026-09-15 定稿）：
 *   · 分类色用于**图标 + 工具名**（9 套，见 toolVisuals）；状态色与分类色**解耦**；
 *   · 成功安静（描边绿勾、无标签）/ 失败吵闹（红底+红边+左红条+实心红叉+「失败」+exit N）/
 *     终止灰虚线「已终止」/ 进行中分类色脉动「执行中」/ 排队空心灰点「排队」；
 *   · 假工具（注入型）：**虚线 + kind 头像 + 「系统」角标**，第二段是主语（task_id / role·instance）。
 */
function toolPill(tool: WebToolProgress, t?: T): ReactNode {
  const status = singleStatus(tool)
    const failed = status === 'all-failed'
  const raw = (tool.status || '').toLowerCase()
  const killed = raw === 'killed' || raw === 'aborted' || raw === 'cancelled'
  const pending = raw === 'pending'
  const generating = raw === 'generating'
  const synName = syntheticShortName(tool, t)
  const isSyn = synName !== null
  const kind = isSyn ? syntheticKindOf(tool) : ''
  const hue = isSyn ? syntheticKindColor(kind) : CATEGORY_COLOR[toolCategory(tool.name)]
  const okColor = 'var(--status-success, #22c55e)'
  const errColor = 'var(--destructive, #ef4444)'
  const name = synName ?? displayName(tool, t)
  const rawParam = isSyn ? syntheticSubject(tool) : toolParam(tool)
  const param = rawParam && rawParam.toLowerCase() !== name.toLowerCase() ? rawParam : ''
  const exit = (tool as unknown as { exitCode?: number }).exitCode
  const label = name + (param ? ' ' + truncate(param, MAX_PARAM_LEN) : '')
  // ⚠️ 精确按 raw status 分支：`singleStatus` 把 pending/generating 也算 running，
  // 用它会让排队/生成中也显示「执行中」。
  const executing = raw === 'running' || raw === 'executing'
  const statusText = failed ? '失败' : killed ? '已终止' : pending ? '排队' : generating ? '生成中' : executing ? '执行中' : ''
  const statusFg = failed ? '#fff' : killed || pending ? 'var(--text-muted)' : hue
  const statusBg = failed
    ? errColor
    : killed || pending
      ? 'color-mix(in srgb, var(--text-muted) 18%, transparent)'
      : `color-mix(in srgb, ${hue} 16%, transparent)`
  const border = failed
    ? `1px solid color-mix(in srgb, ${errColor} 55%, transparent)`
    : (isSyn || killed)
      ? `1px dashed color-mix(in srgb, ${isSyn ? hue : 'var(--text-muted)'} 55%, transparent)`
      : '1px solid var(--border)'
  const bg = failed
    ? `color-mix(in srgb, ${errColor} 12%, transparent)`
    : isSyn ? `color-mix(in srgb, ${hue} 10%, transparent)` : 'var(--bg-secondary)'
  const nameColor = failed ? 'color-mix(in srgb, var(--destructive) 78%, var(--text-primary))' : hue
  return (
    <span
      data-tool-name={tool.name}
      data-tool-status={failed ? 'error' : killed ? 'killed' : pending ? 'pending' : executing || generating ? 'running' : 'done'}
      className="inline-flex min-w-0 max-w-full items-center gap-1 overflow-hidden rounded-full py-0.5 pl-1 pr-2 text-[11px] font-medium"
      // ⚠️ 上限**不能**写在这里：pill 的包含块是外层 `LazyPillPopover` wrapper（内容定宽 = indefinite），
      // 规范规定百分比 max-width 对 indefinite 包含块**按 none 处理** ⇒ `calc(50% - 8px)` 完全失效，
      // 只剩 15rem=240px 生效 ⇒ 手机 362px 行宽下 240×2+gap > 362 ⇒ **每个 pill 独占一行**
      // （2026-09-15 用户真机截图 + E2E 实测：4 个 pill 占 4 行）。上限见 wrapper（那里包含块=行宽，definite）。
      style={{ border, background: bg }}
    >
      {/* 左 3px 色条：**每个** pill 都有（失败=红实条 / 终止=灰虚线 / 其余=分类色）——
          恒定槽位是"所有 pill 的 icon 与首字符左对齐"的前提（用户 2026-09-15 明确要求）。 */}
      <span
        aria-hidden
        className="h-3.5 w-[3px] shrink-0 rounded-full"
        style={{ background: killed ? 'transparent' : failed ? errColor : hue, borderRight: killed ? '3px dotted var(--text-muted)' : undefined }}
      />
      {/* 状态标记：**恒定 14px 槽**，所有状态都塞进同一个 `size-3.5` 盒子 —— running 的点（6px）比
          done 的勾（14px）小 8px，槽位不定宽会让 icon 与名字整体左移（用户 2026-09-15 实测
          「执行中的工具和执行完毕的 align 有问题」）。 */}
      <span aria-hidden className="flex size-3.5 shrink-0 items-center justify-center">
        {failed ? (
          <span className="flex size-3.5 items-center justify-center rounded-full text-white" style={{ background: errColor }}><X className="size-2.5" strokeWidth={3.5} /></span>
        ) : killed ? (
          <span className="flex size-3.5 items-center justify-center rounded-full" style={{ color: 'var(--text-muted)', border: '1.5px dashed var(--border)' }}><Minus className="size-2.5" strokeWidth={3} /></span>
        ) : executing || generating ? (
          <span className="size-1.5 rounded-full" style={{ background: hue, animation: 'pulse-blue 1.2s infinite' }} />
        ) : pending ? (
          <span className="size-1.5 rounded-full" style={{ border: '1.5px solid var(--text-muted)' }} />
        ) : (
          <span className="flex size-3.5 items-center justify-center rounded-full" style={{ color: okColor, border: `1.5px solid ${okColor}` }}><Check className="size-2.5" strokeWidth={3.5} /></span>
        )}
      </span>
      {/* 图标槽位：真工具（12px glyph）与假工具（16px 字母头像）都塞进**同一个 16px 方槽** ——
          槽位宽度恒定是"所有 pill 的 icon 列与名字首字符左对齐"的前提（用户 2026-09-15 要求）。 */}
      <span aria-hidden data-testid="tool-pill-icon" className="flex size-4 shrink-0 items-center justify-center">
        {isSyn
          ? <span className="flex size-4 items-center justify-center rounded-full text-[8px] font-extrabold leading-none text-black/80" style={{ background: hue }}>{syntheticKindBadge(kind)}</span>
          : (() => {
              const Icon = getToolIcon(tool.name) as React.ComponentType<{ className?: string; style?: React.CSSProperties }>
              return <Icon className="size-3" style={{ color: hue }} />
            })()}
      </span>
      {executing && !isSubAgentTool(tool)
        ? <SweepText text={label} color={nameColor} className={`min-w-0 truncate ${isSyn ? '' : 'font-mono'}`} />
        : isSyn
          ? (
            <>
              <span data-testid="tool-pill-name" className="min-w-0 truncate" style={{ color: nameColor }}>{name}</span>
              {param && <span className="min-w-0 truncate font-mono opacity-70">{truncate(param, MAX_PARAM_LEN)}</span>}
            </>
          )
          : <span data-testid="tool-pill-name" className="min-w-0 truncate font-mono" style={{ color: nameColor }}>{label}</span>}
      {isSyn && (
        <span aria-hidden className="shrink-0 rounded-[4px] border px-1 text-[9px] font-extrabold leading-4" style={{ color: hue, borderColor: `color-mix(in srgb, ${hue} 50%, transparent)` }}>系统</span>
      )}
      {statusText && (
        <span aria-hidden className="shrink-0 rounded-full px-1.5 py-px text-[9.5px] font-bold leading-4" style={{ color: statusFg, background: statusBg }}>{statusText}</span>
      )}
      {failed && exit !== undefined && (
        <span aria-hidden className="shrink-0 rounded-full border px-1.5 py-px text-[9.5px] font-bold leading-4" style={{ color: errColor, borderColor: `color-mix(in srgb, ${errColor} 50%, transparent)` }}>exit {exit}</span>
      )}
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
  const { t } = useI18n()
  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const running = status === 'running'
  const failed = status === 'all-failed'
  // 注入型工具：本地化名字（正文字体，等宽渲染 CJK 很怪）+ subject chip
  const shownName = displayName(tool, t)
  const rawSubject = syntheticShortName(tool, t) ? syntheticSubject(tool) : ''
  // 不给与标题重复的 subject（否则标题行出现「插话 💬 插话」这种看起来像 bug 的重复）
  const subject = rawSubject && rawSubject.toLowerCase() !== shownName.toLowerCase() ? rawSubject : ''
  return (
    <div className="flex flex-col gap-2">
      {/* 注入型工具（bg task / 子代理 / 插话…）：卡片自带标题、状态、退出码、耗时、
          承接说明与全部内容 —— 弹层的通用头部与 summary 行只会把同样的信息再重复两遍
          （很吵），所以这里只渲染卡片本身。 */}
      {syntheticShortName(tool, t) !== null ? (
        <ToolRender tool={tool} hideArgs />
      ) : (
        <>
          <div className="flex items-center gap-2 text-xs">
            {running
              ? <span className="size-1.5 shrink-0 rounded-full" style={{ background: color, animation: 'pulse-blue 1.2s infinite' }} />
              : failed
                ? <X className="shrink-0" size={11} strokeWidth={3} style={{ color }} />
                : <Check className="shrink-0" size={11} strokeWidth={3} style={{ color }} />}
            <span data-tool-name={tool.name} className="shrink-0 text-[11.5px] font-medium" style={{ color }}>{shownName}</span>
            {subject && (
              <code className="truncate rounded bg-bg-tertiary/60 px-1 py-0.5 font-mono text-[10px] text-text-muted">
                {subject}
              </code>
            )}
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
              <div className="mb-1 text-[9px] font-semibold uppercase tracking-wider text-text-muted">{t('agent.args')}</div>
              <div className="max-h-[150px] overflow-y-auto rounded-md border border-border">
                <ArgsView args={tool.args} />
              </div>
            </div>
          ) : null}
          <ToolRender tool={tool} hideArgs />
        </>
      )}
    </div>
  )
}

/** 懒挂 Popover 的 pill：未点击前只是 <span>（零 Radix 实例）。
 *
 *  PERF（Trace-20260912T100816）：每个工具 pill 都挂一个 Radix `Popover`
 *  （Presence/useControllableState/useId + PopoverTrigger asChild → Slot →
 *  cloneElement），长 turn 里成百上千个实例的创建/reconcile 占了可观 CPU
 *  （radix 机制 ~6.6% + Slot/cloneElement 4.5%），而浮层只在**点击时**才需要。
 *  懒挂后：关闭态零 radix 成本，点击才创建（行为等价：点 pill 弹详情）。 */
function LazyPillPopover({
  children,
  content,
  testId,
  toolName,
}: {
  children: ReactNode
  content: ReactNode
  testId: string
  /** 内部工具名（稳定标识）：E2E/测试按它定位，绝不依赖可见文案（会随 i18n 变化）。 */
  toolName?: string
}) {
  const [open, setOpen] = useState(false)
  if (!open) {
    return (
      <span
        data-testid={testId}
        data-tool-name={toolName}
        role="button"
        tabIndex={0}
        onClick={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setOpen(true)
          }
        }}
        className="inline-flex min-w-0 cursor-pointer items-center transition-opacity hover:opacity-85"
        // ⚠️ 上限**必须是不含百分比**的确定值：wrapper 的包含块是 flex item（内容尺寸 = indefinite），
        // 百分比（`50%`）在里面无法解析 ⇒ Chrome 把整个 `min()` 当作 `none` ⇒ **上限等于没有**
        // （2026-09-15 真机仍一行一个的根因；`50vw` 是视口单位，永远可解析）。
        style={{ maxWidth: 'min(calc(50vw - 32px), 15rem)' }}
      >
        {children}
      </span>
    )
  }
  return (
    <Popover open onOpenChange={(o) => { if (!o) setOpen(false) }}>
      <PopoverTrigger asChild>
        <span data-testid={testId} data-tool-name={toolName} className="inline-flex min-w-0 cursor-pointer items-center transition-opacity hover:opacity-85" style={{ maxWidth: 'min(calc(50vw - 32px), 15rem)' }}>
          {children}
        </span>
      </PopoverTrigger>
      <PopoverContent align="start" className={POPOVER_CLASS}>
        {content}
      </PopoverContent>
    </Popover>
  )
}

/** 折叠行 pill 列表：≤8 全量；>8 显示前 7 pill + "+N" 徽标。
 *  点哪个 pill 弹哪个工具的浮窗（summary + 参数 + 渲染）——互不混叠；
 *  "+N" 弹溢出工具的全量列表。 */
const MergedPills = memo(function MergedPills({ tools }: { tools: WebToolProgress[] }) {
  const { t } = useI18n()
  const overflow = tools.length > PILL_INLINE_MAX
  const shown = overflow ? tools.slice(0, PILL_INLINE_HEAD) : tools
  return (
    <span className="flex min-w-0 max-w-full flex-wrap items-center gap-1.5">
      {shown.map((tool, i) => (
        <LazyPillPopover key={`${tool.name}-${i}`} testId="tool-pill" toolName={tool.name} content={<ToolPopoverDetail tool={tool} />}>
          {toolPill(tool, t)}
        </LazyPillPopover>
      ))}
      {overflow && <OverflowPillsMenu tools={tools} />}
    </span>
  )
})

/** "+N" 溢出菜单：被收纳工具的全量列表（点击条目展开该工具卡片）。 */
function OverflowPillsMenu({ tools }: { tools: WebToolProgress[] }) {
  const hidden = tools.slice(PILL_INLINE_HEAD)
  return (
    <LazyPillPopover
      testId="tool-pill-more"
      toolName="__overflow__"
      content={<ToolPopoverContent tools={hidden} />}
    >
      <span className="inline-flex shrink-0 cursor-pointer items-center rounded-full bg-bg-hover px-2 py-0.5 text-[11px] font-medium text-text-muted transition-opacity hover:opacity-85">
        +{hidden.length}
      </span>
    </LazyPillPopover>
  )
}

/**
 * 浮层内容：全量工具列表。每条 = 状态图标 + name + label（单行 truncate）+ 耗时；
 * 点击一条展开该工具完整 fancy 渲染（ToolCard —— 与原地展开版同一组件）。
 * 浮层在 Portal 内，内部展开的行高变化不进入虚拟列表布局树（零 relayout）。
 */
function ToolPopoverContent({ tools }: { tools: WebToolProgress[] }) {
  const { t } = useI18n()
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
              className="flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-left text-xs transition-colors hover:bg-bg-hover"
            >
              {running
                ? <span className="size-1.5 shrink-0 rounded-full" style={{ background: c, animation: 'pulse-blue 1.2s infinite' }} />
                : failed
                  ? <X className="shrink-0" size={11} strokeWidth={3} style={{ color: c }} />
                  : <Check className="shrink-0" size={11} strokeWidth={3} style={{ color: c }} />}
              <span className="shrink-0 font-mono text-[11px] font-medium" style={{ color: c }}>{displayName(tool, t)}</span>
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
  const { t } = useI18n()
  const name = tool.name || 'tool'

  // GenUI tool: no card chrome, just render the GenUI directly.
  if (tool.uiMode) {
    return <ToolRender tool={tool} />
  }

  const status = singleStatus(tool)
  const color = statusColorVar(status)
  const dn = displayName(tool, t)
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
}: FoldedToolGroupProps) {
  // GenUI 工具永不折叠（metadata 驱动）；non-GenUI 才进入 pill 行/浮层。
  // useMemo 必须在 early return 之前（hooks 规则）；tools 为空时结果为空数组，
  // 随后 return null。
  const { genuiTools, otherTools } = useMemo(
    () => ({ genuiTools: tools.filter((t) => t.uiMode), otherTools: tools.filter((t) => !t.uiMode) }),
    [tools],
  )
  // pill 行 JSX：依赖 tools 引用（otherTools 由上方 useMemo 派生，引用稳定）——
  // tools 不变时 pill 行 re-render 零重建（pill 浮窗开合由 radix/懒挂管理）。
  const pillsRow = useMemo(() => <MergedPills tools={otherTools} />, [otherTools])

  // 行级失败告警：组内任一工具失败 ⇒ 行左侧红条 + `N 失败` chip（折叠/滚动时也不漏）。
  const failedCount = useMemo(() => otherTools.filter((x) => isFailed(x.status)).length, [otherTools])

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

  // 唯一形态：pill 行——每个 pill 独立浮窗（该工具 summary+参数+fancy 渲染），
  // +N 徽标弹溢出列表。行本身不是 trigger（无 ▸ 箭头，用户要求）。
  return (
    <div className="flex flex-col gap-1.5">
      {genuiElements}
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        {failedCount > 0 && (
          <>
            <span aria-hidden className="h-4 w-[3px] shrink-0 rounded-full" style={{ background: 'var(--destructive)' }} />
            <span
              data-testid="tool-group-failed"
              className="shrink-0 rounded-full px-1.5 py-px text-[10px] font-bold text-white"
              style={{ background: 'var(--destructive)' }}
            >
              {failedCount} 失败
            </span>
          </>
        )}
        <div data-testid="tool-pill-row" className={ROW_ROW_CLASS}>{pillsRow}</div>
      </div>
    </div>
  )
})
