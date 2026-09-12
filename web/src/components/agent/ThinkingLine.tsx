/**
 * ThinkingLine — 思考折叠行（设计稿 1:1 统一形态）。
 *
 * LiveIteration（流式）与 TurnBody（committed 历史）共用同一形态：
 * brain 图标 + label，展开内容为 border-l-2 缩进块。
 * label 由调用方传入（live = 流式字数增长；commit = 耗时秒数）。
 * 无展开箭头指示器（用户要求删除）；点击热区仅覆盖图标+文字内容
 * 宽度（w-fit），点行内空白不触发。
 *
 * 展开/收起动画复用 AnimatedCollapse（CSS grid 0fr→1fr 180ms 过渡，
 * 与 FoldedToolGroup 的折叠动画同一形态）；lazy + unmountOnClose 保持
 * 轻量 —— 折叠时 reasoning markdown 不参与渲染。
 */
import { memo, useState, type ReactNode } from 'react'
import { Brain } from 'lucide-react'


import { AnimatedCollapse } from '@/components/ui/animated-collapse'
import { getReasoningOpen, reasoningKey, setReasoningOpen } from './reasoningOpenState'

interface ThinkingLineProps {
  /** brain 图标后的文本（耗时秒数 / 字数 / SweepText 流式态）。 */
  label: ReactNode
  children: ReactNode
  defaultOpen?: boolean
  /**
   * 展开态共享键（`turnID:iteration`，用 `reasoningKey()` 构造）—— live 与
   * committed 两种形态读写同一份状态：形态切换必然 remount（LiveIteration →
   * IterationGroup），共享键让新实例恢复用户此前的展开选择，不再"commit 时自动
   * 收起"（2026-09-12 用户报告）。
   */
  stateKey?: string
}

/**
 * ⚠️ memo：TurnBody/LiveIteration 每帧都会重渲染（liveProgress 引用每帧变化），
 * 每个迭代一个 ThinkingLine —— 没有 memo 时每个流式帧都会重渲染全部迭代的
 * Brain 图标 + <button>（trace 实测 lucide 4.0% + button/Slot 4.5%）。
 * props = {label, children, defaultOpen, stateKey}，label 是字符串/元素
 *（引用稳定即可命中），stateKey 是稳定字符串。
 */
export const ThinkingLine = memo(function ThinkingLine({
  label,
  children,
  defaultOpen = false,
  stateKey,
}: ThinkingLineProps) {
  // remount（live → committed）时从共享存储恢复展开态：用户展开的思考块不自动收起。
  const [open, setOpen] = useState(() =>
    stateKey ? (getReasoningOpen(stateKey) ?? defaultOpen) : defaultOpen,
  )
  return (
    <div>
      {/* 点击热区仅收缩到图标+文字内容宽度（w-fit）——不占满整行，点行内空白不触发展开 */}
      <button
        type="button"
        data-testid="thinking-line"
        onClick={() => {
          const next = !open
          setOpen(next)
          if (stateKey) setReasoningOpen(stateKey, next)
        }}
        className="flex w-fit max-w-full items-center gap-1 rounded-lg px-1 py-0.5 text-left text-[10px]"
        style={{ color: 'var(--text-muted)' }}
      >
        <Brain className="shrink-0" size={10} />
        <span className="min-w-0 truncate text-left">{label}</span>
      </button>
      <AnimatedCollapse open={open} lazy unmountOnClose>
        {/* 展开文字 dim（用户要求）：muted 色 + 降透明度，弱于正文 */}
        <div
          className="mt-1 border-l-2 pl-2.5 text-[12.5px] opacity-75"
          style={{ borderColor: 'var(--border)', color: 'var(--text-muted)' }}
        >
          {children}
        </div>
      </AnimatedCollapse>
    </div>
  )
})

/** committed 迭代的思考块 stateKey（IterationGroup 用；与 live 侧同构）。 */
export function iterationReasoningKey(turnID: number | undefined, iteration: number | undefined): string {
  return reasoningKey(turnID, iteration ?? 0)
}
