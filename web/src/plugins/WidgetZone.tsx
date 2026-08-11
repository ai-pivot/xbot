/**
 * WidgetZone — renders a plugin widget zone's structured spans.
 *
 * Maps WebWidgetSpan.style (StyleClass semantics) to Tailwind text colors.
 * The zone is rendered as an inline flex row; spans with the same style are
 * wrapped in <span class="..."> with an optional lucide icon prefix.
 */
import type { CSSProperties, ReactNode } from 'react'

import { usePluginWidgets } from './PluginWidgetProvider'
import type { WebWidgetSpan } from '@/types/shared'

/** StyleClass → Tailwind text color mapping (mirrors TUI semantic colors). */
const STYLE_CLASSES: Record<string, string> = {
  normal: 'text-gray-800',
  dim: 'text-gray-400',
  accent: 'text-indigo-600',
  success: 'text-green-600',
  warning: 'text-amber-600',
  error: 'text-red-600',
  info: 'text-blue-600',
  muted: 'text-gray-500',
}

export interface WidgetZoneProps {
  /** Zone name: titleBarLeft|titleBarRight|statusBarLeft|statusBarRight|infoBar|footer|toolHint */
  zone: string
  /** Fallback rendered when the zone has no content. */
  empty?: ReactNode
  /** Optional additional className for the container. */
  className?: string
  style?: CSSProperties
}

/** Render a single styled span. */
export function WidgetSpanView({ span }: { span: WebWidgetSpan }) {
  const cls = STYLE_CLASSES[span.style ?? 'normal'] ?? STYLE_CLASSES.normal
  const content: ReactNode = span.href ? (
    <a
      href={span.href}
      target="_blank"
      rel="noreferrer"
      className={`${cls} hover:underline`}
    >
      {span.text}
    </a>
  ) : (
    span.text
  )
  return <span className={`${cls} whitespace-nowrap`}>{content}</span>
}

/** Render all spans for a zone as a horizontal inline flow. */
export function WidgetZone({ zone, empty = null, className, style }: WidgetZoneProps) {
  const { zones } = usePluginWidgets()
  const spans = zones[zone] ?? []
  if (spans.length === 0) return <>{empty}</>
  return (
    <div className={`flex items-center gap-2 overflow-x-auto ${className ?? ''}`} style={style}>
      {spans.map((span, i) => (
        <WidgetSpanView key={i} span={span} />
      ))}
    </div>
  )
}
