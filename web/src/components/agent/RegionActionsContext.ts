import { createContext, useContext } from 'react'

import type { WebIteration } from '@/types/shared'

/**
 * RegionActionsContext —— 「展示区域段 / 迭代详情」**按需取回**的回调通道
 * （见 docs/plan-history-fold-windowing.md §3.2 D2 / §3.3 D3）。
 *
 * 为什么用 Context 而不是 props：消费点在组件树**最深处**——
 * `ToolPopoverDetail`（浮层）在 CopyTarget → TurnBody → CommittedTurn →
 * CommittedChunk → IterationBlock → IterationGroup → FoldedToolGroup →
 * MergedPills → LazyPillPopover 之下。逐层透传 props 会给这条链上每个
 * memo 边界都加上新 props ⇒ **每个流式帧击穿全部 memo**（gotchas 铁律：
 * props 链穿透 TurnBody 会击穿 memo）。Context 不参与 props 比较，零成本。
 *
 * 与 `ToolSessionContext`（会话身份）同族，但这里带**动作**；默认值是
 * 全 no-op（历史/离线/测试场景渲染同一棵树 ⇒ 零请求、零异常）。
 */
export interface RegionActions {
  /**
   * 浮层详情按需：`POST /api/iteration_detail` → `iterations_loaded`
   * （`mergeIterations` **同号覆盖** → 组件以完整数据重渲染；迭代号不变）。
   *
   * @returns true = 已把完整迭代灌进状态机；false = 失败（调用方显示重试，
   *          **不静默**）。同一 `(turnID, iteration)` 并发调用返回**同一个**
   *          promise（in-flight 去重 —— 同一浮层/两个浮层只发一次请求）。
   */
  loadIterationDetail(turnID: number, iteration: number): Promise<boolean>
  /** 该 `(turnID, iteration)` 是否已有详情请求在飞（去重状态可观测）。 */
  fetchInFlight(turnID: number, iteration: number): boolean
  /**
   * 区域段按需（向**更旧**方向）：`POST /api/regions` →
   * `iterations_loaded({turnID, iterations, regionsBefore})`。
   *
   * @param beforeIteration 当前窗口**最早**迭代号（调用方从已加载迭代读）。
   * @returns true = 段已灌进状态机；false = 失败（分隔条进入 retry 态）。
   *          **同一 turn 同时只允许一个段请求**（并发调用复用同一 promise）。
   */
  loadRegionSegment(turnID: number, beforeIteration: number): Promise<boolean>
  /** 该 turn 是否已有区域段请求在飞（哨兵做 in-flight 去重的可观测面）。 */
  segmentInFlight(turnID: number): boolean
}

const NOOP_ACTIONS: RegionActions = {
  loadIterationDetail: () => Promise.resolve(false),
  fetchInFlight: () => false,
  loadRegionSegment: () => Promise.resolve(false),
  segmentInFlight: () => false,
}

export const RegionActionsContext = createContext<RegionActions>(NOOP_ACTIONS)

export function useRegionActions(): RegionActions {
  return useContext(RegionActionsContext)
}

/** `iterations_loaded` 事件的载荷（chat/types.ts）—— 区域段 / 详情两端点共用。 */
export interface IterationsLoadedEvent {
  turnID: number
  iterations: WebIteration[]
  /** 缺省 = 不变（详情端点）；给定 = 权威覆盖区域计数（区域段端点）。 */
  regionsBefore?: number
}

/**
 * 当前行（turn）的 turnID —— 由 `AssistantMessage` 提供，`TurnBody` 之下的
 * 迭代/工具组件读取。
 *
 * 为什么需要它：`ToolPopoverDetail` 必须知道「我属于哪个 turn」才能按
 * `(turnID, iteration)` 拉详情；而 turnID 只到 `TurnBody` 的 props 为止
 * （`IterationBlock` → `IterationGroup` 不透传，且 `TurnBody.tsx` 是禁改文件）。
 * 默认 0 = 无 turn 归属（standalone / legacy 行 ⇒ 不发任何请求）。
 */
export const TurnIDContext = createContext<number>(0)

export function useTurnID(): number {
  return useContext(TurnIDContext)
}
