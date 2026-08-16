/**
 * PluginPanelContainer —— 宿主组件：把贡献到某容器的插件视图渲染出来。
 *
 * 用 usePluginRuntime() 查询 registry（getViewsByContainer）并按 entry
 * 加载组件（loadViewComponent），挂到 ViewSlot。这是插件视图与宿主布局
 * 的接缝——任何声明 container 的插件 view 都会在此渲染。
 */
import { useCallback } from 'react'

import type { ViewContainer } from '@/plugin-api'
import { usePluginRuntime } from '@/plugin-runtime'
import { ViewSlot } from '@/plugin-runtime/ViewSlot'

export function PluginPanelContainer({ container }: { container: ViewContainer }) {
  const runtime = usePluginRuntime()

  const getViews = useCallback(
    () => runtime.getViewsByContainer(container),
    [runtime, container],
  )

  const loadView = useCallback(
    (view: import('@/plugin-api').ViewContribution) => {
      // 视图属于哪个插件？从贡献点反查（view.id 形如 "<pluginId>.<viewId>"）。
      // 简化：取贡献点 id 的第一段作为插件 id（约定）。
      const pluginId = view.id.split('.')[0]
      return runtime.loadViewComponent(pluginId, view)
    },
    [runtime],
  )

  return <ViewSlot container={container} getViews={getViews} loadView={loadView} className="h-full" />
}
