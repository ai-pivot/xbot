/**
 * Declarative web UI components (web_ui protocol).
 *
 * Each component type maps a JSON `props` object to a React view. All props
 * are validated defensively (numeric/string coercion) because they come from
 * plugin-authored JSON. Interactive components call `onAction(action, data)`
 * which the parent wires to the web_ui_action RPC.
 */
import { useMemo } from 'react'

export interface ComponentAction {
  (action: string, data?: unknown): void
}

/** Tone → Tailwind color classes shared by badge/progress/metric/list. */
const TONES: Record<string, { text: string; bg: string; border: string; bar: string }> = {
  success: { text: 'text-green-600', bg: 'bg-green-50', border: 'border-green-200', bar: 'bg-green-500' },
  warning: { text: 'text-amber-600', bg: 'bg-amber-50', border: 'border-amber-200', bar: 'bg-amber-500' },
  error: { text: 'text-red-600', bg: 'bg-red-50', border: 'border-red-200', bar: 'bg-red-500' },
  info: { text: 'text-blue-600', bg: 'bg-blue-50', border: 'border-blue-200', bar: 'bg-blue-500' },
  accent: { text: 'text-indigo-600', bg: 'bg-indigo-50', border: 'border-indigo-200', bar: 'bg-indigo-500' },
  muted: { text: 'text-slate-500', bg: 'bg-slate-50', border: 'border-slate-200', bar: 'bg-slate-400' },
  normal: { text: 'text-slate-700', bg: 'bg-slate-50', border: 'border-slate-200', bar: 'bg-slate-400' },
}

function tone(name: unknown): string {
  return typeof name === 'string' ? name : 'normal'
}
function num(v: unknown, dflt = 0): number {
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : dflt
}
function str(v: unknown, dflt = ''): string {
  return typeof v === 'string' ? v : dflt
}

export function BadgeWidget({ props, action }: { props: Record<string, unknown>; action?: ComponentAction }) {
  const t = TONES[tone(props.tone)] ?? TONES.normal
  const text = str(props.text, '')
  const pulse = Boolean(props.pulse)
  const btnProps =
    action && typeof props.action === 'string'
      ? { onClick: () => action(str(props.action), props.data), style: { cursor: 'pointer' } as const }
      : {}
  return (
    <span className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium ${t.text} ${t.bg} ${t.border}`} {...btnProps}>
      {pulse && <span className={`h-1.5 w-1.5 animate-pulse rounded-full ${t.bar}`} />}
      {text}
    </span>
  )
}

export function ProgressWidget({ props }: { props: Record<string, unknown> }) {
  const value = Math.max(0, Math.min(100, num(props.value)))
  const max = Math.max(1, num(props.max, 100))
  const pct = Math.max(0, Math.min(100, (value / max) * 100))
  const t = TONES[tone(props.tone)] ?? TONES.info
  const label = str(props.label)
  return (
    <div className="w-full">
      {label && <div className="mb-1 text-xs text-slate-600">{label}</div>}
      <div className="h-2 w-full overflow-hidden rounded-full bg-slate-100">
        <div className={`h-full rounded-full transition-all ${t.bar}`} style={{ width: `${pct}%` }} />
      </div>
      {typeof props.show_value === 'undefined' || props.show_value ? (
        <div className="mt-0.5 text-right text-[10px] tabular-nums text-slate-400">{Math.round(pct)}%</div>
      ) : null}
    </div>
  )
}

export function MetricWidget({ props, action }: { props: Record<string, unknown>; action?: ComponentAction }) {
  const t = TONES[tone(props.tone)] ?? TONES.normal
  const label = str(props.label)
  const value = str(props.value)
  const delta = str(props.delta)
  const icon = str(props.icon)
  const deltaPositive = typeof props.delta === 'string' && !props.delta.startsWith('-')
  const btnProps =
    action && typeof props.action === 'string'
      ? { onClick: () => action(str(props.action), props.data), style: { cursor: 'pointer' } as const }
      : {}
  return (
    <div className={`flex items-center gap-2 rounded-lg border px-2.5 py-1.5 ${t.bg} ${t.border}`} {...btnProps}>
      {icon && <span className="text-base leading-none">{icon}</span>}
      <div className="min-w-0">
        {label && <div className="truncate text-[10px] uppercase tracking-wide text-slate-500">{label}</div>}
        <div className="flex items-baseline gap-1.5">
          <span className={`text-base font-semibold leading-tight ${t.text}`}>{value}</span>
          {delta && (
            <span className={`text-[10px] ${deltaPositive ? 'text-green-600' : 'text-red-600'}`}>{delta}</span>
          )}
        </div>
      </div>
    </div>
  )
}

export function SparklineWidget({ props }: { props: Record<string, unknown> }) {
  const data = useMemo(() => {
    const raw = Array.isArray(props.data) ? props.data : []
    return raw.map((v) => num(v, 0))
  }, [props.data])
  const color = str(props.color, '#6366f1')
  const height = Math.max(16, Math.min(200, num(props.height, 48)))
  const bar = props.type === 'bar'
  if (data.length === 0) return <div className="text-xs text-slate-400">no data</div>
  const max = Math.max(...data, 1)
  const w = 100
  const pts = data.map((v, i) => `${(i / (data.length - 1)) * w},${height - 4 - (v / max) * (height - 10)}`)
  return (
    <svg viewBox={`0 0 ${w} ${height}`} className="h-full w-full" preserveAspectRatio="none">
      {bar ? (
        data.map((v, i) => {
          const bw = Math.max(1, w / data.length - 1)
          return (
            <rect
              key={i}
              x={(i / data.length) * w + 0.5}
              y={height - 3 - (v / max) * (height - 8)}
              width={bw}
              height={(v / max) * (height - 8)}
              fill={color}
              opacity={0.75}
            />
          )
        })
      ) : (
        <>
          <polygon points={`0,${height - 2} ${pts.join(' ')} ${w},${height - 2}`} fill={color} opacity={0.1} />
          <polyline points={pts.join(' ')} fill="none" stroke={color} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
        </>
      )}
    </svg>
  )
}

export function TableWidget({ props, action }: { props: Record<string, unknown>; action?: ComponentAction }) {
  const columns = Array.isArray(props.columns) ? props.columns.map((c) => str(c)) : []
  const rows = Array.isArray(props.rows) ? (props.rows as Record<string, unknown>[]) : []
  const maxHeight = Math.max(60, num(props.max_height, 260))
  if (columns.length === 0) return null
  return (
    <div className="overflow-auto rounded-lg border border-slate-200" style={{ maxHeight }}>
      <table className="w-full text-left text-xs">
        <thead className="sticky top-0 bg-slate-50 text-slate-500">
          <tr>
            {columns.map((c) => (
              <th key={c} className="px-2.5 py-1.5 font-medium">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {rows.map((row, ri) => (
            <tr key={ri} className={ri % 2 ? 'bg-slate-50/50' : 'bg-white'}>
              {columns.map((c) => {
                const cell = row[c]
                const onClick =
                  action && typeof cell === 'object' && cell !== null && typeof (cell as { action?: string }).action === 'string'
                    ? () => action(str((cell as { action: string }).action), row)
                    : undefined
                return (
                  <td key={c} className="px-2.5 py-1.5 text-slate-700" onClick={onClick} style={onClick ? { cursor: 'pointer' } : undefined}>
                    {renderCell(cell)}
                  </td>
                )
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function renderCell(cell: unknown): React.ReactNode {
  if (typeof cell === 'object' && cell !== null) {
    const o = cell as { text?: string; tone?: string }
    const t = TONES[tone(o.tone)] ?? TONES.normal
    return (
      <span className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 ${t.text} ${t.bg}`}>
        {o.text ?? ''}
      </span>
    )
  }
  return <>{String(cell ?? '')}</>
}

