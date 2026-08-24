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
  /** dockview panel params（含 viewParams）——动态视图的参数经 props 传给插件组件。 */
  panelParams?: { viewParams?: Record<string, unknown>; title?: string; viewId?: string; viewKey?: string }
}

/** 插件视图崩溃边界：任何 render 异常只显示错误占位，不 unmount 整棵树。
 * 错误详情（message + componentStack）直接渲染在界面上，方便截图诊断。 */
class PluginViewErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean; message: string; stack: string }
> {
  state = { failed: false, message: '', stack: '' }

  static getDerivedStateFromError(error: unknown) {
    const message = error instanceof Error ? error.message : String(error)
    return { failed: true, message, stack: '' }
  }

  componentDidCatch(error: unknown, info: unknown) {
    const componentStack =
      typeof info === 'object' && info !== null && 'componentStack' in info
        ? String((info as { componentStack: string }).componentStack)
        : ''
    console.error('[plugin-runtime] 插件视图渲染崩溃', error, info)
    this.setState({ message: error instanceof Error ? error.message : String(error), stack: componentStack })
  }

  render() {
    if (this.state.failed) {
      return (
        <div className="rounded border border-red-200 bg-red-50 p-2 font-mono text-[10px] leading-relaxed text-red-700">
          <div className="font-sans text-xs font-semibold">插件视图崩溃（已隔离，不影响应用）</div>
          {this.state.message && (
            <pre className="mt-1 whitespace-pre-wrap break-all">{this.state.message}</pre>
          )}
          {this.state.stack && (
            <pre className="mt-1 whitespace-pre-wrap break-all text-red-500">{this.state.stack.slice(0, 2000)}</pre>
          )}
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
export function PluginView({ pluginId, view, panelParams }: LoadedViewProps) {
  // 内置视图（builtin: 前缀）——同步渲染，无异步加载态。
  if (view.entry?.startsWith('builtin:')) {
    return <BuiltinView view={view} />
  }
  // 第三方插件（URL 加载）：独立组件，hooks 数量恒定，不受内置视图影响。
  return <AsyncPluginView pluginId={pluginId} view={view} viewParams={panelParams?.viewParams} />
}

function AsyncPluginView({
  pluginId,
  view,
  viewParams,
}: LoadedViewProps & { viewParams?: Record<string, unknown> }) {
  const runtime = usePluginRuntime()
  const [state, setState] = useState<{ comp: ComponentType | null; error: string | null }>({ comp: null, error: null })

  useEffect(() => {
    let alive = true
    setState({ comp: null, error: null })
    runtime.loadViewComponent(pluginId, view).then((c) => {
      if (!alive) return
      // loadViewComponent 成功但返回 null（import 失败/非组件）—— 抛出，让
      // ErrorBoundary 把诊断信息渲染到崩溃界面（便于直接截图排查）。
      if (!c) {
        setState({ comp: null, error: `组件加载失败或返回了非组件对象: plugin=${pluginId} view=${view.id} entry=${view.entry ?? ''}（详见 Console 的 [plugin-runtime] 日志）` })
        return
      }
      setState({ comp: c, error: null })
    })
    return () => {
      alive = false
    }
  }, [runtime, pluginId, view])

  if (state.error) {
    return (
      <div className="rounded border border-red-200 bg-red-50 p-2 font-mono text-[10px] text-red-700">
        <div className="font-sans text-xs font-semibold">插件视图加载失败</div>
        <pre className="mt-1 whitespace-pre-wrap break-all">{state.error}</pre>
      </div>
    )
  }

  if (!state.comp) {
    return <div className="animate-pulse rounded border border-slate-200 p-3 text-xs text-slate-400">加载 {view.title}…</div>
  }

  // 动态视图（ctx.ui.openViewTab 打开）把 params 作为 props 传给组件——
  // 插件 view 组件从 props 拿参数（如 { path, commit }）。
  return (
    <PluginViewErrorBoundary>
      <state.comp {...(viewParams ?? {})} />
    </PluginViewErrorBoundary>
  )
}