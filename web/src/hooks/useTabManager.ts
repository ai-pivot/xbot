/**
 * useTabManager — workspace tab operations + state derived from Dockview.
 *
 * Dockview owns the panels; this hook wraps its imperative API and mirrors the
 * panel list into React state (`tabs`/`activeTabId`) so non-dockview UI
 * (counts, badges) can read it. There is exactly one DockviewApi per app,
 * registered by `DockviewContainer` via `bindApi`.
 *
 * Acceptance rules:
 *   - Agent tabs are not closable (closeTab is a no-op for them; the custom
 *     TabHeader also suppresses the close button for `closable=false`).
 *   - At least one Agent tab stays open.
 *   - Work tabs open in a reusable right-side group, VSCode-style.
 *
 * Why derive state from dockview rather than own it: dockview already tracks
 * panel add/remove/active transitions, drag-split, and popouts. Owning a
 * parallel list and keeping it in sync would duplicate that source of truth
 * and race on drag/drop. Deriving avoids the duplication (KISS).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { DockviewApi, IDockviewPanel } from 'dockview'
import type { Tab } from '@/types/shared'
import type { PanelParams } from '@/types/tab'

let idSeq = 0
function genId(prefix: string): string {
  idSeq += 1
  return `${prefix}-${Date.now().toString(36)}-${idSeq}`
}

/** Build a logical Tab from a dockview panel (params is the source). */
function panelToTab(panel: IDockviewPanel): Tab | null {
  const params = panel.params as PanelParams | undefined
  if (!params?.tabId) return null
  return {
    id: params.tabId,
    type: params.type,
    title: params.title,
    icon: params.icon,
    closable: params.closable,
    data:
      params.type === 'file'
          ? {
              filePath: params.filePath,
              editorId: params.editorId,
              initialLine: params.initialLine,
              initialHighlight: params.initialHighlight,
              fileLanguage: params.fileLanguage,
              fileViewMode: params.fileViewMode,
            }
        : params.type === 'agent'
          ? {
              filePath: params.sessionId,
              subAgentRole: params.subAgentRole,
              subAgentInstance: params.subAgentInstance,
              parentChatID: params.parentChatID,
              parentChannel: params.parentChannel,
              agentChatID: params.agentChatID,
            }
          : params.type === 'terminal'
            ? { terminalId: params.terminalId }
            : params.type === 'background'
              ? {
                  taskID: params.taskID,
                  command: params.command,
                  taskChannel: params.taskChannel,
                  taskChatID: params.taskChatID,
                }
              : params.type === 'plugin'
            ? {
                viewId: params.viewId,
                pluginId: params.pluginId,
                viewKey: params.viewKey,
                viewParams: params.viewParams,
              }
            : params.type === 'diff'
              ? {
                  editorId: params.editorId,
                  diffKey: params.diffKey,
                  original: params.original,
                  modified: params.modified,
                  diffPath: params.diffPath,
                  diffScope: params.diffScope,
                }
              : undefined,
  }
}

export interface TabManager {
  tabs: Tab[]
  activeTabId: string | null
  /** Open or focus a tab by logical key; returns the tab id. */
  openTab: (tab: Omit<Tab, 'id'>) => string
  /** Close a tab (agent tabs protected). */
  closeTab: (id: string) => void
  /** Focus a tab by id. */
  setActiveTab: (id: string) => void
  /** Move a tab's panel into a new group to its right (split view). */
  splitRight: (id: string) => void
  /** Forget the remembered right-side work group after a session layout swap. */
  resetWorkGroup: () => void
  /** Register the DockviewApi (called by DockviewContainer on ready). */
  bindApi: (api: DockviewApi | null) => void
  /** Serialize the full dockview layout (grid groups, panel positions, multi-instance). */
  getLayoutJSON: () => unknown
  /** Restore a dockview layout (grid + panels, including plugin views). */
  applyLayoutJSON: (layout: unknown) => void
  /** Serialize work-tab dockview layout, filtering the常驻 agent panel (agent 由 sessionStore 驱动，不随布局持久化)。 */
  getWorkLayoutJSON: () => unknown
}

