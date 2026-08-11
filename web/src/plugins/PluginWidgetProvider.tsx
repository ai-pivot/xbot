/**
 * PluginWidgetProvider — tracks plugin web widget content (web_widgets SSE).
 *
 * Backend (WebChannel.NotifyWidgetsUpdated) renders structured widget zones
 * per session and pushes them as `web_widgets` messages. This provider stores
 * the zones for the ACTIVE session and exposes them via `usePluginWidgets()`.
 *
 * Zone names match the TUI widget system: titleBarLeft/Right, statusBarLeft/
 * Right, infoBar, footer, toolHint. Each zone is a list of WebWidgetSpan
 * (text + semantic style) that WidgetZone renders with Tailwind colors.
 */
import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'

import { useWSConnection } from '@/hooks/useWSConnection'
import { useSessionStore } from '@/hooks/useSessionStore'
import type { WebUIComponentDecl, WebWidgetZones } from '@/types/shared'

export interface PluginWidgetsContextValue {
  /** Structured widget zones for the active session (zone → spans). */
  zones: WebWidgetZones
  /** Declarative web UI components for the active session (web_ui protocol). */
  components: WebUIComponentDecl[]
  /** Monotonic revision from the backend (incremental merge). */
  revision: number
}

export const PluginWidgetsContext = createContext<PluginWidgetsContextValue>({
  zones: {},
  components: [],
  revision: 0,
})

export function PluginWidgetProvider({ children }: { children: ReactNode }) {
  const ws = useWSConnection()
  const session = useSessionStore()
  const activeSession = session.activeSession
  const [zones, setZones] = useState<WebWidgetZones>({})
  const [components, setComponents] = useState<WebUIComponentDecl[]>([])
  const [revision, setRevision] = useState(0)
  const activeRef = useRef(activeSession)
  activeRef.current = activeSession

  // Subscribe to web_widgets messages for the active session.
  useEffect(() => {
    if (!activeSession) return
    const listenerChannel = activeSession.channel
    const off = ws.onMessage((msg) => {
      if (msg.type !== 'web_widgets' && msg.type !== 'web_ui') return
      const cur = activeRef.current
      if (!cur) return
      if (msg.chat_id && cur.chatID !== msg.chat_id) return
      if (listenerChannel !== cur.channel) return
      if (!msg.content) return
      try {
        const payload = JSON.parse(msg.content) as {
          zones?: WebWidgetZones
          components?: WebUIComponentDecl[]
          revision?: number
        }
        if (payload.zones) {
          setZones(payload.zones)
          if (typeof payload.revision === 'number') setRevision(payload.revision)
        }
        if (Array.isArray(payload.components)) {
          setComponents(payload.components)
        }
      } catch {
        // Malformed payload — ignore, keep last good state.
      }
    })
    return off
  }, [activeSession, ws])

  // Reset on session switch (avoid cross-session leak).
  // Initial full snapshot is pushed by the backend when the client subscribes
  // (WebChannel pushes web_widgets on connect); here we only clear stale state.
  useEffect(() => {
    setZones({})
    setComponents([])
  }, [activeSession?.chatID, activeSession?.channel])

  const value = useMemo(
    () => ({ zones, components, revision }),
    [zones, components, revision],
  )
  return <PluginWidgetsContext.Provider value={value}>{children}</PluginWidgetsContext.Provider>
}

export function usePluginWidgets(): PluginWidgetsContextValue {
  return useContext(PluginWidgetsContext)
}
