/**
 * RightSidebar — the right panel container.
 *
 * VSCode-style right sidebar:
 *   - collapsed by default (activePanel === null ⇒ not rendered; the right
 *     ActivityBar column stays)
 *   - selecting a panel expands to 280px
 *   - a drag handle resizes between 200–500px
 *   - panels cross-fade via Framer Motion AnimatePresence
 *
 * The container is a pure layout/animation shell; each panel is its own
 * component (FileExplorer, FileSearch, SessionInfo). The shared
 * tabManager is passed down so the file browser/search can open file tabs in
 * the same Dockview instance.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { AnimatePresence, motion } from 'framer-motion'

import { useI18n } from '@/providers/i18n'
import { FileExplorer } from './FileExplorer'
import { FileSearch } from './FileSearch'
import { SessionInfo } from './SessionInfo'
import { TasksPanel } from './TasksPanel'
import { TerminalList } from './TerminalList'
import { PluginView } from '@/plugin-runtime/PluginView'
import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import { useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS } from '@/plugin-runtime/layoutTypes'
import type { TabManager } from '@/hooks/useTabManager'
import { useTerminal } from '@/hooks/useTerminal'

/**
 * 内置侧栏面板（宿主固定功能），与插件 view 贡献点是两回事。
 * 插件 view 的 id 直接作为动态面板 id 出现在侧栏 —— 插件声明一次，
 * 桌面 + 移动两端自动都有该面板 tab，无需分别 contribute。
 */
export type BuiltinSidebarPanel = 'files' | 'search' | 'info' | 'tasks' | 'terminal'

export type SidebarPanel = string

export interface RightSidebarProps {
  activePanel: SidebarPanel | null
  tabManager: TabManager
}

const MIN_WIDTH = 200
const MAX_WIDTH = 500
const RIGHT_RATIO = 0.26

// 内置面板 → 布局项 id 映射（布局注册表里的 desktop.sidebar 项）。
const BUILTIN_PANEL_TO_LAYOUT: Record<BuiltinSidebarPanel, string> = {
  files: BUILTIN_LAYOUT_ITEMS.desktopFiles,
  search: BUILTIN_LAYOUT_ITEMS.desktopSearch,
  info: BUILTIN_LAYOUT_ITEMS.desktopInfo,
  tasks: BUILTIN_LAYOUT_ITEMS.desktopTasks,
  terminal: BUILTIN_LAYOUT_ITEMS.desktopTerminal,
}

