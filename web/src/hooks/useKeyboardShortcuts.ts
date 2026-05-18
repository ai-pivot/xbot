import { useEffect, useRef } from 'react'

export interface ShortcutConfig {
  /** Key name (e.g. 'k', 'Escape', '/') */
  key: string
  /** Require Ctrl or Cmd */
  ctrl?: boolean
  /** Whether this shortcut is currently active */
  enabled?: boolean
  /** Handler function — return true to stop propagation */
  handler: () => void | boolean
  /** Description for documentation */
  description?: string
}

/**
 * Centralized keyboard shortcut manager.
 * Registers shortcuts on window keydown with proper priority.
 * Shortcuts are evaluated in order — first match wins.
 * Automatically cleans up on unmount.
 */
export function useKeyboardShortcuts(shortcuts: ShortcutConfig[]): void {
  const shortcutsRef = useRef(shortcuts)
  shortcutsRef.current = shortcuts

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      const active = shortcutsRef.current.filter(s => s.enabled !== false)

      for (const shortcut of active) {
        const keyMatch = e.key === shortcut.key
        const ctrlMatch = shortcut.ctrl
          ? (e.ctrlKey || e.metaKey)
          : (!e.ctrlKey && !e.metaKey)

        if (keyMatch && ctrlMatch) {
          const result = shortcut.handler()
          if (result !== false) {
            e.preventDefault()
            e.stopPropagation()
          }
          return // first match wins
        }
      }
    }

    window.addEventListener('keydown', handleKeyDown, true) // capture phase for priority
    return () => window.removeEventListener('keydown', handleKeyDown, true)
  }, [])
}
