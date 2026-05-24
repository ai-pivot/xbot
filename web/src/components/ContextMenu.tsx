import { useEffect, useRef, memo, type ReactNode } from 'react'

export interface ContextMenuItem {
  label: string
  icon?: ReactNode
  onClick: () => void
  danger?: boolean
}

interface ContextMenuProps {
  x: number
  y: number
  items: ContextMenuItem[]
  onClose: () => void
  visible: boolean
}

/**
 * Custom context menu for right-click / long-press interactions.
 * Positions itself within viewport boundaries.
 * Accessible: role="menu", role="menuitem", Escape to close.
 */
export default memo(function ContextMenu({ x, y, items, onClose, visible }: ContextMenuProps) {
  const menuRef = useRef<HTMLDivElement>(null)

  // Position clamp helper
  const clampPosition = (el: HTMLElement, px: number, py: number) => {
    const vw = window.innerWidth
    const vh = window.innerHeight
    const rect = el.getBoundingClientRect()
    let left = px
    let top = py
    if (px + rect.width > vw - 8) left = vw - rect.width - 8
    if (py + rect.height > vh - 8) top = vh - rect.height - 8
    left = Math.max(8, left)
    top = Math.max(8, top)
    el.style.left = `${left}px`
    el.style.top = `${top}px`
  }

  // Close on Escape
  useEffect(() => {
    if (!visible) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [visible, onClose])

  // Close on click outside + clamp position
  useEffect(() => {
    if (!visible) return
    const el = menuRef.current
    if (el) clampPosition(el, x, y)

    const handler = (e: MouseEvent | PointerEvent) => {
      if (el && !el.contains(e.target as Node)) {
        onClose()
      }
    }
    // Delay to avoid same-event close
    const timer = setTimeout(() => {
      document.addEventListener('pointerdown', handler)
    }, 0)
    return () => {
      clearTimeout(timer)
      document.removeEventListener('pointerdown', handler)
    }
  }, [visible, onClose, x, y])

  if (!visible || items.length === 0) return null

  return (
    <div
      ref={menuRef}
      className="context-menu"
      role="menu"
      aria-orientation="vertical"
      style={{ left: x, top: y }}
    >
      {items.map((item, i) => (
        <button
          key={i}
          role="menuitem"
          className={`context-menu-item ${item.danger ? 'context-menu-item-danger' : ''}`}
          onClick={() => {
            item.onClick()
            onClose()
          }}
          tabIndex={0}
        >
          {item.icon && <span className="context-menu-icon">{item.icon}</span>}
          <span>{item.label}</span>
        </button>
      ))}
    </div>
  )
})
