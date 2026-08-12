import { useCallback, useEffect, useState } from 'react'

const STORAGE_KEY = 'xbot:developer-mode'
const EVENT = 'xbot:developer-mode'

function readStored(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === '1'
  } catch {
    return false
  }
}

/**
 * Developer-mode flag (default OFF). When enabled, developer-only UI surfaces
 * appear (e.g. the SSE REC recorder on AgentPanel). Persisted to localStorage;
 * changes propagate across components via a window CustomEvent so the Settings
 * toggle and the consuming panels stay in sync without a shared context.
 */
export function useDeveloperMode() {
  const [enabled, setEnabledState] = useState(readStored)

  useEffect(() => {
    const handler = (e: Event) => setEnabledState((e as CustomEvent<boolean>).detail)
    window.addEventListener(EVENT, handler)
    return () => window.removeEventListener(EVENT, handler)
  }, [])

  const setEnabled = useCallback((v: boolean) => {
    try {
      localStorage.setItem(STORAGE_KEY, v ? '1' : '0')
    } catch {
      // storage unavailable — keep in-memory only
    }
    setEnabledState(v)
    window.dispatchEvent(new CustomEvent(EVENT, { detail: v }))
  }, [])

  return { enabled, setEnabled }
}
