/**
 * Inbox —— 待处理看板（用户看板卡片的唯一处理界面）。
 *
 * 数据来自共享 inboxStore 的 `GET /api/inbox`（kind ∈ {ask_user, report}、
 * answered=0、cancelled=0），不是从聊天流里捞 —— 卡片行 record_type='notice'
 * 本来就不进对话流/历史回放，等消息流永远等不到它。
 *
 * **scope 必须是当前会话**：服务端按会话解析 tenant，不带 chat_id 时读到的是
 * 默认会话那份（常常是空的）。所以看板由 StructuredWorkspace 把路由里的
 * scope 传进来，与消息流用同一份 —— 徽标/列表才和用户看到的消息流对得上。
 *
 * 交互保持与既有 E2E 一致：
 * - 点条目 → 展开详情（渲染 StructuredCard，ask_user 给回答表单、report 给准/驳/改）；
 * - 展开区顶部是本轮新增的四动作（准 / 驳 / 忽略 / 回答）→ 走 `/api/inbox/resolve`，
 *   成功后该条从列表消失、侧栏角标 -1；幂等命中（duplicate=true）只清残留、不重复扣。
 * 动作按钮放在展开区而不是每条常驻，是为了不改变「点条目=展开」的点击语义。
 */
import { useState } from 'react'

import { StructuredCard, KIND_META, LongText, formatSender, messageSummary, type AnswerFn, type DecideFn } from './StructuredCards'
import { useInboxBoard, type InboxScope } from './inboxStore'
import type { InboxResolveAction, StructuredMessage } from './types'

/** 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前 / M月D日 HH:mm。 */
export function relativeTime(iso: string, now: number = Date.now()): string {
  const d = new Date(iso)
  const ts = d.getTime()
  if (Number.isNaN(ts)) return ''
  const diff = Math.max(0, now - ts)
  const min = Math.floor(diff / 60_000)
  if (min < 1) return '刚刚'
  if (min < 60) return `${min} 分钟前`
  const hours = Math.floor(min / 60)
  if (hours < 24) return `${hours} 小时前`
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days} 天前`
  // 老条目回落到本地日期（手工拼，避免 zh-CN toLocaleString 输出 "9/1"）。
  const mm = String(d.getMonth() + 1)
  const dd = String(d.getDate())
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}月${dd}日 ${hh}:${mi}`
}

const ACTIONS: { action: Exclude<InboxResolveAction, 'answer'>; label: string }[] = [
  { action: 'approve', label: '准' },
  { action: 'reject', label: '驳' },
  { action: 'dismiss', label: '忽略' },
]

function KindTag({ message }: { message: StructuredMessage }) {
  const meta = KIND_META[message.kind] ?? KIND_META.event
  return (
    <span className={`shrink-0 rounded border px-1 py-0.5 text-[10px] font-medium ${meta.badge}`}>
      {meta.label}
    </span>
  )
}

