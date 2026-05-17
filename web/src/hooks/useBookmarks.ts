import { useState, useCallback, useEffect } from 'react'

/* -------------------------------------------------------------------------- */
/*  Types                                                                     */
/* -------------------------------------------------------------------------- */

export interface Bookmark {
  messageId: string
  content: string
  timestamp: number
}

export interface UseBookmarksReturn {
  bookmarks: Bookmark[]
  toggleBookmark: (messageId: string, content?: string) => void
  isBookmarked: (messageId: string) => boolean
  clearAllBookmarks: () => void
}

/* -------------------------------------------------------------------------- */
/*  Constants                                                                 */
/* -------------------------------------------------------------------------- */

const STORAGE_KEY = 'xbot-bookmarks'
const MAX_BOOKMARKS = 100

/* -------------------------------------------------------------------------- */
/*  Helpers                                                                   */
/* -------------------------------------------------------------------------- */

function loadBookmarks(): Bookmark[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed
  } catch {
    return []
  }
}

function persistBookmarks(bookmarks: Bookmark[]): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(bookmarks))
  } catch {
    // localStorage full or unavailable — silently ignore
  }
}

/* -------------------------------------------------------------------------- */
/*  Hook                                                                      */
/* -------------------------------------------------------------------------- */

export function useBookmarks(): UseBookmarksReturn {
  const [bookmarks, setBookmarks] = useState<Bookmark[]>(() => loadBookmarks())

  // Sync from other tabs via storage event
  useEffect(() => {
    const handler = (e: StorageEvent) => {
      if (e.key === STORAGE_KEY) {
        setBookmarks(loadBookmarks())
      }
    }
    window.addEventListener('storage', handler)
    return () => window.removeEventListener('storage', handler)
  }, [])

  const toggleBookmark = useCallback((messageId: string, content?: string) => {
    setBookmarks(prev => {
      const idx = prev.findIndex(b => b.messageId === messageId)
      if (idx >= 0) {
        // Remove bookmark
        const next = prev.filter(b => b.messageId !== messageId)
        persistBookmarks(next)
        return next
      }
      // Add bookmark (with cap)
      const entry: Bookmark = {
        messageId,
        content: content ?? '',
        timestamp: Date.now(),
      }
      const next = [entry, ...prev].slice(0, MAX_BOOKMARKS)
      persistBookmarks(next)
      return next
    })
  }, [])

  const isBookmarked = useCallback(
    (messageId: string) => bookmarks.some(b => b.messageId === messageId),
    [bookmarks],
  )

  const clearAllBookmarks = useCallback(() => {
    setBookmarks([])
    persistBookmarks([])
  }, [])

  return { bookmarks, toggleBookmark, isBookmarked, clearAllBookmarks }
}
