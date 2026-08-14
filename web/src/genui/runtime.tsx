/**
 * XBOT_UI — the fancy GenUI runtime.
 *
 * A global component library + hooks injected into the compile scope of
 * LLM-generated TSX (GenUIBlock / SandboxedUI). It gives the LLM:
 *   - rich UI primitives (Button/Card/Table/Stat/Sparkline/Progress/Badge/
 *     Tabs/Modal/Form/Toast)
 *   - declarative ECharts (Chart option={...})
 *   - 3D scenes (useThreeScene)
 *   - motion animation (framer-motion, already a dependency)
 *   - component-to-component events (useBus)
 *
 * The runtime is metadata-driven: any tool declaring ui.mode="genui" gets it.
 * See docs/agent/genui-plugin-design.md §9.
 *
 * IMPORTANT: these components render inside an opaque-origin iframe, but the
 * compiled component FUNCTIONS execute in the parent page's compile scope (the
 * `new Function` wrapper). The runtime object is injected as an argument — the
 * component can only reach what we hand it (React + XBOT_UI). No DOM access to
 * the parent page from inside the iframe.
 */
import React, {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { motion } from 'framer-motion'

// ─── Theme ────────────────────────────────────────────────────
// Detected from the parent page (<html class="dark"> + Tailwind dark: variant).
export interface GenUITheme {
  dark: boolean
}

export const GenUIThemeContext = createContext<GenUITheme>({ dark: false })

export function useGenUITheme(): GenUITheme {
  return useContext(GenUIThemeContext)
}

// Detect dark mode from the parent document (runs in parent page, safe).
export function detectDarkMode(): boolean {
  if (typeof document === 'undefined') return false
  return document.documentElement.classList.contains('dark')
}

// ─── Icons (lucide-react subset) ───────────────────────────────
export { Icon } from './icons'

// ─── Typography / Layout helpers ───────────────────────────────

function cn(...parts: (string | false | undefined)[]): string {
  return parts.filter(Boolean).join(' ')
}

// ─── Button ────────────────────────────────────────────────────
export function Button({
  variant = 'primary',
  size = 'md',
  className,
  children,
  ...rest
}: {
  variant?: 'primary' | 'ghost' | 'outline' | 'danger' | 'success'
  size?: 'sm' | 'md' | 'lg'
  className?: string
  children?: ReactNode
  [key: string]: unknown
}) {
  const base =
    'inline-flex items-center justify-center gap-1.5 rounded-lg font-medium transition-all active:scale-[0.98] cursor-pointer select-none'
  const sizes = {
    sm: 'px-2.5 py-1 text-xs',
    md: 'px-3.5 py-1.5 text-sm',
    lg: 'px-5 py-2.5 text-base',
  }
  const variants = {
    primary: 'bg-indigo-600 text-white shadow-sm hover:bg-indigo-500 dark:bg-indigo-500 dark:hover:bg-indigo-400',
    ghost: 'bg-transparent text-gray-700 hover:bg-gray-100 dark:text-slate-200 dark:hover:bg-slate-800',
    outline: 'border border-gray-300 text-gray-700 hover:bg-gray-50 dark:border-slate-600 dark:text-slate-200 dark:hover:bg-slate-800',
    danger: 'bg-rose-600 text-white shadow-sm hover:bg-rose-500',
    success: 'bg-emerald-600 text-white shadow-sm hover:bg-emerald-500',
  }
  return (
    <button
      className={cn(base, sizes[size], variants[variant], className)}
      {...(rest as React.ButtonHTMLAttributes<HTMLButtonElement>)}
    >
      {children}
    </button>
  )
}

// ─── Card ──────────────────────────────────────────────────────
export function Card({
  title,
  subtitle,
  actions,
  className,
  children,
}: {
  title?: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  className?: string
  children?: ReactNode
}) {
  return (
    <div className={cn('rounded-xl border border-gray-200 bg-white shadow-sm dark:border-slate-700 dark:bg-slate-900', className)}>
      {(title || actions) && (
        <div className="flex items-center justify-between gap-2 border-b border-gray-100 px-4 py-2.5 dark:border-slate-800">
          <div className="min-w-0">
            {title && <div className="text-sm font-semibold text-gray-900 dark:text-slate-100">{title}</div>}
            {subtitle && <div className="text-xs text-gray-500 dark:text-slate-400">{subtitle}</div>}
          </div>
          {actions && <div className="shrink-0">{actions}</div>}
        </div>
      )}
      <div className="p-4">{children}</div>
    </div>
  )
}

// ─── Stat ──────────────────────────────────────────────────────
export function Stat({
  label,
  value,
  delta,
  trend = 'up',
  icon,
}: {
  label: string
  value: ReactNode
  delta?: number
  trend?: 'up' | 'down' | 'flat'
  icon?: ReactNode
}) {
  const deltaColor =
    trend === 'up' ? 'text-emerald-600 dark:text-emerald-400' : trend === 'down' ? 'text-rose-600 dark:text-rose-400' : 'text-gray-500'
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-slate-700 dark:bg-slate-900">
      <div className="flex items-center justify-between">
        <div className="text-xs font-medium uppercase tracking-wide text-gray-500 dark:text-slate-400">{label}</div>
        {icon && <div className="text-gray-400 dark:text-slate-500">{icon}</div>}
      </div>
      <div className="mt-1 text-2xl font-bold tabular-nums text-gray-900 dark:text-slate-100">{value}</div>
      {typeof delta === 'number' && (
        <div className={cn('mt-1 text-xs font-medium', deltaColor)}>
          {trend === 'up' ? '▲' : trend === 'down' ? '▼' : '■'} {Math.abs(delta * 100).toFixed(1)}%
        </div>
      )}
    </div>
  )
}

