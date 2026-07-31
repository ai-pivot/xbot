/**
 * useSendKeyMode — reads/writes the send-key preference (Spec C §1.1).
 *
 * Two modes:
 *   - 'ctrl-enter' (default): Ctrl/Cmd+Enter sends, Enter inserts a newline.
 *   - 'enter': Enter sends, Shift/Ctrl+Enter inserts a newline.
 *
 * Uses useSyncExternalStore for same-window synchronisation: when the
 * settings panel changes the mode, every component instance that calls
 * useSendKeyMode re-renders immediately. Cross-window sync is handled
 * by the storage event listener which updates the same global store.
 *
 * Also synced to the server via userSettings (cross-browser).
 */
import { useCallback, useSyncExternalStore } from 'react'

import {
  DEFAULT_SEND_KEY_MODE,
  SEND_KEY_MODES,
  SEND_KEY_MODE_STORAGE_KEY,
  type SendKeyMode,
} from '@/types/agent'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

function readStoredMode(): SendKeyMode {
  try {
    const v = localStorage.getItem(SEND_KEY_MODE_STORAGE_KEY)
    if (v && (SEND_KEY_MODES as string[]).includes(v)) return v as SendKeyMode
  } catch {
    /* ignore */
  }
  return DEFAULT_SEND_KEY_MODE
}

type Listener = () => void

const listeners = new Set<Listener>()

function notify() {
  listeners.forEach((l) => l())
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e: StorageEvent) => {
    if (e.key === SEND_KEY_MODE_STORAGE_KEY) notify()
  })
  window.addEventListener(SETTINGS_SYNCED_EVENT, notify)
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function getSnapshot(): SendKeyMode {
  return readStoredMode()
}

export function useSendKeyMode(): {
  mode: SendKeyMode
  setMode: (mode: SendKeyMode) => void
} {
  const mode = useSyncExternalStore(subscribe, getSnapshot, getSnapshot)

  const setMode = useCallback((next: SendKeyMode) => {
    try {
      localStorage.setItem(SEND_KEY_MODE_STORAGE_KEY, next)
      syncSettingToServer(SEND_KEY_MODE_STORAGE_KEY, next)
      notify()
    } catch {
      /* ignore */
    }
  }, [])

  return { mode, setMode }
}

/**
 * Returns true if the given keyboard event should trigger a "send" action
 * under the specified send-key mode. Callers should `preventDefault()` when
 * this returns true.
 */
export function isSendKey(e: React.KeyboardEvent, mode: SendKeyMode): boolean {
  if (mode === 'enter') {
    // Enter sends (without Shift/Ctrl), Shift+Enter / Ctrl+Enter inserts newline.
    return Boolean(e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey)
  }
  // 'ctrl-enter': Ctrl/Cmd+Enter sends, Enter inserts newline.
  return Boolean((e.ctrlKey || e.metaKey) && e.key === 'Enter' && !e.shiftKey)
}