export function RightSidebar({ activePanel, tabManager }: RightSidebarProps) {
  const { t } = useI18n()
  const [width, setWidth] = useState(() => adaptiveRightWidth())
  const dragging = useRef(false)
  const userSized = useRef(false)
  const terminalManager = useTerminal(tabManager)

  // 插件 view 贡献点（right_sidebar 容器）——动态面板 tab 的唯一来源。
  const pluginViews = usePluginViewPanels('right_sidebar')
  const pluginViewsMap: PluginViewMap = new Map(
    pluginViews.map((p) => [p.id, { pluginId: p.pluginId, view: p.view }]),
  )
  // 布局配置：面板被用户移出 desktop.sidebar slot 时不渲染。
  const layoutItems = useLayoutItems('desktop.sidebar')
  const layoutEnabledIds = new Set(layoutItems.map((i) => i.id))

  // 布局过滤：内置面板 id 映射到布局项 id（插件 view id 即布局项 id）。
  const isPanelEnabled = (panel: SidebarPanel): boolean => {
    const layoutId = panel in BUILTIN_PANEL_TO_LAYOUT ? BUILTIN_PANEL_TO_LAYOUT[panel as BuiltinSidebarPanel] : panel
    return layoutEnabledIds.has(layoutId)
  }

  // Pointer-based resize: hold the handle, move the pointer, clamp to bounds.
  const onPointerDown = useCallback((e: React.PointerEvent) => {
    e.preventDefault()
    dragging.current = true
    document.body.style.userSelect = 'none'
  }, [])

  useEffect(() => {
    const onMove = (e: PointerEvent) => {
      if (!dragging.current) return
      userSized.current = true
      // Sidebar is on the right edge; width grows as the pointer moves left.
      const right = window.innerWidth - e.clientX
      const next = clampRightWidth(right)
      setWidth(Math.round(next))
    }
    const onUp = () => {
      if (!dragging.current) return
      dragging.current = false
      document.body.style.userSelect = ''
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
    return () => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
    }
  }, [])

  useEffect(() => {
    const onResize = () => {
      setWidth((current) => userSized.current ? clampRightWidth(current) : adaptiveRightWidth())
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  // The aside is always mounted; it animates width between 0 (collapsed) and
  // `width` (expanded) so collapse/expand is smooth, not instant. Content is
  // rendered only while expanded to avoid offscreen work and stale panels.
  const targetWidth = activePanel === null ? 0 : width
  const panel = activePanel

  return (
    <motion.aside
      initial={false}
      animate={{ width: targetWidth, opacity: activePanel === null ? 0 : 1 }}
      transition={{ duration: 0.18, ease: 'easeOut' }}
      className="absolute right-12 top-0 z-40 flex h-full shrink-0 flex-col overflow-hidden bg-bg-secondary shadow-xl"
      style={{ borderLeftWidth: activePanel === null ? 0 : 1, borderLeftStyle: 'solid', borderLeftColor: 'var(--border)' }}
    >
      {panel !== null && isPanelEnabled(panel) && (
        <>
          <header className="flex h-9 shrink-0 items-center justify-between pl-3 pr-2 text-xs font-semibold uppercase tracking-wide text-text-secondary">
            <span className="truncate">{titleFor(panel, pluginViewsMap, t)}</span>
          </header>

          {/* Panel content cross-fade keyed on the active panel. */}
          <div className="relative min-h-0 flex-1">
            <AnimatePresence mode="wait" initial={false}>
              <motion.div
                key={panel}
                initial={{ opacity: 0, x: 10 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -10 }}
                transition={{ duration: 0.15 }}
                className="h-full"
              >
                {renderPanel(panel, tabManager, pluginViewsMap, terminalManager)}
              </motion.div>
            </AnimatePresence>
          </div>

          {/* Drag handle to resize the sidebar (left edge). */}
          <div
            role="separator"
            aria-orientation="vertical"
            aria-label={t('sidebar.resizeLabel')}
            onPointerDown={onPointerDown}
            className="absolute left-0 top-0 h-full w-1 cursor-col-resize bg-transparent transition-colors hover:bg-app-accent/40"
          />
        </>
      )}
    </motion.aside>
  )
}

function adaptiveRightWidth(): number {
  if (typeof window === 'undefined') return 300
  return clampRightWidth(window.innerWidth * RIGHT_RATIO)
}

function clampRightWidth(width: number): number {
  const viewportMax = typeof window === 'undefined' ? MAX_WIDTH : Math.max(MIN_WIDTH, Math.min(MAX_WIDTH, window.innerWidth * 0.42))
  return Math.round(Math.max(MIN_WIDTH, Math.min(viewportMax, width)))
}

function renderPanel(
  panel: SidebarPanel,
  tabManager: TabManager,
  pluginViews: PluginViewMap,
  terminalManager?: ReturnType<typeof useTerminal>,
) {
  switch (panel) {
    case 'files':
      return <FileExplorer tabManager={tabManager} />
    case 'search':
      return <FileSearch tabManager={tabManager} />
    case 'info':
      return <SessionInfo tabManager={tabManager} />
    case 'tasks':
      return <TasksPanel tabManager={tabManager} />
    case 'terminal':
      return terminalManager ? <TerminalList terminalManager={terminalManager} /> : null
    default: {
      // 插件 view 贡献点：id 即面板 id，声明一次两端自动出现。
      const entry = pluginViews.get(panel)
      if (!entry) return null
      return <PluginView pluginId={entry.pluginId} view={entry.view} />
    }
  }
}

function titleFor(
  panel: SidebarPanel,
  pluginViews: PluginViewMap,
  t: (k: string) => string,
): string {
  switch (panel) {
    case 'files':
      return t('sidebar.files')
    case 'search':
      return t('sidebar.search')
    case 'info':
      return t('sidebar.info')
    case 'tasks':
      return t('sidebar.tasks')
    case 'terminal':
      return t('sidebar.terminal')
    default:
      return pluginViews.get(panel)?.view.title ?? panel
  }
}

/** 插件 view id → 渲染信息 的查找表（由 usePluginViewPanels 派生）。 */
export type PluginViewMap = Map<string, { pluginId: string; view: import('@/plugin-api').ViewContribution }>
