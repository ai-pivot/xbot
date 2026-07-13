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

  useEffect(() => {
    // Clean up previous subscription if chatID/channel changed.
    if (subIDRef.current !== null) {
      ws.removeSubscription(subIDRef.current)
      subIDRef.current = null
    }

    if (!chatID) return

    const id = ws.addSubscription(chatID, channel)
    subIDRef.current = id

    return () => {
      if (subIDRef.current !== null) {
        ws.removeSubscription(subIDRef.current)
        subIDRef.current = null
      }
    }
  }, [ws, chatID, channel])
}
