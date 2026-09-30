/**
 * RegionsDivider —— 「⬆ 该 turn 更早的 N 个展示区域」分隔条（D1 线）。
 *
 * 位置：assistant 行内、`TurnBody` **之前**（该 turn 所有已加载迭代的上方），
 * 仅当 `regions_before > 0`（该 turn 未完整下发）时渲染 —— 是**唯一新增可见物**
 * （G2 无感验收：默认视图零差异）。
 *
 * 三态（恒高，`h-7`）：
 *   idle    ⬆ + 「更早的 {{count}} 个区域」
 *   loading spinner + 「正在加载更早区域…」
 *   error   「加载失败 · 重试」（点击 → 手动重试）
 *
 * ⚠️ 容器高度恒定原则（gotchas 的 content-visibility 事故教训）：本组件是**真实
 * 内容挂载**，三态用**同一行高**；绝不用估算高度占位、也不在三态之间改变高度 ——
 * 分隔条的插入/消失由既有高度机制（MessageList 的 ResizeObserver 补偿）兜住，
 * 「加载前后滚动总高一致性」依赖这里的高度可预测。
 *
 * ⚠️ 根节点即 IO 哨兵（`sentinelRef`）：`useRegionWindow` 观察它的可见性来决定
 * 何时向更旧方向取段。三态复用同一个 DOM 节点（不换 key、不条件卸载）——
 * 卸载会让 observer 失去观察目标而 effect 依赖不含哨兵重建。
 */
import { Loader2, TriangleAlert, ChevronUp } from 'lucide-react'
import { memo } from 'react'

import { useI18n } from '@/providers/i18n'

import type { RegionWindowStatus } from '@/hooks/useRegionWindow'

export interface RegionsDividerProps {
  /** 仍剩的更早展示区域数（后端 `regions_before`）。 */
  count: number
  status: RegionWindowStatus
  /** 手动重试（error 态按钮）。 */
  onRetry: () => void
  /** 哨兵 ref（由 `useRegionWindow` 提供；挂在根节点上）。 */
  sentinelRef?: React.RefObject<HTMLDivElement | null>
}

/** 三态共用的行容器 —— **高度恒定**是三态唯一的结构不变量。 */
const ROW_CLASS = 'flex h-7 min-w-0 items-center justify-center gap-1.5 px-1 text-[11.5px] text-text-muted'

export const RegionsDivider = memo(function RegionsDivider({
  count,
  status,
  onRetry,
  sentinelRef,
}: RegionsDividerProps) {
  const { t } = useI18n()
  return (
    <div
      ref={sentinelRef}
      data-testid="regions-divider"
      data-state={status}
      data-regions-before={count}
      className={ROW_CLASS}
    >
      {status === 'loading' ? (
        <>
          <Loader2 aria-hidden data-testid="regions-divider-spinner" className="size-3 animate-spin" />
          <span>{t('agent.regions.loading')}</span>
        </>
      ) : status === 'error' ? (
        <button
          type="button"
          data-testid="regions-divider-retry"
          onClick={onRetry}
          className="flex min-w-0 items-center gap-1.5 rounded px-1.5 py-0.5 transition-colors hover:bg-bg-hover hover:text-text-secondary"
        >
          <TriangleAlert aria-hidden className="size-3" />
          <span>{t('agent.regions.retry')}</span>
        </button>
      ) : (
        <>
          <ChevronUp aria-hidden className="size-3" />
          <span data-testid="regions-divider-label">{t('agent.regions.loadEarlier', { count })}</span>
        </>
      )}
    </div>
  )
})
