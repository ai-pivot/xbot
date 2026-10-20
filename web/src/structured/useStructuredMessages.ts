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

export function useStructuredMessages(scope: StructuredScope): UseStructuredMessages {
  const [messages, setMessages] = useState<StructuredMessage[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasMore, setHasMore] = useState(false)
  const [inbox, setInbox] = useState<InboxResult | null>(null)
  const [inboxLoading, setInboxLoading] = useState(false)
  const [inboxError, setInboxError] = useState<string | null>(null)

  const cursorRef = useRef(0)
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
      setMessages(acc)
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
      setMessages((prev) => mergeMessages(prev, res.items))
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

  // SSE 增量：按 message_id 去重。断线重连后重新拉一页（游标补齐）。
  useEffect(() => {
    if (!scope.chat_id) return
    const q = new URLSearchParams()
    if (scope.channel) q.set('channel', scope.channel)
    q.set('chat_id', scope.chat_id)
    const es = new EventSource(`/api/sse?${q.toString()}`)
    es.onmessage = (ev) => {
      try {
        const payload = JSON.parse(ev.data) as {
          type?: string
          structured?: StructuredMessage
        }
        if (payload.type !== 'structured_message' || !payload.structured) return
        if (!payload.structured.message_id) return
        setMessages((prev) => mergeMessages(prev, [payload.structured!]))
        void refreshInbox()
      } catch {
        /* 非 JSON 事件（心跳等）忽略 */
      }
    }
    es.onopen = () => {
      // 重连补齐：重新拉最新一页，merge 去重，不覆盖已有更早消息。
      void fetchStructuredMessages(scope, { limit: 200 })
        .then((res) => setMessages((prev) => mergeMessages(prev, res.items)))
        .catch(() => {})
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
        setMessages((prev) =>
          mergeMessages(prev, [
            {
              ...res.message,
              answered: res.message.answered || !cancelled,
              cancelled: res.message.cancelled || cancelled,
            },
          ]),
        )
        // 更新原问题的终态（乐观 + 服务端权威）。
        setMessages((prev) =>
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
      setMessages((prev) =>
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
        setMessages((prev) => mergeMessages(prev, [res.message]))
        void refreshInbox()
      } catch (e) {
        setMessages((prev) =>
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
