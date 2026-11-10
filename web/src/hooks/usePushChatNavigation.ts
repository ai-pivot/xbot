import { useEffect, useEffectEvent } from 'react'

/**
 * Handles a WebPush notification click:
 * - an existing window receives { type: 'xbot:open-chat', chatId } from the SW
 * - a cold start arrives as /?chat=<id>
 *
 * The callback should actually select/open the session (not just focus the app).
 */
export function usePushChatNavigation(onOpen: (chatID: string) => void): void {
  const open = useEffectEvent(onOpen)

  useEffect(() => {
    const handleMessage = (event: MessageEvent) => {
      const data = event.data as { type?: string; chatId?: string } | undefined
      if (data?.type === 'xbot:open-chat' && data.chatId) {
        open(data.chatId)
      }
    }
    if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
      navigator.serviceWorker.addEventListener('message', handleMessage)
    }

    if (typeof window !== 'undefined') {
      const url = new URL(window.location.href)
      const chatID = url.searchParams.get('chat')
      if (chatID) {
        open(chatID)
        url.searchParams.delete('chat')
        window.history.replaceState({}, '', url.toString())
      }
    }

    return () => {
      if (typeof navigator !== 'undefined' && 'serviceWorker' in navigator) {
        navigator.serviceWorker.removeEventListener('message', handleMessage)
      }
    }
  }, [])
}
