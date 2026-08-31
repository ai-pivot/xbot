/**
 * usePanelManager — Panel（独立卡片）管理器，与 TabManager 平行。
 *
 * Tab 和 Panel 是两个不同的概念：
 *   - Tab = 主卡片内部的标签页（多会话切换），由 TabManager 管理
 *   - Panel = 独立卡片（与主卡片并列分屏），由 PanelManager 管理
 *
 * 两个 manager 各自 bindApi，共享同一个 DockviewApi 实例。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { DockviewApi, IDockviewPanel } from 'dockview-core'
import { isMasterGroup } from '@/workspace/layoutEngine'

// ── 类型 ──────────────────────────────────────────────────────────────────────

export type PanelDirection = 'left' | 'right' | 'above' | 'below'

export interface AddPanelOptions {
  /** Dockview component 名（如 'panel', 'agent', 'file', 'terminal'） */
  component: string
  /** 面板标题 */
  title: string
  /** 面板参数 */
  params: Record<string, unknown>
  /** 分屏方向；不传 = 默认加到活跃 group */
  direction?: PanelDirection
  /** 引用面板 id（在其旁边分屏）；不传 = 默认在主 group 旁分屏 */
  referencePanelId?: string
  /** 初始宽度（像素）；Dockview addPanel 原生支持 */
  initialWidth?: number
  /** 初始高度（像素） */
  initialHeight?: number
}

export interface PanelInfo {
  /** Dockview panel id */
  id: string
  /** 面板标题 */
  title: string
  /** 面板参数中的 panelId（如 'sessions', 'files'） */
  panelKey: string
}

export interface PanelManager {
  /** 当前打开的独立 Panel 列表 */
  panels: PanelInfo[]
  /** 创建独立 Panel（卡片），直接调 api.addPanel + position，不经过 tab 系统。返回 panel id。 */
  addPanel: (options: AddPanelOptions) => string
  /** 关闭独立 Panel。 */
  removePanel: (panelId: string) => void
  /** Toggle：已打开则关闭，未打开则创建。返回操作后的 panel id（关闭时为 null）。 */
  togglePanel: (options: AddPanelOptions & { panelKey: string }) => string | null
  /** Tab → Panel：把 tab 从 tab 栏拆出为独立卡片。需要传 tab 对应的 dockview panelId。 */
  tabToPanel: (dockviewPanelId: string, direction?: PanelDirection) => void
  /** Panel → Tab：把独立卡片合并回主 group（包含 agent tabs 的 group）。 */
  panelToTab: (panelId: string) => void
  /** 检查指定 panelKey 的 Panel 是否已打开。 */
  isPanelOpen: (panelKey: string) => boolean
  /** Register the DockviewApi (called by DockviewContainer on ready). */
  bindApi: (api: DockviewApi | null) => void
}

// ── 实现 ──────────────────────────────────────────────────────────────────────

