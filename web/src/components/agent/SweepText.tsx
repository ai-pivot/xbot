import { memo, type CSSProperties } from 'react'

import { cn } from '@/lib/utils'

interface SweepTextProps {
  text: string
  color?: string
  className?: string
}

type SweepStyle = CSSProperties & { '--sweep-color': string }

/** Threshold: beyond this many characters, use CSS gradient sweep instead of
 *  per-character spans. Prevents thousands of DOM nodes for long reasoning. */
const SWEEP_GRADIENT_THRESHOLD = 200

/** Character-delayed opacity sweep shared by live Agent status surfaces.
 *
 *  For short text (≤200 chars): renders one <span> per character with
 *  staggered animationDelay — the classic sweep effect.
 *
 *  For long text (>200 chars): degrades to a single <span> with a CSS
 *  gradient sweep animation. This prevents thousands of DOM nodes that
 *  cause layout thrashing on long reasoning text. */
export const SweepText = memo(function SweepText({ text, color = 'var(--text-primary)', className }: SweepTextProps) {
  const chars = Array.from(text)

  // Long text: use CSS gradient sweep (1 DOM node instead of N)
  if (chars.length > SWEEP_GRADIENT_THRESHOLD) {
    return (
      <span
        className={cn('sweep-text', 'sweep-text-gradient', className)}
        style={{ '--sweep-color': color } as SweepStyle}
        aria-label={text}
      >
        {text}
      </span>
    )
  }

  return (
    <span
      className={cn('sweep-text', className)}
      style={{ '--sweep-color': color } as SweepStyle}
      aria-label={text}
    >
      {chars.map((char, index) => (
        <span
          key={`${index}-${char}`}
          className="sweep-text-char"
          style={{ animationDelay: `${index * 0.15}s` }}
          aria-hidden="true"
        >
          {char === ' ' ? '\u00A0' : char}
        </span>
      ))}
    </span>
  )
})
