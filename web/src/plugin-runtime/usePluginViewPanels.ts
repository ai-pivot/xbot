/**
 * usePluginViewPanels —— 兼容 shim（布局 v4）。
 *
 * v4「一切皆面板」后，插件 view 贡献点由 usePluginRuntimeHost 的 syncViews
 * 自动注册进 panelRegistry（id 沿用 view.id）。本 hook 保留旧签名与旧返回
 * 形状，存量消费点（MobileAppShell / PluginPanelContainer）零改动地继续工作：
 * 面板列表改从 panelRegistry 读取，view 对象仍从 runtime.listAllViews() 匹配
 * （消费方需要 view.icon / view.align / <PluginView view>）。
 *
 * 只返回有 view 贡献点的面板——纯 ctx.panels.register 的面板没有 view 对象，
 * 由 PanelDock 直接渲染（不经本 hook）。
 */
import { useEffect, useState } from 'react'

import type { ViewContainer, ViewContribution } from '@/plugin-api'
import { useOptionalPluginRuntime } from '@/plugin-runtime'
import { panelRegistry } from '@/plugin-runtime/panelRegistry'

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
 * 订阅 panelRegistry 变化（插件加载/热加载/卸载时自动刷新）。
 *
 * dynamic 视图（参数化动态视图，如 git diff / commit 详情）被过滤——它们
 * 没有静态入口，只能通过 ctx.ui.openViewTab({viewId, params}) 打开。
 */
export function usePluginViewPanels(container: ViewContainer): PluginViewPanel[] {
  const runtime = useOptionalPluginRuntime()
  const [panels, setPanels] = useState<PluginViewPanel[]>([])

  // 订阅 panelRegistry 集合变化，变化时重算面板列表（re-read registry）。
  useEffect(() => {
    const recompute = () => {
      const views = new Map<string, { pluginId: string; view: ViewContribution }>()
      if (runtime) {
        for (const { pluginId, view } of runtime.listAllViews()) {
          if (!view.dynamic) views.set(view.id, { pluginId, view })
        }
      }
      setPanels(
        panelRegistry
          .listPanels()
          .filter((p) => p.source && p.source !== 'core')
          .flatMap((p) => {
            const hit = views.get(p.id)
            // ⚠️ 必须按 container 过滤——v4 shim 一度丢失此过滤，导致
            // status_bar_right 等无插件声明的容器全量命中：PluginPanelContainer
            // 在 header/顶栏渲染完整面板内容（skill 列表等）→ 全屏遮挡主视图
            // （用户报告的 P0：电脑全屏插件列表、手机叠三层）。
            if (!hit || hit.view.container !== container) return []
            // 布局 v5：徽章面板（bar 类容器贡献，location.zone 非 side）已并入
            // 徽章形态——不再作为容器面板暴露：PluginPanelContainer(status_bar_right)
            // 等旧直渲染点返回空，徽章由 rail 渲染点消费 def.badgeRender。
            if (p.location && p.location.zone !== 'side') return []
            return [{
              id: p.id,
              pluginId: hit.pluginId,
              title: p.title,
              container: hit.view.container,
              view: hit.view,
            }]
          }),
      )
    }
    recompute()
    const unsubscribe = panelRegistry.subscribePanels(recompute)
    return unsubscribe
  }, [runtime, container])

  return panels
}
