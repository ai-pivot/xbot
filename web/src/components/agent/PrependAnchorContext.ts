/**
 * PrependAnchorContext —— 「prepend 之前给列表拍下**不许动的锚**」的通道。
 *
 * ⛔ 用户硬要求（2026-10-12）：「加载更多触发之后，新加载的历史直接加在最上方，用户的
 * 视角不变 —— 用户**看到的东西一动不动**」，并且**不接受**事后快照 + 时间窗 +「估算→
 * 实测」重试那类靠时序赌运气的补偿。
 *
 * 因此两条 prepend 路径（历史分页 `onLoadMore` 与区域段 `loadRegionSegment`）必须在
 * **发请求之前**走**同一个**入口，把「锚」交给列表：
 *   - 锚 = **DOM 真相**：当前视口里最上方那一可见行 + 它此刻的视口内 top；
 *   - 列表在提交后**同一帧**（layout effect，paint 之前）把该行的 top 钉回原值 ⇒
 *     浏览器只会画最终帧，用户一帧都看不到位移；
 *   - 收敛即结束（连续两次零偏移销账）—— 没有 deadline、没有重试窗口。
 *
 * 由 `MessageList` 提供（它在树上包住所有行），`useRegionWindow` 消费。
 * 无 Provider 时是安全的 no-op（测试/独立渲染不受影响）。
 */
import { createContext, useContext } from 'react'

export interface PrependAnchor {
  /**
   * 段/prepend 请求**发出前**调用一次：拍下当前「不许动的锚」。
   * 幂等：重复调用只覆盖为最新一帧的锚。
   */
  beginPrependAnchor: () => void
}

export const NOOP_PREPEND_ANCHOR: PrependAnchor = { beginPrependAnchor: () => {} }

export const PrependAnchorContext = createContext<PrependAnchor>(NOOP_PREPEND_ANCHOR)

/** 读取 prepend 锚通道（无 Provider ⇒ no-op，绝不抛错）。 */
export function usePrependAnchor(): PrependAnchor {
  return useContext(PrependAnchorContext)
}
