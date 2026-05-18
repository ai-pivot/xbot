import { createContext, useContext, useState, useCallback, useRef, type ReactNode } from 'react'

export interface MediaPlayerContextValue {
  /** Register a player, returns unique ID */
  registerPlayer: () => string
  /** Unregister a player by ID */
  unregisterPlayer: (id: string) => void
  /** Current active player ID */
  activePlayerId: string | null
  /** Set the active player; calls onPause callback on the previously active player */
  setActive: (id: string, onPause?: () => void) => void
}

const MediaPlayerContext = createContext<MediaPlayerContextValue | null>(null)

let nextPlayerId = 0

export function useMediaPlayerContext(): MediaPlayerContextValue {
  const ctx = useContext(MediaPlayerContext)
  if (!ctx) throw new Error('useMediaPlayerContext must be used within MediaPlayerProvider')
  return ctx
}

export function MediaPlayerProvider({ children }: { children: ReactNode }) {
  const [activePlayerId, setActivePlayerId] = useState<string | null>(null)
  // Store pause callbacks so we can pause previous player when a new one activates
  const pauseCallbacks = useRef<Map<string, () => void>>(new Map())

  const registerPlayer = useCallback((): string => {
    const id = `media-player-${++nextPlayerId}`
    return id
  }, [])

  const unregisterPlayer = useCallback((id: string) => {
    pauseCallbacks.current.delete(id)
    setActivePlayerId(prev => (prev === id ? null : prev))
  }, [])

  const setActive = useCallback((id: string, onPause?: () => void) => {
    if (onPause) {
      pauseCallbacks.current.set(id, onPause)
    }
    setActivePlayerId(prev => {
      if (prev && prev !== id) {
        // Pause the previously active player
        const prevPause = pauseCallbacks.current.get(prev)
        if (prevPause) prevPause()
      }
      return id
    })
  }, [])

  return (
    <MediaPlayerContext.Provider value={{ registerPlayer, unregisterPlayer, activePlayerId, setActive }}>
      {children}
    </MediaPlayerContext.Provider>
  )
}
