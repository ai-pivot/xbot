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
