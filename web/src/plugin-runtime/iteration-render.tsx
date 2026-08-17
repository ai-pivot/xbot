/**
 * iteration-render —— 迭代级 UI 注入点。
 *
 * 插件声明 `container: 'iteration'` 的 view 贡献点即可在每个迭代的渲染处
 * 追加 UI。宿主（IterationGroup / LiveIteration）通过 <IterationSlot> 把这些
 * view 渲染出来，并用 React Context 把当前迭代的指标（token / TTFT / tool
 * 耗时 / 实时 tokens-per-sec）传给插件组件 —— 插件内用 `useIterationStats()`
 * 读取，获得精确类型（见 @xbot/plugin-api 的 IterationStats / LiveStreamStats）。
 */
import { createContext, useContext, useMemo, type ReactNode } from 'react'

import type { IterationStats, LiveStreamStats } from '@/plugin-api'
import { useOptionalPluginRuntime } from '@/plugin-runtime'

import { PluginView } from './PluginView'

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
  const views = useMemo(
    () => (runtime ? runtime.listAllViews().filter(({ view }) => view.container === 'iteration') : []),
    [runtime],
  )
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