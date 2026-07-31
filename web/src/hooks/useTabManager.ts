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
          ? { filePath: params.filePath }
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
    }
    // File/work tabs open in the same group as Agent, as a sibling tab
    // (not a separate right-side column). Agent panels use renderer 'always'
    // to keep their virtual list (MessageList) mounted in the DOM — otherwise
    // dockview detaches the content when inactive, collapsing the virtualizer's
    // scroll element to 0 height and rendering 0 messages. Other panels (file,
    // terminal, background) stay on the default 'onlyWhenVisible' so heavy
    // components like Monaco editors are detached when not visible.
    api.addPanel({
      id: panelId,
      title: input.title,
      component: input.type,
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
    }),
    [tabs, activeTabId, openTab, closeTab, setActiveTab, splitRight, resetWorkGroup, bindApi],
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
  return ''
}
