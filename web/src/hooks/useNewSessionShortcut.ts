import { useEffect, useRef } from 'react'
import { dispatchSessionShortcut } from '@/components/session/session-shortcuts'
import { useSessionShortcuts } from './useSessionShortcuts'

export function useNewSessionShortcut(createSession: () => void | Promise<unknown>, enabled = true) {
  const { bindings } = useSessionShortcuts()
  const pending = useRef(false)

  useEffect(() => {
    if (!enabled) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (document.querySelector('[role="dialog"]:not([data-state="closed"]), [role="alertdialog"]:not([data-state="closed"]), [role="menu"]:not([data-state="closed"])')) return
      dispatchSessionShortcut(event, {
        newSession: async () => {
          if (pending.current) return
          pending.current = true
          try { await createSession() }
          finally { pending.current = false }
        },
      }, bindings)
    }
    // Creation is global, so a focused row must not select itself first.
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [bindings, createSession, enabled])
}
