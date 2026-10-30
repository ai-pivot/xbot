/**
 * inboxStore —— 用户看板（收件箱）的单一状态源。
 *
 * 为什么必须是单例 store，而不是像原来那样各组件自己拉：
 * 同一份「待处理集合 + 未处理数」至少三处要同时可见 ——
 * 侧栏入口角标（InboxEntry）、看板列表（Inbox）、新到提醒（toast）。
 * 各拉各的话，用户在看板上点了「准」，角标还挂着要等下一次轮询才变
 * （N1 要修的正是「状态不流转」）。
 *
 * 口径与后端 /api/inbox 完全一致：
 * - items：kind ∈ {ask_user, report} 且 answered=0 且 cancelled=0；
 * - unanswered：服务端 COUNT（本地不自己数，避免分页窗口与总数打架）。
 *
 * 状态流转与幂等边界（N1 验收的第 ②③ 条）：
 * - resolve 成功（duplicate=false）→ 本地移除该条 + 未处理数 -1；
 * - resolve 返回 duplicate=true（服务端早已处理，重复点/刷新后重点）→
 *   只移除残留条目，**不再减一**，否则角标会被减成负数；
 * - 409 conflict（已处理但换了幂等键）→ 状态已是终态，同样不移除数字。
 */
import { useCallback, useEffect, useRef, useSyncExternalStore } from 'react'
import { toast } from 'sonner'

import { APIError } from '@/lib/api'
import { fetchInbox, resolveInboxItem } from './api'
import type { InboxResolveAction, InboxResult, StructuredMessage } from './types'

export interface InboxState {
  items: StructuredMessage[]
  unanswered: number
  loading: boolean
  error: string | null
}

export interface ResolveOutcome {
  /** 服务端判定为幂等命中（这张卡早已处理过）。 */
  duplicate: boolean
  /** 本地列表里原本是否有这一条（决定要不要扣数字）。 */
  removed: boolean
}

const EMPTY_STATE: InboxState = { items: [], unanswered: 0, loading: false, error: null }

/** 看板页面路由：toast 的「去处理」跳到它。 */
export const INBOX_ROUTE = '/structured'

let state: InboxState = EMPTY_STATE
/** 上一次成功刷新时的待处理 id 集合（用于判定「新到」）。 */
let knownIds = new Set<number>()
/** 首次刷新前不弹提醒：一进页面就炸一堆 toast 不是「非侵入」。 */
let primed = false
let inflight: Promise<void> | null = null
const listeners = new Set<() => void>()

function emit(patch: Partial<InboxState>) {
  state = { ...state, ...patch }
  for (const listener of listeners) listener()
}

export function getInboxState(): InboxState {
  return state
}

