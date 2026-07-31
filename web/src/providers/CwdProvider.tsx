/**
 * CwdProvider — tracks the agent's current working directory (CWD).
 *
 * Initialization: when activeSessionId changes, fetches CWD via /api/cwd.
 * Live tracking: subscribes to `progress_structured` SSE events and reads
 * `progress.cwd` (populated by the backend on each iteration).
 * Also detects completed `Cd` tool calls as a fallback when `cwd` is absent.
 *
 * Children access CWD via `useCwd()`; file browser/search auto-refresh
 * when the CWD changes (they depend on `cwd` from the context).
 */
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'

import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import { fetchCwd } from '@/components/agent/api'
import type { WSMessage } from '@/types/shared'

export interface CwdContextValue {
  /** The current working directory, or null before the first fetch resolves. */
  cwd: string | null
  /** True while the initial CWD is loading. */
  loading: boolean
}

export const CwdContext = createContext<CwdContextValue>({
  cwd: null,
  loading: true,
})

export function CwdProvider({ children }: { children: ReactNode }) {
  const ws = useWSConnection()
  const session = useSessionStore()
  const activeSession = session.activeSession
  const [cwd, setCwd] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // Fetch CWD via Web REST API when the active session changes.
  useEffect(() => {
    if (!activeSession) {
      setCwd(null)
      setLoading(false)
      return
    }
    let cancelled = false
    setLoading(true)
    fetchCwd(activeSession)
      .then((data) => {
        if (cancelled) return
        setCwd(data.dir || null)
      })
      .catch(() => {
        if (cancelled) return
        setCwd(null)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [activeSession, ws])

  // Track CWD changes from progress events.
  useEffect(() => {
    const off = ws.onMessage((msg: WSMessage) => {
      if (msg.type !== 'progress_structured') return
      if (!matchesActiveSession(msg.chat_id, activeSession)) return
      const p = msg.progress
      if (!p) return

      // Primary: use the `cwd` field populated by the backend.
      if (typeof p.cwd === 'string' && p.cwd) {
        setCwd(p.cwd)
        return
      }

      // Fallback: detect completed Cd tool calls.
      const completed = p.completed_tools
      if (!Array.isArray(completed)) return
      for (const tool of completed) {
        if (!tool || typeof tool !== 'object') continue
        const t = tool as Record<string, unknown>
        if (t.name !== 'Cd') continue
        const args = typeof t.args === 'string' ? t.args : ''
        if (!args) continue
        try {
          const parsed = JSON.parse(args)
          const path = typeof parsed === 'string' ? parsed : parsed?.path
          if (typeof path === 'string' && path) {
            setCwd(path)
          }
        } catch {
          setCwd(args)
        }
      }
    })

    // Listen for manual CWD changes (e.g. from SessionInfo panel's PUT API).
    const onCwdChanged = (e: Event) => {
      const detail = (e as CustomEvent).detail
      if (typeof detail === 'string' && detail) setCwd(detail)
    }
    window.addEventListener('xbot:cwd-changed', onCwdChanged)

    return () => {
      off()
      window.removeEventListener('xbot:cwd-changed', onCwdChanged)
    }
  }, [activeSession, ws])

  const value = useMemo<CwdContextValue>(() => ({ cwd, loading }), [cwd, loading])

  return <CwdContext.Provider value={value}>{children}</CwdContext.Provider>
}

function matchesActiveSession(chatID: string | undefined, active: { channel: string; chatID: string } | null): boolean {
  if (!chatID || !active) return true
  return chatID === active.chatID || chatID === `${active.channel}:${active.chatID}`
}

export function useCwd(): CwdContextValue {
  return useContext(CwdContext)
}
