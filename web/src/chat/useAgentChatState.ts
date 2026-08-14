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

import { useEffect, useMemo, useRef, useSyncExternalStore } from 'react'
import type { ChatMessage, ProgressSnapshot } from '@/types/shared'
import type { WSConnection } from '@/hooks/useWSConnection'
import { deriveRows } from './derive'
import { historyToReplaced, liveProgressFromState, rowsToChatMessages } from './integrate'
import { normalizeEvent } from './normalize'
import { ChatStore } from './store'
import { initialChatState, type DomainEvent } from './types'

export interface UseAgentChatStateArgs {
  /** 事件归属的 chat（SSE chat_id 匹配用，支持带/不带 channel 前缀）。 */
  readonly progressChatID: string | null
  readonly ws: WSConnection
  /** DB 历史输入（useChatMessages 的 messages —— 变化即 dispatch history_replaced）。 */
  readonly historyMessages: readonly ChatMessage[]
  readonly historyReady: boolean
  /** messages 的属主（useChatMessages.resolvedChatID，fetch 成功才设置）。
   *  与 historyChatID 不符时【跳过 dispatch】—— 切会话窗口期 messages 还是
   *  旧会话的，灌进新 store 会被 merge 保留（跨会话污染根因）。 */
  readonly historyOwner: string | null
  /** 期望属主（AgentPanel 的 chatID）。 */
  readonly historyChatID: string | null
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
  const { progressChatID, ws, historyMessages, historyReady, historyOwner, historyChatID, initialProgress, resetKey } = args

  // per-chat store（ref 式切换 —— 渲染期只做幂等 ref 变更，无 setState/dispose，
  // 避免 render-phase update 的时序陷阱；key 变化 = 丢弃旧实例换新空 store）。
  const boxRef = useRef<{ key: string; store: ChatStore } | null>(null)
  if (boxRef.current === null || boxRef.current.key !== resetKey) {
    boxRef.current?.store.dispose()
    boxRef.current = { key: resetKey, store: new ChatStore(progressChatID ?? 'none') }
  }
  const store = boxRef.current.store
  const state = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)

  // rows 缓存：state 引用不变 → 不重算（immutable 保证）。
  const rows = useMemo(() => deriveRows(state), [state])
  const messages = useMemo(() => rowsToChatMessages(rows), [rows])
  const liveProgress = useMemo(() => liveProgressFromState(state), [state])

  // SSE 订阅：全部消息喂状态机（normalizeEvent 内部完成 chat 过滤 + 非法丢弃）。
  // 附轻量诊断（window.__xbotChatDiag）：记录 事件类型→normalize 结果→
  // 状态机去向，下次问题报告自带现场（无需用户配合抓包）。
  useEffect(() => {
    if (!progressChatID) return
    const off = ws.onMessage((raw: unknown) => {
      const ev = normalizeEvent(raw, progressChatID)
      const rec = ev !== null ? ev.type : `drop:${diagDropReason(raw)}`
      chatDiag(rec, ev, store)
      if (ev !== null) store.dispatch(ev)
    })
    return off
  }, [progressChatID, store, ws])

  // history 映射：messages/initialProgress 变化（reload/loadMore/hydration）→
  // history_replaced（DB 权威，merge 语义）。双重 gate：
  //  1. historyReady（fetchHistory 完成）
  //  2. 属主匹配（resolvedChatID === chatID）—— 切会话窗口期 resolvedChatID
  //     还是旧会话（fetch 成功才更新），旧 messages 不得灌入新 store（merge
  //     会保留它们 → 跨会话污染）。
  useEffect(() => {
    if (!historyReady) return
    if (historyChatID === null) return
    if (historyOwner === null || historyOwner !== historyChatID) return
    const ev = historyToReplaced(historyMessages, initialProgress)
    store.dispatch(ev)
  }, [historyReady, historyMessages, initialProgress, progressChatID, store, historyOwner, historyChatID])

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

// ─── 轻量诊断（window.__xbotChatDiag） ────────────────────────
// ring buffer（最近 200 条）：事件类型 → 状态机去向。排障时控制台直接读
// window.__xbotChatDiag.dump()。零配置常开（每条 ~40B，无字符串大对象）。

interface DiagEntry {
  t: number
  ev: string
  turn: number | null
  active: number | null
  live: boolean
}

const diagBuf: DiagEntry[] = []
const diagGlobal = {
  dump(): DiagEntry[] {
    return diagBuf.slice(-50)
  },
  counts(): Record<string, number> {
    const c: Record<string, number> = {}
    for (const e of diagBuf) c[e.ev] = (c[e.ev] ?? 0) + 1
    return c
  },
}
if (typeof window !== 'undefined') {
  ;(window as unknown as { __xbotChatDiag?: typeof diagGlobal }).__xbotChatDiag = diagGlobal
}

function chatDiag(rec: string, ev: DomainEvent | null, store: ChatStore): void {
  if (diagBuf.length >= 200) diagBuf.shift()
  const s = store.getSnapshot()
  const active = s.activeTurn
  const at = active !== null ? s.turns.get(active) : undefined
  diagBuf.push({
    t: Date.now(),
    ev: rec,
    turn: ev !== null && 'turnID' in ev && ev.turnID !== null ? Number(ev.turnID) : null,
    active: active !== null ? Number(active) : null,
    live: at?.phase.kind === 'live',
  })
}

function diagDropReason(raw: unknown): string {
  const env = raw as { type?: unknown; progress?: { turn_id?: unknown; chat_id?: unknown }; chat_id?: unknown } | null
  if (!env || typeof env !== 'object') return 'not-object'
  const ty = typeof env.type === 'string' ? env.type : `unknown(${JSON.stringify(env.type)?.slice(0, 20)})`
  const prog = env.progress as { turn_id?: unknown; chat_id?: unknown } | undefined
  const chat = typeof env.chat_id === 'string' ? env.chat_id : typeof prog?.chat_id === 'string' ? prog.chat_id : '?'
  const tid = typeof prog?.turn_id === 'number' ? prog.turn_id : '?'
  return `${ty}[chat=${chat},turn=${tid}]`
}
