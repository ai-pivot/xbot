/**
 * 通信视图三类空态的统一外壳（活动流 / 流向图 / 线程）。
 *
 * 用户 2026-10-22 反馈第一条：「Sankey 主区 1000×600 全空白，只有一行『当前时间
 * 窗口没有跨泳道消息』，第一印象=页面坏了」。空白画布 = 故障，不是空态。
 * 所以：空态必须**占住版面**（不是一行小字），必须说清「这里会出现什么」，
 * 并且给**能立刻点的下一步**。
 */
import type { ReactNode } from 'react'

interface Props {
  /** 图标/符号（emoji 或字符，不引第三方图标库，避免空态依赖加载）。 */
  glyph: string
  title: string
  description: ReactNode
  /** 卡片式容器（活动流用整块卡片；侧栏小空态用 plain）。 */
  plain?: boolean
  /** 紧凑版：插画缩小、内边距收紧 —— 用于 320px 宽的右详情栏（P2 降噪）。 */
  compact?: boolean
  testId?: string
  children?: ReactNode
}

export function EmptyState({ glyph, title, description, plain = false, compact = false, testId, children }: Props) {
  const shell = plain
    ? 'w-full'
    : compact
      ? 'mx-auto w-full rounded-lg border border-border bg-bg-secondary/60 px-3 py-4 text-center'
      : 'mx-auto w-full max-w-[560px] rounded-lg border border-border bg-bg-secondary/60 px-6 py-8 text-center'
  const badge = compact
    ? 'mx-auto flex size-7 items-center justify-center rounded-full bg-bg-tertiary text-[13px] text-text-secondary'
    : 'mx-auto flex size-11 items-center justify-center rounded-full bg-bg-tertiary text-[20px] text-text-secondary'
  return (
    <div
      data-testid={testId}
      className={compact
        ? 'flex min-h-0 flex-1 items-center justify-center overflow-auto p-2'
        : 'flex min-h-0 flex-1 items-center justify-center overflow-auto p-6'}
    >
      <div className={shell}>
        <div aria-hidden className={badge}>
          {glyph}
        </div>
        <h3 className={compact ? 'mt-2 text-[11.5px] font-semibold text-text-primary' : 'mt-3 text-[13px] font-semibold text-text-primary'}>
          {title}
        </h3>
        <div className={compact ? 'mt-1 text-[10.5px] leading-4 text-text-secondary' : 'mt-1.5 text-[11.5px] leading-5 text-text-secondary'}>
          {description}
        </div>
        {children ? (
          <div className={compact ? 'mt-2 flex flex-wrap items-center justify-center gap-1.5' : 'mt-4 flex flex-wrap items-center justify-center gap-2'}>
            {children}
          </div>
        ) : null}
      </div>
    </div>
  )
}
