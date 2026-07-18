import { memo, type CSSProperties } from 'react'

import { cn } from '@/lib/utils'

interface SweepTextProps {
  text: string
  color?: string
  className?: string
}

type SweepStyle = CSSProperties & { '--sweep-color': string }

/** Character-delayed opacity sweep shared by live Agent status surfaces. */
export const SweepText = memo(function SweepText({ text, color = 'var(--text-primary)', className }: SweepTextProps) {
  const chars = Array.from(text)

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
