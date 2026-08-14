/**
 * useAgentChatState — AgentPanel 的新状态机接线（M4 切换的核心）。
 *
 * 单一事实源：ChatStore（web/src/chat/ 状态机）。
 *   - 全部 SSE 消息 → normalizeEvent → dispatch
 *   - useChatMessages 的 DB 历史（chat.messages）→ history_replaced
 *   - 渲染输出：rows（ChatMessage[] 适配）+ liveProgress（快照适配）
 *
 * useChatMessages 保留职责（REST 面，非渲染）：sendMessage/cancel/upload/
 * reload/loadMore/rewind 支持。其 messages 输出只作为 history 映射的输入。
 */

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react'
import type { ChatMessage, ProgressSnapshot } from '@/types/shared'
import type { WSConnection } from '@/hooks/useWSConnection'
import { deriveRows } from './derive'
import { historyToReplaced, liveProgressFromState, rowsToChatMessages } from './integrate'
import { normalizeEvent } from './normalize'
import { ChatStore } from './store'
import { initialChatState } from './types'

export interface UseAgentChatStateArgs {
  /** 事件归属的 chat（SSE chat_id 匹配用，支持带/不带 channel 前缀）。 */
  readonly progressChatID: string | null
  readonly ws: WSConnection
  /** DB 历史输入（useChatMessages 的 messages —— 变化即 dispatch history_replaced）。 */
  readonly historyMessages: readonly ChatMessage[]
  readonly historyReady: boolean
  readonly initialProgress: unknown
  /** 会话切换时重置（chatKey 变化）。 */
  readonly resetKey: string
}

export interface AgentChatState {
  readonly messages: ChatMessage[]
  readonly liveProgress: ProgressSnapshot
  readonly busyFallback: boolean
  readonly tokenPrompt: number | null
  readonly reset: () => void
}

export function useAgentChatState(args: UseAgentChatStateArgs): AgentChatState {
  const { progressChatID, ws, historyMessages, historyReady, initialProgress, resetKey } = args

  // per-chat store（resetKey 变化 = 新实例，旧实例整棵丢弃 —— 无跨会话残留）。
  const [boxed, setBoxed] = useState(() => ({
    key: resetKey,
    store: new ChatStore(progressChatID ?? 'none'),
  }))
  if (boxed.key !== resetKey) {
    boxed.store.dispose()
    setBoxed({ key: resetKey, store: new ChatStore(progressChatID ?? 'none') })
  }
  const store = boxed.store
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)

  // rows 缓存：state 引用不变 → 不重算（immutable 保证）。
  const rows = useMemo(() => deriveRows(state), [state])
  const messages = useMemo(() => rowsToChatMessages(rows), [rows])
  const liveProgress = useMemo(() => liveProgressFromState(state), [state])

  // SSE 订阅：全部消息喂状态机（normalizeEvent 内部完成 chat 过滤 + 非法丢弃）。
  useEffect(() => {
    if (!progressChatID) return
    const off = ws.onMessage((raw: unknown) => {
      const msg = raw as { chat_id?: string } | null
      // 无 chat_id 的控制消息（connect/heartbeat）直接跳过 —— normalize 也会丢。
      void msg
      const ev = normalizeEvent(raw, progressChatID)
      if (ev !== null) store.dispatch(ev)
    })
    return off
  }, [progressChatID, store, ws])

  // history 映射：messages/initialProgress 变化（reload/loadMore/hydration）→
  // history_replaced 全量替换（DB 权威）。historyReady gate：fetchHistory 完成
  // 后才替换（避免半截历史清掉 live）。
  const historyVersion = useRef(-1)
  useEffect(() => {
    if (!historyReady) return
    historyVersion.current = historyMessages.length
    const ev = historyToReplaced(historyMessages, initialProgress)
    store.dispatch(ev)
  }, [historyReady, historyMessages, initialProgress, progressChatID, store])

  // token 提示（会话上下文刷新由 AgentPanel 的 sessionContext 负责，这里只透出）。
  const tokenPrompt = liveProgress.tokenUsage && liveProgress.tokenUsage.promptTokens > 0
    ? liveProgress.tokenUsage.promptTokens
    : null

  const reset = useMemo(
    () => () => {
      store.hydrate(initialChatState(progressChatID ?? 'none'))
    },
    [store, progressChatID],
  )

  return {
    messages,
    liveProgress,
    busyFallback: liveProgress.streaming && state.activeTurn !== null,
    tokenPrompt,
    reset,
  }
}
