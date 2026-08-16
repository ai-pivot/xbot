/**
 * ViewSlot——渲染插件声明的视图贡献点到宿主容器。
 *
 * 宿主把 ViewSlot 组件挂到布局位（right_sidebar/panel/bottom/info_bar/…），
 * 它查询 registry 中 container 匹配的 view 贡献点并渲染。
 * 插件视图 entry 是 React 组件（由 plugin-runtime 动态 import）。
 */
import { useEffect, useMemo, useState, type ReactNode } from 'react'

import type { ViewContainer, ViewContribution } from '@/plugin-api'

export interface ViewSlotProps {
  container: ViewContainer
  /** 从 registry 查询该容器的视图贡献点（宿主注入）。 */
  getViews: (container: ViewContainer) => readonly ViewContribution[]
  /** 按贡献点 entry 加载 React 组件（宿主注入，缓存）。 */
  loadView: (view: ViewContribution) => Promise<React.ComponentType | null>
  /** 空态渲染。 */
  empty?: ReactNode
  className?: string
}

/** 渲染某容器下所有插件视图（叠加）。 */
export function ViewSlot({ container, getViews, loadView, empty = null, className }: ViewSlotProps) {
  const views = useMemo(() => getViews(container), [container, getViews])
  if (views.length === 0) return <>{empty}</>
  return (
    <div className={`flex flex-col gap-2 overflow-y-auto ${className ?? ''}`}>
      {views.map((v) => (
        <LazyView key={v.id} view={v} loadView={loadView} />
      ))}
    </div>
  )
}

function LazyView({ view, loadView }: { view: ViewContribution; loadView: ViewSlotProps['loadView'] }) {
  const [Comp, setComp] = useState<React.ComponentType | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let alive = true
    setComp(null)
    setFailed(false)
    loadView(view)
      .then((c) => {
        if (alive) setComp(c)
      })
      .catch(() => {
        if (alive) setFailed(true)
      })
    return () => {
      alive = false
    }
  }, [view, loadView])

  if (failed) {
    return <div className="rounded border border-red-200 bg-red-50 p-2 text-xs text-red-600">插件视图加载失败: {view.id}</div>
  }
  if (!Comp) return <div className="animate-pulse rounded border border-slate-200 p-3 text-xs text-slate-400">加载 {view.title}…</div>
  return <Comp />
}
