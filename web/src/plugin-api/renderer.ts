/**
 * 消息渲染器（§3.5）——匹配条件精化渲染参数类型。
 *
 * `matches` 是运行时匹配条件，同时是类型源头：`MatchedMessage<M>` 按 M 的形状
 * 精化 render 收到的消息类型。"匹配了什么，就能安全处理什么"。
 */
import type { ReactNode } from 'react'

import type { SafeMessage, SafeAssistantMessage, SafeUserMessage } from './safe'

/** 工具结果类型表（核心内置；后端插件用声明合并扩展）。 */
export interface ToolResultMap {
  display_html: { code: string; summary: string }
  shell: { command: string; output: string; exitCode: number }
  web_search: { query: string; results: readonly unknown[] }
  git_status: {
    branch: string
    dirty: boolean
    changes: number
    ahead: number
    behind: number
  }
}

export type Matcher =
  | { tool: keyof ToolResultMap }
  | { role: 'assistant' | 'user' | 'system' }
  // 通用匹配：空对象（Record<string, never> 保证 excess property 检查生效，
  // 不会像裸 {} 那样吞掉 {role:'admin'} 之类的非法字面量）。
  | Record<string, never>

/** 条件类型：匹配 → 消息类型精化。 */
export type MatchedMessage<M extends Matcher> =
  M extends { tool: infer T extends keyof ToolResultMap }
    ? SafeMessage & { tool: { name: T; result: ToolResultMap[T] } }
    : M extends { role: 'assistant' }
      ? SafeAssistantMessage
      : M extends { role: 'user' }
        ? SafeUserMessage
        : SafeMessage

export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  /** 大者优先；render 返回 null 时继续 fallback 到下一个。 */
  priority: number
  matches: M
  /** 渲染函数：msg 类型由 matches 精化。 */
  render: (msg: MatchedMessage<M>, ctx: RenderContext) => ReactNode | null
}

/** 渲染上下文：当前会话、可用 UI 原语。 */
export interface RenderContext {
  chatID: string
  /** 渲染到某个已声明视图 slot（返回 JSX 由运行时挂载）。 */
  renderTo: (slotId: string) => ReactNode
  /** 本地 UI 状态（如折叠）。 */
  collapsed?: boolean
}
