/**
 * useCodeWordWrap — reads/writes the code-block word-wrap preference.
 *
 * When true (default), long code lines wrap to the next line.
 * When false, code blocks scroll horizontally instead of wrapping.
 *
 * Uses useSyncExternalStore for same-window synchronisation: when the
 * settings panel changes the value, every component instance that calls
 * useCodeWordWrap re-renders immediately. Cross-window sync is handled
 * by the storage event listener which updates the same global store.
 *
 * Also synced to the server via userSettings (cross-browser).
 */
import { useCallback, useSyncExternalStore } from 'react'

import {
  CODE_WORD_WRAP_STORAGE_KEY,
  DEFAULT_CODE_WORD_WRAP,
} from '@/types/agent'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

function readStoredValue(): boolean {
  try {
    const v = localStorage.getItem(CODE_WORD_WRAP_STORAGE_KEY)
    if (v === 'false') return false
    if (v === 'true') return true
  } catch {
    /* ignore */
  }
  return DEFAULT_CODE_WORD_WRAP
}

type Listener = () => void

const listeners = new Set<Listener>()

function notify() {
  listeners.forEach((l) => l())
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', (e: StorageEvent) => {
    if (e.key === CODE_WORD_WRAP_STORAGE_KEY) notify()
  })
  window.addEventListener(SETTINGS_SYNCED_EVENT, notify)
}

function subscribe(listener: Listener): () => void {
  listeners.add(listener)
  return () => listeners.delete(listener)
}

function getSnapshot(): boolean {
  return readStoredValue()
}

export function useCodeWordWrap(): {
  wordWrap: boolean
  setWordWrap: (value: boolean) => void
} {
  const wordWrap = useSyncExternalStore(subscribe, getSnapshot, getSnapshot)

  const setWordWrap = useCallback((value: boolean) => {
    try {
      localStorage.setItem(CODE_WORD_WRAP_STORAGE_KEY, String(value))
      syncSettingToServer(CODE_WORD_WRAP_STORAGE_KEY, String(value))
      notify()
    } catch {
      /* ignore */
    }
  }, [])

  return { wordWrap, setWordWrap }
}
