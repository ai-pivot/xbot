import { useMemo, useSyncExternalStore } from 'react'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'
import {
  SESSION_SHORTCUTS, SESSION_SHORTCUT_ACTIONS, parseShortcutBinding,
  type SessionShortcutAction, type SessionShortcutBindings,
} from '@/components/session/session-shortcuts'

export const SESSION_SHORTCUT_STORAGE_KEY = 'xbot-session-shortcuts'
type Overrides = Partial<SessionShortcutBindings>
const listeners = new Set<() => void>()
const notify = () => listeners.forEach(listener => listener())
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

function readRaw(): string {
  try { return localStorage.getItem(SESSION_SHORTCUT_STORAGE_KEY) ?? '{}' }
  catch { return '{}' }
}

function parseOverrides(raw: string): Overrides {
  try {
    const data: unknown = JSON.parse(raw)
    if (!data || typeof data !== 'object' || Array.isArray(data)) return {}
    const overrides: Overrides = {}
    const values = data as Record<string, unknown>
    for (const action of SESSION_SHORTCUT_ACTIONS) {
      // Preserve the binding (including disabled) when replacing the old action.
      const value = action === 'newSession' && !Object.hasOwn(values, action)
        ? values.openInTab : values[action]
      if (value === null) overrides[action] = null
      else if (typeof value === 'string') {
        const binding = parseShortcutBinding(value)
        if (binding !== undefined) overrides[action] = binding
      }
    }
    return overrides
  } catch { return {} }
}

function writeOverrides(overrides: Overrides) {
  const raw = JSON.stringify(overrides)
  localStorage.setItem(SESSION_SHORTCUT_STORAGE_KEY, raw)
  syncSettingToServer(SESSION_SHORTCUT_STORAGE_KEY, raw)
  notify()
}

function setBinding(action: SessionShortcutAction, binding: string | null) {
  const overrides = parseOverrides(readRaw())
  if (binding === SESSION_SHORTCUTS[action]) delete overrides[action]
  else overrides[action] = binding
  writeOverrides(overrides)
}

const resetAll = () => writeOverrides({})

if (typeof window !== 'undefined') {
  window.addEventListener('storage', event => {
    if (event.key === SESSION_SHORTCUT_STORAGE_KEY || event.key === null) notify()
  })
  window.addEventListener(SETTINGS_SYNCED_EVENT, notify)
}

export function useSessionShortcuts() {
  const raw = useSyncExternalStore(subscribe, readRaw, readRaw)
  const bindings = useMemo<SessionShortcutBindings>(() => ({ ...SESSION_SHORTCUTS, ...parseOverrides(raw) }), [raw])
  return { bindings, setBinding, resetAll }
}
