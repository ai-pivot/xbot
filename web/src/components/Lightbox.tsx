import { useState, useEffect, useCallback, memo } from 'react'
import { createPortal } from 'react-dom'

interface LightboxProps {
  src: string
  alt?: string
  onClose: () => void
}

/** Image lightbox with zoom (scroll), drag, and ESC-to-close. */
const Lightbox = memo(function Lightbox({ src, alt, onClose }: LightboxProps) {
  const [scale, setScale] = useState(1)
  const [pos, setPos] = useState({ x: 0, y: 0 })
  const [dragging, setDragging] = useState(false)
  const dragStart = useState({ x: 0, y: 0, px: 0, py: 0 })[0]

  const handleClose = useCallback((e: React.MouseEvent) => {
    if (e.target === e.currentTarget) onClose()
  }, [onClose])

  const handleWheel = useCallback((e: React.WheelEvent) => {
    e.preventDefault()
    setScale(prev => Math.min(5, Math.max(0.5, prev + (e.deltaY < 0 ? 0.15 : -0.15))))
  }, [])

  const handleMouseDown = useCallback((e: React.MouseEvent) => {
    if (e.button !== 0) return
    setDragging(true)
    dragStart.x = e.clientX
    dragStart.y = e.clientY
    dragStart.px = pos.x
    dragStart.py = pos.y
  }, [pos, dragStart])

  const handleMouseMove = useCallback((e: React.MouseEvent) => {
    if (!dragging) return
    setPos({
      x: dragStart.px + (e.clientX - dragStart.x),
      y: dragStart.py + (e.clientY - dragStart.y),
    })
  }, [dragging, dragStart])

  const handleMouseUp = useCallback(() => {
    setDragging(false)
  }, [])

  // ESC to close
  useEffect(() => {
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    document.body.style.overflow = 'hidden'
    return () => {
      window.removeEventListener('keydown', handler)
      document.body.style.overflow = ''
    }
  }, [onClose])

  // Touch support
  const handleTouchStart = useCallback((e: React.TouchEvent) => {
    const t = e.touches[0]
    dragStart.x = t.clientX
    dragStart.y = t.clientY
    dragStart.px = pos.x
    dragStart.py = pos.y
    setDragging(true)
  }, [pos, dragStart])

  const handleTouchMove = useCallback((e: React.TouchEvent) => {
    if (!dragging) return
    const t = e.touches[0]
    setPos({
      x: dragStart.px + (t.clientX - dragStart.x),
      y: dragStart.py + (t.clientY - dragStart.y),
    })
  }, [dragging, dragStart])

  const handleTouchEnd = useCallback(() => {
    setDragging(false)
  }, [])

  return createPortal(
    <div
      className="xbot-lightbox-overlay"
      onClick={handleClose}
      onWheel={handleWheel}
    >
      <img
        src={src}
        alt={alt || ''}
        className="xbot-lightbox-img"
        style={{
          transform: `translate(${pos.x}px, ${pos.y}px) scale(${scale})`,
          transition: dragging ? 'none' : 'transform 0.15s ease-out',
        }}
        onClick={(e) => e.stopPropagation()}
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseUp={handleMouseUp}
        onMouseLeave={handleMouseUp}
        onTouchStart={handleTouchStart}
        onTouchMove={handleTouchMove}
        onTouchEnd={handleTouchEnd}
        draggable={false}
      />
    </div>,
    document.body,
  )
})

export default Lightbox
