/**
 * useStructuredMessages —— 结构化消息流状态。
 *
 * - 首屏分页拉取 /api/messages（按 message_id 去重、按 created_at + id 排序）；
 * - SSE /api/sse 增量更新：type=structured_message 的信封按 message_id 去重；
 * - 断线重连（EventSource 自动重连）后重新拉最新一页并合并（游标补齐）；
 * - answer/decision 走 /api/answer 与 /api/message，带幂等键 + 防抖。
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import {
  answerStructuredMessage,
  appendStructuredMessage,
  fetchInbox,
  fetchStructuredMessages,
  type StructuredScope,
} from './api'
import type { InboxResult, StructuredAnswerInput, StructuredMessage } from './types'

export interface UseStructuredMessages {
  messages: StructuredMessage[]
  loading: boolean
  error: string | null
  hasMore: boolean
  loadMore: () => Promise<void>
  inbox: InboxResult | null
  inboxLoading: boolean
  inboxError: string | null
  refreshInbox: () => Promise<void>
  answer: (
    correlationId: string,
    answers: StructuredAnswerInput[],
    cancelled: boolean,
  ) => Promise<void>
  decide: (
    target: StructuredMessage,
    verdict: 'approve' | 'reject' | 'modify',
    comments?: string,
  ) => Promise<void>
}

function sortMessages(list: StructuredMessage[]): StructuredMessage[] {
  return [...list].sort((a, b) => {
    const ta = Date.parse(a.created_at) || 0
    const tb = Date.parse(b.created_at) || 0
    if (ta !== tb) return ta - tb
    return a.message_id - b.message_id
  })
}

function mergeMessages(prev: StructuredMessage[], incoming: StructuredMessage[]): StructuredMessage[] {
  const map = new Map<number, StructuredMessage>()
  for (const m of prev) map.set(m.message_id, m)
  for (const m of incoming) {
    // 同 message_id 以服务端最新状态为准（answered/cancelled 会变化）。
    const old = map.get(m.message_id)
    if (!old || m.answered || m.cancelled || m.direction === 'in') map.set(m.message_id, m)
  }
  return sortMessages([...map.values()])
}

/**
 * deriveTargetStates —— 把 answer/decision 行投影回被它们闭环的目标消息。
 *
 * 这是跨端同步的核心，设计上保证线性一致性：
 * - 服务端 CAS 在同一个 IMMEDIATE 事务里完成（单写者 ⇒ 线性化点明确），
 *   decision/answer 行一旦提交就不再变化；
 * - 客户端只做幂等 + 单调的 OR 合并：answered 只会 false→true（不存在「撤销批红」），
 *   所以事件重复、乱序、重连补齐都不会让状态回退；
 * - 于是所有客户端（含刚重连的）都收敛到「消息流的纯函数」，而不是各自的本地状态。
 *
 * 服务端字段 answered 保留为兜底：decision 行落在未加载的历史窗口外时，
 * 仍能靠服务端快照显示「已批红」。
 */
function deriveTargetStates(list: StructuredMessage[]): StructuredMessage[] {
  const answered = new Set<number>()
  const cancelled = new Set<number>()
  // 批红结果也要投影：卡片是否显示「准/驳/改」按钮取决于 payload.decision，
  // 而跨端收到的 decision 是一条独立消息。只投影 answered 会出现
  // 「状态已批红、按钮还在」的不一致视图。
  const decisions = new Map<number, { verdict: string; comments?: string }>()
  for (const m of list) {
    if (m.kind !== 'answer' && m.kind !== 'decision') continue
    const target = Number(m.reply_to)
    if (!Number.isFinite(target) || target <= 0) continue
    if (m.cancelled) {
      cancelled.add(target)
      continue
    }
    answered.add(target)
    if (m.kind === 'decision') {
      const verdict = typeof m.payload?.verdict === 'string' ? m.payload.verdict : ''
      if (verdict) {
        decisions.set(target, {
          verdict,
          comments: typeof m.payload?.comments === 'string' ? m.payload.comments : undefined,
        })
      }
    }
  }
  if (!answered.size && !cancelled.size) return list
  let changed = false
  const out = list.map((m) => {
    const a = answered.has(m.message_id) && !m.answered
    const c = cancelled.has(m.message_id) && !m.cancelled
    const d = decisions.get(m.message_id)
    const hasDecision = !!d && !(m.payload && typeof m.payload.decision === 'object')
    if (!a && !c && !hasDecision) return m
    changed = true
    return {
      ...m,
      answered: m.answered || a,
      cancelled: m.cancelled || c,
      ...(hasDecision ? { payload: { ...(m.payload ?? {}), decision: d } } : {}),
    }
  })
  return changed ? out : list
}

