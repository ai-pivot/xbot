/**
 * StructuredCards —— 结构化消息卡片渲染器。
 *
 * kind=ask_user → 琥珀问题卡（单选/多选/自定义/取消；等待=脉冲、已回答/已取消=禁用）
 * kind=report   → 紫色奏折卡（结论 + evidence 路径 + 准/驳/改）
 * kind=decision → 绿色结果卡（准/驳/改 + 理由）
 * kind=answer   → 绿色回答记录
 * kind=text     → 普通文本
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
  StructuredMessage,
} from './types'

const FOLD_LIMIT = 2000

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
        className="mt-1 text-[11px] text-sky-400 hover:underline"
        onClick={() => setExpanded((v) => !v)}
      >
        {expanded ? '收起' : `展开（共 ${text.length} 字）`}
      </button>
    </div>
  )
}

function CardShell({
  tone,
  title,
  badge,
  children,
  testId,
}: {
  tone: 'amber' | 'violet' | 'emerald' | 'slate'
  title: string
  badge?: string
  children: React.ReactNode
  testId: string
}) {
  const palette = {
    amber: 'border-amber-400/40 bg-amber-50/80 dark:bg-amber-950/30',
    violet: 'border-violet-400/40 bg-violet-50/80 dark:bg-violet-950/30',
    emerald: 'border-emerald-400/40 bg-emerald-50/80 dark:bg-emerald-950/30',
    slate: 'border-slate-300/50 bg-white/70 dark:bg-slate-900/50',
  }[tone]
  return (
    <section
      data-testid={testId}
      className={`rounded-xl border ${palette} px-3.5 py-3 shadow-sm backdrop-blur`}
    >
      <header className="mb-2 flex items-center gap-2">
        <span className="text-[11px] font-semibold uppercase tracking-wide opacity-70">{title}</span>
        {badge ? (
          <span className="rounded-full bg-black/10 px-2 py-0.5 text-[10px] font-medium dark:bg-white/10">
            {badge}
          </span>
        ) : null}
      </header>
      {children}
    </section>
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
      tone="amber"
      title="请旨"
      badge={message.cancelled ? '已取消' : message.answered ? '已回答' : '等待回答'}
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
                          <span className="ml-1 text-[11px] opacity-60">{opt.description}</span>
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
                    className="rounded-lg border border-amber-300/50 bg-white/70 px-2.5 py-1.5 text-sm outline-none focus:border-amber-500 disabled:opacity-60 dark:bg-slate-900/60"
                  />
                ) : null}
              </div>
            </div>
          )
        })}
      </div>
      {payload.context_refs?.length ? (
        <p className="mt-1 text-[11px] opacity-60">依据：{payload.context_refs.join(' · ')}</p>
      ) : null}
      {err ? <p className="mt-1 text-[11px] text-red-500">{err}</p> : null}
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
        {done ? <span className="text-[11px] opacity-60">该问题已关闭</span> : null}
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
      setErr(e instanceof Error ? e.message : '批红失败')
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <CardShell
      tone="violet"
      title="奏折"
      badge={message.answered ? '已批红' : '待批红'}
      testId="report-card"
    >
      <LongText text={payload.summary ?? message.text} />
      {payload.evidence?.length ? (
        <ul className="mt-2 space-y-0.5 text-[11px]">
          {payload.evidence.map((ev, i) => (
            <li key={`${ev.path}-${i}`} className="flex gap-1.5">
              <span className="opacity-50">证据</span>
              <code className="break-all rounded bg-black/10 px-1 dark:bg-white/10">{ev.path}</code>
            </li>
          ))}
        </ul>
      ) : null}
      {payload.state_ref ? (
        <p className="mt-1 text-[11px] opacity-60">事实源：{payload.state_ref}</p>
      ) : null}
      {decision ? (
        <div className="mt-2 rounded-lg bg-emerald-500/15 px-2.5 py-1.5 text-xs">
          <span className="font-semibold">
            {decision.verdict === 'approve' ? '准' : decision.verdict === 'reject' ? '驳' : '改'}
          </span>
          {decision.comments ? <span className="ml-1 opacity-80">{decision.comments}</span> : null}
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
              className="mt-2 w-full rounded-lg border border-violet-300/50 bg-white/70 px-2.5 py-1.5 text-sm outline-none focus:border-violet-500 dark:bg-slate-900/60"
            />
          ) : null}
          {err ? <p className="mt-1 text-[11px] text-red-500">{err}</p> : null}
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
    <CardShell tone="emerald" title="批红" badge={String(label ?? '')} testId="decision-card">
      <LongText text={message.text} />
      {payload.comments ? (
        <p className="mt-1 text-[11px] opacity-70">理由：{payload.comments}</p>
      ) : null}
    </CardShell>
  )
}

export function AnswerCard({ message }: { message: StructuredMessage }) {
  return (
    <CardShell
      tone="emerald"
      title="回答"
      badge={message.cancelled ? '已取消' : '已回答'}
      testId="answer-card"
    >
      <LongText text={message.text} />
    </CardShell>
  )
}

export function TextCard({ message }: { message: StructuredMessage }) {
  return (
    <div
      data-testid="text-card"
      className="rounded-xl border border-slate-300/40 bg-white/60 px-3.5 py-2.5 dark:bg-slate-900/40"
    >
      <LongText text={message.text} />
    </div>
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
        return <TextCard message={message} />
    }
  }, [message, onAnswer, onDecide])
  return <div data-message-id={message.message_id}>{content}</div>
}
