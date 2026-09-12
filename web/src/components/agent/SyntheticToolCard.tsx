/**
 * SyntheticToolCard — fancy cards for **injected (built-in) notification tools**:
 * background task finished, sub-agent finished, cron fired, async message,
 * delivery, cancel marker, loop guard, pre-turn reminder, user interjection.
 *
 * These are not real tools: the backend injects system notifications as fake
 * tool-call pairs so the model sees them mid-Run. For the UI it also attaches a
 * UI-only structured payload in `tool.toolHints` (tools.SyntheticToolHints —
 * never sent to the model), carrying the ORIGINAL task/command, role/instance,
 * status, exit code, duration, message body and an output preview. These cards
 * render that payload as a rich panel; rows written before the payload existed
 * degrade to `summary` / `detail` text.
 *
 * Icons are lucide SVG — NEVER emoji (emoji glyphs render as tofu boxes
 * wherever the environment lacks an emoji font; caught by an E2E screenshot).
 */
import { memo, useMemo, useState, type ReactNode } from 'react'
import {
  Ban,
  Bot,
  Check,
  ChevronDown,
  Clock,
  Copy,
  CornerUpLeft,
  Mail,
  RotateCcw,
  Send,
  Terminal,
  Zap,
} from 'lucide-react'

import { useI18n } from '@/providers/i18n'
import { MarkdownRenderer } from './MarkdownRenderer'
import type { WebToolProgress } from '@/types/shared'

// ── payload ───────────────────────────────────────────────────────────

interface SyntheticHints {
  kind?: string
  task_id?: string
  /** The ORIGINAL thing that was asked for: shell command, or sub-agent task. */
  task?: string
  role?: string
  instance?: string
  status?: string
  exit_code?: number
  elapsed_ms?: number
  message?: string
  output?: string
  error?: string
}

/** Parse the UI-only payload the backend puts in toolHints for injected
 *  notification tools. Returns null when absent (legacy rows / real tools). */
export function parseSyntheticHints(raw: string): SyntheticHints | null {
  const s = (raw || '').trim()
  if (!s.startsWith('{')) return null
  try {
    const v = JSON.parse(s) as SyntheticHints
    return v && typeof v === 'object' ? v : null
  } catch {
    return null
  }
}

/** Tool names injected by the backend as fake notification tool-calls. */
const SYNTHETIC_TOOL_NAMES = new Set([
  'background_task_result',
  'cron_fired',
  'async_message',
  'user_cancelled',
  'delivered_message',
  'loop_detected',
  'pre_turn_end',
  'user_interrupt',
])

export function isSyntheticToolName(name: string): boolean {
  return name.startsWith('bg_subagent_') || SYNTHETIC_TOOL_NAMES.has(name)
}

/** Kind is normally carried by the payload; derive it from the tool name for
 *  history rows written before the payload existed. */
export function inferSyntheticKind(name: string): string {
  if (name.startsWith('bg_subagent_')) return 'subagent'
  switch (name) {
    case 'background_task_result': return 'bg_task'
    case 'cron_fired': return 'cron'
    case 'async_message': return 'async'
    case 'delivered_message': return 'delivered'
    case 'user_cancelled': return 'cancel'
    case 'loop_detected': return 'loop'
    case 'user_interrupt': return 'interrupt'
    default: return 'pre_turn_end'
  }
}

export function syntheticKindOf(tool: WebToolProgress): string {
  return parseSyntheticHints(tool.toolHints)?.kind || inferSyntheticKind(tool.name || '')
}

// ── look & feel ───────────────────────────────────────────────────────

interface Looks {
  Icon: typeof Terminal
  /** Card border + tinted surface. */
  card: string
  /** Round avatar behind the kind icon. */
  avatar: string
  icon: string
}

