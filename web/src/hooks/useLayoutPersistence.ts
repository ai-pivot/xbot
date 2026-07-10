/**
 * useLayoutPersistence — saves and restores the workspace tab layout per
 * chatID in localStorage (Child 5 §3).
 *
 * When the active session changes:
 *   1. Serialize the current tab list (excluding the always-present Agent tab)
 *      to localStorage keyed by `xbot-layout:<chatID>`.
 *   2. Close all closable tabs.
 *   3. Restore the saved tab list for the new session (re-open file/terminal tabs).
 *
 * The Agent tab is never closed or saved — it's always present and follows
 * the active session. Terminal tabs are saved but may need reconnection
 * after restore (the terminal store handles this via restoreFromBackend).
 *
 * Layout state per chatID:
 *   { tabs: [{type, title, icon, closable, ...data}], activeTabId: string }
 */
import { useEffect, useRef } from 'react'
import { tabLogicalKey, type TabManager } from '@/hooks/useTabManager'
import type { useSessionStore } from '@/hooks/useSessionStore'
import { sessionKey } from '@/lib/session-grouping'

/** Serializable tab info — a subset of Tab that survives JSON round-trip. */
interface SavedTab {
  type: 'file' | 'terminal' | 'agent' | 'background'
  title: string
  icon?: string
  closable: boolean
  data?: {
    filePath?: string
    terminalId?: string
    subAgentRole?: string
    subAgentInstance?: string
    parentChatID?: string
    parentChannel?: string
    agentChatID?: string
    taskID?: string
    command?: string
    taskChannel?: string
    taskChatID?: string
  }
}

interface LayoutState {
  tabs: SavedTab[]
  activeTabId: string | null
  activeKey?: string | null
  workGroupOpen?: boolean
}

function layoutKey(chatID: string): string {
  return `xbot-layout:${chatID}`
}

function saveLayout(chatID: string, tabs: SavedTab[], activeTabId: string | null, activeKey: string | null): void {
  try {
    const state: LayoutState = {
      tabs,
      activeTabId,
      activeKey,
      workGroupOpen: tabs.length > 0,
    }
    localStorage.setItem(layoutKey(chatID), JSON.stringify(state))
  } catch {
    /* localStorage may be full or disabled — non-fatal */
  }
}

function loadLayout(chatID: string): LayoutState | null {
  try {
    const raw = localStorage.getItem(layoutKey(chatID))
    if (!raw) return null
    const parsed = JSON.parse(raw) as LayoutState
    if (!Array.isArray(parsed.tabs)) return null
    return parsed
  } catch {
    return null
  }
}

/**
 * Extract serializable tab info from the tab manager's current state.
 * Excludes the always-present Agent tab (not closable).
 */
function extractTabs(tabManager: TabManager): SavedTab[] {
  return tabManager.tabs
    .filter((t) => t.closable && t.type !== 'terminal')
    .map((t) => ({
      type: t.type,
      title: t.title,
      icon: t.icon,
      closable: t.closable,
      data: {
        filePath: t.data?.filePath,
        terminalId: t.data?.terminalId,
        subAgentRole: t.data?.subAgentRole,
        subAgentInstance: t.data?.subAgentInstance,
        parentChatID: t.data?.parentChatID,
        parentChannel: t.data?.parentChannel,
        agentChatID: t.data?.agentChatID,
        taskID: t.data?.taskID,
        command: t.data?.command,
        taskChannel: t.data?.taskChannel,
        taskChatID: t.data?.taskChatID,
      },
    }))
}

/**
 * Restore tabs for a session: close all closable tabs, then re-open saved ones.
 */
function restoreTabs(tabManager: TabManager, layout: LayoutState): void {
  // Close all closable tabs (file/terminal/SubAgent).
  const closableTabs = tabManager.tabs.filter((t) => t.closable)
  for (const t of closableTabs) {
    tabManager.closeTab(t.id)
  }
  tabManager.resetWorkGroup()
  // Re-open saved tabs. Terminal tabs are intentionally skipped while the
  // backend PTY API is disabled.
  let restoredActiveId: string | null = null
  const activeKey = layout.activeKey ?? null
  for (const tab of layout.tabs.filter((t) => t.type !== 'terminal')) {
    const tabId = tabManager.openTab({
      type: tab.type,
      title: tab.title,
      icon: tab.icon,
      closable: tab.closable,
      data: tab.data,
    })
    if (activeKey && tabLogicalKey(tab) === activeKey) restoredActiveId = tabId
  }
  if (restoredActiveId) {
    tabManager.setActiveTab(restoredActiveId)
  }
}

export function useLayoutPersistence(
  tabManager: TabManager,
  sessionStore: ReturnType<typeof useSessionStore>,
): void {
  const prevChatIDRef = useRef<string | null>(null)
  const tabManagerRef = useRef(tabManager)
  tabManagerRef.current = tabManager

  useEffect(() => {
    const currentChatID = sessionStore.activeSession ? sessionKey(sessionStore.activeSession) : null
    const prevChatID = prevChatIDRef.current

    if (currentChatID === prevChatID) return

    // Save layout for the previous session.
    if (prevChatID) {
      const mgr = tabManagerRef.current
      const tabs = extractTabs(mgr)
      const activeTab = mgr.tabs.find((tab) => tab.id === mgr.activeTabId)
      const activeKey = activeTab?.closable ? tabLogicalKey(activeTab) : null
      saveLayout(prevChatID, tabs, mgr.activeTabId, activeKey)
    }

    // Restore layout for the new session.
    if (currentChatID) {
      const layout = loadLayout(currentChatID)
      if (layout && layout.tabs.length > 0) {
        restoreTabs(tabManagerRef.current, layout)
      } else {
        // No saved layout — close all closable tabs (fresh session).
        const mgr = tabManagerRef.current
        const closable = mgr.tabs.filter((t) => t.closable)
        for (const t of closable) {
          mgr.closeTab(t.id)
        }
        mgr.resetWorkGroup()
      }
    }

    prevChatIDRef.current = currentChatID
  }, [sessionStore.activeSession, tabManager])
}
