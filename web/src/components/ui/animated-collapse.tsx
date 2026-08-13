import { useEffect, useState, type ReactNode } from 'react'

import { cn } from '@/lib/utils'

interface AnimatedCollapseProps {
  open: boolean
  children: ReactNode
  className?: string
  contentClassName?: string
  lazy?: boolean
  /** 折叠动画结束后是否卸载内容（默认 false）。重内容（tool 折叠）应设
   *  true，避免 stream 期间折叠内容跟着父组件 re-render 导致展开/折叠卡顿；
   *  轻量内容（如 todo 列表）保持 false 以便快速切换。 */
  unmountOnClose?: boolean
}

/** Shared CSS-grid disclosure motion with optional first-open lazy mounting. */
export function AnimatedCollapse({
  open,
  children,
  className,
  contentClassName,
  lazy = false,
  unmountOnClose = false,
}: AnimatedCollapseProps) {
  const [mounted, setMounted] = useState(open || !lazy)
  const [revealed, setRevealed] = useState(open)

  useEffect(() => {
    if (open) {
      setMounted(true)
      // lazy：先以 0fr 渲染一帧，再切到 1fr 触发展开过渡。
      if (lazy) {
        const frame = requestAnimationFrame(() => setRevealed(true))
        return () => cancelAnimationFrame(frame)
      }
      setRevealed(true)
      return
    }
    // 折叠。unmountOnClose：先播放 180ms 收起动画（revealed=false），动画
    // 结束后卸载（stream 期间折叠的重内容不再参与渲染）。
    if (unmountOnClose) {
      setRevealed(false)
      const timer = setTimeout(() => setMounted(false), 200)
      return () => clearTimeout(timer)
    }
    setRevealed(false)
  }, [lazy, open, unmountOnClose])

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
