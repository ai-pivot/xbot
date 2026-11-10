/**
 * 「重试上一轮」上下文。
 *
 * 为什么需要 context：错误气泡渲染在 AssistantMessage 深处（MessageItem →
 * AssistantMessage），而「重新发送上一条用户消息」这个动作只有 AgentPanel 拿得到
 * （`chat.sendMessage`）。层层透传会击穿 TurnBody/MessageItem 的 memo，所以用
 * context —— 值是一个稳定引用，不会让虚拟列表的可见行重渲染。
 *
 * 没有 Provider 时（单测、静态渲染）为 null，错误气泡只显示人话 + 技术详情，
 * 不渲染一个点了没反应的按钮。
 */
import { createContext, useContext } from 'react'

type RetryFn = () => void

export const RetryLastTurnContext = createContext<RetryFn | null>(null)

export function useRetryLastTurn(): RetryFn | null {
  return useContext(RetryLastTurnContext)
}
