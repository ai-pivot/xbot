/**
 * Inbox —— 收件箱（待人工处理的结构化消息：ask_user 待回答 + report 待批复）。
 *
 * 列表：谁发来的 / 摘要 / 相对时间（「3 分钟前」）；点击展开直接处理
 * （复用 StructuredCard：ask_user 给回答表单，report 给「准 / 驳 / 改」）。
 * 空态/加载态/错误态完整。
 */
import { useState } from 'react'

import { StructuredCard, formatClock, formatSender, messageSummary, type AnswerFn, type DecideFn } from './StructuredCards'
import type { InboxResult } from './types'

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

export function Inbox({
  inbox,
  loading,
  error,
  onAnswer,
  onDecide,
  onRefresh,
}: {
  inbox: InboxResult | null
  loading: boolean
  error: string | null
  onAnswer: AnswerFn
  onDecide: DecideFn
  onRefresh: () => void
}) {
  const [expanded, setExpanded] = useState<number | null>(null)
  const items = inbox?.items ?? []

  return (
    <aside
      data-testid="inbox"
      // 窄屏（<768px）改成上下堆叠：340px 固定宽度会把消息流挤到 230px 并裁字
      // （2026-10-24 390×844 实测）。桌面仍是右侧固定宽栏。
      className="flex min-h-0 w-full shrink-0 basis-[45%] flex-col border-t border-border bg-bg-secondary md:h-full md:basis-auto md:w-[340px] md:border-l md:border-t-0"
    >
      <header className="flex items-center justify-between border-b border-border px-3 py-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-text-primary">收件箱</span>
          <span
            data-testid="inbox-badge"
            className="rounded-full bg-kind-ask-user px-1.5 py-0.5 text-[10px] font-bold text-bg-primary"
          >
            {inbox?.unanswered ?? 0}
          </span>
        </div>
        <button
          type="button"
          onClick={onRefresh}
          className="text-[11px] text-app-accent hover:underline"
        >
          刷新
        </button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {loading && items.length === 0 ? (
          <p data-testid="inbox-loading" className="p-3 text-xs text-text-muted">
            加载中…
          </p>
        ) : error ? (
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
              <li key={m.message_id}>
                <button
                  type="button"
                  data-testid="inbox-item"
                  onClick={() => setExpanded((cur) => (cur === m.message_id ? null : m.message_id))}
                  className="w-full rounded-lg border border-kind-ask-user/30 bg-bg-primary px-2.5 py-2 text-left hover:bg-bg-tertiary"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate text-[13px] font-medium text-text-primary">
                      {formatSender(m)}
                    </span>
                    <span
                      className="shrink-0 text-[13px] text-text-muted"
                      title={`${m.created_at} · ${formatClock(m.created_at)}`}
                    >
                      {relativeTime(m.created_at)}
                    </span>
                  </div>
                  <p className="mt-0.5 line-clamp-2 text-xs text-text-secondary">{messageSummary(m)}</p>
                </button>
                {expanded === m.message_id ? (
                  <div className="mt-1.5">
                    <StructuredCard message={m} onAnswer={onAnswer} onDecide={onDecide} />
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
