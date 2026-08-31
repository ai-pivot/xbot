/**
 * useLayoutPersistence — saves and restores the workspace tab layout.
 *
 * **v2 (session-per-tab architecture)**: The per-session save/restore on
 * activeSession change is DISABLED. In the new architecture, switching tabs
 * changes activeSession (via activateSession in onDidActivePanelChange) —
 * triggering per-session layout save/restore would clear ALL agent tabs
 * (applyLayoutJSON replaces the entire dockview layout). Agent tabs are now
 * persistent (each carries its own session); work tabs (file/terminal) are
 * global (not per-session). Layout persistence is a future enhancement
 * (global save/restore on page load/unload).
 *
 * The loadLayout/saveLayout/restoreLayout utilities are kept for future use.
 */
import { useEffect, useRef } from 'react'
import { filterTerminalPanels, tabLogicalKey, type TabManager } from '@/hooks/useTabManager'
import type { useSessionStore } from '@/hooks/useSessionStore'

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

export function saveLayout(chatID: string, tabManager: TabManager, activeKey: string | null): void {
  try {
    // 完整 dockview 布局序列化（保留 group 分割/多实例 grid），filter terminal
    //（后端 PTY 禁用时跳过 terminal tab）。agent 常驻 tab 完整保留（无 sessionId，
    // 内容由 AgentPanel 读 activeSession 动态恢复）。
    const layout = filterTerminalPanels(tabManager.getLayoutJSON())
    if (!layout) return
    // Diff tab 的 original/modified 是完整文件内容（可达数百 KB）——写入
    // localStorage 会撞 5MB 上限导致整个布局保存失败，且刷新后的内容快照
    // 已失效。剥离内容只保留 tab 骨架，恢复后 DiffPanel 显示"请重新打开"
    // 提示（空内容守卫）。
    stripDiffContents(layout)
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

/** 就地剥离布局 JSON 中 diff tab 的 original/modified 大内容（tab 骨架保留）。 */
function stripDiffContents(layout: {
  panels?: Record<string, { params?: Record<string, unknown> }>
} | null): void {
  if (!layout?.panels) return
  for (const panel of Object.values(layout.panels)) {
    if (panel.params?.type === 'diff') {
      delete panel.params.original
      delete panel.params.modified
    }
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
 * A persisted layout is restorable only if its grid root is a branch with
 * array data — dockview's fromJSON throws unless BOTH hold ("root must be of
 * type branch" + Array.isArray(root.data)). Layouts persisted by the old
 * filterPanels (which promoted a single-child root to its leaf child) crash
 * on EVERY restore; they are corrupted data and must be discarded so the
 * session falls back to the default layout instead of crashing forever.
 */
function isRestorableLayout(layout: unknown): boolean {
  if (!layout || typeof layout !== 'object') return false
  const grid = (layout as { grid?: { root?: { type?: unknown; data?: unknown } } }).grid
  return grid?.root?.type === 'branch' && Array.isArray(grid.root.data)
}

/**
 * Restore the full dockview layout for a session (grid split + panels, including
 * plugin views + file tabs). The常驻 agent tab survives the fromJSON round-trip
 * because it carries no sessionId (AgentPanel reads activeSession dynamically).
 */
export function restoreLayout(tabManager: TabManager, layout: LayoutState): void {
  tabManager.applyLayoutJSON(layout.layout)
  // 恢复激活的 closable tab（fromJSON 恢复 active group，但精确激活 tab 需 setActive）。
  const activeKey = layout.activeKey ?? null
  if (activeKey) {
    const tab = tabManager.tabs.find((t) => t.closable && tabLogicalKey(t) === activeKey)
    if (tab) tabManager.setActiveTab(tab.id)
  }
}

const LAYOUT_VERSION = 'v6-cards'
const GLOBAL_LAYOUT_KEY = `xbot-layout:global:${LAYOUT_VERSION}`

/**
 * v2 layout persistence: global save/restore (not per-session).
 *
 * On page load: restore the saved dockview layout (all tabs — agent tabs
 * carry their own sessionId, so they're recreated with the correct session).
 * On page unload: save the full layout to localStorage.
 */
export function useLayoutPersistence(
  tabManager: TabManager,
  _sessionStore: ReturnType<typeof useSessionStore>,
): void {
  const tabManagerRef = useRef(tabManager)
  tabManagerRef.current = tabManager

  // Restore global layout on mount (page load).
  useEffect(() => {
    try {
      const raw = localStorage.getItem(GLOBAL_LAYOUT_KEY)
      if (!raw) return
      const parsed = JSON.parse(raw)
      if (!parsed?.layout || !isRestorableLayout(parsed.layout)) return
      tabManagerRef.current.applyLayoutJSON(parsed.layout)
    } catch {
      /* non-fatal — fall back to seed tab */
    }
  }, [])

  // Save global layout on page unload.
  useEffect(() => {
    const handler = () => {
      try {
        const layout = filterTerminalPanels(tabManagerRef.current.getLayoutJSON())
        if (!layout) return
        stripDiffContents(layout as { panels?: Record<string, { params?: Record<string, unknown> }> })
        localStorage.setItem(GLOBAL_LAYOUT_KEY, JSON.stringify({ layout }))
      } catch {
        /* non-fatal */
      }
    }
    window.addEventListener('beforeunload', handler)
    return () => window.removeEventListener('beforeunload', handler)
  }, [])
}
