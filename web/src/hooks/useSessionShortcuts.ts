import { useMemo, useSyncExternalStore } from 'react'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'
import {
  SESSION_SHORTCUT_ACTIONS, getSessionShortcutDefaults, isWindowsPlatform, parseShortcutBinding,
  type SessionShortcutAction, type SessionShortcutBindings,
} from '@/components/session/session-shortcuts'

export const SESSION_SHORTCUT_STORAGE_KEY = 'xbot-session-shortcuts'
export const WINDOWS_SESSION_SHORTCUT_STORAGE_KEY = 'xbot-session-shortcuts-windows'
type Overrides = Partial<SessionShortcutBindings>
const listeners = new Set<() => void>()
const notify = () => listeners.forEach(listener => listener())
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

function storageKey(): string {
  return isWindowsPlatform() ? WINDOWS_SESSION_SHORTCUT_STORAGE_KEY : SESSION_SHORTCUT_STORAGE_KEY
}

function readRaw(): string {
  try { return localStorage.getItem(storageKey()) ?? '{}' }
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
  const key = storageKey()
  const raw = JSON.stringify(overrides)
  localStorage.setItem(key, raw)
  syncSettingToServer(key, raw)
  notify()
}

function setBinding(action: SessionShortcutAction, binding: string | null) {
  const overrides = parseOverrides(readRaw())
  if (binding === getSessionShortcutDefaults()[action]) delete overrides[action]
  else overrides[action] = binding
  writeOverrides(overrides)
}

const resetAll = () => writeOverrides({})

if (typeof window !== 'undefined') {
  window.addEventListener('storage', event => {
    if (event.key === storageKey() || event.key === null) notify()
  })
  window.addEventListener(SETTINGS_SYNCED_EVENT, notify)
}

export function useSessionShortcuts() {
  const defaults = getSessionShortcutDefaults()
  const raw = useSyncExternalStore(subscribe, readRaw, readRaw)
  const bindings = useMemo<SessionShortcutBindings>(() => ({ ...defaults, ...parseOverrides(raw) }), [defaults, raw])
  return { bindings, defaults, setBinding, resetAll }
}