export function useTabManager(): TabManager {
  const apiRef = useRef<DockviewApi | null>(null)
  // logical tabId → dockview panel id
  const panelIdByTab = useRef<Map<string, string>>(new Map())
  const rightGroupPanelIdRef = useRef<string | null>(null)
  // pending tabs queued before the API is bound (so openTab before ready works)
  const pending = useRef<Omit<Tab, 'id'>[]>([])

  const [tabs, setTabs] = useState<Tab[]>([])
  const [activeTabId, setActiveTabId] = useState<string | null>(null)

  const resync = useCallback(() => {
    const api = apiRef.current
    if (!api) return
    const nextTabs = api.panels.map(panelToTab).filter(Boolean) as Tab[]
    const nextPanelMap = new Map<string, string>()
    for (const panel of api.panels) {
      const params = panel.params as PanelParams | undefined
      if (params?.tabId) nextPanelMap.set(params.tabId, panel.id)
    }
    panelIdByTab.current = nextPanelMap
    if (rightGroupPanelIdRef.current && !api.getPanel(rightGroupPanelIdRef.current)) {
      rightGroupPanelIdRef.current = null
    }
    if (!rightGroupPanelIdRef.current) {
      const workPanel = api.panels.find((panel) => {
        const params = panel.params as PanelParams | undefined
        return params?.closable === true
      })
      rightGroupPanelIdRef.current = workPanel?.id ?? null
    }
    setTabs(nextTabs)
    const active = api.activePanel ? (api.activePanel.params as PanelParams).tabId : null
    setActiveTabId(active)
  }, [])

  const openTabInternalRef = useRef<(input: Omit<Tab, 'id'>) => string>(() => '')

  const bindApi = useCallback(
    (api: DockviewApi | null) => {
      apiRef.current = api
      if (!api) return
      const offAdd = api.onDidAddPanel(resync)
      const offRemove = api.onDidRemovePanel(resync)
      const offActive = api.onDidActivePanelChange(resync)
      // Snapshot current state and flush queued tabs.
      resync()
      const queued = pending.current
      pending.current = []
      queued.forEach((t) => openTabInternalRef.current(t))
      // Cleanup is owned by the container's effect; store disposers on the api ref.
      ;(apiRef as unknown as { _dispose?: () => void })._dispose = () => {
        offAdd.dispose()
        offRemove.dispose()
        offActive.dispose()
      }
    },
    [resync],
  )

  const openTab = useCallback((input: Omit<Tab, 'id'>): string => {
    const api = apiRef.current
    if (!api) {
      pending.current.push(input)
      return ''
    }
    const key = tabLogicalKey(input)
    // Focus an existing tab with the same logical key instead of duplicating.
    if (key) {
      for (const [tabId, panelId] of panelIdByTab.current) {
        const panel = api.getPanel(panelId)
        const params = panel?.params as PanelParams | undefined
        if (params && tabLogicalKeyFromParams(params) === key) {
          panel?.api.setActive()
          return tabId
        }
      }
    }
    const tabId = genId(input.type)
    const panelId = `dv-${tabId}`
    const params: PanelParams = {
      tabId,
      type: input.type,
      title: input.title,
      icon: input.icon,
      sessionId: input.type === 'agent' ? input.data?.filePath : undefined,
      filePath: input.type === 'file' ? input.data?.filePath : undefined,
      terminalId: input.type === 'terminal' ? input.data?.terminalId : undefined,
      closable: input.closable,
      subAgentRole: input.type === 'agent' ? input.data?.subAgentRole : undefined,
      subAgentInstance: input.type === 'agent' ? input.data?.subAgentInstance : undefined,
      parentChatID: input.type === 'agent' ? input.data?.parentChatID : undefined,
      parentChannel: input.type === 'agent' ? input.data?.parentChannel : undefined,
      agentChatID: input.type === 'agent' ? input.data?.agentChatID : undefined,
      taskID: input.type === 'background' ? input.data?.taskID : undefined,
      command: input.type === 'background' ? input.data?.command : undefined,
      taskChannel: input.type === 'background' ? input.data?.taskChannel : undefined,
      taskChatID: input.type === 'background' ? input.data?.taskChatID : undefined,
      viewId: input.type === 'plugin' ? input.data?.viewId : undefined,
      pluginId: input.type === 'plugin' ? input.data?.pluginId : undefined,
      viewKey: input.type === 'plugin' ? input.data?.viewKey : undefined,
      viewParams: input.type === 'plugin' ? input.data?.viewParams : undefined,
      diffKey: input.type === 'diff' ? input.data?.diffKey : undefined,
      original: input.type === 'diff' ? input.data?.original : undefined,
      modified: input.type === 'diff' ? input.data?.modified : undefined,
      diffPath: input.type === 'diff' ? input.data?.diffPath : undefined,
      diffScope: input.type === 'diff' ? input.data?.diffScope : undefined,
      editorId: (input.type === 'file' || input.type === 'diff') ? input.data?.editorId : undefined,
      initialLine: input.type === 'file' ? input.data?.initialLine : undefined,
      initialHighlight: input.type === 'file' ? input.data?.initialHighlight : undefined,
      fileLanguage: input.type === 'file' ? input.data?.fileLanguage : undefined,
      fileViewMode: input.type === 'file' ? input.data?.fileViewMode : undefined,
    }
    // File/work tabs open in the same group as Agent, as a sibling tab
    // (not a separate right-side column). Agent panels use renderer 'always'
    // to keep their virtual list (MessageList) mounted in the DOM — otherwise
    // dockview detaches the content when inactive, collapsing the virtualizer's
    // scroll element to 0 height and rendering 0 messages. Other panels (file,
    // terminal, background) stay on the default 'onlyWhenVisible' so heavy
    // components like Monaco editors are detached when not visible.
    //
    // Plugin tabs use component=viewId (NOT 'plugin'): DockviewContainer's
    // ReactContentRenderer looks up CONTENT_COMPONENTS[component] first and
    // falls back to the plugin view by `view.id === component` — a generic
    // 'plugin' component name would never match any view id (rendered blank).
    api.addPanel({
      id: panelId,
      title: input.title,
      component: input.type === 'plugin' ? (input.data?.viewId ?? 'plugin') : input.type,
      params,
      renderer: input.type === 'agent' ? 'always' : 'onlyWhenVisible',
    })
    panelIdByTab.current.set(tabId, panelId)
    const panel = api.getPanel(panelId)
    if (panel) {
      panel.api.setActive()
    }
    return tabId
  }, [])

  // Keep the ref in sync so bindApi can flush queued tabs through openTab.
  openTabInternalRef.current = openTab

  const closeTab = useCallback((id: string) => {
    const api = apiRef.current
    const panelId = panelIdByTab.current.get(id)
    if (!api || !panelId) return
    const panel = api.getPanel(panelId)
    if (!panel) return
    const params = panel.params as PanelParams
    if (!params.closable) return // agent tabs are not closable
    // Block closing the last agent tab.
    if (params.type === 'agent') {
      const agentCount = api.panels.filter((p) => (p.params as PanelParams).type === 'agent').length
      if (agentCount <= 1) return
    }
    panel.api.close()
    panelIdByTab.current.delete(id)
    if (rightGroupPanelIdRef.current === panelId) rightGroupPanelIdRef.current = null
  }, [])

  const setActiveTab = useCallback((id: string) => {
    const api = apiRef.current
    const panelId = panelIdByTab.current.get(id)
    const panel = panelId ? api?.getPanel(panelId) : undefined
    panel?.api.setActive()
  }, [])

  const splitRight = useCallback((id: string) => {
    const api = apiRef.current
    const panelId = panelIdByTab.current.get(id)
    const panel = panelId ? api?.getPanel(panelId) : undefined
    if (!api || !panel) return
    // Move the panel into a brand-new group to the right of its current group.
    panel.api.moveTo({ group: panel.group, position: 'right' })
    rightGroupPanelIdRef.current = panelId ?? null
  }, [])

  const resetWorkGroup = useCallback(() => {
    rightGroupPanelIdRef.current = null
  }, [])

  const getLayoutJSON = useCallback(() => apiRef.current?.toJSON() ?? null, [])
  const applyLayoutJSON = useCallback((layout: unknown) => {
    apiRef.current?.fromJSON(layout as never)
    resync()
  }, [resync])
  const getWorkLayoutJSON = useCallback(() => {
    const layout = apiRef.current?.toJSON()
    if (!layout) return null
    return filterAgentPanels(layout)
  }, [])

  // When unmounting, drop the dockview disposers we attached on bindApi.
  useEffect(() => {
    return () => {
      const disposer = (apiRef as unknown as { _dispose?: () => void })._dispose
      disposer?.()
      panelIdByTab.current.clear()
      rightGroupPanelIdRef.current = null
    }
  }, [])

  return useMemo<TabManager>(
    () => ({
      tabs,
      activeTabId,
      openTab,
      closeTab,
      setActiveTab,
      splitRight,
      resetWorkGroup,
      bindApi,
      getLayoutJSON,
      applyLayoutJSON,
      getWorkLayoutJSON,
    }),
    [tabs, activeTabId, openTab, closeTab, setActiveTab, splitRight, resetWorkGroup, bindApi, getLayoutJSON, applyLayoutJSON, getWorkLayoutJSON],
  )
}

