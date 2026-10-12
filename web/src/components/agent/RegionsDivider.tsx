/**
 * RegionsDivider —— 「更早展示区域」的**不可见**取回哨兵（D1 线）。
 *
 * ⛔⛔ 不变量（用户明确要求，2026-10-11 —— AGENTS.md 最高优先级）：**消息/上下文不连续
 * 零容忍**。渲染出的历史任何时候都必须连续；绝不允许在可见流里出现
 * `iter x → 加载哨兵 → iter x+y（y≠1）` 这种「中间跳过 y 条消息」的结构。
 *
 * 因此本组件**不再渲染任何可见物**（没有折线、没有「更早的 N 个区域」文字、没有可点
 * 的折叠条 —— 那正是被点名的 `content - 折叠 - content` 形态）。它只剩一个**零高度、
 * 不可见**的哨兵节点：`useRegionWindow` 观察它在**预取带**内的可见性，在用户**看到边界
 * 之前**就把更早的区域链式取回（内容从下往上连续生长，像历史一次就加载好）。
 *
 * 位置：assistant 行内、`TurnBody` **之前**（该 turn 所有已加载迭代的上方）。
 *
 * 失败态（网络错误 / 服务端拒绝）是唯一的例外：此时自动重试已按保守停止，必须让用户
 * 有出路 ⇒ 渲染一个小的「重试」按钮（显式失败态，罕见；不参与 idle/loading 的零高度
 * 契约）。
 *
 * ⚠️ 根节点即 IO 哨兵（`sentinelRef`）：三态复用同一个 DOM 节点（不换 key、不条件卸载）
 * —— 卸载会让 observer 失去观察目标。
 */
import { TriangleAlert } from 'lucide-react'
import { memo } from 'react'

import { useI18n } from '@/providers/i18n'

import type { RegionWindowStatus } from '@/hooks/useRegionWindow'

export interface RegionsDividerProps {
  /** 仍剩的更早展示区域数（后端 `regions_before`）。仅作为诊断属性挂在 DOM 上。 */
  count: number
  status: RegionWindowStatus
  /** 手动重试（error 态按钮）。 */
  onRetry: () => void
  /** 哨兵 ref（由 `useRegionWindow` 提供；挂在根节点上）。 */
  sentinelRef?: React.RefObject<HTMLDivElement | null>
}

export const RegionsDivider = memo(function RegionsDivider({
  count,
  status,
  onRetry,
  sentinelRef,
}: RegionsDividerProps) {
  const { t } = useI18n()
  // 零高度、无背景、无文字 —— 视觉上完全不存在。`h-px` 保留一个 1px 的真实布局盒，
  // 保证 IntersectionObserver 在所有浏览器都能可靠观察（零面积目标的 isIntersecting
  // 实现不一致，绝不能依赖它）。
  // ⚠️ 唯一例外是 error 态：必须让容器长到能容纳「重试」按钮（h-7），否则 1px 容器里的
  // 按钮会溢出并**盖在下面的迭代正文上**（E2E 实测：`<p>iter 21…</p>` 拦截点击）。
  // 高度只在显式失败态变化（罕见；idle↔loading 的常见翻转仍恒为 h-px）。
  return (
    <div
      ref={sentinelRef}
      data-testid="regions-divider"
      data-state={status}
      data-regions-before={count}
      className={
        status === 'error'
          ? 'flex h-7 w-full items-center justify-center'
          : 'h-px w-full bg-transparent'
      }
    >
      {status === 'error' && (
        <button
          type="button"
          data-testid="regions-divider-retry"
          onClick={onRetry}
          className="mx-auto flex h-7 min-w-0 items-center justify-center gap-1.5 px-1 text-[11.5px] text-text-muted transition-colors hover:text-text-secondary"
        >
          <TriangleAlert aria-hidden className="size-3" />
          <span>{t('agent.regions.retry')}</span>
        </button>
      )}
    </div>
  )
})
