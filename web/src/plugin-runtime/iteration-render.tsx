/**
 * iteration-render —— 迭代级 UI 注入点。
 *
 * 插件声明 `container: 'iteration'` 的 view 贡献点即可在每个迭代的渲染处
 * 追加 UI。宿主（IterationGroup / LiveIteration）通过 <IterationSlot> 把这些
 * view 渲染出来，并用 React Context 把当前迭代的指标（token / TTFT / tool
 * 耗时 / 实时 tokens-per-sec）传给插件组件 —— 插件内用 `useIterationStats()`
 * 读取，获得精确类型（见 @xbot/plugin-api 的 IterationStats / LiveStreamStats）。
 */
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import * as React from 'react'

import type { IterationStats, LiveStreamStats, ViewContribution } from '@/plugin-api'
import { useOptionalPluginRuntime } from '@/plugin-runtime'

import { PluginView } from './PluginView'

// 暴露到 window 供独立 ESM 插件模块使用（无法 import 内部模块路径）。
// 独立插件通过 window.__xbot_iteration__.useIterationStats() 获取数据，
// 通过 window.React 获取 React（避免独立 bundle 重复打包 React）。
if (typeof window !== 'undefined') {
  const w = window as unknown as { __xbot_iteration__?: unknown; React?: unknown }
  w.__xbot_iteration__ = { useIterationStats }
  w.React = React
}

/** 传给迭代插件的联合数据：完成迭代（stats）或进行中的 live 流（live）。 */
export interface IterationRenderData {
  /** 已完成迭代的聚合指标（per-iteration token/TTFT/tool 耗时）。 */
  stats?: IterationStats
  /** 当前正在生成的实时指标（tokens/s），来自 ProgressSnapshot.streamStats。 */
  live?: LiveStreamStats
}

const IterationStatsContext = createContext<IterationRenderData>({})

/** 插件视图内读取当前迭代指标（在 <IterationSlot> 渲染树内有效）。 */
export function useIterationStats(): IterationRenderData {
  return useContext(IterationStatsContext)
}

/**
 * IterationSlot —— 迭代渲染处的插件视图挂载点。
 * 查询 registry 中 `container === 'iteration'` 的 view 并渲染，同时用 Context
 * 把该迭代的 stats/live 数据注入给插件组件。
 */
export function IterationSlot({ data, children }: { data: IterationRenderData; children?: ReactNode }) {
  const runtime = useOptionalPluginRuntime()
  // 订阅 registry 的 view 集合变化：插件激活/热加载/卸载后，container === 'iteration'
  // 的视图列表自动刷新。用 useMemo([runtime]) 是死缓存 —— 插件激活后 runtime 引用
  // 不变，views 永远为空，UI 不渲染。
  const [views, setViews] = useState<Array<{ pluginId: string; view: ViewContribution }>>([])
  useEffect(() => {
    if (!runtime) {
      setViews([])
      return
    }
    const recompute = () =>
      setViews(runtime.listAllViews().filter(({ view }) => view.container === 'iteration'))
    recompute()
    return runtime.subscribeViews(recompute)
  }, [runtime])
  // 无 PluginRuntimeProvider（单元测试/降级）→ 只渲染宿主 children，不注入插件。
  if (!runtime) return <>{children}</>
  if (views.length === 0) return <>{children}</>
  return (
    <IterationStatsContext.Provider value={data}>
      {children}
      <div className="flex flex-col gap-1">
        {views.map(({ pluginId, view }) => (
          <PluginView key={view.id} pluginId={pluginId} view={view} />
        ))}
      </div>
    </IterationStatsContext.Provider>
  )
}