import { useEffect } from 'react'

import type { WSConnection } from '@/types/ws'

interface ActiveSSESubscriptionOptions {
  ws: WSConnection
  chatID: string | null
  channel: string
  active: boolean
}

/** The focused Agent panel is the sole owner of the app's EventSource target. */
export function useActiveSSESubscription({
  ws,
  chatID,
  channel,
  active,
}: ActiveSSESubscriptionOptions): void {
  useEffect(() => {
    if (!active) return
    if (!chatID) {
      ws.disconnect()
      return
    }
    ws.subscribe(chatID, channel)
  }, [active, channel, chatID, ws])
}
