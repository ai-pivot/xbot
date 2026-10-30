/**
 * inboxStore —— 用户看板（收件箱）的单一状态源。
 *
 * 为什么必须是单例 store，而不是像原来那样各组件自己拉：
 * 同一份「待处理集合 + 未处理数」至少三处要同时可见 ——
 * 侧栏入口角标（InboxEntry）、看板列表（Inbox）、新到提醒（toast）。
 * 各拉各的话，用户在看板上点了「准」，角标还挂着要等下一次轮询才变
 * （N1 要修的正是「状态不流转」）。
 *
 * 取数 scope（2026-10-30 实测，别退回去）：
 * 服务端 `sessionOf(sel)` 把 tenant 按**会话**解析 —— 同一份后端数据
 *   GET /api/inbox?channel=web                  → unanswered 0（落到默认会话的 tenant）
 *   GET /api/inbox?channel=web&chat_id=chat_X   → unanswered 1（目标会话的 tenant）
 * 所以调用方必须把**当前会话**交进来（看板用路由里的 scope、侧栏用 activeSession）；
 * 不能在 store 里写死 `{channel:'web'}`，那会让徽标恒 0（本版曾因此让
 * `structured-cards.spec.ts` 收件箱用例从绿转红）。
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

export interface InboxScope {
  channel?: string
  chat_id?: string
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
let scope: InboxScope = { channel: 'web' }
/** 上一次成功刷新时的待处理 id 集合（用于判定「新到」）。 */
let knownIds = new Set<number>()
/** 首次刷新前不弹提醒：一进页面就炸一堆 toast 不是「非侵入」。 */
let primed = false
let inflight: Promise<void> | null = null
/** 会话切换计数：旧会话在途的响应必须丢弃，不能落到新会话上。 */
let generation = 0
const listeners = new Set<() => void>()

function emit(patch: Partial<InboxState>) {
  state = { ...state, ...patch }
  for (const listener of listeners) listener()
}

export function getInboxState(): InboxState {
  return state
}

export function getInboxScope(): InboxScope {
  return scope
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
  scope = { channel: 'web' }
  knownIds = new Set()
  primed = false
  inflight = null
  generation += 1
}

/**
 * 切换取数会话。tenant 是按会话解析的（见文件头实测），所以看板、侧栏入口
 * 都必须显式设置当前会话，否则看到的永远是「默认会话」那一份（常常是空的）。
 */
export function setInboxScope(next: InboxScope): void {
  const sameScope =
    (scope.channel ?? '') === (next.channel ?? '') && (scope.chat_id ?? '') === (next.chat_id ?? '')
  if (sameScope) return
  scope = { channel: next.channel || 'web', ...(next.chat_id ? { chat_id: next.chat_id } : {}) }
  generation += 1
  inflight = null
  knownIds = new Set()
  // 换会话=换数据源：首屏不算「新到」，不弹提醒。
  primed = false
  emit({ items: [], unanswered: 0, loading: false, error: null })
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
 * 拉一次收件箱（当前 scope）。并发调用合并成一次请求（多组件同时挂载时只发一个）。
 * notify=false 用于「已知变化」的刷新（如 resolve 之后），不弹「新到」提醒。
 */
export function refreshInbox(opts: { notify?: boolean } = {}): Promise<void> {
  if (inflight) return inflight
  const gen = generation
  const requestedScope = scope
  emit({ loading: true, error: null })
  inflight = (async () => {
    try {
      const res: InboxResult = await fetchInbox(requestedScope, { limit: 100 })
      if (gen !== generation) return // 会话已切换：丢弃旧响应
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
      if (gen !== generation) return
      // 失败保留上一次的列表与数字（网络抖动不该让入口消失）。
      emit({ loading: false, error: e instanceof Error ? e.message : '收件箱加载失败' })
    } finally {
      if (gen === generation) inflight = null
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
      channel: scope.channel,
      ...(scope.chat_id ? { chat_id: scope.chat_id } : {}),
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

/**
 * 需要轮询的挂载点（常驻入口）用这个；引用计数保证只跑一个定时器。
 * scope 变化时会切数据源并立刻重拉。
 */
export function useInboxPolling(
  scopeOrUndefined?: InboxScope,
  intervalMs: number = INBOX_POLL_MS,
): InboxState {
  const snapshot = useInboxStore()
  const channel = scopeOrUndefined?.channel
  const chatID = scopeOrUndefined?.chat_id
  useEffect(() => {
    setInboxScope({ channel, chat_id: chatID })
    startPolling(intervalMs)
    return () => stopPolling()
  }, [channel, chatID, intervalMs])
  return snapshot
}

/** 看板用：拿状态 + 两个动作（refresh / resolve）。 */
export function useInboxBoard(scopeArg: InboxScope) {
  const snapshot = useInboxStore()
  const channel = scopeArg.channel
  const chatID = scopeArg.chat_id
  const refresh = useCallback(() => {
    setInboxScope({ channel, chat_id: chatID })
    return refreshInbox({ notify: false })
  }, [channel, chatID])
  // 打开看板 / 切换会话就先拉一次：看到的是最新待处理，而不是其它会话的快照。
  useEffect(() => {
    void refresh()
  }, [refresh])

  const inFlight = useRef<Set<number>>(new Set())
  const resolve = useCallback(
    async (messageId: number, action: InboxResolveAction, comment?: string) => {
      // 同一张卡在处理中时不重复提交（双击）。
      if (inFlight.current.has(messageId)) return null
      inFlight.current.add(messageId)
      try {
        return await resolveInboxCard(messageId, action, comment)
      } finally {
        inFlight.current.delete(messageId)
      }
    },
    [],
  )
  return { ...snapshot, refresh, resolve }
}
