/**
 * PluginPanelContainer —— 宿主组件：把贡献到某容器的插件视图渲染出来。
 *
 * UI 统一核心：桌面 RightSidebar 和移动 MobileDetail 都渲染同一个容器
 * （如 right_sidebar），本组件通过 usePluginViewPanels 动态查询该容器下
 * 所有插件 view，每个 view 用 PluginView（自带 ErrorBoundary）渲染。
 * 插件只需声明一次 view，两端自动出现——不需要分别在桌面/移动硬编码。
 */
import type { ViewContainer } from '@/plugin-api'

import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import { PluginView } from '@/plugin-runtime/PluginView'

export function PluginPanelContainer({ container, className }: { container: ViewContainer; className?: string }) {
  const panels = usePluginViewPanels(container)

  if (panels.length === 0) return null

  return (
    <div className={`flex flex-col gap-2 overflow-y-auto ${className ?? ''}`}>
      {panels.map((panel) => (
        <PluginView key={panel.id} pluginId={panel.pluginId} view={panel.view} />
      ))}
    </div>
  )
}