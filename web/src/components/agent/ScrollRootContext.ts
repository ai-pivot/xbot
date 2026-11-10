/**
 * ScrollRootContext —— 消息列表的**滚动容器**（IntersectionObserver 的 root）。
 *
 * ── 为什么必须有（P1，2026-10-18 审查）────────────────────────────────────────
 * `useRegionWindow` 的哨兵（`RegionsDivider` 的根节点）位于 `MessageList` 的
 * `overflow-y-auto` 滚动容器**内部**。`IntersectionObserver` 求交时会与 target 到 root
 * 之间的**滚动容器 clip** 求交 —— 哨兵被滚出容器时矩形为空，`rootMargin` 扩的是
 * **viewport** 的 root rect，抵不掉祖先裁剪 ⇒ 用 `root: null` 时"±2 屏预取带"在真实
 * DOM 里**等于没加**（边界必须已经可见才触发，"用户永远看不到缺口"的契约失效）。
 *
 * ⇒ 把 root 指到滚动容器本身：`{ root: scrollEl, rootMargin: '200% …' }` 才真正生效
 * （此时 rootMargin 扩的就是容器的可见框，不再有额外的祖先裁剪）。
 *
 * 由 `MessageList` 提供（`scrollRef`），`useRegionWindow` 消费。无 Provider ⇒ `null`：
 * 退化成 `root: null`（单测/独立渲染不受影响，行为与旧实现一致）。
 */
import { createContext, useContext, type RefObject } from 'react'

export const ScrollRootContext = createContext<RefObject<HTMLElement | null> | null>(null)

/** 读滚动容器 ref（无 Provider ⇒ null ⇒ 调用方回落 `root: null`）。 */
export function useScrollRoot(): RefObject<HTMLElement | null> | null {
  return useContext(ScrollRootContext)
}
