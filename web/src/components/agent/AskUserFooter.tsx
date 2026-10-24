/**
 * AskUserFooter —— 主聊天页的「待回答问题」入口（2026-10-22 第三轮修订：置顶）。
 *
 * 历史：`useProgressStream` 收到 `ask_user` 只 `stopStreaming()` 就 return，
 * 所以这个入口是补回来的。前两轮把它放在输入框正上方 —— 用户实测：中间 500px
 * 全空白、横幅在最底下，**根本看不到**。这一轮按要求改到**消息区顶部**
 * （sticky，滚动时不消失），并且横幅必须能说出「是谁、问什么」。
 *
 * 硬规则（前两轮用户实测反馈，继续保留）：
 *   ① **只认当前会话**：待答项必须属于当前激活会话（`channel:chatID` 完全匹配）。
 *   ② **绝不自动展开**：默认只有一条横幅，展开只能由用户点击触发。
 */
import { useCallback, useEffect, useMemo, useState } from 'react'

import { AskUserCard, type AnswerFn } from '@/structured/StructuredCards'
import { answerStructuredMessage, fetchInbox } from '@/structured/api'
import { StructuredLink } from '@/components/StructuredLink'
import type { InboxResult, StructuredMessage } from '@/structured/types'
import type { SessionStore } from '@/hooks/useSessionStore'
import { dispatchAskAnswered, subscribeAskAnswered } from '@/lib/sessionEvents'
import type { AskUserPrompt } from '@/types/agent'

/**
 * live prompt → StructuredMessage（AskUserCard 的入参）。
 * 问题 id 按序号生成：live 侧本来就没有 id，服务端只用 correlation_id 定位问题行。
 */
export function promptToStructuredMessage(
  prompt: AskUserPrompt,
  channel: string,
  chatID: string,
): StructuredMessage {
  return {
    message_id: 0,
    kind: 'ask_user',
    correlation_id: prompt.requestId,
    direction: 'in',
    answered: false,
    cancelled: false,
    // live prompt 没有「背景」这一层（后端要求结构化 ask_user 必须带 message 背景）；
    // 这里留空，让卡片明确显示"缺少上下文"的警示，而不是把问题重复一遍当背景。
    text: '',
    payload: {
      questions: prompt.questions.map((q, i) => ({
        id: `q${i}`,
        question: q.question,
        options: (q.options ?? []).map((o) => ({ id: o, label: o })),
        multi_select: q.multiSelect === true,
        allow_other: q.allowOther === true,
      })),
    },
    sender: { type: 'agent', id: `${channel}:${chatID}` },
    created_at: new Date().toISOString(),
  }
}

/**
 * 这一行是不是属于 `channel:chatID` 这个会话？
 * 服务端在信封里给了 `from.session`（= `channel:chatID`）就严格比对；
 * 老数据没有该字段时只能信任请求本身的作用域（返回 true）。
 */
export function belongsToSession(m: StructuredMessage, channel: string, chatID: string): boolean {
  const env = m.envelope as Record<string, unknown> | undefined
  const from = env && typeof env.from === 'object' ? (env.from as Record<string, unknown>) : undefined
  const session = typeof from?.session === 'string' ? from.session : ''
  if (!session) return true
  return session === `${channel}:${chatID}`
}

/**
 * 从收件箱里挑出与 live prompt 对应的行：
 *   ① correlation_id 完全相等的（结构化 ask_user 走这条）；
 *   ② 收件箱里恰好只有 1 个未答问题时，视作同一次提问的持久化投影 ——
 *      live 的 `requestId` 与结构化行的 `correlation_id` 是两套 id。
 */
export function matchInboxRow(
  prompt: AskUserPrompt,
  items: StructuredMessage[],
): StructuredMessage | null {
  const exact = items.find((m) => m.correlation_id && m.correlation_id === prompt.requestId)
  if (exact) return exact
  const pending = items.filter((m) => !m.answered && !m.cancelled)
  if (pending.length === 1) return pending[0]
  return null
}

