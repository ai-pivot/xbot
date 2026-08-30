/**
 * usePluginViewPanels —— 兼容 shim（布局 v4）。
 *
 * v4「一切皆面板」后，插件 view 贡献点由 usePluginRuntimeHost 的 syncViews
 * 自动注册进 panelRegistry。本 hook 保留旧签名与旧返回形状，存量消费点
 * （MobileAppShell / PluginPanelContainer）零改动地继续工作。
 *
 * 直接从 runtime.listAllViews() 获取（不走 panelRegistry）——手机端没有
 * usePluginRuntimeHost（panelRegistry 未 populate），必须从 runtime 读。
 * 桌面端 TopRail 走 panelRegistry（不走本 hook），两者不冲突。
 *
 * 只返回有 view 贡献点的面板——纯 ctx.panels.register 的面板没有 view 对象，
 * 由 PanelDock 直接渲染（不经本 hook）。
 */
import { useEffect, useState } from 'react'

import type { ViewContainer, ViewContribution } from '@/plugin-api'
import { useOptionalPluginRuntime } from '@/plugin-runtime'

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
 * 返回所有插件 view 面板（含内置 plugin-manager、git-info 等）。
 *
 * dynamic 视图（参数化动态视图，如 git diff / commit 详情）被过滤——它们
 * 没有静态入口，只能通过 ctx.ui.openViewTab({viewId, params}) 打开。
 *
 * 直接从 runtime.listAllViews() 获取（不走 panelRegistry）——手机端没有
 * usePluginRuntimeHost（panelRegistry 未 populate），必须从 runtime 读。
 */
export function usePluginViewPanels(container: ViewContainer): PluginViewPanel[] {
  const runtime = useOptionalPluginRuntime()
  const [panels, setPanels] = useState<PluginViewPanel[]>([])

  useEffect(() => {
    if (!runtime) {
      setPanels([])
      return
    }
    const recompute = () => {
      setPanels(
        runtime
          .listAllViews()
          .filter(({ view }) => !view.dynamic && view.container === container)
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
    const unsubscribe = runtime.subscribeViews?.(recompute) ?? (() => {})
    return unsubscribe
  }, [runtime, container])

  return panels
}
