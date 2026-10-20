/**
 * Inbox —— 收件箱（未回答的 ask_user）。
 *
 * 列表：谁问的 / 问题 / 时间；点击展开直接回答（复用 AskUserCard）。
 * 空态/加载态/错误态完整。
 */
import { useState } from 'react'

import { AskUserCard, type AnswerFn } from './StructuredCards'
import type { AskPayload, InboxResult, StructuredMessage } from './types'

function questionPreview(m: StructuredMessage): string {
  const payload = (m.payload ?? {}) as Partial<AskPayload>
  const first = payload.questions?.[0]
  return first?.question ?? m.text
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString()
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
            没有待回答的问题。
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
                    <span className="truncate text-[11px] font-medium opacity-70">
                      {m.sender.name || m.sender.id || '未知'}
                    </span>
                    <span className="shrink-0 text-[10px] opacity-50">{formatTime(m.created_at)}</span>
                  </div>
                  <p className="mt-0.5 line-clamp-2 text-xs">{questionPreview(m)}</p>
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
