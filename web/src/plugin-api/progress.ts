/** 工具进度（progress 事件载荷中的工具状态）。 */
export type ToolStatus = 'generating' | 'running' | 'done' | 'error' | 'pending'

export interface ToolProgress {
  name: string
  status: ToolStatus
  label?: string
  args?: string
  result?: string
  /** 所属迭代号（1-based）。 */
  iteration?: number
  /** 工具提示（插件 hints）。 */
  hints?: readonly string[]
}

/** 单次迭代的 LLM 指标（供迭代 UI 插件渲染）。 */
export interface IterationStats {
  /** 迭代号（1-based）。 */
  iteration: number
  /** 该迭代生成的 completion tokens（per-iteration，非累计）。 */
  tokens?: number
  /** 首 token 延迟（ms）。 */
  ttftMs?: number
  /** 平均生成速度（tokens/sec）。 */
  tokensPerSec?: number
  /** 该迭代工具总耗时（ms）。 */
  toolMs?: number
}

/** 实时流式指标（当前正在生成的 token/s 等）。 */
export interface LiveStreamStats {
  /** 平均生成速度（tokens/sec）。 */
  tokensPerSec?: number
  /** 首 token 延迟（ms，当次流式调用）。 */
  ttftMs?: number
  /** 累计 completion tokens。 */
  completionTokens?: number
}
