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

/**
 * Panel 分屏方向：堆叠列在 master 卡片的哪一侧（left = 左侧，默认）。
 *
 * master/stack 平铺布局（LayoutEngine）：所有 secondary 卡片在与 master
 * 并列的堆叠列内上下排列（水平切分），LayoutEngine 按列宽（master 80%）+
 * 列内均分高度分配。direction 只决定建列位置（首张 secondary 时使用），
 * 后续 secondary 一律 'bottom' 追加到列尾（由 addPanel 内部处理）。
 */
export type PanelDirection = 'left' | 'right'

export interface AddPanelOptions {
  /** Dockview component 名（如 'panel', 'agent', 'file', 'terminal'） */
  component: string
  /** 面板标题 */
  title: string
  /** 面板参数 */
  params: Record<string, unknown>
  /** 堆叠列位置（master 左/右侧），默认 'left'。仅建列时生效，后续卡片自动追加到列尾。 */
  direction?: PanelDirection
  /** 引用面板 id（建列时在其旁边分屏）；不传 = 默认相对主 group 建列 */
  referencePanelId?: string
  /** 初始宽度（像素）；Dockview addPanel 原生支持 */
  initialWidth?: number
  /** 初始高度（像素） */
  initialHeight?: number
  /**
   * 悬浮卡片（floating）：addPanel 后转为 dockview floating group——
   * 弹窗式悬浮（拖动 handle 移动 + 拖到 grid 边缘停靠平铺）。
   * false/省略 = 平铺（master/stack 堆叠列）。
   */
  floating?: boolean
  /** 悬浮卡片宽（px，floating=true 时生效；省略 = dockview 默认） */
  floatWidth?: number
  /** 悬浮卡片高（px，floating=true 时生效；省略 = dockview 默认） */
  floatHeight?: number
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

    // 悬浮卡片（floating）：addPanel 独立位置（不进堆叠列）→ addFloatingGroup
    // 转出 grid——弹窗式悬浮卡片（dockview 原生：拖 handle 移动 + 拖到 grid
    // 边缘停靠平铺）。位置/尺寸由 FloatingGroupOptions 控制。
    if (options.floating) {
      addOpts.position = { direction: 'right' }
      api.addPanel(addOpts)
      const panel = api.getPanel(panelId)
      if (panel) {
        api.addFloatingGroup(panel, {
          ...(options.floatWidth ? { width: options.floatWidth } : {}),
          ...(options.floatHeight ? { height: options.floatHeight } : {}),
        })
      }
      return panelId
    }

    // Panel（sidebar 卡片）进 master 旁的堆叠列，卡片上下排列（水平切分）：
    // - 堆叠列已存在（有非 master group）→ 'bottom' 相对列内最后一个 group
    //   追加。dockview grid 语义（getRelativeLocation）：'bottom' 相对 root 层
    //   secondary 建嵌套 VERTICAL branch（首张建列），相对嵌套层 secondary
    //   同层追加 —— 堆叠列由此自然形成
    // - 堆叠列不存在 → options.direction（默认 'left'，列在 master 哪侧）
    //   相对 master group 建列。不用 api.activePanel — 上一个新 Panel 的
    //   setActive 会把它变成 sidebar panel 导致建列位置错误
    const secondaryGroups = api.groups.filter((g) => !isMasterGroup(g))
    if (secondaryGroups.length > 0) {
      const stackTail = secondaryGroups[secondaryGroups.length - 1]
      addOpts.position = { direction: 'below', referencePanel: stackTail.panels[0] }
    } else {
      let ref: IDockviewPanel | undefined
      if (options.referencePanelId) {
        ref = api.getPanel(options.referencePanelId) ?? undefined
      } else {
        const mainGroup = api.groups.find(isMasterGroup)
        ref = mainGroup?.panels[0] ?? undefined
      }
      const direction = options.direction ?? 'left'
      if (ref) {
        addOpts.position = { direction, referencePanel: ref }
      } else {
        addOpts.position = { direction }
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
    // 水平切分（left/right）；垂直切分被 PanelDirection 类型禁止
    panel.api.moveTo({ group: panel.group, position: direction })
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