export function useStructuredMessages(scope: StructuredScope): UseStructuredMessages {
  const [rawMessages, setRawMessages] = useState<StructuredMessage[]>([])
  // 派生状态：answered/cancelled 由消息流投影得出（见 deriveTargetStates）。
  // 放在 useMemo 里而不是 merge 时改写，是为了让「任何来源的新行」（SSE、重连补齐、
  // 分页加载、本端乐观写入）都走同一条推导路径 —— 多端最终收敛到同一投影。
  const messages = useMemo(() => deriveTargetStates(rawMessages), [rawMessages])
  useEffect(() => {
    rawRef.current = rawMessages
  }, [rawMessages])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasMore, setHasMore] = useState(false)
  const [inbox, setInbox] = useState<InboxResult | null>(null)
  const [inboxLoading, setInboxLoading] = useState(false)
  const [inboxError, setInboxError] = useState<string | null>(null)

  const cursorRef = useRef(0)
  const rawRef = useRef<StructuredMessage[]>([])
  const pendingRef = useRef<Set<string>>(new Set())
  const scopeKey = `${scope.channel ?? ''}:${scope.chat_id ?? ''}`

  const loadFirstPage = useCallback(async () => {
    if (!scope.chat_id) return
    setLoading(true)
    setError(null)
    try {
      // 首屏最多取 1000 条（5 页 × 200），供虚拟列表性能基线使用。
      let acc: StructuredMessage[] = []
      let cursor = 0
      let more = true
      for (let page = 0; page < 5 && more; page++) {
        const res = await fetchStructuredMessages(scope, { cursor, limit: 200 })
        acc = mergeMessages(acc, res.items)
        more = res.has_more
        cursor = res.next_cursor
        if (!res.items.length) break
      }
      cursorRef.current = cursor
      setRawMessages(acc)
      setHasMore(more)
    } catch (e) {
      setError(e instanceof Error ? e.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [scopeKey]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void loadFirstPage()
  }, [loadFirstPage])

  const loadMore = useCallback(async () => {
    if (!scope.chat_id || loading) return
    try {
      const res = await fetchStructuredMessages(scope, { cursor: cursorRef.current, limit: 200 })
      cursorRef.current = res.next_cursor
      setHasMore(res.has_more)
      setRawMessages((prev) => mergeMessages(prev, res.items))
    } catch (e) {
      setError(e instanceof Error ? e.message : '加载失败')
    }
  }, [scopeKey, loading]) // eslint-disable-line react-hooks/exhaustive-deps

  const refreshInbox = useCallback(async () => {
    if (!scope.chat_id) return
    setInboxLoading(true)
    setInboxError(null)
    try {
      const res = await fetchInbox(scope, { limit: 100 })
      setInbox(res)
    } catch (e) {
      setInboxError(e instanceof Error ? e.message : '收件箱加载失败')
    } finally {
      setInboxLoading(false)
    }
  }, [scopeKey]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    void refreshInbox()
  }, [refreshInbox])

  // scheduleInboxRefresh 合并短时间内的多次收件箱刷新（150ms 窗口）。
  // SSE 事件可能成批到达（一次回答紧跟一次批红），逐个刷新会造成请求风暴。
  const inboxTimerRef = useRef<number | null>(null)
  const scheduleInboxRefresh = useCallback(() => {
    if (inboxTimerRef.current !== null) return
    inboxTimerRef.current = window.setTimeout(() => {
      inboxTimerRef.current = null
      void refreshInbox()
    }, 150)
  }, [refreshInbox])
  useEffect(() => {
    return () => {
      if (inboxTimerRef.current !== null) {
        window.clearTimeout(inboxTimerRef.current)
        inboxTimerRef.current = null
      }
    }
  }, [])

  // backfill 按 id 单调补齐断线期间漏掉的结构化消息。
  // 上限 20 批 × 200 条，避免异常情况下无限拉取；每批都是 id 升序，天然可续。
  const backfill = useCallback(async () => {
    let after = rawRef.current.reduce((mx, m) => Math.max(mx, m.message_id), 0)
    for (let i = 0; i < 20; i++) {
      const res = await fetchStructuredMessages(scope, { after_id: after, limit: 200 })
      if (!res.items.length) break
      setRawMessages((prev) => mergeMessages(prev, res.items))
      after = res.next_cursor
      if (!res.has_more) break
    }
  }, [scopeKey]) // eslint-disable-line react-hooks/exhaustive-deps

  // SSE 增量：按 message_id 去重；重连后走 backfill 按 id 补齐。
  useEffect(() => {
    if (!scope.chat_id) return
    const q = new URLSearchParams()
    if (scope.channel) q.set('channel', scope.channel)
    q.set('chat_id', scope.chat_id)
    const es = new EventSource(`/api/sse?${q.toString()}`)
    // 服务端以「命名事件」发送：`id:..\nevent:structured_message\ndata:...`
    // （见 channel/web/web_sse.go writeSSEEvent）。EventSource 只把默认 message
    // 事件派发给 onmessage，命名事件必须 addEventListener —— 之前用 onmessage
    // 导致「服务端广播成功、前端永远收不到」：单端靠乐观更新看不出来，双端 e2e 必挂。
    const handleStructuredEvent = (ev: MessageEvent) => {
      try {
        const payload = JSON.parse(ev.data) as {
          type?: string
          structured?: StructuredMessage
        }
        if (payload.type !== 'structured_message' || !payload.structured) return
        if (!payload.structured.message_id) return
        setRawMessages((prev) => mergeMessages(prev, [payload.structured!]))
        // 只有会改变收件箱的 kind 才刷新：event/text/report 不影响未答集合。
        // 1000 条 event 灌入时若每条都 refreshInbox，会产生 1000 次 HTTP 请求。
        const k = payload.structured.kind
        if (k === 'ask_user' || k === 'answer' || k === 'decision') scheduleInboxRefresh()
      } catch {
        /* 非 JSON 事件（心跳等）忽略 */
      }
    }
    es.addEventListener('structured_message', handleStructuredEvent as EventListener)
    es.onopen = () => {
      // 重连补齐：按 after_id 增量拉取（不能只拉最新一页）。
      // 深历史下「最新一页」会漏掉中间的 answer/decision 行，而派生状态依赖它们 ——
      // 漏一条 decision 就会让已批红的卡片在重连后回退，破坏跨端一致性。
      void backfill()
      void refreshInbox()
    }
    return () => es.close()
  }, [scopeKey, refreshInbox]) // eslint-disable-line react-hooks/exhaustive-deps

  const answer = useCallback(
    async (correlationId: string, answers: StructuredAnswerInput[], cancelled: boolean) => {
      // 防抖：同一 correlation 的提交只允许一个在途。
      if (pendingRef.current.has(correlationId)) return
      pendingRef.current.add(correlationId)
      const idem = `web:${correlationId}:${cancelled ? 'cancel' : 'answer'}`
      try {
        const res = await answerStructuredMessage({
          correlation_id: correlationId,
          answers,
          cancelled,
          idempotency_key: idem,
          channel: scope.channel,
          chat_id: scope.chat_id,
        })
        setRawMessages((prev) =>
          mergeMessages(prev, [
            {
              ...res.message,
              answered: res.message.answered || !cancelled,
              cancelled: res.message.cancelled || cancelled,
            },
          ]),
        )
        // 更新原问题的终态（乐观 + 服务端权威）。
        setRawMessages((prev) =>
          sortMessages(
            prev.map((m) =>
              m.correlation_id === correlationId && m.kind === 'ask_user'
                ? { ...m, answered: !cancelled, cancelled }
                : m,
            ),
          ),
        )
        void refreshInbox()
      } finally {
        pendingRef.current.delete(correlationId)
      }
    },
    [scopeKey, refreshInbox], // eslint-disable-line react-hooks/exhaustive-deps
  )

  const decide = useCallback(
    async (target: StructuredMessage, verdict: 'approve' | 'reject' | 'modify', comments?: string) => {
      const key = `decision:${target.message_id}`
      if (pendingRef.current.has(key)) return
      pendingRef.current.add(key)
      const previous = target
      // 乐观更新：报告卡立即显示批红结果；失败回滚。
      setRawMessages((prev) =>
        prev.map((m) =>
          m.message_id === target.message_id
            ? { ...m, answered: true, payload: { ...(m.payload ?? {}), decision: { verdict, comments } } }
            : m,
        ),
      )
      try {
        const payload: Record<string, unknown> = {
          decision_id: `dec_${target.message_id}_${Date.now()}`,
          target_message_id: String(target.message_id),
          target_report_id: (target.payload?.report_id as string) ?? '',
          verdict,
          ...(comments ? { comments } : {}),
        }
        const res = await appendStructuredMessage({
          kind: 'decision',
          payload,
          correlation_id: target.correlation_id,
          reply_to: String(target.message_id),
          idempotency_key: `web:${key}`,
          channel: scope.channel,
          chat_id: scope.chat_id,
        })
        setRawMessages((prev) => mergeMessages(prev, [res.message]))
        void refreshInbox()
      } catch (e) {
        setRawMessages((prev) =>
          prev.map((m) => (m.message_id === target.message_id ? previous : m)),
        )
        throw e
      } finally {
        pendingRef.current.delete(key)
      }
    },
    [scopeKey, refreshInbox], // eslint-disable-line react-hooks/exhaustive-deps
  )

  return useMemo(
    () => ({
      messages,
      loading,
      error,
      hasMore,
      loadMore,
      inbox,
      inboxLoading,
      inboxError,
      refreshInbox,
      answer,
      decide,
    }),
    [messages, loading, error, hasMore, loadMore, inbox, inboxLoading, inboxError, refreshInbox, answer, decide],
  )
}