export function tabLogicalKey(input: Pick<Tab, 'type' | 'data'>): string {
  if (input.type === 'file' && input.data?.filePath) return `file:${input.data.filePath}`
  // SubAgent tabs key on parentChatID + role + instance.
  if (input.type === 'agent' && input.data?.subAgentRole) {
    return input.data.agentChatID
      ? `agent-history:${input.data.agentChatID}`
      : `agent-subagent:${input.data.parentChannel ?? 'web'}:${input.data.parentChatID ?? ''}:${input.data.subAgentRole}:${input.data.subAgentInstance ?? ''}`
  }
  if (input.type === 'agent' && input.data?.filePath) return `agent:${input.data.filePath}`
  // Terminal tabs key on their unique frontend terminal id so each terminal
  // gets its own tab (multi-terminal). A missing terminalId → no dedup.
  if (input.type === 'terminal' && input.data?.terminalId) return `terminal:${input.data.terminalId}`
  if (input.type === 'background' && input.data?.taskID) return `background:${input.data.taskID}`
  // Plugin tabs: dynamic instances (openViewTab with key) dedup by key —
  // same view id can open MULTIPLE tabs (one per file/commit); static views
  // (activity bar) dedup by view id.
  if (input.type === 'plugin' && input.data?.viewKey) return `plugin-view:${input.data.viewKey}`
  if (input.type === 'plugin' && input.data?.viewId) return `plugin:${input.data.viewId}`
  // 原生 diff tab：按 diffKey 去重（同一文件/commit 的 diff 只开一个 tab）。
  if (input.type === 'diff' && input.data?.diffKey) return `diff:${input.data.diffKey}`
  return ''
}

