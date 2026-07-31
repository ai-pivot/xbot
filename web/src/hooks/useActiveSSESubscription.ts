import { useEffect, useRef } from 'react'

import type { WSConnection } from '@/types/ws'

interface ActiveSSESubscriptionOptions {
  ws: WSConnection
  chatID: string | null
  channel: string
  /** Kept for API compatibility — no longer controls SSE subscription. */
  active?: boolean
}

/**
 * Dynamic SSE subscription manager for concurrent Agent panels.
 *
 * Each Agent panel calls this hook with its chatID+channel. The hook creates a
 * persistent SSE subscription via `ws.addSubscription()` that stays alive until
 * the panel unmounts (closes). Switching the active tab does NOT disconnect
 * the non-active panel's SSE stream — all open panels receive their events
 * simultaneously.
 *
 * The `active` parameter is kept for backward API compatibility but no longer
 * affects SSE behavior. Subscriptions are always created when a chatID is
 * available.
 */
export function useActiveSSESubscription({
  ws,
  chatID,
  channel,
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
    // Clean up previous subscription if chatID/channel changed.
    if (subIDRef.current !== null) {
      wsRef.current.removeSubscription(subIDRef.current)
      subIDRef.current = null
    }

    if (!chatID) return

    const id = wsRef.current.addSubscription(chatID, channel)
    subIDRef.current = id

    return () => {
      if (subIDRef.current !== null) {
        wsRef.current.removeSubscription(subIDRef.current)
        subIDRef.current = null
      }
    }
  }, [chatID, channel])
}