const LOOKS: Record<string, Looks> = {
  bg_task: {
    Icon: Terminal,
    card: 'border-sky-400/50 bg-sky-500/[0.05] dark:border-sky-500/40 dark:bg-sky-500/[0.08]',
    avatar: 'bg-sky-500/15 text-sky-600 dark:text-sky-400',
    icon: 'text-sky-600 dark:text-sky-400',
  },
  subagent: {
    Icon: Bot,
    card: 'border-violet-400/50 bg-violet-500/[0.05] dark:border-violet-500/40 dark:bg-violet-500/[0.08]',
    avatar: 'bg-violet-500/15 text-violet-600 dark:text-violet-400',
    icon: 'text-violet-600 dark:text-violet-400',
  },
  cron: {
    Icon: Clock,
    card: 'border-amber-400/50 bg-amber-500/[0.05] dark:border-amber-500/40 dark:bg-amber-500/[0.08]',
    avatar: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
    icon: 'text-amber-600 dark:text-amber-400',
  },
  async: {
    Icon: Mail,
    card: 'border-teal-400/50 bg-teal-500/[0.05] dark:border-teal-500/40 dark:bg-teal-500/[0.08]',
    avatar: 'bg-teal-500/15 text-teal-600 dark:text-teal-400',
    icon: 'text-teal-600 dark:text-teal-400',
  },
  delivered: {
    Icon: Send,
    card: 'border-teal-400/50 bg-teal-500/[0.05] dark:border-teal-500/40 dark:bg-teal-500/[0.08]',
    avatar: 'bg-teal-500/15 text-teal-600 dark:text-teal-400',
    icon: 'text-teal-600 dark:text-teal-400',
  },
  cancel: {
    Icon: Ban,
    card: 'border-border bg-bg-tertiary/40',
    avatar: 'bg-bg-hover text-text-secondary',
    icon: 'text-text-secondary',
  },
  loop: {
    Icon: RotateCcw,
    card: 'border-amber-400/50 bg-amber-500/[0.05] dark:border-amber-500/40 dark:bg-amber-500/[0.08]',
    avatar: 'bg-amber-500/15 text-amber-600 dark:text-amber-400',
    icon: 'text-amber-600 dark:text-amber-400',
  },
  pre_turn_end: {
    Icon: CornerUpLeft,
    card: 'border-border bg-bg-tertiary/40',
    avatar: 'bg-bg-hover text-text-secondary',
    icon: 'text-text-secondary',
  },
  interrupt: {
    Icon: Zap,
    card: 'border-violet-400/60 bg-violet-500/[0.07] dark:border-violet-500/50 dark:bg-violet-500/[0.1]',
    avatar: 'bg-violet-500/15 text-violet-600 dark:text-violet-400',
    icon: 'text-violet-600 dark:text-violet-400',
  },
}

const TITLE_KEYS: Record<string, string> = {
  bg_task: 'agent.tool.syntheticTitleBgTask',
  subagent: 'agent.tool.syntheticTitleSubAgent',
  cron: 'agent.tool.syntheticTitleCron',
  async: 'agent.tool.syntheticTitleAsync',
  delivered: 'agent.tool.syntheticTitleDelivered',
  cancel: 'agent.tool.syntheticTitleCancel',
  loop: 'agent.tool.syntheticTitleLoop',
  pre_turn_end: 'agent.tool.syntheticTitlePreTurnEnd',
  interrupt: 'agent.tool.syntheticTitleInterrupt',
}

/**
 * Continuity line — the sentence that tells the user WHY this card is here and
 * that it refers to something started EARLIER ("this command had been moved to
 * the background and has now finished"). Without it a card reads like a tool the
 * agent just called out of nowhere; with it the "background → finished" relation
 * is impossible to miss.
 */
const CONTINUITY_KEYS: Record<string, string> = {
  bg_task: 'agent.tool.syntheticContinuityBgTask',
  subagent: 'agent.tool.syntheticContinuitySubAgent',
  cron: 'agent.tool.syntheticContinuityCron',
  async: 'agent.tool.syntheticContinuityAsync',
  delivered: 'agent.tool.syntheticContinuityDelivered',
  cancel: 'agent.tool.syntheticContinuityCancel',
  loop: 'agent.tool.syntheticContinuityLoop',
  pre_turn_end: 'agent.tool.syntheticContinuityPreTurnEnd',
  interrupt: 'agent.tool.syntheticContinuityInterrupt',
}

