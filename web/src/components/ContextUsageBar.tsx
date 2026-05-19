import { memo } from 'react'
import { useTranslation } from '../i18n'

export interface ContextUsageBarProps {
  promptTokens: number
  maxTokens: number
  usagePct: number
}

/**
 * Visual context usage bar above the input area.
 * Mirrors TUI's renderContextTopBorder — shows a gradient fill bar
 * with color coding (green < 50%, yellow < 80%, red ≥ 80%).
 */
export const ContextUsageBar = memo(function ContextUsageBar({
  promptTokens,
  maxTokens,
  usagePct,
}: ContextUsageBarProps) {
  const { t } = useTranslation()

  if (maxTokens <= 0) return null

  const pct = Math.min(100, Math.max(0, usagePct))
  const colorClass =
    pct >= 80 ? 'context-bar-danger' :
    pct >= 50 ? 'context-bar-warning' :
    'context-bar-ok'

  return (
    <div className="context-usage-bar" title={t('contextUsageTitle', { prompt: promptTokens.toLocaleString(), max: maxTokens.toLocaleString() })}>
      <div className={`context-usage-bar-fill ${colorClass}`} style={{ width: `${pct}%` }} />
      <span className="context-usage-bar-text">
        📊 {(promptTokens / 1000).toFixed(1)}K / {(maxTokens / 1000).toFixed(0)}K
      </span>
    </div>
  )
})
