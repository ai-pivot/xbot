import { useEffect, useState, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

interface AnimatedCollapseProps {
  open: boolean
  children: ReactNode
  className?: string
  contentClassName?: string
  lazy?: boolean
}

/** Shared CSS-grid disclosure motion with optional first-open lazy mounting. */
export function AnimatedCollapse({
  open,
  children,
  className,
  contentClassName,
  lazy = false,
}: AnimatedCollapseProps) {
  const [mounted, setMounted] = useState(open || !lazy)
  const [revealed, setRevealed] = useState(open)

  useEffect(() => {
    if (!lazy) {
      setMounted(true)
      setRevealed(open)
      return
    }
    if (!open) {
      setRevealed(false)
      return
    }
    if (!mounted) {
      setMounted(true)
      return
    }
    // Give the browser one closed frame so the grid transition is visible on
    // first open, while keeping heavy children unmounted until interaction.
    const frame = requestAnimationFrame(() => setRevealed(true))
    return () => cancelAnimationFrame(frame)
  }, [lazy, open, mounted])

  if (!mounted) return null

  return (
    <div
      className={cn('fold-container', revealed && 'open', className)}
      data-state={revealed ? 'open' : 'closed'}
      aria-hidden={!revealed}
      inert={!revealed}
    >
      <div className={cn('fold-content', contentClassName)}>{children}</div>
    </div>
  )
}
