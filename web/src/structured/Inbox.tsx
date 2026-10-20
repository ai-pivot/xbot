/**
 * Inbox —— 收件箱（未回答的 ask_user）。
 *
 * 列表：谁问的 / 问题 / 相对时间（「3 分钟前」）；点击展开直接回答（复用 AskUserCard）。
 * 空态/加载态/错误态完整。
 */
import { useState } from 'react'

import { AskUserCard, formatClock, formatSender, messageSummary, type AnswerFn } from './StructuredCards'
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
  onRefresh,
}: {
  inbox: InboxResult | null
  loading: boolean
  error: string | null
  onAnswer: AnswerFn
  onRefresh: () => void
}) {
  const [expanded, setExpanded] = useState<number | null>(null)
  const items = inbox?.items ?? []

  return (
    <aside
      data-testid="inbox"
      className="flex h-full min-h-0 w-[340px] shrink-0 flex-col border-l border-slate-300/40 bg-slate-50/70 dark:border-slate-700/50 dark:bg-slate-950/40"
    >
      <header className="flex items-center justify-between border-b border-slate-300/40 px-3 py-2 dark:border-slate-700/50">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold">收件箱</span>
          <span
            data-testid="inbox-badge"
            className="rounded-full bg-amber-500 px-1.5 py-0.5 text-[10px] font-bold text-white"
          >
            {inbox?.unanswered ?? 0}
          </span>
        </div>
        <button
          type="button"
          onClick={onRefresh}
          className="text-[11px] text-sky-500 hover:underline"
        >
          刷新
        </button>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {loading && items.length === 0 ? (
          <p data-testid="inbox-loading" className="p-3 text-xs opacity-60">
            加载中…
          </p>
        ) : error ? (
          <p data-testid="inbox-error" className="p-3 text-xs text-red-500">
            {error}
          </p>
        ) : items.length === 0 ? (
          <p data-testid="inbox-empty" className="p-3 text-xs opacity-60">
            没有待处理的问题。
          </p>
        ) : (
          <ul className="space-y-1.5">
            {items.map((m) => (
              <li key={m.message_id}>
                <button
                  type="button"
                  data-testid="inbox-item"
                  onClick={() => setExpanded((cur) => (cur === m.message_id ? null : m.message_id))}
                  className="w-full rounded-lg border border-amber-400/30 bg-white/70 px-2.5 py-2 text-left hover:bg-amber-50 dark:bg-slate-900/60 dark:hover:bg-amber-950/30"
                >
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate text-[13px] font-medium text-slate-600 dark:text-slate-300">
                      {formatSender(m)}
                    </span>
                    <span
                      className="shrink-0 text-[13px] text-slate-500 dark:text-slate-400"
                      title={`${m.created_at} · ${formatClock(m.created_at)}`}
                    >
                      {relativeTime(m.created_at)}
                    </span>
                  </div>
                  <p className="mt-0.5 line-clamp-2 text-xs">{messageSummary(m)}</p>
                </button>
                {expanded === m.message_id ? (
                  <div className="mt-1.5">
                    <AskUserCard message={m} onAnswer={onAnswer} />
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