const SHORT_KEYS: Record<string, string> = {
  bg_task: 'agent.tool.syntheticShortBgTask',
  subagent: 'agent.tool.syntheticShortSubAgent',
  cron: 'agent.tool.syntheticShortCron',
  async: 'agent.tool.syntheticShortAsync',
  delivered: 'agent.tool.syntheticShortDelivered',
  cancel: 'agent.tool.syntheticShortCancel',
  loop: 'agent.tool.syntheticShortLoop',
  pre_turn_end: 'agent.tool.syntheticShortPreTurnEnd',
  interrupt: 'agent.tool.syntheticShortInterrupt',
}

/** English fallbacks so a pill / header never shows a raw snake_case tool name
 *  even when no i18n function is at hand. */
const SHORT_FALLBACK: Record<string, string> = {
  bg_task: 'Background task',
  subagent: 'Sub-agent',
  cron: 'Scheduled job',
  async: 'Async message',
  delivered: 'Delivery',
  cancel: 'Cancelled',
  loop: 'Loop blocked',
  pre_turn_end: 'Turn reminder',
  interrupt: 'Interjection',
}

/**
 * Localized short name for a synthetic tool — used by pills and the popover
 * header so users never see the internal name (`bg_subagent_completed`).
 * Returns null for real tools (callers keep their own naming).
 */
export function syntheticShortName(
  tool: WebToolProgress,
  t?: (key: string, params?: Record<string, string | number>) => string,
): string | null {
  const name = tool.name || ''
  if (!isSyntheticToolName(name)) return null
  const kind = syntheticKindOf(tool)
  const key = SHORT_KEYS[kind] || SHORT_KEYS.pre_turn_end
  return t ? t(key) : SHORT_FALLBACK[kind] || SHORT_FALLBACK.pre_turn_end
}

/**
 * Subject of a synthetic tool — the thing it acted on, shown next to the name so
 * the row reads like `Sub-agent · explore/mem-1` (mirrors `Shell: cmd`).
 * Prefers the structured payload, then the tool label (`bgsub:explore/mem-1`).
 *
 * Returns "" when there is nothing meaningful to show — notably for
 * `user_interrupt`, whose label IS the display name ("💬 插话"): rendering it as
 * a subject duplicated the pill's text (`插话` + `💬 插话`).
 */
export function syntheticSubject(tool: WebToolProgress): string {
  const hints = parseSyntheticHints(tool.toolHints)
  if (hints?.role) return `${hints.role}${hints.instance ? '/' + hints.instance : ''}`
  if (hints?.task_id) return hints.task_id
  if (syntheticKindOf(tool) === 'interrupt') return ''
  const label = (tool.label || '').trim()
  const idx = label.indexOf(':')
  const rest = (idx >= 0 ? label.slice(idx + 1) : label).trim()
  // Drop leading emoji/symbols — a label like "💬 插话" must not become a subject.
  const cleaned = rest.replace(/^[^\p{L}\p{N}/_.:-]+/u, '').trim()
  if (!cleaned || cleaned === '{}' || isSyntheticToolName(cleaned)) return ''
  return cleaned
}

// ── formatting helpers ────────────────────────────────────────────────

