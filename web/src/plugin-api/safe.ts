/**
 * Sanitize 后的安全消息类型——插件/渲染器只能接触公共字段。
 * 前端从内部消息模型裁剪（toSafeMessage），不含内部状态。
 */

export interface SafeToolResult {
  name: string
  summary: string
  isError: boolean
  /** 工具专用结果（由 ToolResultMap 提供类型）。 */
  result?: unknown
}

export interface SafeIteration {
  iteration: number
  content: string
  reasoning?: string
  tools?: readonly SafeToolResult[]
}

export interface SafeMessage {
  id: number
  turnID: number
  role: 'user' | 'assistant' | 'system' | 'tool'
  content: string
  createdAt: string
}

export interface SafeUserMessage extends SafeMessage {
  role: 'user'
  /** 是否为通知注入（🔔）。 */
  isNotification?: boolean
}

export interface SafeAssistantMessage extends SafeMessage {
  role: 'assistant'
  iterations?: readonly SafeIteration[]
  reasoning?: string
}

/** 把内部 ChatMessage 裁剪为安全副本（运行时实现位于 plugin-runtime/sanitize.ts）。 */
export interface SafeMessageFactory {
  (m: unknown): SafeMessage
}