export function tabLogicalKeyFromParams(p: PanelParams): string {
  if (p.type === 'file') return p.filePath ? `file:${p.filePath}` : ''
  if (p.type === 'agent' && p.subAgentRole) {
    return p.agentChatID
      ? `agent-history:${p.agentChatID}`
      : `agent-subagent:${p.parentChannel ?? 'web'}:${p.parentChatID ?? ''}:${p.subAgentRole}:${p.subAgentInstance ?? ''}`
  }
  if (p.type === 'agent') return p.sessionId ? `agent:${p.sessionId}` : ''
  if (p.type === 'terminal') return p.terminalId ? `terminal:${p.terminalId}` : ''
  if (p.type === 'background') return p.taskID ? `background:${p.taskID}` : ''
  if (p.type === 'plugin' && p.viewKey) return `plugin-view:${p.viewKey}`
  if (p.type === 'plugin') return p.viewId ? `plugin:${p.viewId}` : ''
  if (p.type === 'diff') return p.diffKey ? `diff:${p.diffKey}` : ''
  return ''
}

/**
 * 从 dockview 完整布局（api.toJSON()）中按 predicate 过滤 panel（递归处理
 * grid 树：leaf group 的 views 移除、空 group 折叠、branch 空子移除）。
 * predicate 判断一个 panel 的 params（GroupviewPanelState.params）是否应过滤。
 *
 * 不变量：grid.root 必须保持 branch 类型（dockview fromJSON 断言 "root must
 * be of type branch"）。绝不把单子 branch 提升为其子节点——历史实现把单子
 * root 提升成 leaf，持久化后每次恢复必崩（稳定崩溃 bug）。整树被过滤光时
 * 返回 null（消费方据此跳过持久化/恢复，不能产出 `{ grid: { root: null } }`）。
 *
 * 结构（dockview SerializedDockview）：
 *   { grid: { root: SerializedGridObject<GroupPanelViewState> }, panels: Record<string, GroupviewPanelState> }
 *   SerializedGridObject = { type:'leaf'|'branch', data: GroupPanelViewState | SerializedGridObject[] }
 *   GroupPanelViewState = { views: string[]（panel id 列表）, activeView?, id }
 *   GroupviewPanelState = { params: { ...PanelParams }, contentComponent, ... }
 */
