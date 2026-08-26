import { useEffect, useRef } from 'react'

import type { WSConnection } from '@/types/ws'

interface ActiveSSESubscriptionOptions {
  ws: WSConnection
  chatID: string | null
  channel: string
  /** Controls whether the SSE subscription is active. Only visible panels
   * (active tab in a group, or split-view sibling) subscribe to SSE —
   * invisible panels (behind another tab) disconnect to save bandwidth. */
  active?: boolean
}

/**
 * Visibility-aware SSE subscription manager for Agent panels.
 *
 * Each Agent panel calls this hook with its chatID+channel. The hook creates
 * an SSE subscription via `ws.addSubscription()` **only when the panel is
 * visible** (`active=true`). When the panel becomes invisible (user switches
 * to another tab in the same group), the subscription is removed — the SSE
 * connection is closed, stopping all traffic for that session.
 *
 * When the panel becomes visible again, a new subscription is created. The
 * SSE connection uses the `last_event_id` cursor (stored per-session in
 * webCache) so the server replays missed events. The caller (AgentPanel)
 * also triggers a history reload via the `wasSubscribed` effect to catch
 * any committed messages that were lost during the disconnect.
 *
 * Split view: both panels are visible → both subscribe (MultiSSEManager
 * creates one SSEConnectionImpl per (chatID, channel) pair).
 */
export function useActiveSSESubscription({
  ws,
  chatID,
  channel,
  active = true,
}: ActiveSSESubscriptionOptions): void {
  const subIDRef = useRef<string | null>(null)
  // Hold ws in a ref — its methods (addSubscription/removeSubscription) delegate
  // to a stable MultiSSEManager instance (useRef in WSProvider), so we don't need
  // ws in the effect deps. Including ws would cause an infinite loop:
  // connected changes → ws identity changes → effect re-runs → cleanup removes
  // subscription → SSE disconnects → connected changes → ...
  const wsRef = useRef(ws)
  wsRef.current = ws

  useEffect(() => {
    // Clean up previous subscription if chatID/channel changed or panel became
    // invisible.
    if (subIDRef.current !== null) {
      wsRef.current.removeSubscription(subIDRef.current)
      subIDRef.current = null
    }

    // Only subscribe when the panel is visible AND we have a chatID.
    if (!chatID || !active) return

    const id = wsRef.current.addSubscription(chatID, channel)
    subIDRef.current = id

    return () => {
      if (subIDRef.current !== null) {
        wsRef.current.removeSubscription(subIDRef.current)
        subIDRef.current = null
      }
    }
  }, [chatID, channel, active])
}
