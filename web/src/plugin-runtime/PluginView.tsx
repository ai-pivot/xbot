/**
 * PluginView —— 渲染单个插件 view 贡献点（供动态面板 tab 使用）。
 *
 * 与 PluginPanelContainer（渲染某容器下所有 view 叠加）不同，这里渲染
 * 由 tab 指定的单个 view，并包 ErrorBoundary —— 插件视图崩溃只塌陷
 * 该 tab，不黑屏整个应用。
 */
import { Component, useEffect, useState, type ReactNode } from 'react'

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

/** 渲染单个插件 view：动态 import 组件 + error boundary + loading 态。 */
export function PluginView({ pluginId, view }: LoadedViewProps) {
  const runtime = usePluginRuntime()
  const [Comp, setComp] = useState<React.ComponentType | null>(null)

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