/**
 * IterationGroup — renders a single iteration: T → O → C order (Spec A §2).
 *
 * Each iteration renders:
 *   - T (reasoning): ThinkingLine —— 与流式态**同一组件/同一交互**（brain 图标、
 *     w-fit 点击热区、AnimatedCollapse 动画一致）；展开态经 stateKey 跨 live→committed
 *     保留（不自动收起）。
 *   - O (text output): MarkdownRenderer, always shown
 *   - C (tools): FoldedToolGroup (每个工具一个独立 pill)
 *
 * The component is used by TurnBody for committed iterations.
 */
import { memo } from 'react'

import { FoldedToolGroup } from './FoldedToolGroup'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ReasoningBlock } from './ReasoningBlock'
import { ThinkingLine } from './ThinkingLine'
import { useI18n } from '@/providers/i18n'
import { IterationSlot } from '@/plugin-runtime/iteration-render'
import type { WebIteration } from '@/types/shared'

interface IterationGroupProps {
  iteration: WebIteration
  /** 思考块展开态共享键（live 侧同一 key）—— 形态切换后不自动收起。 */
  reasoningStateKey?: string
}

export const IterationGroup = memo(function IterationGroup({
  iteration,
  reasoningStateKey,
}: IterationGroupProps) {
  const { t } = useI18n()

  return (
    <div className="flex flex-col gap-1">
      {/* 迭代指标（插件注入点）：把该迭代的 token/TTFT/tool 耗时传给插件。 */}
      <IterationSlot
        data={{
          stats: {
            iteration: iteration.iteration,
            tokens: iteration.tokens,
            ttftMs: iteration.ttftMs,
            tokensPerSec: iteration.tokensPerSec,
            toolMs: iteration.toolMs,
          },
        }}
      />

      {/* T: reasoning —— 与流式态同一形态（brain 图标 + 同 label key + 同交互）。
          用户要求：两种"思考中"必须完全一致，不得有样式/交互差异。 */}
      {iteration.reasoning && (
        <ThinkingLine
          label={t('agent.thoughtChars', { count: iteration.reasoning.length })}
          stateKey={reasoningStateKey}
        >
          <ReasoningBlock content={iteration.reasoning} />
        </ThinkingLine>
      )}

      {/* O: text output (always shown) */}
      {iteration.content && (
        <MarkdownRenderer
          content={iteration.content}
          className="text-sm text-text-primary"
        />
      )}

      {/* C: tool calls (每个工具一个 pill) */}
      {iteration.tools.length > 0 && (
        <FoldedToolGroup tools={iteration.tools} />
      )}

      {/* Fallback: if nothing in this iteration, show a subtle hint */}
      {!iteration.reasoning && iteration.tools.length === 0 && !iteration.content && (
        <span className="text-xs text-text-muted">{t('agent.none')}</span>
      )}
    </div>
  )
})