/** 发问方展示名：子代理名优先，其次角色名，最后才是会话 id。 */
export function askerOf(m: StructuredMessage): string {
  const p = (m.payload ?? {}) as Record<string, unknown>
  const dept = typeof p.department === 'string' ? p.department.trim() : ''
  if (dept) return dept
  if (m.sender?.name && m.sender.name.trim()) return m.sender.name.trim()
  const env = m.envelope as Record<string, unknown> | undefined
  const from = env && typeof env.from === 'object' ? (env.from as Record<string, unknown>) : undefined
  for (const key of ['role', 'name']) {
    const v = from?.[key]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  const id = (m.sender?.id ?? '').replace('agent:', '')
  if (!id) return '某个子代理'
  return id.split('/')[0].split(':')[0] || '某个子代理'
}

/** 一条提问的「是什么事」摘要：第一个问题 > 正文首行 > 明确说没有背景。 */
export function askSummary(m: StructuredMessage): string {
  const p = (m.payload ?? {}) as Record<string, unknown>
  const qs = Array.isArray(p.questions) ? p.questions : []
  for (const q of qs) {
    if (q && typeof q === 'object') {
      const question = (q as Record<string, unknown>).question
      if (typeof question === 'string' && question.trim()) return question.trim()
    }
  }
  const first = (m.text ?? '').split('\n').map((l) => l.trim()).filter(Boolean)[0]
  return first || ''
}

function clip(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s
}

/**
 * 展开态是**会话级共享**的：同一会话可能被多个面板实例渲染（dockview 旧 root
 * 不卸载），如果每个实例各存一份 `expanded`，点开的那份和显示的那份可能不是同一个
 * —— 表现就是"点了没反应"。这里用模块级 Map + 订阅，所有实例永远看到同一个值。
 */
const expandState = new Map<string, boolean>()
const expandSubs = new Map<string, Set<() => void>>()

function setExpandedShared(sessionKey: string, value: boolean): void {
  expandState.set(sessionKey, value)
  for (const fn of expandSubs.get(sessionKey) ?? []) fn()
}

function useSharedExpanded(sessionKey: string): [boolean, (v: boolean) => void] {
  const [, force] = useState(0)
  useEffect(() => {
    if (!sessionKey) return
    const subs = expandSubs.get(sessionKey) ?? new Set<() => void>()
    const rerender = () => force((n) => n + 1)
    subs.add(rerender)
    expandSubs.set(sessionKey, subs)
    return () => {
      subs.delete(rerender)
      if (subs.size === 0) expandSubs.delete(sessionKey)
    }
  }, [sessionKey])
  const set = useCallback((v: boolean) => setExpandedShared(sessionKey, v), [sessionKey])
  return [sessionKey ? (expandState.get(sessionKey) ?? false) : false, set]
}

export interface AskUserFooterProps {
  store: Pick<
    SessionStore,
    'askUserPrompts' | 'clearAskUserPrompt' | 'markAskUserAnswered' | 'hydrateAskUserPrompt'
  >
  channel: string
  chatID: string
  /** 收件箱读取（测试可注入；默认走 /api/inbox）。 */
  loadInbox?: typeof fetchInbox
  /** 提交（测试可注入；默认走 /api/answer）。 */
  submit?: typeof answerStructuredMessage
}

export function AskUserFooter({
  store,
  channel,
  chatID,
  loadInbox = fetchInbox,
  submit = answerStructuredMessage,
}: AskUserFooterProps) {
  const sessionKey = `${channel}:${chatID}`

  const prompt = chatID ? store.askUserPrompts?.get(sessionKey) : undefined
  const [inboxRows, setInboxRows] = useState<StructuredMessage[]>([])
  const [refreshToken, setRefreshToken] = useState(0)
  const [expanded, setExpanded] = useSharedExpanded(sessionKey)

  useEffect(() => {
    setInboxRows([])
    if (!chatID) return
    let alive = true
    loadInbox({ channel, chat_id: chatID }, { limit: 20 })
      .then((res: InboxResult) => {
        if (!alive) return
        const items = (res?.items ?? []).filter(
          (m) => m.kind === 'ask_user' && !m.answered && !m.cancelled && belongsToSession(m, channel, chatID),
        )
        setInboxRows(items)
      })
      .catch(() => {
        if (alive) setInboxRows([])
      })
    return () => {
      alive = false
    }
  }, [prompt, channel, chatID, loadInbox, refreshToken])

  useEffect(() => {
    return subscribeAskAnswered((d) => {
      if (d.sessionKey !== sessionKey) return
      setRefreshToken((n) => n + 1)
    })
  }, [sessionKey])

  const cards = useMemo<StructuredMessage[]>(() => {
    if (!chatID) return []
    if (prompt) return [matchInboxRow(prompt, inboxRows) ?? promptToStructuredMessage(prompt, channel, chatID)]
    return inboxRows.slice(0, 3)
  }, [prompt, inboxRows, channel, chatID])

  // dockview 在切换会话时会把旧面板的整棵 React 树留在 DOM 里（实测同一问题能叠
  // 出 2–3 条横幅，且旧会话的横幅会一直挂着 = 用户报的"换会话也弹"）。这里做
  // DOM 层清理：**只要不属于当前会话，就地移除**。
  useEffect(() => {
    if (typeof document === 'undefined') return
    const entries = Array.from(document.querySelectorAll<HTMLElement>('[data-ask-entry]'))
    const mine: HTMLElement[] = []
    for (const el of entries) {
      if (el.getAttribute('data-ask-entry') !== sessionKey) {
        el.remove()
        continue
      }
      mine.push(el)
    }
    for (const el of mine.slice(0, -1)) el.remove()
    if (cards.length > 0) {
      for (const el of mine) if (el.style.display === 'none') el.style.display = ''
    }
  })

  useEffect(() => {
    if (cards.length === 0) setExpanded(false)
  }, [cards.length])

  const onAnswer = useCallback<AnswerFn>(
    async (correlationId, answers, cancelled) => {
      const boundRow = inboxRows.find((m) => m.correlation_id === correlationId)
      const samePrompt = !prompt || !boundRow || boundRow.correlation_id === correlationId
      if (prompt && samePrompt) {
        store.markAskUserAnswered?.(channel, chatID, prompt.requestId)
        store.clearAskUserPrompt?.(channel, chatID)
      }
      try {
        await submit({ correlation_id: correlationId, answers, cancelled, channel, chat_id: chatID })
        setInboxRows((prev) => prev.filter((m) => m.correlation_id !== correlationId))
        setExpanded(false)
        dispatchAskAnswered(sessionKey)
        if (typeof document !== 'undefined') {
          for (const el of Array.from(document.querySelectorAll<HTMLElement>(`[data-ask-entry="${sessionKey}"]`))) {
            el.style.display = 'none'
          }
        }
      } catch (e) {
        if (prompt && samePrompt) {
          store.markAskUserAnswered?.(channel, chatID, '')
          store.hydrateAskUserPrompt?.(channel, chatID, prompt)
        }
        throw e
      }
    },
    [store, channel, chatID, prompt, submit, inboxRows, setExpanded],
  )

  if (cards.length === 0) return null

  const first = cards[0]
  const who = askerOf(first)
  const what = askSummary(first)
  const more = cards.length > 1

  // ── 收起态：消息区顶部的一条横幅（sticky 由 AgentPanel 负责）──
  if (!expanded) {
    return (
      <div data-ask-entry={sessionKey} data-testid="ask-user-banner-wrap" className="mx-3 mt-2">
        <div
          className="flex items-start gap-2 rounded-lg border border-lane-busy/40 bg-lane-busy/10 px-3 py-2 text-[11px] leading-4"
        >
          <button
            type="button"
            data-testid="ask-user-banner"
            aria-expanded={false}
            onClick={() => setExpanded(true)}
            className="flex min-w-0 flex-1 items-start gap-2 text-left"
          >
            <span aria-hidden className="mt-[1px] text-[12px]">⚠️</span>
            <span className="min-w-0 flex-1">
              <span className="block font-medium text-text-primary">
                {more
                  ? `有 ${cards.length} 个待回答的提问`
                  : `${who}询问：${what ? clip(what, 46) : '（没有填写问题正文）'}`}
              </span>
              {more ? (
                <span className="mt-0.5 block truncate text-[10px] text-text-secondary" data-testid="ask-user-banner-summary">
                  最近：{who}「{what ? clip(what, 40) : '未填写正文'}」
                </span>
              ) : what ? null : (
                <span className="mt-0.5 block text-[10px] text-lane-waiting">发问方没有填写问题正文</span>
              )}
            </span>
            <span className="mt-0.5 shrink-0 text-[10px] font-normal text-text-secondary">展开 ▾</span>
          </button>
          {/* 「去处理」直达结构化消息页（收起态也能走），不触发展开。 */}
          <StructuredLink
            className="mt-0.5 shrink-0 rounded bg-app-accent/15 px-2 py-0.5 text-[10px] font-medium text-app-accent hover:bg-app-accent/25"
            title="打开结构化消息页处理这条提问"
          >
            去处理 →
          </StructuredLink>
        </div>
      </div>
    )
  }

  // ── 展开态（用户点开的）：完整问题 + 选项 + 提交，带「收起」──
  return (
    <div data-testid="ask-user-expanded" data-ask-entry={sessionKey} className="mx-3 mt-2 space-y-2">
      <div className="flex items-center gap-2 text-[11px] font-semibold text-text-primary">
        <span aria-hidden>⚠️</span>
        <span>{more ? `有 ${cards.length} 个待回答的提问` : `${who}询问：${what ? clip(what, 40) : '（未填写正文）'}`}</span>
        <StructuredLink
          className="ml-auto shrink-0 rounded bg-app-accent/15 px-2 py-0.5 text-[10px] font-normal text-app-accent hover:bg-app-accent/25"
          title="打开结构化消息页处理这条提问"
        >
          去处理 →
        </StructuredLink>
        <button
          type="button"
          data-testid="ask-user-collapse"
          onClick={() => setExpanded(false)}
          className="shrink-0 rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] font-normal text-text-secondary hover:text-text-primary"
        >
          收起 ▴
        </button>
      </div>
      {cards.map((m) => (
        <AskUserCard key={`${m.correlation_id ?? ''}:${m.message_id}`} message={m} onAnswer={onAnswer} />
      ))}
    </div>
  )
}
