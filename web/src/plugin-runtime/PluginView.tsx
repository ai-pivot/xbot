/**
 * PluginView —— 渲染单个插件 view 贡献点（供动态面板 tab 使用）。
 *
 * 与 PluginPanelContainer（渲染某容器下所有 view 叠加）不同，这里渲染
 * 由 tab 指定的单个 view，并包 ErrorBoundary —— 插件视图崩溃只塌陷
 * 该 tab，不黑屏整个应用。
 *
 * 内置视图（entry 以 builtin: 开头）**同步渲染**：它们随主 bundle 静态
 * import，直接解析组件渲染，不走 useState/useEffect 异步加载态。
 * 否则在 framer-motion 的 AnimatePresence（flushSync）时序下，异步
 * setState 切换 loading→组件会让 hook 链断裂，触发 React #311。
 */
import { Component, useEffect, useState, type ReactNode } from 'react'
import type { ComponentType } from 'react'

import { GitStatusPanel } from '@/plugins/git-info/GitStatusPanel'
import { PluginManagerPanel } from '@/plugins/manager/PluginManagerPanel'
import type { ViewContribution } from '@/plugin-api'
import { usePluginRuntime } from '@/plugin-runtime'

interface LoadedViewProps {
  view: ViewContribution
  pluginId: string
}

/** 插件视图崩溃边界：任何 render 异常只显示错误占位，不 unmount 整棵树。 */
class PluginViewErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  componentDidCatch(error: unknown) {
    console.error('[plugin-runtime] 插件视图渲染崩溃', error)
  }

  render() {
    if (this.state.failed) {
      return (
        <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-600">
          插件视图崩溃（已隔离，不影响应用）
        </div>
      )
    }
    return this.props.children
  }
}

/**
 * 内置视图同步渲染：内置插件（plugin-manager / git-info）随主 bundle 静态
 * import，故用静态 JSX（而非变量组件）直接渲染，无 useState/useEffect 异步
 * 加载态。否则在 framer-motion 的 AnimatePresence（flushSync）时序下，异步
 * setState 切换 loading→组件会让 hook 链断裂，触发 React #311。
 */
function BuiltinView({ view }: { view: ViewContribution }) {
  switch (view.id) {
    case 'xbot.plugin-manager.panel':
      return (
        <PluginViewErrorBoundary>
          <PluginManagerPanel />
        </PluginViewErrorBoundary>
      )
    case 'git-info.status':
      return (
        <PluginViewErrorBoundary>
          <GitStatusPanel />
        </PluginViewErrorBoundary>
      )
    default:
      return null
  }
}

/** 渲染单个插件 view。内置视图同步渲染；第三方插件走异步加载。 */
export function PluginView({ pluginId, view }: LoadedViewProps) {
  // 内置视图（builtin: 前缀）——同步渲染，无异步加载态。
  if (view.entry?.startsWith('builtin:')) {
    return <BuiltinView view={view} />
  }
  // 第三方插件（URL 加载）：独立组件，hooks 数量恒定，不受内置视图影响。
  return <AsyncPluginView pluginId={pluginId} view={view} />
}

function AsyncPluginView({ pluginId, view }: LoadedViewProps) {
  const runtime = usePluginRuntime()
  const [Comp, setComp] = useState<ComponentType | null>(null)

  useEffect(() => {
    let alive = true
    setComp(null)
    runtime.loadViewComponent(pluginId, view).then((c) => {
      if (alive) setComp(c)
    })
    return () => {
      alive = false
    }
  }, [runtime, pluginId, view])

  if (!Comp) {
    return <div className="animate-pulse rounded border border-slate-200 p-3 text-xs text-slate-400">加载 {view.title}…</div>
  }

  return (
    <PluginViewErrorBoundary>
      <Comp />
    </PluginViewErrorBoundary>
  )
}