export function Inbox({
  scope,
  onAnswer,
  onDecide,
}: {
  scope: InboxScope
  onAnswer: AnswerFn
  onDecide: DecideFn
}) {
  const { items, unanswered, loading, error, refresh, resolve } = useInboxBoard(scope)
  const [expanded, setExpanded] = useState<number | null>(null)
  const [draft, setDraft] = useState<Record<number, string>>({})
  const [busy, setBusy] = useState<number | null>(null)

  const act = async (messageId: number, action: InboxResolveAction) => {
    if (busy === messageId) return
    setBusy(messageId)
    try {
      await resolve(messageId, action, draft[messageId]?.trim() || undefined)
      // 成功后这条已被 store 移除；清掉草稿与展开态即可。
      setDraft((cur) => {
        const next = { ...cur }
        delete next[messageId]
        return next
      })
      setExpanded((cur) => (cur === messageId ? null : cur))
    } catch {
      /* store 已把错误写进 state.error，看板顶部会显示 */
    } finally {
      setBusy(null)
    }
  }

  return (
    // 窄屏（<768px）上下堆叠：340px 固定宽栏会把消息流挤到 230px 并裁字
    // （2026-10-24 390×844 实测）；桌面仍是右侧固定宽栏。
    <aside
      data-testid="inbox"
      className="flex min-h-0 w-full shrink-0 basis-[45%] flex-col border-t border-border bg-bg-secondary md:h-full md:basis-auto md:w-[340px] md:border-l md:border-t-0"
    >
      <header className="flex items-center justify-between border-b border-border px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-text-primary">收件箱</span>
          <span
            data-testid="inbox-badge"
            className="rounded-full bg-kind-ask-user px-1.5 py-0.5 text-[10px] font-bold text-bg-primary"
          >
            {unanswered}
          </span>
        </div>
        <button
          type="button"
          onClick={() => void refresh()}
          className="text-[11px] text-app-accent hover:underline"
        >
          刷新
        </button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {error && items.length > 0 ? (
          <p data-testid="inbox-error" className="mb-1.5 rounded bg-destructive/10 px-2 py-1 text-[11px] text-destructive">
            {error}
          </p>
        ) : null}
        {loading && items.length === 0 ? (
          <p data-testid="inbox-loading" className="p-3 text-xs text-text-muted">
            加载中…
          </p>
        ) : error && items.length === 0 ? (
          <p data-testid="inbox-error" className="p-3 text-xs text-destructive">
            {error}
          </p>
        ) : items.length === 0 ? (
          <p data-testid="inbox-empty" className="p-3 text-xs text-text-secondary">
            没有待处理的消息。子代理的提问（ask_user）和汇报（report）会出现在这里。
          </p>
        ) : (
          <ul className="space-y-1.5">
            {items.map((m) => (
              <li key={m.message_id} data-testid="inbox-item">
                <button
                  type="button"
                  onClick={() => setExpanded((cur) => (cur === m.message_id ? null : m.message_id))}
                  className="w-full rounded-lg border border-kind-ask-user/30 bg-bg-primary px-2.5 py-2 text-left hover:bg-bg-tertiary"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="flex min-w-0 items-center gap-1.5">
                      <KindTag message={m} />
                      <span className="truncate text-[13px] font-medium text-text-primary">
                        {formatSender(m)}
                      </span>
                    </span>
                    <span
                      className="shrink-0 text-[13px] text-text-muted"
                      title={`${m.created_at} · ${relativeTime(m.created_at)}`}
                    >
                      {relativeTime(m.created_at)}
                    </span>
                  </div>
                  <p className="mt-0.5 line-clamp-2 text-xs text-text-secondary">{messageSummary(m)}</p>
                </button>
                {expanded === m.message_id ? (
                  <div className="mt-1.5 space-y-1.5">
                    <div className="flex flex-wrap items-center gap-1.5">
                      {ACTIONS.map(({ action, label }) => (
                        <button
                          key={action}
                          type="button"
                          data-testid={`inbox-${action}`}
                          disabled={busy === m.message_id}
                          onClick={() => void act(m.message_id, action)}
                          className="rounded border border-border px-1.5 py-0.5 text-[11px] text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
                        >
                          {label}
                        </button>
                      ))}
                      {m.kind === 'ask_user' ? (
                        <>
                          <input
                            data-testid="inbox-answer-input"
                            value={draft[m.message_id] ?? ''}
                            onChange={(e) =>
                              setDraft((cur) => ({ ...cur, [m.message_id]: e.target.value }))
                            }
                            placeholder="回答内容"
                            className="min-w-0 flex-1 rounded border border-border bg-bg-primary px-1.5 py-0.5 text-[11px] text-text-primary"
                          />
                          <button
                            type="button"
                            data-testid="inbox-answer"
                            disabled={busy === m.message_id}
                            onClick={() => void act(m.message_id, 'answer')}
                            className="rounded border border-border px-1.5 py-0.5 text-[11px] text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
                          >
                            回答
                          </button>
                        </>
                      ) : null}
                    </div>
                    <StructuredCard message={m} onAnswer={onAnswer} onDecide={onDecide} />
                    <details>
                      <summary className="cursor-pointer text-[11px] text-text-muted">原文</summary>
                      <LongText text={messageSummary(m)} />
                    </details>
                  </div>
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </div>
    </aside>
  )
}
