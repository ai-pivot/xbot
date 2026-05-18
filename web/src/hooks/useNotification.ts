import { useState, useEffect, useCallback } from 'react'

export interface UseNotificationReturn {
  /** Current notification permission state */
  permission: NotificationPermission | 'unsupported'
  /** Whether the page is currently in the background */
  isBackground: boolean
  /** Request notification permission — call from user gesture */
  requestPermission: () => Promise<boolean>
  /** Send a desktop notification (only if permission granted and page is backgrounded) */
  notify: (title: string, options?: NotificationOptions) => void
}

/**
 * Hook for browser desktop notification support.
 * Handles permission requests, background detection, and notification dispatch.
 */
export function useNotification(): UseNotificationReturn {
  const [permission, setPermission] = useState<NotificationPermission | 'unsupported'>(() => {
    if (typeof window === 'undefined' || !('Notification' in window)) return 'unsupported'
    return Notification.permission
  })
  const [isBackground, setIsBackground] = useState(() => {
    if (typeof document === 'undefined') return false
    return document.hidden
  })

  // Track visibility state
  useEffect(() => {
    const handler = () => setIsBackground(document.hidden)
    document.addEventListener('visibilitychange', handler)
    return () => document.removeEventListener('visibilitychange', handler)
  }, [])

  const requestPermission = useCallback(async (): Promise<boolean> => {
    if (!('Notification' in window)) return false
    try {
      const result = await Notification.requestPermission()
      setPermission(result)
      return result === 'granted'
    } catch {
      return false
    }
  }, [])

  const notify = useCallback((title: string, options?: NotificationOptions) => {
    if (permission !== 'granted' || !isBackground) return
    try {
      const notification = new Notification(title, {
        icon: '/favicon.svg',
        ...options,
      })
      notification.onclick = () => {
        window.focus()
        notification.close()
      }
    } catch {
      // Notification constructor may fail in some environments
    }
  }, [permission, isBackground])

  return { permission, isBackground, requestPermission, notify }
}
