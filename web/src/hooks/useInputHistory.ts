import { useState, useEffect, useCallback, useRef } from 'react'

/**
 * Hook for browsing input history with Shift+Up/Down.
 * Matches TUI's Shift+Up/Down input history browsing behavior.
 */
export function useInputHistory(maxEntries = 100) {
  const [history, setHistory] = useState<string[]>([])
  const [browsingIndex, setBrowsingIndex] = useState(-1)
  const [draft, setDraft] = useState('')
  const historyRef = useRef(history)
  historyRef.current = history

  const addEntry = useCallback((content: string) => {
    const trimmed = content.trim()
    if (!trimmed) return
    setHistory(prev => {
      // Don't add duplicates at the top
      if (prev[0] === trimmed) return prev
      const next = [trimmed, ...prev]
      return next.length > maxEntries ? next.slice(0, maxEntries) : next
    })
    setBrowsingIndex(-1)
    setDraft('')
  }, [maxEntries])

  const navigateUp = useCallback((currentContent: string): string | null => {
    const h = historyRef.current
    if (h.length === 0) return null

    // Save current as draft if starting navigation
    if (browsingIndex === -1) {
      setDraft(currentContent)
      setBrowsingIndex(0)
      return h[0]
    }

    if (browsingIndex < h.length - 1) {
      const nextIndex = browsingIndex + 1
      setBrowsingIndex(nextIndex)
      return h[nextIndex]
    }
    return null
  }, [browsingIndex])

  const navigateDown = useCallback((): string | null => {
    if (browsingIndex === -1) return null

    if (browsingIndex === 0) {
      setBrowsingIndex(-1)
      return draft
    }

    const nextIndex = browsingIndex - 1
    setBrowsingIndex(nextIndex)
    return historyRef.current[nextIndex]
  }, [browsingIndex, draft])

  const resetBrowse = useCallback(() => {
    setBrowsingIndex(-1)
    setDraft('')
  }, [])

  // Reset browsing when component loses focus or on new entries
  useEffect(() => {
    setBrowsingIndex(-1)
  }, [history.length])

  return {
    history,
    browsingIndex,
    addEntry,
    navigateUp,
    navigateDown,
    resetBrowse,
  }
}
