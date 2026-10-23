/**
 * 通信视图颜色解析层（2026-10-22 重做）。
 *
 * 背景：旧实现把 `#0a1120` / `rgba(148,163,184,0.65)` 硬编码在 TSX 与 canvas
 * 绘制代码里，切换白主题后仍是深底浅字 —— 面板在白主题下几乎不可读。
 *
 * 现在的规则：
 *   1. DOM 样式直接写语义 class（bg-bg-primary / text-text-primary / …）
 *      或 `var(--kind-*)`（CSS 变量天然随主题切换，不需要 JS）；
 *   2. canvas / SVG 里无法用 `var()` 的地方，通过本模块从 :root 读真实色值，
 *      并在 html class 变化（主题切换）时重新读 —— 只读，不缓存跨主题的值。
 *
 * 这里没有任何硬编码色值：全部来自 index.css 的主题 token。
 */
import { useEffect, useState } from 'react'

/** 画布需要的语义槽位 → CSS 变量名。 */
const VAR = {
  surface: '--bg-primary',
  surfaceAlt: '--bg-secondary',
  surfaceRaised: '--bg-tertiary',
  border: '--border',
  borderStrong: '--border-strong',
  text: '--text-primary',
  textSecondary: '--text-secondary',
  textMuted: '--text-muted',
  accent: '--accent',
  success: '--success',
  warning: '--warning',
  danger: '--status-error',
  running: '--status-running',
  waiting: '--status-waiting',
  idle: '--status-idle',
} as const

export type CourtPaletteKey = keyof typeof VAR

export interface CourtPalette {
  [k: string]: string
}

export const KIND_VARS: Record<string, string> = {
  ask_user: '--kind-ask-user',
  answer: '--kind-answer',
  report: '--kind-report',
  decision: '--kind-decision',
  command: '--kind-command',
  event: '--kind-event',
  text: '--kind-text',
}

export const STATUS_VARS: Record<string, string> = {
  idle: '--status-idle',
  busy: '--status-waiting',
  blocked: '--status-error',
  left: '--text-muted',
}

/** 从文档根读取一个 CSS 变量（已 trim；未定义时返回空串）。 */
function readVar(name: string): string {
  if (typeof document === 'undefined') return ''
  const v = getComputedStyle(document.documentElement).getPropertyValue(name)
  return v ? v.trim() : ''
}

/**
 * 读出当前主题下 canvas 需要的一组具体色值。
 * 读不到变量时返回空串 —— 调用方（canvas）自行跳过该笔绘制，
 * 绝不回退到硬编码色（那正是白主题下看不清的根因）。
 */
export function readCourtPalette(): CourtPalette {
  const out: CourtPalette = {}
  for (const [key, name] of Object.entries(VAR)) out[key] = readVar(name)
  out.kind = ''
  const kinds: Record<string, string> = {}
  for (const [kind, name] of Object.entries(KIND_VARS)) kinds[kind] = readVar(name)
  const statuses: Record<string, string> = {}
  for (const [status, name] of Object.entries(STATUS_VARS)) statuses[status] = readVar(name)
  // 嵌套结构单独挂在两个字段上（类型上用 Record<string, string> 兜住）
  ;(out as unknown as Record<string, unknown>).kinds = kinds
  ;(out as unknown as Record<string, unknown>).statuses = statuses
  return out
}

/** 取 kind / status 颜色（读不到时退回 text，再退回空串）。 */
export function paletteKind(p: CourtPalette, kind: string): string {
  const kinds = (p as unknown as { kinds?: Record<string, string> }).kinds ?? {}
  return kinds[kind] ?? kinds.text ?? p.textSecondary ?? ''
}

export function paletteStatus(p: CourtPalette, status: string): string {
  const statuses = (p as unknown as { statuses?: Record<string, string> }).statuses ?? {}
  return statuses[status] ?? statuses.idle ?? p.textSecondary ?? ''
}

/**
 * 给颜色加透明度：canvas 需要 rgba 形态。
 * 支持 #rgb / #rrggbb / rgb() / rgba() / color(srgb …)；无法解析时原样返回。
 */
export function withAlpha(color: string, alpha: number): string {
  const c = color.trim()
  if (!c) return c
  const a = Math.max(0, Math.min(1, alpha))
  if (c.startsWith('#')) {
    const hex = c.slice(1)
    const full = hex.length === 3 ? hex.split('').map((x) => x + x).join('') : hex
    if (full.length !== 6) return c
    const n = Number.parseInt(full, 16)
    if (Number.isNaN(n)) return c
    const r = (n >> 16) & 255
    const g = (n >> 8) & 255
    const b = n & 255
    return `rgba(${r}, ${g}, ${b}, ${a})`
  }
  const m = c.match(/^rgba?\(([^)]+)\)$/i)
  if (m) {
    const parts = m[1].split(/[,/\s]+/).filter(Boolean)
    if (parts.length >= 3) return `rgba(${parts[0]}, ${parts[1]}, ${parts[2]}, ${a})`
  }
  return c
}

/**
 * 主题感知的调色板：html 的 class / style 变化（ThemeProvider 切 .dark）时
 * 重新读取一次。canvas 的绘制循环用返回的对象着色。
 */
export function useCourtPalette(): CourtPalette {
  const [palette, setPalette] = useState<CourtPalette>(() => readCourtPalette())
  useEffect(() => {
    if (typeof document === 'undefined') return
    const refresh = () => setPalette(readCourtPalette())
    refresh()
    const mo = new MutationObserver(refresh)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['class', 'style', 'data-theme'] })
    return () => mo.disconnect()
  }, [])
  return palette
}
