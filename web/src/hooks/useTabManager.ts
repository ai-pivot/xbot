import { useState, useCallback, useEffect, useRef } from 'react'

export interface Tab {
  chatId: string
  label: string
}

const STORAGE_KEY = 'xbot-open-tabs'
const ACTIVE_KEY = 'xbot-active-tab'

function loadTabs(): Tab[] {
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : []
  } catch {
    return []
  }
}

function saveTabs(tabs: Tab[]) {
  try {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(tabs))
  } catch {
    // sessionStorage may be unavailable
  }
}

function loadActiveTab(): string {
  try {
    return sessionStorage.getItem(ACTIVE_KEY) || ''
  } catch {
    return ''
  }
}

function saveActiveTab(id: string) {
  try {
    sessionStorage.setItem(ACTIVE_KEY, id)
  } catch { /* ignore */ }
}

export interface UseTabManagerReturn {
  tabs: Tab[]
  activeTabId: string
  openTab: (chatId: string, label: string) => void
  closeTab: (chatId: string) => void
  switchTab: (chatId: string) => void
  renameTab: (chatId: string, label: string) => void
  reorderTabs: (fromIndex: number, toIndex: number) => void
}

/**
 * Manages open browser-style tabs for multi-session support.
 * Persists to sessionStorage for page refresh recovery.
 */
export function useTabManager(
  onSwitchChat: (chatId: string) => void,
  onNewChat: () => void,
): UseTabManagerReturn {
  const [tabs, setTabs] = useState<Tab[]>(loadTabs)
  const [activeTabId, setActiveTabId] = useState<string>(loadActiveTab)

  // Use refs to avoid stale closures in closeTab
  const tabsRef = useRef(tabs)
  tabsRef.current = tabs
  const activeTabIdRef = useRef(activeTabId)
  activeTabIdRef.current = activeTabId

  // Persist on change
  useEffect(() => { saveTabs(tabs) }, [tabs])
  useEffect(() => { saveActiveTab(activeTabId) }, [activeTabId])

  const openTab = useCallback((chatId: string, label: string) => {
    setTabs(prev => {
      if (prev.some(t => t.chatId === chatId)) return prev
      return [...prev, { chatId, label }]
    })
    setActiveTabId(chatId)
  }, [])

  const switchTab = useCallback((chatId: string) => {
    setActiveTabId(chatId)
    onSwitchChat(chatId)
  }, [onSwitchChat])

  const closeTab = useCallback((chatId: string) => {
    // Read current state from refs to avoid stale closures
    const currentTabs = tabsRef.current
    const currentActive = activeTabIdRef.current

    const idx = currentTabs.findIndex(t => t.chatId === chatId)
    if (idx === -1) return

    const next = currentTabs.filter(t => t.chatId !== chatId)
    setTabs(next)

    // Only switch active tab if closing the active one
    if (chatId === currentActive) {
      if (next.length === 0) {
        onNewChat()
        setActiveTabId('')
      } else {
        const newActive = next[Math.min(idx, next.length - 1)]
        setActiveTabId(newActive.chatId)
        onSwitchChat(newActive.chatId)
      }
    }
  }, [onSwitchChat, onNewChat])

  const renameTab = useCallback((chatId: string, label: string) => {
    setTabs(prev => prev.map(t => t.chatId === chatId ? { ...t, label } : t))
  }, [])

  const reorderTabs = useCallback((fromIndex: number, toIndex: number) => {
    setTabs(prev => {
      if (fromIndex === toIndex) return prev
      const next = [...prev]
      const [moved] = next.splice(fromIndex, 1)
      next.splice(toIndex, 0, moved)
      return next
    })
  }, [])

  return { tabs, activeTabId, openTab, closeTab, switchTab, renameTab, reorderTabs }
}