export function ListWidget({ props, action }: { props: Record<string, unknown>; action?: ComponentAction }) {
  const title = str(props.title)
  const items = Array.isArray(props.items) ? (props.items as Record<string, unknown>[]) : []
  return (
    <div>
      {title && <div className="mb-1.5 text-xs font-semibold text-slate-600">{title}</div>}
      <ul className="space-y-0.5">
        {items.map((item, i) => {
          const key = str(item.key, `item-${i}`)
          const val = str(item.value)
          const t = TONES[tone(item.tone)] ?? TONES.normal
          const onClick =
            action && typeof item.action === 'string' ? () => action(str(item.action), item) : undefined
          return (
            <li
              key={i}
              className={`flex items-center justify-between gap-2 rounded px-1.5 py-1 text-xs ${onClick ? 'cursor-pointer hover:bg-slate-50' : ''}`}
              onClick={onClick}
            >
              <span className="truncate text-slate-600">{key}</span>
              <span className={`shrink-0 font-medium ${t.text}`}>{val}</span>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

export function MarkdownWidget({ props }: { props: Record<string, unknown> }) {
  const content = str(props.content)
  if (!content) return null
  // Simple inline renderer — full GFM is handled by SandboxedUI / GenUI path.
  return (
    <div className="prose prose-sm max-w-none text-slate-700">
      {content.split('\n').map((line, i) => (
        <p key={i} className="my-1">
          {line || '\u00a0'}
        </p>
      ))}
    </div>
  )
}

/** Dispatch a declarative component by type. */
export function renderDeclarativeComponent(
  type: string,
  props: Record<string, unknown>,
  action?: ComponentAction,
): React.ReactNode {
  switch (type) {
    case 'badge':
      return <BadgeWidget props={props} action={action} />
    case 'progress':
      return <ProgressWidget props={props} />
    case 'metric':
      return <MetricWidget props={props} action={action} />
    case 'sparkline':
      return <SparklineWidget props={props} />
    case 'table':
      return <TableWidget props={props} action={action} />
    case 'list':
      return <ListWidget props={props} action={action} />
    case 'markdown':
      return <MarkdownWidget props={props} />
    default:
      // Unknown declarative type — degrade to a text badge.
      return <BadgeWidget props={{ text: props.text ?? JSON.stringify(props).slice(0, 120) }} />
  }
}
