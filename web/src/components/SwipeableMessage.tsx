import { useRef, useState, useCallback, memo } from 'react'
import { IconReply, IconTrash } from './Icons'

const SWIPE_THRESHOLD = 60
const LOCK_RATIO = 1.5 // horizontal must be > 1.5× vertical to lock

interface SwipeableMessageProps {
  children: React.ReactNode
  onSwipeLeft?: () => void
  onSwipeRight?: () => void
  className?: string
}

/**
 * Swipeable wrapper for touch devices.
 * - Left swipe → reveal delete action (red)
 * - Right swipe → reveal reply action (blue)
 * - Only intercepts when horizontal distance > 1.5× vertical (avoids virtual scroll conflict)
 * - Snaps back on release; triggers action if past threshold
 */
export default memo(function SwipeableMessage({
  children,
  onSwipeLeft,
  onSwipeRight,
  className = '',
}: SwipeableMessageProps) {
  const startX = useRef(0)
  const startY = useRef(0)
  const currentX = useRef(0)
  const [offset, setOffset] = useState(0)
  const locked = useRef<'h' | 'v' | null>(null)

  const onTouchStart = useCallback((e: React.TouchEvent) => {
    const t = e.touches[0]
    startX.current = t.clientX
    startY.current = t.clientY
    currentX.current = t.clientX
    locked.current = null
  }, [])

  const onTouchMove = useCallback(
    (e: React.TouchEvent) => {
      const t = e.touches[0]
      const dx = t.clientX - startX.current
      const dy = t.clientY - startY.current

      // Determine direction lock
      if (locked.current === null && (Math.abs(dx) > 5 || Math.abs(dy) > 5)) {
        if (Math.abs(dx) > dy * LOCK_RATIO) {
          locked.current = 'h'
        } else {
          locked.current = 'v'
        }
      }

      if (locked.current !== 'h') return

      // Prevent vertical scroll during horizontal swipe
      e.preventDefault()

      currentX.current = t.clientX
      // Clamp to [-threshold*2, threshold*2] for rubber-band feel
      const raw = t.clientX - startX.current
      const clamped = Math.max(-SWIPE_THRESHOLD * 2, Math.min(SWIPE_THRESHOLD * 2, raw))
      setOffset(clamped)
    },
    [],
  )

  const onTouchEnd = useCallback(() => {
    if (locked.current !== 'h') {
      setOffset(0)
      return
    }

    if (offset < -SWIPE_THRESHOLD && onSwipeLeft) {
      onSwipeLeft()
    } else if (offset > SWIPE_THRESHOLD && onSwipeRight) {
      onSwipeRight()
    }

    // Snap back
    setOffset(0)
    locked.current = null
  }, [offset, onSwipeLeft, onSwipeRight])

  // Determine which action is revealed
  const leftAction = offset > SWIPE_THRESHOLD // right swipe reveals left action (reply)
  const rightAction = offset < -SWIPE_THRESHOLD // left swipe reveals right action (delete)

  return (
    <div
      className={`swipeable-message ${className}`}
      onTouchStart={onTouchStart}
      onTouchMove={onTouchMove}
      onTouchEnd={onTouchEnd}
    >
      {/* Background action layers */}
      <div className="swipeable-actions">
        {/* Right side: reply (blue) — revealed when swiping right */}
        {onSwipeRight && (
          <div className={`swipeable-action-reply ${leftAction ? 'swipeable-action-visible' : ''}`}>
            <span className="swipeable-action-icon"><IconReply /></span>
            <span className="swipeable-action-label">Reply</span>
          </div>
        )}
        {/* Left side: delete (red) — revealed when swiping left */}
        {onSwipeLeft && (
          <div className={`swipeable-action-delete ${rightAction ? 'swipeable-action-visible' : ''}`}>
            <span className="swipeable-action-icon"><IconTrash /></span>
            <span className="swipeable-action-label">Delete</span>
          </div>
        )}
      </div>

      {/* Sliding content */}
      <div
        className="swipeable-content"
        style={{
          transform: `translateX(${offset}px)`,
          transition: locked.current === 'h' ? 'none' : 'transform 0.25s cubic-bezier(0.25, 1, 0.5, 1)',
        }}
      >
        {children}
      </div>
    </div>
  )
})