// ─── Sparkline (pure SVG, no deps) ─────────────────────────────
export function Sparkline({ data, color = '#6366f1', height = 40 }: { data: number[]; color?: string; height?: number }) {
  if (!data || data.length < 2) return <div style={{ height }} />
  const w = 120
  const min = Math.min(...data)
  const max = Math.max(...data)
  const range = max - min || 1
  const pts = data.map((v, i) => `${(i / (data.length - 1)) * w},${height - 3 - ((v - min) / range) * (height - 6)}`)
  const area = `0,${height} ${pts.join(' ')} ${w},${height}`
  return (
    <svg viewBox={`0 0 ${w} ${height}`} width="100%" height={height} preserveAspectRatio="none">
      <polygon points={area} fill={color} opacity={0.15} />
      <polyline points={pts.join(' ')} fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

// ─── Progress ──────────────────────────────────────────────────
export function Progress({ value, label, color = 'bg-indigo-500' }: { value: number; label?: string; color?: string }) {
  const pct = Math.max(0, Math.min(100, Math.round(value * 100)))
  return (
    <div className="w-full">
      {label && (
        <div className="mb-1 flex items-center justify-between text-xs">
          <span className="text-gray-600 dark:text-slate-300">{label}</span>
          <span className="font-medium tabular-nums text-gray-500 dark:text-slate-400">{pct}%</span>
        </div>
      )}
      <div className="h-2 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-slate-700">
        <div className={cn('h-full rounded-full transition-all duration-500', color)} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

// ─── Badge ─────────────────────────────────────────────────────
export function Badge({ text, color = 'gray', dot }: { text: ReactNode; color?: string; dot?: boolean }) {
  const colors: Record<string, string> = {
    gray: 'bg-gray-100 text-gray-700 dark:bg-slate-800 dark:text-slate-300',
    green: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300',
    red: 'bg-rose-100 text-rose-700 dark:bg-rose-900/40 dark:text-rose-300',
    blue: 'bg-sky-100 text-sky-700 dark:bg-sky-900/40 dark:text-sky-300',
    amber: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
    indigo: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300',
  }
  const dotColor = { gray: 'bg-gray-400', green: 'bg-emerald-500', red: 'bg-rose-500', blue: 'bg-sky-500', amber: 'bg-amber-500', indigo: 'bg-indigo-500' }
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium', colors[color] ?? colors.gray)}>
      {dot && <span className={cn('h-1.5 w-1.5 rounded-full', (dotColor as Record<string, string>)[color] ?? dotColor.gray)} />}
      {text}
    </span>
  )
}

// ─── Table ─────────────────────────────────────────────────────
export interface TableColumn {
  key: string
  label: ReactNode
  align?: 'left' | 'right' | 'center'
  render?: (row: Record<string, unknown>, index: number) => ReactNode
}
export function Table({ data, columns, maxHeight }: { data: Array<Record<string, unknown>>; columns: TableColumn[]; maxHeight?: number }) {
  const alignCls = { left: 'text-left', right: 'text-right', center: 'text-center' }
  return (
    <div className="overflow-x-auto rounded-lg border border-gray-200 dark:border-slate-700" style={maxHeight ? { maxHeight } : undefined}>
      <table className="w-full text-sm">
        <thead>
          <tr className="border-b border-gray-200 bg-gray-50 dark:border-slate-700 dark:bg-slate-800">
            {columns.map((c) => (
              <th key={c.key} className={cn('px-3 py-2 text-xs font-semibold uppercase tracking-wide text-gray-500 dark:text-slate-400', alignCls[c.align ?? 'left'])}>
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {data.map((row, i) => (
            <tr key={i} className="border-b border-gray-100 last:border-0 hover:bg-gray-50 dark:border-slate-800 dark:hover:bg-slate-800/50">
              {columns.map((c) => (
                <td key={c.key} className={cn('px-3 py-2 text-gray-800 dark:text-slate-200', alignCls[c.align ?? 'left'])}>
                  {c.render ? c.render(row, i) : String(row[c.key] ?? '')}
                </td>
              ))}
            </tr>
          ))}
          {data.length === 0 && (
            <tr>
              <td colSpan={columns.length} className="px-3 py-6 text-center text-sm text-gray-400 dark:text-slate-500">
                No data
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}

// ─── Tabs ──────────────────────────────────────────────────────
export interface TabDef {
  key: string
  label: ReactNode
  content: ReactNode
}
export function Tabs({ tabs, defaultKey }: { tabs: TabDef[]; defaultKey?: string }) {
  const [active, setActive] = useState(defaultKey ?? tabs[0]?.key ?? '')
  const current = tabs.find((t) => t.key === active) ?? tabs[0]
  return (
    <div>
      <div className="flex gap-1 overflow-x-auto border-b border-gray-200 dark:border-slate-700">
        {tabs.map((t) => (
          <button
            key={t.key}
            onClick={() => setActive(t.key)}
            className={cn(
              'shrink-0 rounded-t-lg px-3 py-1.5 text-sm font-medium transition-colors cursor-pointer',
              t.key === current?.key
                ? 'border-b-2 border-indigo-500 text-indigo-600 dark:text-indigo-400'
                : 'text-gray-500 hover:text-gray-800 dark:text-slate-400 dark:hover:text-slate-200',
            )}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div className="pt-3">{current?.content}</div>
    </div>
  )
}

// ─── Modal ─────────────────────────────────────────────────────
export function Modal({ open, onClose, title, children, width = 480 }: { open: boolean; onClose?: () => void; title?: ReactNode; children?: ReactNode; width?: number }) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-[9999] flex items-center justify-center p-4" role="dialog">
      <div className="absolute inset-0 bg-black/40 backdrop-blur-sm" onClick={onClose} />
      <div className="relative w-full rounded-2xl border border-gray-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900" style={{ maxWidth: width }}>
        {title && (
          <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3 dark:border-slate-800">
            <div className="text-sm font-semibold text-gray-900 dark:text-slate-100">{title}</div>
            {onClose && (
              <button onClick={onClose} className="cursor-pointer rounded p-1 text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-slate-800 dark:hover:text-slate-300">
                ✕
              </button>
            )}
          </div>
        )}
        <div className="max-h-[70vh] overflow-y-auto p-5">{children}</div>
      </div>
    </div>
  )
}

// ─── Form (controlled, single onSubmit) ────────────────────────
export interface FormField {
  name: string
  label: string
  type?: 'text' | 'number' | 'select' | 'textarea'
  options?: Array<{ value: string; label: string }>
  placeholder?: string
  required?: boolean
  defaultValue?: string
}
export function Form({
  fields,
  onSubmit,
  submitLabel = 'Submit',
  layout = 'stack',
}: {
  fields: FormField[]
  onSubmit: (values: Record<string, string>) => void
  submitLabel?: string
  layout?: 'stack' | 'grid'
}) {
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(fields.map((f) => [f.name, f.defaultValue ?? ''])),
  )
  const inputCls =
    'w-full rounded-lg border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 outline-none transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/20 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100'
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(values)
      }}
      className={cn('space-y-3', layout === 'grid' && 'grid grid-cols-2 gap-3 space-y-0')}
    >
      {fields.map((f) => (
        <label key={f.name} className="block">
          <span className="mb-1 block text-xs font-medium text-gray-600 dark:text-slate-300">{f.label}</span>
          {f.type === 'select' ? (
            <select className={inputCls} value={values[f.name] ?? ''} onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}>
              {f.options?.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </select>
          ) : f.type === 'textarea' ? (
            <textarea
              className={cn(inputCls, 'min-h-[72px]')}
              placeholder={f.placeholder}
              value={values[f.name] ?? ''}
              onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
            />
          ) : (
            <input
              type={f.type ?? 'text'}
              className={inputCls}
              placeholder={f.placeholder}
              required={f.required}
              value={values[f.name] ?? ''}
              onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))}
            />
          )}
        </label>
      ))}
      <div className="pt-1">
        <Button type="submit">{submitLabel}</Button>
      </div>
    </form>
  )
}

// ─── Toast (simple stacking) ───────────────────────────────────
export interface ToastItem {
  id: number
  text: ReactNode
  kind?: 'info' | 'success' | 'error'
}
export function Toast({ show, text, kind = 'info' }: { show: boolean; text: ReactNode; kind?: 'info' | 'success' | 'error' }) {
  const kinds = {
    info: 'border-sky-300 bg-sky-50 text-sky-800 dark:border-sky-700 dark:bg-sky-900/40 dark:text-sky-200',
    success: 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-200',
    error: 'border-rose-300 bg-rose-50 text-rose-800 dark:border-rose-700 dark:bg-rose-900/40 dark:text-rose-200',
  }
  return (
    <div
      className={cn(
        'pointer-events-none fixed bottom-4 left-1/2 z-[9998] -translate-x-1/2 rounded-xl border px-4 py-2 text-sm shadow-lg transition-all duration-300',
        kinds[kind],
        show ? 'translate-y-0 opacity-100' : 'pointer-events-none translate-y-2 opacity-0',
      )}
    >
      {text}
    </div>
  )
}

// ─── ECharts (declarative, lazy CDN) ───────────────────────────
let echartsModule: any = null
let echartsLoading: Promise<any> | null = null

async function loadECharts(): Promise<any> {
  if (echartsModule) return echartsModule
  if (!echartsLoading) {
    echartsLoading = (async () => {
      const win = window as unknown as { echarts?: any }
      if (win.echarts) {
        echartsModule = win.echarts
        return win.echarts
      }
      // CDN lazy load (jsdelivr default; configurable via XBOT_UI_CDN).
      const base = (window as unknown as { XBOT_UI_CDN?: string }).XBOT_UI_CDN || 'https://cdn.jsdelivr.net/npm/'
      await new Promise<void>((resolve, reject) => {
        const s = document.createElement('script')
        s.src = `${base}echarts@5/dist/echarts.min.js`
        s.onload = () => resolve()
        s.onerror = () => reject(new Error('echarts CDN load failed'))
        document.head.appendChild(s)
      })
      echartsModule = win.echarts
      return win.echarts
    })()
  }
  return echartsLoading
}

export function Chart({
  option,
  height = 280,
  theme = 'default',
  onReady,
}: {
  option: Record<string, unknown>
  height?: number
  theme?: string
  onReady?: (chart: unknown) => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const chartRef = useRef<{ dispose: () => void } | null>(null)
  const { dark } = useGenUITheme()

  useEffect(() => {
    let disposed = false
    let chart: any = null
    let ro: ResizeObserver | null = null
    loadECharts()
      .then((mod) => {
        if (disposed || !ref.current) return
        const echarts = mod
        const el = ref.current
        chart = echarts.init(el, theme === 'default' ? (dark ? 'dark' : 'default') : theme)
        chart.setOption(option)
        chartRef.current = chart
        onReady?.(chart)
        ro = new ResizeObserver(() => chart?.resize())
        ro.observe(el)
      })
      .catch(() => {
        // Chart lib unavailable — degrade to option text (never blank).
      })
    return () => {
      disposed = true
      ro?.disconnect()
      chartRef.current?.dispose()
      chartRef.current = null
    }
  }, [option, dark]) // eslint-disable-line react-hooks/exhaustive-deps

  return <div ref={ref} className="w-full" style={{ height }} />
}

// ─── three.js scene hook (lazy CDN) ────────────────────────────
let THREE_ANY: any = null
async function loadThree(): Promise<any> {
  if (THREE_ANY) return THREE_ANY
  const win = window as unknown as { THREE?: any }
  if (win.THREE) {
    THREE_ANY = win.THREE
    return win.THREE
  }
  const base = (window as unknown as { XBOT_UI_CDN?: string }).XBOT_UI_CDN || 'https://cdn.jsdelivr.net/npm/'
  await new Promise<void>((resolve, reject) => {
    const s = document.createElement('script')
    s.src = `${base}three@0.160.0/build/three.min.js`
    s.onload = () => resolve()
    s.onerror = () => reject(new Error('three CDN load failed'))
    document.head.appendChild(s)
  })
  THREE_ANY = win.THREE
  return win.THREE
}

/**
 * useThreeScene — mount a three.js scene into a ref div.
 * Usage:
 *   const ref = XBOT_UI.useThreeScene((scene, THREE) => {
 *     scene.add(new THREE.Mesh(...))
 *   })
 *   return <div ref={ref} style={{height: 300}} />
 */
export function useThreeScene(setup: (scene: any, THREE: any) => void, height = 300) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    let disposed = false
    let renderer: any = null
    let raf = 0
    loadThree()
      .then((THREE) => {
        if (disposed || !ref.current) return
        const scene = new THREE.Scene()
        const camera = new THREE.PerspectiveCamera(60, 1, 0.1, 1000)
        camera.position.z = 4
        renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true })
        renderer.setClearColor(0x000000, 0)
        renderer.setSize(ref.current.clientWidth || 300, height)
        ref.current.appendChild(renderer.domElement)
        scene.add(new THREE.AmbientLight(0xffffff, 0.6))
        const dir = new THREE.DirectionalLight(0xffffff, 0.9)
        dir.position.set(1, 2, 3)
        scene.add(dir)
        setup?.(scene, THREE)
        const animate = () => {
          raf = requestAnimationFrame(animate)
          renderer.render(scene, camera)
        }
        animate()
      })
      .catch(() => {})
    return () => {
      disposed = true
      cancelAnimationFrame(raf)
      renderer?.dispose()
      if (ref.current) ref.current.innerHTML = ''
    }
  }, [height]) // eslint-disable-line react-hooks/exhaustive-deps
  return ref
}