export function subscribeInbox(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** 仅测试用：清空单例，避免用例之间互相污染。 */
export function resetInboxStore(): void {
  state = EMPTY_STATE
  knownIds = new Set()
  primed = false
  inflight = null
}

/** 「去处理」：已经在看板上就原地不动（只关 toast）。 */
function openInbox() {
  if (typeof window === 'undefined') return
  if (!window.location.pathname.startsWith(INBOX_ROUTE)) {
    window.location.assign(INBOX_ROUTE)
  }
}

function notifyNew(count: number) {
  toast(count > 1 ? `收件箱有 ${count} 条新的待处理` : '收件箱有 1 条新的待处理', {
    // 固定 id：连续到达时覆盖上一条，不堆叠成一堵墙。
    id: 'inbox-new-pending',
    description: '子代理在等批复或回答',
    duration: 8000,
    action: { label: '去处理', onClick: openInbox },
  })
}

/**
 * 拉一次收件箱。并发调用合并成一次请求（多组件同时挂载时只发一个）。
 * notify=false 用于「已知变化」的刷新（如 resolve 之后），不弹「新到」提醒。
 */
export function refreshInbox(opts: { notify?: boolean } = {}): Promise<void> {
  if (inflight) return inflight
  emit({ loading: true, error: null })
  inflight = (async () => {
    try {
      const res: InboxResult = await fetchInbox({ channel: 'web' }, { limit: 100 })
      const items = res.items ?? []
      const nextIds = new Set(items.map((item) => item.message_id))
      const fresh = [...nextIds].filter((id) => !knownIds.has(id))
      if (primed && opts.notify !== false && fresh.length > 0) notifyNew(fresh.length)
      knownIds = nextIds
      primed = true
      emit({
        items,
        unanswered: typeof res.unanswered === 'number' ? res.unanswered : items.length,
        loading: false,
        error: null,
      })
    } catch (e) {
      // 失败保留上一次的列表与数字（网络抖动不该让入口消失）。
      emit({ loading: false, error: e instanceof Error ? e.message : '收件箱加载失败' })
    } finally {
      inflight = null
    }
  })()
  return inflight
}

/**
 * 处理一张卡（approve / reject / answer / dismiss）。
 *
 * 服务端成功后本地立刻收敛到终态：条目消失、未处理数 -1 —— 不等下一次轮询。
 * 幂等命中（duplicate=true）只清残留条目，绝不重复扣数字。
 */
export async function resolveInboxCard(
  messageId: number,
  action: InboxResolveAction,
  comment?: string,
): Promise<ResolveOutcome> {
  const wasPending = state.items.some((item) => item.message_id === messageId)
  try {
    const res = await resolveInboxItem({
      message_id: messageId,
      action,
      ...(comment ? { comment } : {}),
      // 默认幂等键与服务端兜底同构：同一张卡的同一个动作只生效一次。
      idempotency_key: `web:inbox:${messageId}:${action}`,
      channel: 'web',
    })
    const items = state.items.filter((item) => item.message_id !== messageId)
    // 只有「首次流转」且本地确实还挂着这一条时才扣 1：
    // duplicate=true 说明本地数字早已算过它了（重复点/刷新后重点）。
    const dec = !res.duplicate && wasPending ? 1 : 0
    emit({ items, unanswered: Math.max(0, state.unanswered - dec) })
    knownIds.delete(messageId)
    return { duplicate: !!res.duplicate, removed: wasPending }
  } catch (e) {
    if (e instanceof APIError && e.status === 409) {
      // 服务端已处理（换幂等键重复提交）：本地同样收敛，但不扣数字。
      emit({ items: state.items.filter((item) => item.message_id !== messageId) })
      knownIds.delete(messageId)
      return { duplicate: true, removed: wasPending }
    }
    emit({ error: e instanceof Error ? e.message : '处理失败' })
    throw e
  }
}

/** 轮询间隔：结构化消息不频繁，30s 足够；focus / 看板打开时立即刷。 */
export const INBOX_POLL_MS = 30_000

let pollRefs = 0
let pollTimer: ReturnType<typeof setInterval> | null = null

function onWindowFocus() {
  void refreshInbox({ notify: false })
}

function startPolling(intervalMs: number) {
  pollRefs += 1
  if (pollTimer !== null) return
  void refreshInbox()
  pollTimer = setInterval(() => void refreshInbox(), intervalMs)
  if (typeof window !== 'undefined') window.addEventListener('focus', onWindowFocus)
}

function stopPolling() {
  pollRefs = Math.max(0, pollRefs - 1)
  if (pollRefs > 0 || pollTimer === null) return
  clearInterval(pollTimer)
  pollTimer = null
  if (typeof window !== 'undefined') window.removeEventListener('focus', onWindowFocus)
}

/** 只读订阅（角标/看板用）。 */
export function useInboxStore(): InboxState {
  return useSyncExternalStore(subscribeInbox, getInboxState, getInboxState)
}

/** 需要轮询的挂载点（常驻入口）用这个；引用计数保证只跑一个定时器。 */
export function useInboxPolling(intervalMs: number = INBOX_POLL_MS): InboxState {
  const snapshot = useInboxStore()
  useEffect(() => {
    startPolling(intervalMs)
    return () => stopPolling()
  }, [intervalMs])
  return snapshot
}

/** 看板用：拿状态 + 两个动作（refresh / resolve）。 */
export function useInboxBoard() {
  const snapshot = useInboxStore()
  const resolve = useCallback(
    (messageId: number, action: InboxResolveAction, comment?: string) =>
      resolveInboxCard(messageId, action, comment),
    [],
  )
  const refresh = useCallback(() => refreshInbox({ notify: false }), [])
  const inFlight = useRef<Set<number>>(new Set())
  const resolveOnce = useCallback(
    async (messageId: number, action: InboxResolveAction, comment?: string) => {
      // 同一张卡在处理中时不重复提交（双击）。
      if (inFlight.current.has(messageId)) return null
      inFlight.current.add(messageId)
      try {
        return await resolve(messageId, action, comment)
      } finally {
        inFlight.current.delete(messageId)
      }
    },
    [resolve],
  )
  return { ...snapshot, refresh, resolve: resolveOnce }
}
