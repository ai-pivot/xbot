/**
 * usePluginViewPanels —— UI 逻辑统一的核心。
 *
 * 插件只要声明一个 view 贡献点（container + title + icon），它的面板 tab
 * 就**自动**出现在桌面侧栏和移动侧栏，不需要插件作者在两处分别 declare。
 *
 * 宿主（桌面 RightSidebar + 移动 MobileAppShell）都调用这个 hook，从
 * PluginRuntime 订阅所有 view 贡献，返回某个 container 下的面板列表。
 * view 注册/卸载（热加载/卸载插件）时列表自动更新。
 */
import { useEffect, useState } from 'react'

import type { ViewContainer, ViewContribution } from '@/plugin-api'
import { usePluginRuntime } from '@/plugin-runtime'

export interface PluginViewPanel {
  /** 面板唯一 id（= view.id）。 */
  id: string
  /** 所属插件 id。 */
  pluginId: string
  title: string
  container: ViewContainer
  view: ViewContribution
}

/**
 * 返回某容器下所有插件 view 面板（含内置 plugin-manager、git-info 等）。
 * 订阅 registry 的 view 集合变化，插件加载/热加载/卸载时自动刷新。
 */
export function usePluginViewPanels(container: ViewContainer): PluginViewPanel[] {
  const runtime = usePluginRuntime()
  const [panels, setPanels] = useState<PluginViewPanel[]>([])

  // 订阅 view 变化，变化时重算面板列表。
  useEffect(() => {
    const recompute = () => {
      setPanels(
        runtime
          .listAllViews()
          .filter(({ view }) => view.container === container)
          .map(({ pluginId, view }) => ({
            id: view.id,
            pluginId,
            title: view.title,
            container: view.container,
            view,
          })),
      )
    }
    recompute()
    const unsubscribe = runtime.subscribeViews(recompute)
    return unsubscribe
  }, [runtime, container])

  return panels
}