export function filterPanels(layout: unknown, shouldPrune: (params: { type?: string; closable?: boolean }) => boolean): unknown {
  if (!layout || typeof layout !== 'object') return layout
  const l = layout as {
    grid?: { root?: unknown }
    panels?: Record<string, unknown>
    [k: string]: unknown
  }
  const prunedIds = new Set<string>()
  for (const [id, p] of Object.entries(l.panels ?? {})) {
    const params = (p as { params?: { type?: string; closable?: boolean } })?.params
    if (params && shouldPrune(params)) prunedIds.add(id)
  }
  if (prunedIds.size === 0) return layout

  const panels: Record<string, unknown> = {}
  for (const [id, p] of Object.entries(l.panels ?? {})) {
    if (!prunedIds.has(id)) panels[id] = p
  }

  const prune = (node: unknown): unknown | null => {
    if (!node || typeof node !== 'object') return null
    const n = node as { type?: string; data?: unknown }
    if (n.type === 'leaf') {
      const g = (n.data ?? {}) as { views?: string[]; activeView?: string }
      const views = (g.views ?? []).filter((id) => !prunedIds.has(id))
      if (views.length === 0) return null
      const activeView = g.activeView && views.includes(g.activeView) ? g.activeView : views[0]
      return { ...n, data: { ...g, views, activeView } }
    }
    // branch: drop pruned/empty children; NEVER promote a single child — the
    // root must stay a branch (dockview fromJSON invariant), and promoting
    // nested branches buys nothing (a single-child branch restores fine).
    const children = (Array.isArray(n.data) ? n.data : []).map(prune).filter((x): x is unknown => x !== null)
    if (children.length === 0) return null
    return { ...n, data: children }
  }

  const root = prune(l.grid?.root)
  if (!root) return null
  return { ...l, panels, grid: { ...l.grid, root } }
}

/**
 * 过滤常驻 agent panel（closable=false 的主 agent tab，由 sessionStore 驱动）。
 */
export function filterAgentPanels(layout: unknown): unknown {
  return filterPanels(layout, (params) => params.closable === false)
}

/** 过滤 terminal panel（后端 PTY API 禁用时跳过 terminal tab）。 */
export function filterTerminalPanels(layout: unknown): unknown {
  return filterPanels(layout, (params) => params.type === 'terminal')
}
