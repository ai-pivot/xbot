import { createContext, useContext } from 'react'

/**
 * 工具卡片会话身份 —— ToolRender 树内的组件（如 Shell 卡片的
 * "转后台" 按钮）需要知道当前渲染的是哪个会话，才能调用后端
 * promote_shell RPC（session_key = `${channel}:${chatID}`）。
 *
 * 由 AgentPanel 提供（主会话 = messageChannel:chatID；子代理面板 =
 * agent:agentChatID），默认值渲染历史/离线场景（无会话身份 → 不渲染
 * 会话敏感的交互）。
 */
export interface ToolSessionCtx {
  channel: string
  chatID: string | null
}

export const ToolSessionContext = createContext<ToolSessionCtx>({
  channel: 'web',
  chatID: null,
})

export function useToolSession(): ToolSessionCtx {
  return useContext(ToolSessionContext)
}