// ─── Component-to-component bus ────────────────────────────────
export function useBus() {
  const busRef = useRef<{ map: Map<string, Set<(payload: unknown) => void>> } | null>(null)
  if (!busRef.current) busRef.current = { map: new Map() }
  const bus = busRef.current
  return useMemo(
    () => ({
      on(event: string, fn: (payload: unknown) => void) {
        if (!bus.map.has(event)) bus.map.set(event, new Set())
        bus.map.get(event)!.add(fn)
        return () => bus.map.get(event)?.delete(fn)
      },
      emit(event: string, payload?: unknown) {
        bus.map.get(event)?.forEach((fn) => fn(payload))
      },
    }),
    [bus],
  )
}

// ─── XBOT_UI aggregate ─────────────────────────────────────────
export const XBOT_UI = {
  Button,
  Card,
  Stat,
  Sparkline,
  Progress,
  Badge,
  Table,
  Tabs,
  Modal,
  Form,
  Toast,
  Chart,
  useThreeScene,
  useBus,
  useGenUITheme,
  // motion — framer-motion (already a dependency).
  motion,
  // Icon — lucide subset. MUST be in the aggregate: LLM-generated code uses
  // <XBOT_UI.Icon name="check" size={16}/> — a missing key renders undefined
  // and React throws "Element type is invalid" at render time.
  Icon,
}

export default XBOT_UI
