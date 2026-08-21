/**
 * useLeftPluginPanels —— 桌面左侧栏「用户移入的插件 view」列表。
 *
 * 左侧栏本体（SessionSidebar）是 channel/会话列表的整体区域，这里只取
 * 「用户主动移到桌面左侧 slot（desktop.activity_bar）的插件 view」，每个
 * view 由 SidebarSectionStack 渲染为独立 section（VSCode 式垂直堆叠，
 * 可拖拽调整高度、可折叠），与会话列表物理隔离。
 */
import { useEffect, useState } from 'react'

import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { useLayoutItems } from '@/plugin-runtime/layoutRegistry'
import { BUILTIN_LAYOUT_ITEMS } from '@/plugin-runtime/layoutTypes'
import type { ViewContribution } from '@/plugin-api'

export interface LeftPluginPanel {
  id: string
  pluginId: string
  title: string
  view: ViewContribution
}

export function useLeftPluginPanels(): LeftPluginPanel[] {
  const runtime = useOptionalPluginRuntime()
  const layoutItems = useLayoutItems('desktop.activity_bar')
  const [panels, setPanels] = useState<LeftPluginPanel[]>([])

  // 组合：desktop.activity_bar slot 里「非会话列表」的布局项（= 用户移入的
  // 插件 view），匹配 runtime 的 view 贡献。
  useEffect(() => {
    if (!runtime) {
      setPanels([])
      return
    }
    const recompute = () => {
      const viewIds = new Set(
        layoutItems
          .filter((it) => it.id !== BUILTIN_LAYOUT_ITEMS.desktopSessions)
          .map((it) => it.id),
      )
      const all = runtime.listAllViews()
      setPanels(
        all
          .filter(({ view }) => viewIds.has(view.id))
          .map(({ pluginId, view }) => ({ id: view.id, pluginId, title: view.title, view })),
      )
    }
    recompute()
    return runtime.subscribeViews(recompute)
  }, [runtime, layoutItems])

  return panels
}