export function formatDuration(ms: number): string {
  if (!ms || ms <= 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`
  const m = Math.floor(s / 60)
  const rest = Math.round(s - m * 60)
  return `${m}m ${rest}s`
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// ── atoms ─────────────────────────────────────────────────────────────

function Chip({ children, tone = 'muted' }: { children: ReactNode; tone?: 'green' | 'red' | 'muted' | 'accent' }) {
  const cls =
    tone === 'green'
      ? 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-400'
      : tone === 'red'
        ? 'border-red-500/40 bg-red-500/10 text-red-700 dark:text-red-400'
        : tone === 'accent'
          ? 'border-transparent bg-accent/15 text-accent'
          : 'border-border bg-bg-tertiary/50 text-text-muted'
  return (
    <span className={`inline-flex shrink-0 items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px] font-medium leading-none ${cls}`}>
      {children}
    </span>
  )
}

/** A labelled block: tiny section caption + content. */
function Field({
  label,
  extra,
  children,
}: {
  label: string
  extra?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center gap-2">
        <span className="text-[9px] font-semibold uppercase tracking-wider text-text-muted">{label}</span>
        {extra}
      </div>
      {children}
    </div>
  )
}

/**
 * Mono code block with a copy affordance — the original command / task.
 *
 * Collapsed by default (3 lines + 展开): the command is CONTEXT, the output is the
 * point. A long one-liner command (e.g. a giant ssh invocation) used to push the
 * whole card apart while the useful output sat clipped below it.
 */
function CodeBlock({
  text,
  copyLabel,
  copiedLabel,
  moreLabel,
  lessLabel,
}: {
  text: string
  copyLabel: string
  copiedLabel: string
  moreLabel: string
  lessLabel: string
}) {
  const [copied, setCopied] = useState(false)
  const [open, setOpen] = useState(false)
  const collapsible = text.split('\n').length > 3 || text.length > 220
  return (
    <div className="group/code relative rounded-lg border border-border/60 bg-bg-tertiary/40">
      <pre
        data-testid="synthetic-command"
        className={`overflow-x-auto whitespace-pre-wrap break-words px-2 py-1.5 pr-8 font-mono text-[11px] leading-5 text-text-secondary ${
          collapsible && !open ? 'max-h-[3.75rem] overflow-y-hidden' : 'max-h-[420px] overflow-y-auto'
        }`}
      >
        {text}
      </pre>
      <button
        type="button"
        title={copyLabel}
        aria-label={copyLabel}
        onClick={() => {
          void navigator.clipboard?.writeText(text).then(() => {
            setCopied(true)
            window.setTimeout(() => setCopied(false), 1200)
          })
        }}
        className="absolute right-1 top-1 rounded-md border border-border/60 bg-bg-secondary/80 p-1 text-text-muted opacity-0 transition-opacity hover:text-text-primary focus:opacity-100 group-hover/code:opacity-100"
      >
        {copied ? <Check size={11} /> : <Copy size={11} />}
        <span className="sr-only">{copied ? copiedLabel : copyLabel}</span>
      </button>
      {collapsible && (
        <button
          type="button"
          data-testid="synthetic-command-toggle"
          onClick={() => setOpen((v) => !v)}
          className="flex w-full items-center justify-center gap-0.5 rounded-b-lg border-t border-border/40 bg-bg-tertiary/30 py-0.5 text-[10px] text-accent hover:underline"
        >
          <ChevronDown size={10} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
          {open ? lessLabel : moreLabel}
        </button>
      )}
    </div>
  )
}

// ── the cards ─────────────────────────────────────────────────────────

/** Fancy card for an injected notification tool (bg task / sub-agent / cron /
 *  async / delivery / cancel / loop / pre-turn reminder). */
export const SyntheticToolCard = memo(function SyntheticToolCard({ tool }: { tool: WebToolProgress }) {
  const { t } = useI18n()
  const hints = useMemo(() => parseSyntheticHints(tool.toolHints), [tool.toolHints])
  const [open, setOpen] = useState(false)

  const kind = hints?.kind || inferSyntheticKind(tool.name || '')
  const look = LOOKS[kind] || LOOKS.bg_task
  const KindIcon = look.Icon
  const title = t(TITLE_KEYS[kind] || TITLE_KEYS.pre_turn_end)

  const rawStatus = (hints?.status || '').toLowerCase()
  const status = rawStatus || (kind === 'cancel' ? 'cancelled' : '')
  const failed = status === 'error' || status === 'killed'
  const tone: 'green' | 'red' | 'muted' = status === 'done' ? 'green' : failed ? 'red' : 'muted'
  const statusKey =
    status === 'done' ? 'agent.tool.syntheticStatusDone'
    : status === 'error' ? 'agent.tool.syntheticStatusError'
    : status === 'killed' ? 'agent.tool.syntheticStatusKilled'
    : status === 'cancelled' ? 'agent.tool.syntheticStatusCancelled'
    : status === 'running' ? 'agent.tool.syntheticStatusRunning'
    : ''

  const subject = hints?.role
    ? `${hints.role}${hints.instance ? '/' + hints.instance : ''}`
    : hints?.task_id || ''

  const task = hints?.task || ''
  const taskLabel = kind === 'bg_task' ? t('agent.tool.syntheticCommand') : t('agent.tool.syntheticOriginalTask')
  // Prefer the structured output; fall back to detail/summary so legacy rows
  // still say something (the previous card showed "（无输出）" and nothing else).
  const body = hints?.output || hints?.message || tool.detail || tool.summary || ''
  // 输出是重点：只有当它确实很长时才折叠（旧阈值 600 字符 + max-h-24 太激进，
  // 结果「命令」铺满整卡、真正有用的输出反而被折叠成一个 96px 的小窗口）。
  const clipped = body.length > 1500
  const lines = body ? body.split('\n').length : 0

  const elapsed = formatDuration(hints?.elapsed_ms || tool.elapsedMs || 0)
  const meta: ReactNode[] = []
  if (hints?.task_id) {
    meta.push(
      <span key="id" className="inline-flex items-center gap-1">
        <span className="text-text-muted/70">{t('agent.tool.syntheticTaskID')}</span>
        <code className="font-mono text-text-secondary">{hints.task_id}</code>
      </span>,
    )
  }
  if (typeof tool.iteration === 'number' && tool.iteration > 0) {
    meta.push(
      <span key="iter" className="inline-flex items-center gap-1">
        <span className="text-text-muted/70">{t('agent.tool.syntheticIteration')}</span>
        <span className="tabular-nums text-text-secondary">{tool.iteration}</span>
      </span>,
    )
  }

  return (
    <div
      data-testid="synthetic-tool-card"
      className={`overflow-hidden rounded-xl border shadow-sm ${look.card}`}
    >
      {/* header: 完成徽标 + avatar + 标题（独占一行，永不截断）+ chips（第二行自动换行） */}
      <div className="flex items-start gap-2.5 px-3 py-2">
        <span className={`relative mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full ${look.avatar}`}>
          <KindIcon size={13} aria-hidden="true" />
          {(status === 'done' || kind === 'cancel') && (
            <span
              data-testid="synthetic-done-badge"
              className="absolute -bottom-0.5 -right-0.5 flex size-3 items-center justify-center rounded-full bg-emerald-500 text-white ring-2 ring-bg-secondary"
            >
              <Check size={7} strokeWidth={4} />
            </span>
          )}
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-[12.5px] font-semibold leading-snug text-text-primary">{title}</span>
          <div className="flex flex-wrap items-center gap-1.5">
            {subject && (
              <code className="shrink-0 rounded bg-bg-tertiary/60 px-1.5 py-0.5 font-mono text-[10px] text-text-secondary">
                {subject}
              </code>
            )}
            {statusKey && <Chip tone={tone}>{t(statusKey)}</Chip>}
            {typeof hints?.exit_code === 'number' && (
              <Chip tone={hints.exit_code === 0 ? 'green' : 'red'}>
                {t('agent.tool.syntheticExitCode')} {hints.exit_code}
              </Chip>
            )}
            {elapsed && <Chip>{elapsed}</Chip>}
          </div>
        </div>
      </div>

      {/* 承接说明：明确这是"之前就在跑的东西，现在结束了"（而不是凭空出现的工具调用） */}
      <div className="border-t border-border/40 px-3 py-1.5 text-[10.5px] leading-relaxed text-text-muted">
        {t(CONTINUITY_KEYS[kind] || CONTINUITY_KEYS.pre_turn_end)}
      </div>

      {/* body: original task / output / error + meta footer */}
      <div className="flex flex-col gap-2 border-t border-border/40 px-3 py-2">
        {task && (
          <Field label={taskLabel}>
            <CodeBlock
              text={task}
              copyLabel={t('agent.tool.copy')}
              copiedLabel={t('agent.tool.copied')}
              moreLabel={t('agent.tool.syntheticShowMore')}
              lessLabel={t('agent.tool.syntheticShowLess')}
            />
          </Field>
        )}

        {body ? (
          <Field
            label={t('agent.tool.syntheticOutput')}
            extra={<span className="text-[9px] text-text-muted">{t('agent.tool.syntheticOutputStats', { lines, size: formatBytes(body.length) })}</span>}
          >
            {/* 统一渲染：注入型工具的 result 是给模型看的 markdown（模型回复/通知正文），
                直接用 MarkdownRenderer 渲染 —— 不再 dump 成纯文本/等宽块。
                例外：bg_task 的 stdout 是命令日志（不是 markdown），保持终端样式。 */}
            {kind === 'bg_task' ? (
              <pre
                className={`overflow-x-hidden whitespace-pre-wrap break-words rounded-lg border border-border/60 bg-bg-tertiary/40 px-2 py-1.5 font-mono text-[11px] leading-5 text-text-secondary ${
                  clipped && !open ? 'max-h-24 overflow-y-hidden' : 'max-h-[420px] overflow-y-auto'
                }`}
              >
                {body}
              </pre>
            ) : (
              <div
                className={`rounded-lg border border-border/60 bg-bg-tertiary/25 px-2.5 py-2 text-[12.5px] leading-relaxed text-text-primary ${
                  clipped && !open ? 'max-h-24 overflow-hidden' : 'max-h-[420px] overflow-y-auto'
                }`}
              >
                <MarkdownRenderer content={body} noDebounce />
              </div>
            )}
            {clipped && (
              <button
                type="button"
                data-testid="synthetic-output-toggle"
                onClick={() => setOpen((v) => !v)}
                className="inline-flex w-fit items-center gap-0.5 text-[10px] text-accent hover:underline"
              >
                <ChevronDown size={10} className={open ? 'rotate-180 transition-transform' : 'transition-transform'} />
                {open ? t('agent.tool.syntheticShowLess') : t('agent.tool.syntheticShowMore')}
              </button>
            )}
          </Field>
        ) : (
          <div className="text-[10px] text-text-muted">{t('agent.tool.syntheticNoDetails')}</div>
        )}

        {hints?.error && (
          <Field label={t('agent.tool.syntheticErrorSection')}>
            <div className="whitespace-pre-wrap break-words rounded-lg border border-red-500/30 bg-red-500/[0.06] px-2 py-1.5 font-mono text-[11px] leading-5 text-red-700 dark:text-red-400">
              {hints.error}
            </div>
          </Field>
        )}

        {meta.length > 0 && (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-border/40 pt-1.5 text-[10px]">
            {meta}
          </div>
        )}
      </div>
    </div>
  )
})

/**
 * Fancy card for `user_interrupt` — the Web ⚡ "say something to the agent right
 * now without stopping it" interjection. The body is user prose, so it is
 * rendered as readable text (not a mono dump) with an obvious "live" accent.
 */
export const InterruptCard = memo(function InterruptCard({ tool }: { tool: WebToolProgress }) {
  const { t } = useI18n()
  const hints = useMemo(() => parseSyntheticHints(tool.toolHints), [tool.toolHints])
  const look = LOOKS.interrupt
  const text = hints?.message || hints?.output || tool.summary || tool.detail || tool.args || ''
  return (
    <div
      data-testid="interrupt-card"
      className={`overflow-hidden rounded-xl border shadow-sm ${look.card}`}
    >
      <div className="flex items-center gap-2 px-3 py-2">
        <span className={`flex size-6 shrink-0 items-center justify-center rounded-full ${look.avatar}`}>
          <Zap size={13} aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-semibold text-text-primary">
          {t('agent.tool.syntheticTitleInterrupt')}
        </span>
        <Chip tone="accent">{t('agent.tool.syntheticShortInterrupt')}</Chip>
        {typeof tool.iteration === 'number' && tool.iteration > 0 && (
          <Chip>
            {t('agent.tool.syntheticIteration')} {tool.iteration}
          </Chip>
        )}
      </div>
      <div className="border-t border-border/40 px-3 py-2">
        {/* 插话正文是用户/通知的 markdown 文本 → 直接渲染 markdown（统一字段：
            hints.message（干净原文）> hints.output > tool.detail > summary）。 */}
        {t('agent.tool.syntheticContinuityInterrupt') && (
          <div className="mb-1.5 text-[10.5px] leading-relaxed text-text-muted">
            {t('agent.tool.syntheticContinuityInterrupt')}
          </div>
        )}
        {text ? (
          <div className="text-[12.5px] leading-relaxed text-text-primary">
            <MarkdownRenderer content={text} noDebounce />
          </div>
        ) : (
          <p className="text-[12px] text-text-muted">{t('agent.tool.syntheticNoDetails')}</p>
        )}
      </div>
    </div>
  )
})
