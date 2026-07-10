/**
 * ShimmerThinking — bold borderless "正在思考" text with a character-by-character
 * shimmer sweep effect (each character lights up in sequence, loop).
 */
import { memo } from 'react'

import { useI18n } from '@/providers/i18n'

export const ShimmerThinking = memo(function ShimmerThinking() {
  const { t } = useI18n()
  const text = t('agent.reasoningStreaming') // "思考中…" / "thinking…"
  // Split into characters for the per-character sweep animation
  const chars = Array.from(text)

  return (
    <div className="mt-2">
      <span
        className="thinking-shimmer font-bold"
        style={{ color: 'var(--text-primary)' }}
        aria-label={text}
      >
        {chars.map((ch, i) => (
          <span
            key={i}
            className="thinking-char"
            style={{ animationDelay: `${i * 0.15}s` }}
          >
            {ch === ' ' ? '\u00A0' : ch}
          </span>
        ))}
      </span>
    </div>
  )
})
