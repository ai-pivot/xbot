/**
 * StructuredCards —— 结构化消息卡片/行渲染器。
 *
 * 统一行头（每行必有）：`HH:mm:ss` 本地时间（zh-CN，24h）+ kind 徽标 + 发送者。
 *   ask_user → 琥珀「提问」卡（单选/多选/自定义/取消；等待=脉冲、已回答/已取消=禁用）
 *   report   → 紫色「汇报」卡（结论 + evidence 路径 + 准/驳/改）
 *   decision → 绿色实心「决策」卡（准/驳/改 + 理由）
 *   answer   → 绿色描边「回答」卡
 *   event / text / command → 32–36px 紧凑单行（摘要超长省略）
 *
 * 安全：全部走 React 文本节点（不做 dangerouslySetInnerHTML）。
 */
import { useMemo, useState } from 'react'

import type {
  AskPayload,
  AskQuestion,
  DecisionPayload,
  ReportPayload,
  StructuredAnswerInput,
  StructuredKind,
  StructuredMessage,
} from './types'

const FOLD_LIMIT = 2000

/** 本地时间 HH:mm:ss（zh-CN 24 小时制，不用美式 12h AM/PM）。 */
export function formatClock(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '--:--:--'
  return d.toLocaleTimeString('zh-CN', {
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** 发送者展示名（agent 行可能只有 id）。 */
export function formatSender(m: StructuredMessage): string {
  return m.sender?.name || m.sender?.id || '系统'
}

interface KindMeta {
  label: string
  badge: string
}

/** kind 区色：徽标底色 + 文字色（深色主题单独提亮）。 */
export const KIND_META: Record<StructuredKind, KindMeta> = {
  ask_user: {
    label: '提问',
    badge: 'border-amber-500/40 bg-amber-500/15 text-amber-700 dark:text-amber-300',
  },
  report: {
    label: '汇报',
    badge: 'border-violet-500/40 bg-violet-500/15 text-violet-700 dark:text-violet-300',
  },
  decision: {
    label: '决策',
    badge: 'border-emerald-600 bg-emerald-600 text-white dark:border-emerald-400 dark:bg-emerald-500 dark:text-emerald-950',
  },
  answer: {
    label: '回答',
    badge: 'border-emerald-500/60 bg-transparent text-emerald-700 dark:text-emerald-300',
  },
  event: {
    label: '事件',
    badge: 'border-slate-400/40 bg-slate-500/15 text-slate-600 dark:text-slate-300',
  },
  text: {
    label: '消息',
    badge: 'border-sky-500/40 bg-sky-500/15 text-sky-700 dark:text-sky-300',
  },
  command: {
    label: '指令',
    badge: 'border-indigo-500/40 bg-indigo-500/15 text-indigo-700 dark:text-indigo-300',
  },
}

const COMPACT_KINDS: StructuredKind[] = ['event', 'text', 'command']

/** 紧凑行（event/text/command）判定：流层用它决定行距。 */
export function isCompactKind(kind: StructuredKind): boolean {
  return COMPACT_KINDS.includes(kind)
}

/** 行内容摘要：text 优先，其次按 kind 从 payload 派生。 */
export function messageSummary(m: StructuredMessage): string {
  if (m.text) return m.text
  const payload = (m.payload ?? {}) as Record<string, unknown>
  if (m.kind === 'report' && typeof payload.summary === 'string') return payload.summary
  if (m.kind === 'ask_user') {
    const questions = (payload.questions ?? []) as AskQuestion[]
    if (questions[0]?.question) return questions[0].question
  }
  if (m.kind === 'event' && typeof payload.message === 'string') return payload.message
  if (typeof payload.text === 'string') return payload.text
  return ''
}

function KindBadge({ kind }: { kind: StructuredKind }) {
  const meta = KIND_META[kind] ?? KIND_META.event
  return (
    <span
      data-testid="kind-badge"
      className={`shrink-0 rounded border px-1.5 py-[1px] text-[11px] font-semibold leading-4 ${meta.badge}`}
    >
      {meta.label}
    </span>
  )
}

function RowMeta({ message, state }: { message: StructuredMessage; state?: string }) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-[13px]">
      <time
        dateTime={message.created_at}
        data-testid="row-time"
        className="shrink-0 tabular-nums text-slate-500 dark:text-slate-400"
      >
        {formatClock(message.created_at)}
      </time>
      <KindBadge kind={message.kind} />
      <span className="min-w-0 truncate text-slate-600 dark:text-slate-300">
        {formatSender(message)}
      </span>
      {state ? (
        <span className="ml-auto shrink-0 rounded-full bg-black/[0.06] px-2 py-0.5 text-[12px] font-medium text-slate-600 dark:bg-white/10 dark:text-slate-300">
          {state}
        </span>
      ) : null}
    </div>
  )
}

export function LongText({ text }: { text: string }) {
  const [expanded, setExpanded] = useState(false)
  if (!text) return null
  if (text.length <= FOLD_LIMIT) {
    return <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">{text}</p>
  }
  return (
    <div>
      <p className="whitespace-pre-wrap break-words text-sm leading-relaxed">
        {expanded ? text : `${text.slice(0, FOLD_LIMIT)}…`}
      </p>
      <button
        type="button"
        className="mt-1 text-[13px] text-sky-600 hover:underline dark:text-sky-400"
        onClick={() => setExpanded((v) => !v)}
      >
        {expanded ? '收起' : `展开（共 ${text.length} 字）`}
      </button>
    </div>
  )
}

// 卡片外壳：统一行头（时间 + kind 徽标 + 发送者 + 状态），正文可留呼吸感。
function CardShell({
  message,
  tone,
  state,
  children,
  testId,
}: {
  message: StructuredMessage
  tone: 'amber' | 'violet' | 'emerald'
  state?: string
  children: React.ReactNode
  testId: string
}) {
  const palette = {
    amber: 'border-amber-400/40 bg-amber-50/80 dark:border-amber-500/30 dark:bg-amber-950/30',
    violet: 'border-violet-400/40 bg-violet-50/80 dark:border-violet-500/30 dark:bg-violet-950/30',
    emerald:
      'border-emerald-400/40 bg-emerald-50/80 dark:border-emerald-500/30 dark:bg-emerald-950/30',
  }[tone]
  return (
    <section
      data-testid={testId}
      className={`rounded-xl border ${palette} px-3.5 py-2.5 shadow-sm backdrop-blur`}
    >
      <RowMeta message={message} state={state} />
      <div className="mt-2">{children}</div>
    </section>
  )
}

/** 紧凑行：event/text/command。固定 34px 行高，摘要单行省略。 */
export function CompactRow({ message }: { message: StructuredMessage }) {
  const summary = messageSummary(message)
  return (
    <div
      data-testid={`${message.kind}-row`}
      className="flex h-[34px] items-center gap-2 rounded-md px-2 text-[13px] hover:bg-black/[0.04] dark:hover:bg-white/[0.05]"
    >
      <time
        dateTime={message.created_at}
        data-testid="row-time"
        className="shrink-0 tabular-nums text-slate-500 dark:text-slate-400"
      >
        {formatClock(message.created_at)}
      </time>
      <KindBadge kind={message.kind} />
      <span className="max-w-[128px] shrink-0 truncate text-slate-600 dark:text-slate-300">
        {formatSender(message)}
      </span>
      <span
        data-testid="row-summary"
        className="min-w-0 flex-1 truncate text-slate-800 dark:text-slate-100"
        title={summary}
      >
        {summary}
      </span>
    </div>
  )
}

export type AnswerFn = (
  correlationId: string,
  answers: StructuredAnswerInput[],
  cancelled: boolean,
) => Promise<void>

export type DecideFn = (
  target: StructuredMessage,
  verdict: 'approve' | 'reject' | 'modify',
  comments?: string,
) => Promise<void>

export function AskUserCard({
  message,
  onAnswer,
}: {
  message: StructuredMessage
  onAnswer: AnswerFn
}) {
  const payload = (message.payload ?? {}) as Partial<AskPayload>
  const questions: AskQuestion[] = Array.isArray(payload.questions) ? payload.questions : []
  const [selected, setSelected] = useState<Record<string, string[]>>({})
  const [custom, setCustom] = useState<Record<string, string>>({})
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const done = message.answered || message.cancelled
  const waiting = !done

  // 空载荷防线（旧 AskUserPanel 的 `questions 为空 ⇒ 不渲染` 契约搬到新组件）：
  // 一条 ask_user 行但没有任何问题（服务端历史脏数据 / 通用 mock 把 RPC 回成
  // `{ok:true}` 的现场）不得伪造一张"没问题却带提交按钮"的空卡片。
  // 组件测试：StructuredCards.test.tsx「renders nothing when the payload has no questions」；
  // E2E：askuser-empty-payload.spec.ts ①。
  if (questions.length === 0) return null

  const toggle = (q: AskQuestion, optionId: string) => {
    if (done) return
    setSelected((prev) => {
      const cur = prev[q.id] ?? []
      if (q.multi_select) {
        return {
          ...prev,
          [q.id]: cur.includes(optionId) ? cur.filter((x) => x !== optionId) : [...cur, optionId],
        }
      }
      return { ...prev, [q.id]: [optionId] }
    })
  }

  const submit = async () => {
    if (done || submitting) return
    const answers: StructuredAnswerInput[] = questions.map((q) => ({
      question_id: q.id,
      option_ids: selected[q.id] ?? [],
      ...(custom[q.id] ? { text: custom[q.id] } : {}),
    }))
    setSubmitting(true)
    setErr(null)
    try {
      await onAnswer(message.correlation_id ?? '', answers, false)
    } catch (e) {
      setErr(e instanceof Error ? e.message : '提交失败')
    } finally {
      setSubmitting(false)
    }
  }

  const cancel = async () => {
    if (done || submitting) return
    setSubmitting(true)
    setErr(null)
    try {
      await onAnswer(message.correlation_id ?? '', [], true)
    } catch (e) {
      setErr(e instanceof Error ? e.message : '取消失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <CardShell
      message={message}
      tone="amber"
      state={message.cancelled ? '已取消' : message.answered ? '已回答' : '等待处理'}
      testId="ask-user-card"
    >
      <div className={waiting ? 'animate-pulse' : ''}>
        {questions.map((q) => {
          const chosen = selected[q.id] ?? []
          return (
            <div key={q.id} className="mb-3 last:mb-0">
              <p className="mb-1.5 text-sm font-medium">{q.question}</p>
              <div className="flex flex-col gap-1.5">
                {(q.options ?? []).map((opt) => {
                  const active = chosen.includes(opt.id)
                  return (
                    <label
                      key={opt.id}
                      className={`flex cursor-pointer items-start gap-2 rounded-lg border px-2.5 py-1.5 text-sm transition ${
                        active
                          ? 'border-amber-500 bg-amber-100/80 dark:bg-amber-900/40'
                          : 'border-transparent bg-black/5 hover:bg-black/10 dark:bg-white/5 dark:hover:bg-white/10'
                      } ${done ? 'cursor-not-allowed opacity-60' : ''}`}
                    >
                      <input
                        type={q.multi_select ? 'checkbox' : 'radio'}
                        name={`q-${message.message_id}-${q.id}`}
                        checked={active}
                        disabled={done}
                        onChange={() => toggle(q, opt.id)}
                        className="mt-0.5"
                      />
                      <span>
                        <span className="font-medium">{opt.label}</span>
                        {opt.description ? (
                          <span className="ml-1 text-[13px] text-slate-500 dark:text-slate-400">
                            {opt.description}
                          </span>
                        ) : null}
                      </span>
                    </label>
                  )
                })}
                {q.allow_other ? (
                  <input
                    type="text"
                    aria-label={`自定义回答 ${q.id}`}
                    value={custom[q.id] ?? ''}
                    disabled={done}
                    onChange={(e) => setCustom((prev) => ({ ...prev, [q.id]: e.target.value }))}
                    placeholder="自定义回答…"
                    className="rounded-lg border border-amber-300/50 bg-white/70 px-2.5 py-1.5 text-sm outline-none focus:border-amber-500 disabled:opacity-60 dark:border-amber-500/30 dark:bg-slate-900/60"
                  />
                ) : null}
              </div>
            </div>
          )
        })}
      </div>
      {payload.context_refs?.length ? (
        <p className="mt-1 text-[13px] text-slate-500 dark:text-slate-400">
          依据：{payload.context_refs.join(' · ')}
        </p>
      ) : null}
      {err ? <p className="mt-1 text-[13px] text-red-500">{err}</p> : null}
      <div className="mt-2 flex items-center gap-2">
        <button
          type="button"
          data-testid="ask-user-submit"
          disabled={done || submitting}
          onClick={() => void submit()}
          className="rounded-lg bg-amber-500 px-3 py-1 text-xs font-semibold text-white disabled:opacity-50"
        >
          {submitting ? '提交中…' : '提交'}
        </button>
        {payload.allow_cancel !== false ? (
          <button
            type="button"
            data-testid="ask-user-cancel"
            disabled={done || submitting}
            onClick={() => void cancel()}
            className="rounded-lg border border-amber-500/50 px-3 py-1 text-xs font-medium disabled:opacity-50"
          >
            取消
          </button>
        ) : null}
        {done ? (
          <span className="text-[13px] text-slate-500 dark:text-slate-400">该问题已关闭</span>
        ) : null}
      </div>
    </CardShell>
  )
}

export function ReportCard({
  message,
  onDecide,
}: {
  message: StructuredMessage
  onDecide: DecideFn
}) {
  const payload = (message.payload ?? {}) as Partial<ReportPayload>
  const decision = (message.payload?.decision ?? null) as DecisionPayload | null
  const [comments, setComments] = useState('')
  const [modifyOpen, setModifyOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const act = async (verdict: 'approve' | 'reject' | 'modify') => {
    if (decision || submitting) return
    setSubmitting(true)
    setErr(null)
    try {
      await onDecide(message, verdict, verdict === 'modify' ? comments : comments || undefined)
    } catch (e) {
      setErr(e instanceof Error ? e.message : '决策失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <CardShell
      message={message}
      tone="violet"
      state={message.answered ? '已决策' : '待决策'}
      testId="report-card"
    >
      <LongText text={payload.summary ?? message.text} />
      {payload.evidence?.length ? (
        <ul className="mt-2 space-y-0.5">
          {payload.evidence.map((ev, i) => (
            <li key={`${ev.path}-${i}`} className="flex gap-1.5 text-[13px]">
              <span className="shrink-0 text-slate-500 dark:text-slate-400">证据</span>
              <code className="break-all rounded bg-black/10 px-1 text-slate-700 dark:bg-white/10 dark:text-slate-200">
                {ev.path}
              </code>
            </li>
          ))}
        </ul>
      ) : null}
      {payload.state_ref ? (
        <p className="mt-1 text-[13px] text-slate-500 dark:text-slate-400">
          事实源：{payload.state_ref}
        </p>
      ) : null}
      {decision ? (
        <div className="mt-2 rounded-lg bg-emerald-500/15 px-2.5 py-1.5 text-[13px]">
          <span className="font-semibold">
            {decision.verdict === 'approve' ? '准' : decision.verdict === 'reject' ? '驳' : '改'}
          </span>
          {decision.comments ? (
            <span className="ml-1 text-slate-700 dark:text-slate-200">{decision.comments}</span>
          ) : null}
        </div>
      ) : (
        <>
          {modifyOpen ? (
            <input
              type="text"
              aria-label="修改意见"
              value={comments}
              onChange={(e) => setComments(e.target.value)}
              placeholder="修改意见…"
              className="mt-2 w-full rounded-lg border border-violet-300/50 bg-white/70 px-2.5 py-1.5 text-sm outline-none focus:border-violet-500 dark:border-violet-500/30 dark:bg-slate-900/60"
            />
          ) : null}
          {err ? <p className="mt-1 text-[13px] text-red-500">{err}</p> : null}
          <div className="mt-2 flex items-center gap-2">
            <button
              type="button"
              data-testid="report-approve"
              disabled={submitting}
              onClick={() => void act('approve')}
              className="rounded-lg bg-emerald-600 px-3 py-1 text-xs font-semibold text-white disabled:opacity-50"
            >
              准
            </button>
            <button
              type="button"
              data-testid="report-reject"
              disabled={submitting}
              onClick={() => void act('reject')}
              className="rounded-lg bg-red-600 px-3 py-1 text-xs font-semibold text-white disabled:opacity-50"
            >
              驳
            </button>
            <button
              type="button"
              data-testid="report-modify"
              disabled={submitting}
              onClick={() => (modifyOpen ? void act('modify') : setModifyOpen(true))}
              className="rounded-lg border border-violet-500/50 px-3 py-1 text-xs font-semibold disabled:opacity-50"
            >
              改
            </button>
          </div>
        </>
      )}
    </CardShell>
  )
}

export function DecisionCard({ message }: { message: StructuredMessage }) {
  const payload = (message.payload ?? {}) as Partial<DecisionPayload>
  const label =
    payload.verdict === 'approve'
      ? '准'
      : payload.verdict === 'reject'
        ? '驳'
        : payload.verdict === 'modify'
          ? '改'
          : payload.verdict
  return (
    <CardShell message={message} tone="emerald" state={String(label ?? '')} testId="decision-card">
      <LongText text={message.text} />
      {payload.comments ? (
        <p className="mt-1 text-[13px] text-slate-600 dark:text-slate-300">
          理由：{payload.comments}
        </p>
      ) : null}
    </CardShell>
  )
}

export function AnswerCard({ message }: { message: StructuredMessage }) {
  return (
    <CardShell
      message={message}
      tone="emerald"
      state={message.cancelled ? '已取消' : '已回答'}
      testId="answer-card"
    >
      <LongText text={message.text} />
    </CardShell>
  )
}

export function StructuredCard({
  message,
  onAnswer,
  onDecide,
}: {
  message: StructuredMessage
  onAnswer: AnswerFn
  onDecide: DecideFn
}) {
  const content = useMemo(() => {
    switch (message.kind) {
      case 'ask_user':
        return <AskUserCard message={message} onAnswer={onAnswer} />
      case 'report':
        return <ReportCard message={message} onDecide={onDecide} />
      case 'decision':
        return <DecisionCard message={message} />
      case 'answer':
        return <AnswerCard message={message} />
      default:
        return <CompactRow message={message} />
    }
  }, [message, onAnswer, onDecide])
  return <div data-message-id={message.message_id}>{content}</div>
}