function genId(prefix: string): string {
  return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`
}

export function usePanelManager(): PanelManager {
  const apiRef = useRef<DockviewApi | null>(null)
  const [panels, setPanels] = useState<PanelInfo[]>([])

  /** 从 dockview panels 中提取 Panel（有 panelId 参数但没有 tabId 参数的 panel） */
  const resync = useCallback(() => {
    const api = apiRef.current
    if (!api) return
    const next: PanelInfo[] = []
    for (const panel of api.panels) {
      const params = panel.params as Record<string, unknown> | undefined
      if (!params?.panelId && !params?.panelKey) continue
      // 跳过 Tab（有 tabId 的是 tab，不是独立 panel）
      if (params?.tabId) continue
      next.push({
        id: panel.id,
        title: (panel.title as string) ?? '',
        panelKey: (params.panelId as string) ?? (params.panelKey as string) ?? '',
      })
    }
    setPanels(next)
  }, [])

  const bindApi = useCallback((api: DockviewApi | null) => {
    apiRef.current = api
    if (!api) return
    const offAdd = api.onDidAddPanel(resync)
    const offRemove = api.onDidRemovePanel(resync)
    resync()
    ;(apiRef as unknown as { _dispose?: () => void })._dispose = () => {
      offAdd.dispose()
      offRemove.dispose()
    }
  }, [resync])

  useEffect(() => {
    return () => {
      const disposer = (apiRef as unknown as { _dispose?: () => void })._dispose
      disposer?.()
    }
  }, [])

  const addPanel = useCallback((options: AddPanelOptions): string => {
    const api = apiRef.current
    if (!api) return ''
    const panelId = `panel-${genId('p')}`
    const addOpts: Parameters<DockviewApi['addPanel']>[0] = {
      id: panelId,
      title: options.title,
      component: options.component,
      params: options.params as never,
    }
    // Dockview addPanel 原生支持 initialWidth/initialHeight
    if (options.initialWidth) addOpts.initialWidth = options.initialWidth
    if (options.initialHeight) addOpts.initialHeight = options.initialHeight
    if (options.direction) {
      // 优先用显式传入的 referencePanelId；否则找主卡片（含 agent tab 的 group 的首个 panel）
      // 不用 api.activePanel — 上一个新 Panel 的 setActive 会把它变成 sidebar panel
      // 导致后续 Panel 相对于 sidebar 而非 Agent 分屏（"窄栏里上下分屏"根因）
      let ref: IDockviewPanel | undefined
      if (options.referencePanelId) {
        ref = api.getPanel(options.referencePanelId) ?? undefined
      } else {
        const mainGroup = api.groups.find(isMasterGroup)
        ref = mainGroup?.panels[0] ?? undefined
      }
      if (ref) {
        addOpts.position = { direction: options.direction, referencePanel: ref }
      } else {
        addOpts.position = { direction: options.direction }
      }
    }
    api.addPanel(addOpts)
    // 不调 setActive — 避免 activePanel 变成 sidebar panel 影响后续 addPanel 的 referencePanel
    return panelId
  }, [])

  const removePanel = useCallback((panelId: string) => {
    const api = apiRef.current
    const panel = api?.getPanel(panelId)
    if (!api || !panel) return
    api.removePanel(panel)
  }, [])

  const isPanelOpen = useCallback((panelKey: string): boolean => {
    const api = apiRef.current
    if (!api) return false
    return api.panels.some((p: IDockviewPanel) => {
      const params = p.params as Record<string, unknown> | undefined
      return (params?.panelId === panelKey || params?.panelKey === panelKey) && !params?.tabId
    })
  }, [])

  const togglePanel = useCallback((options: AddPanelOptions & { panelKey: string }): string | null => {
    // 已打开 → 关闭
    if (isPanelOpen(options.panelKey)) {
      const api = apiRef.current
      if (!api) return null
      const panel = api.panels.find((p: IDockviewPanel) => {
        const params = p.params as Record<string, unknown> | undefined
        return (params?.panelId === options.panelKey || params?.panelKey === options.panelKey) && !params?.tabId
      })
      if (panel) {
        api.removePanel(panel)
        return null
      }
    }
    // 未打开 → 创建
    return addPanel(options)
  }, [addPanel, isPanelOpen])

  const tabToPanel = useCallback((dockviewPanelId: string, direction: PanelDirection = 'right') => {
    const api = apiRef.current
    const panel = api?.getPanel(dockviewPanelId)
    if (!api || !panel) return
    const pos = direction === 'above' ? 'top' : direction === 'below' ? 'bottom' : direction
    panel.api.moveTo({ group: panel.group, position: pos as never })
  }, [])

  const panelToTab = useCallback((panelId: string) => {
    const api = apiRef.current
    const panel = api?.getPanel(panelId)
    if (!api || !panel) return
    // 找到主 group：包含 agent tab 的 group
    const mainGroup = api.groups.find(isMasterGroup) ?? api.activePanel?.group
    if (mainGroup) {
      panel.api.moveTo({ group: mainGroup })
    }
  }, [])

  return useMemo<PanelManager>(
    () => ({
      panels,
      addPanel,
      removePanel,
      togglePanel,
      tabToPanel,
      panelToTab,
      isPanelOpen,
      bindApi,
    }),
    [panels, addPanel, removePanel, togglePanel, tabToPanel, panelToTab, isPanelOpen, bindApi],
  )
}
