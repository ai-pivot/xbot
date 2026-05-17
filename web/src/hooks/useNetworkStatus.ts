import { useState, useEffect, useCallback, useRef } from 'react'

export interface NetworkStatus {
  /** Whether the browser reports being online */
  online: boolean
  /** Whether WebSocket is connected */
  wsConnected: boolean
  /** Whether currently reconnecting */
  wsReconnecting: boolean
  /** Server explicitly stopped (e.g. shutdown) */
  serverStopped: boolean
}

/**
 * Hook to monitor network status (browser online/offline) and
 * combine it with WebSocket connection state.
 */
export function useNetworkStatus(wsConnected: boolean, wsReconnecting: boolean, serverStopped: boolean): NetworkStatus {
  const [online, setOnline] = useState<boolean>(() => typeof navigator !== 'undefined' ? navigator.onLine : true)

  useEffect(() => {
    const handleOnline = () => setOnline(true)
    const handleOffline = () => setOnline(false)

    window.addEventListener('online', handleOnline)
    window.addEventListener('offline', handleOffline)

    return () => {
      window.removeEventListener('online', handleOnline)
      window.removeEventListener('offline', handleOffline)
    }
  }, [])

  return { online, wsConnected, wsReconnecting, serverStopped }
}

/**
 * Show a toast when network status changes.
 */
export function useNetworkToasts(
  online: boolean,
  _wsConnected: boolean,
  showToast: (msg: string, type?: 'info' | 'error' | 'success') => void,
): void {
  const prevOnlineRef = useRef(online)

  useEffect(() => {
    if (!prevOnlineRef.current && online) {
      showToast('📶 网络已恢复', 'success')
    } else if (prevOnlineRef.current && !online) {
      showToast('📶 网络已断开，部分功能不可用', 'error')
    }
    prevOnlineRef.current = online
  }, [online, showToast])
}

/**
 * Simple retry helper: retries a function with exponential backoff.
 */
export function useRetry() {
  const retry = useCallback(async <T,>(
    fn: () => Promise<T>,
    maxAttempts: number = 3,
    baseDelay: number = 1000,
  ): Promise<T> => {
    let lastError: Error | undefined
    for (let attempt = 0; attempt < maxAttempts; attempt++) {
      try {
        return await fn()
      } catch (err) {
        lastError = err instanceof Error ? err : new Error(String(err))
        if (attempt < maxAttempts - 1) {
          const delay = baseDelay * Math.pow(2, attempt) + Math.random() * 500
          await new Promise(resolve => setTimeout(resolve, delay))
        }
      }
    }
    throw lastError
  }, [])

  return retry
}
