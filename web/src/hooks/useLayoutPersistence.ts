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
import { filterTerminalPanels, tabLogicalKey, type TabManager } from '@/hooks/useTabManager'
import type { useSessionStore } from '@/hooks/useSessionStore'
import { sessionKey } from '@/lib/session-grouping'

/** Serializable tab info — a subset of Tab that survives JSON round-trip. */
interface LayoutState {
  /** 完整 dockview 布局（含多组 grid 位置，terminal 已过滤）。 */
  layout: unknown
  activeKey?: string | null
  workGroupOpen?: boolean
}

function layoutKey(chatID: string): string {
  return `xbot-layout:${chatID}`
}

function saveLayout(chatID: string, tabManager: TabManager, activeKey: string | null): void {
  try {
    // 完整 dockview 布局序列化（保留 group 分割/多实例 grid），filter terminal
    //（后端 PTY 禁用时跳过 terminal tab）。agent 常驻 tab 完整保留（无 sessionId，
    // 内容由 AgentPanel 读 activeSession 动态恢复）。
    const layout = filterTerminalPanels(tabManager.getLayoutJSON())
    if (!layout) return
    const state: LayoutState = {
      layout,
      activeKey,
      workGroupOpen: activeKey !== null,
    }
    localStorage.setItem(layoutKey(chatID), JSON.stringify(state))
  } catch {
    /* localStorage may be full or disabled — non-fatal */
  }
}

export function loadLayout(chatID: string): LayoutState | null {
  try {
    const raw = localStorage.getItem(layoutKey(chatID))
    if (!raw) return null
    const parsed = JSON.parse(raw) as LayoutState
    if (!isRestorableLayout(parsed.layout)) return null
    return parsed
  } catch {
    return null
  }
}

/**
 * A persisted layout is restorable only if its grid root is a branch —
 * dockview's fromJSON asserts "root must be of type branch" and throws
 * otherwise. Layouts persisted by the old filterPanels (which promoted a
 * single-child root to its leaf child) crash on EVERY restore; they are
 * corrupted data and must be discarded so the session falls back to the
 * default layout instead of crashing forever.
 */
function isRestorableLayout(layout: unknown): boolean {
  if (!layout || typeof layout !== 'object') return false
  const grid = (layout as { grid?: { root?: { type?: unknown } } }).grid
  return grid?.root?.type === 'branch'
}

/**
 * Restore the full dockview layout for a session (grid split + panels, including
 * plugin views + file tabs). The常驻 agent tab survives the fromJSON round-trip
 * because it carries no sessionId (AgentPanel reads activeSession dynamically).
 */
function restoreLayout(tabManager: TabManager, layout: LayoutState): void {
  tabManager.applyLayoutJSON(layout.layout)
  // 恢复激活的 closable tab（fromJSON 恢复 active group，但精确激活 tab 需 setActive）。
  const activeKey = layout.activeKey ?? null
  if (activeKey) {
    const tab = tabManager.tabs.find((t) => t.closable && tabLogicalKey(t) === activeKey)
    if (tab) tabManager.setActiveTab(tab.id)
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
      const activeTab = mgr.tabs.find((tab) => tab.id === mgr.activeTabId)
      const activeKey = activeTab?.closable ? tabLogicalKey(activeTab) : null
      saveLayout(prevChatID, mgr, activeKey)
    }

    // Restore layout for the new session.
    if (currentChatID) {
      const layout = loadLayout(currentChatID)
      if (layout && layout.layout) {
        restoreLayout(tabManagerRef.current, layout)
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
