/**
 * AskUserPanel — renders an active ask_user prompt and collects answers
 * (Spec 4 §3.8, overhauled).
 *
 * Guarantees:
 *   - EVERY question always exposes a free-text input (users can ignore the
 *     options and type whatever they want).
 *   - `multiSelect` questions render checkbox-style buttons (multiple picks).
 *   - `allowOther` questions append an "Other" button that expands a text
 *     input when selected.
 *   - Answers are keyed by question index (string keys) matching the backend's
 *   - Answers are keyed by question index (string keys) matching the backend's
 *     AskUserResponse; multi-select values are joined with "、".
 *
 * Design: multi-question prompts use a step-wizard (one question per view with
 * a progress bar + dot indicators). Single-question prompts render directly.
 * Each option is an independent full-width card with top-aligned glyphs so
 * long option text wraps naturally instead of ballooning into a rectangle.
 */
import { useEffect, useMemo, useState } from 'react'
import { Check, ChevronLeft, ChevronRight, HelpCircle } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { MarkdownRenderer } from '@/components/agent/MarkdownRenderer'
import { useI18n } from '@/providers/i18n'
import type { AskUserPrompt } from '@/types/agent'
import { cn } from '@/lib/utils'

interface AskUserPanelProps {
  prompt: AskUserPrompt
  onRespond: (answers: Record<string, string>) => void
  onCancel: () => void
}

const MULTI_JOIN = '、'

export function AskUserPanel({ prompt, onRespond, onCancel }: AskUserPanelProps) {
  const { t } = useI18n()
  const [selected, setSelected] = useState<Record<string, string[]>>({})
  const [otherInputs, setOtherInputs] = useState<Record<string, string>>({})
  const [freeInputs, setFreeInputs] = useState<Record<string, string>>({})
  const [currentStep, setCurrentStep] = useState(0)

  // Reset local state whenever a new prompt arrives.
  useEffect(() => {
    setSelected({})
    setOtherInputs({})
    setFreeInputs({})
    setCurrentStep(0)
  }, [prompt.requestId])

  /** Toggle one option for question i (multi-select appends, single replaces). */
  const toggleOption = (index: number, opt: string, multiSelect: boolean) => {
    const key = String(index)
    setSelected((prev) => {
      const cur = prev[key] ?? []
      if (multiSelect) {
        return { ...prev, [key]: cur.includes(opt) ? cur.filter((v) => v !== opt) : [...cur, opt] }
      }
      return { ...prev, [key]: cur.includes(opt) ? [] : [opt] }
    })
  }

  /** Final merged answer per question: free text > other input > selected options. */
  const mergedAnswers = useMemo(() => {
    const merged: Record<string, string> = {}
    for (let i = 0; i < prompt.questions.length; i++) {
      const key = String(i)
      const free = freeInputs[key]?.trim()
      if (free) {
        merged[key] = free
        continue
      }
      // "Other" is a toggle trigger, not an answer — its input value wins.
      const otherVal = otherInputs[key]?.trim()
      if (otherVal) {
        merged[key] = otherVal
        continue
      }
      const realPicks = (selected[key] ?? []).filter((v) => v !== t('agent.askUserOther'))
      if (realPicks.length > 0) {
        merged[key] = realPicks.join(MULTI_JOIN)
      }
    }
    return merged
  }, [prompt.questions, freeInputs, otherInputs, selected, t])

  const allAnswered = prompt.questions.every((_, i) => Boolean(mergedAnswers[String(i)]))

  const submit = () => {
    onRespond(mergedAnswers)
  }

  const total = prompt.questions.length
  const isMulti = total > 1
  const currentIdx = Math.min(currentStep, total - 1)
  const q = prompt.questions[currentIdx]
  const key = String(currentIdx)

  // allowOther appends an "Other" toggle automatically.
  const options = q.allowOther
    ? [...(q.options ?? []), t('agent.askUserOther')]
    : q.options
  const hasOptions = (options?.length ?? 0) > 0
  const multi = Boolean(q.multiSelect) && hasOptions
  const picks = selected[key] ?? []
  const otherSelected = q.allowOther && picks.includes(t('agent.askUserOther'))

  return (
    <div className="mx-auto my-4 w-full max-w-2xl shrink-0 overflow-hidden rounded-2xl border border-border/60 bg-card shadow-lg shadow-black/[0.04]">
      {/* Header */}
      <div className="flex items-center gap-2.5 px-5 pb-1 pt-4">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-accent/10">
          <HelpCircle className="size-3.5 text-accent" />
        </span>
        <span className="text-sm font-semibold tracking-tight text-text-primary">
          {t('agent.askUserTitle')}
        </span>
        {isMulti && (
          <span className="ml-auto rounded-full bg-bg-tertiary px-2 py-0.5 text-xs font-medium text-text-muted">
            {currentIdx + 1} / {total}
          </span>
        )}
      </div>

      {/* Progress bar (multi-question only) */}
      {isMulti && (
        <div className="px-5 pt-2.5 pb-1">
          <div className="h-1.5 overflow-hidden rounded-full bg-bg-tertiary">
            <div
              className="h-full rounded-full bg-accent transition-all duration-300 ease-out"
              style={{ width: `${((currentIdx + 1) / total) * 100}%` }}
            />
          </div>
          {/* Dot indicators — click to jump to a question */}
          <div className="mt-2.5 flex items-center justify-center gap-2">
            {prompt.questions.map((_, i) => (
              <button
                key={i}
                type="button"
                onClick={() => setCurrentStep(i)}
                className={cn(
                  'h-2 rounded-full transition-all duration-200',
                  i === currentIdx
                    ? 'w-6 bg-accent'
                    : mergedAnswers[String(i)]
                      ? 'w-2 bg-accent/50 hover:bg-accent/70'
                      : 'w-2 bg-border hover:bg-accent/40',
                )}
                aria-label={`Question ${i + 1}`}
              />
            ))}
          </div>
        </div>
      )}

      {/* Current question */}
      <div className="max-h-[50vh] overflow-y-auto px-5 py-4">
        <div className="flex flex-col gap-3">
          {/* Question title — skipped when the LLM emitted no prompt text. */}
          {q.question.trim() && (
            <label className="flex items-start gap-2.5 text-sm font-medium leading-relaxed text-text-primary">
              {isMulti && (
                <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-accent/10 text-xs font-semibold text-accent">
                  {currentIdx + 1}
                </span>
              )}
              <span className="min-w-0 flex-1">
                <MarkdownRenderer content={q.question} />
              </span>
            </label>
          )}

          {/* Options — independent full-width cards, gap-separated. */}
          {hasOptions && (
            <div className="flex flex-col gap-2">
              {options!.map((opt) => {
                const isOther = q.allowOther && opt === t('agent.askUserOther')
                const selectedOpt = picks.includes(opt)
                if (isOther) {
                  return (
                    <button
                      key={opt}
                      type="button"
                      onClick={() => toggleOption(currentIdx, opt, multi)}
                      className={cn(
                        'group flex w-full items-start gap-3 rounded-xl border border-dashed px-4 py-3 text-left text-sm transition-all',
                        otherSelected
                          ? 'border-accent/70 bg-accent/[0.07] text-text-primary'
                          : 'border-border/60 text-text-secondary hover:border-accent/40 hover:bg-bg-tertiary/60',
                      )}
                    >
                      <span
                        className={cn(
                          'mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border-2 transition-colors',
                          otherSelected ? 'border-accent bg-accent' : 'border-input',
                        )}
                      >
                        {otherSelected && (
                          <Check className="size-3 text-text-primary dark:text-black" strokeWidth={3.5} />
                        )}
                      </span>
                      <span className="min-w-0 flex-1 leading-relaxed">{opt}</span>
                    </button>
                  )
                }
                if (multi) {
                  return (
                    <button
                      key={opt}
                      type="button"
                      role="checkbox"
                      aria-checked={selectedOpt}
                      onClick={() => toggleOption(currentIdx, opt, true)}
                      className={cn(
                        'group flex w-full items-start gap-3 rounded-xl border px-4 py-3 text-left text-sm transition-all',
                        selectedOpt
                          ? 'border-accent/70 bg-accent/[0.07] text-text-primary'
                          : 'border-border/60 text-text-secondary hover:border-accent/40 hover:bg-bg-tertiary/60',
                      )}
                    >
                      <span
                        className={cn(
                          'mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border-2 transition-colors',
                          selectedOpt
                            ? 'border-accent bg-accent'
                            : 'border-input group-hover:border-accent/40',
                        )}
                      >
                        {selectedOpt && (
                          <Check className="size-3 text-text-primary dark:text-black" strokeWidth={3.5} />
                        )}
                      </span>
                      <span className="min-w-0 flex-1 leading-relaxed">{opt}</span>
                    </button>
                  )
                }
                return (
                  <button
                    key={opt}
                    type="button"
                    role="radio"
                    aria-checked={selectedOpt}
                    onClick={() => toggleOption(currentIdx, opt, false)}
                    className={cn(
                      'group flex w-full items-center gap-3 rounded-xl border px-4 py-3 text-left text-sm transition-all',
                      selectedOpt
                        ? 'border-accent/70 bg-accent/[0.07] text-text-primary'
                        : 'border-border/60 text-text-secondary hover:border-accent/40 hover:bg-bg-tertiary/60',
                    )}
                  >
                    <span className="min-w-0 flex-1 leading-relaxed">{opt}</span>
                    <span
                      className={cn(
                        'flex size-5 shrink-0 items-center justify-center rounded-full border-2 transition-colors',
                        selectedOpt ? 'border-accent' : 'border-input group-hover:border-accent/40',
                      )}
                    >
                      {selectedOpt && <span className="size-2.5 rounded-full bg-accent" />}
                    </span>
                  </button>
                )
              })}
              {multi && (
                <span className="px-1 text-[11px] text-text-muted">{t('agent.askUserMultiHint')}</span>
              )}
            </div>
          )}

          {/* Other custom input (expands when "Other" selected) */}
          {otherSelected && (
            <div>
              <Input
                autoFocus
                value={otherInputs[key] ?? ''}
                onChange={(e) =>
                  setOtherInputs((prev) => ({ ...prev, [key]: e.target.value }))
                }
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.nativeEvent.isComposing && !e.shiftKey && allAnswered) {
                    e.preventDefault()
                    submit()
                  }
                }}
                placeholder={t('agent.askUserOtherPlaceholder')}
                className="max-w-xl"
              />
            </div>
          )}

          {/* Free-text input — ALWAYS present */}
          <div className="flex flex-col gap-1.5">
            <span className="text-[11px] text-text-muted">
              {t('agent.askUserFreeInput')}
            </span>
            {hasOptions ? (
              <Input
                value={freeInputs[key] ?? ''}
                onChange={(e) =>
                  setFreeInputs((prev) => ({ ...prev, [key]: e.target.value }))
                }
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.nativeEvent.isComposing && !e.shiftKey && allAnswered) {
                    e.preventDefault()
                    submit()
                  }
                }}
                placeholder={t('agent.askUserFreeInputPlaceholder')}
                className="max-w-xl"
              />
            ) : (
              <Textarea
                value={freeInputs[key] ?? ''}
                onChange={(e) =>
                  setFreeInputs((prev) => ({ ...prev, [key]: e.target.value }))
                }
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.nativeEvent.isComposing && (e.metaKey || e.ctrlKey) && allAnswered) {
                    e.preventDefault()
                    submit()
                  }
                }}
                placeholder={t('agent.askUserPlaceholder')}
                className="max-w-xl min-h-[76px]"
              />
            )}
          </div>
        </div>
      </div>

      {/* Footer: Cancel | Prev | Next | Submit */}
      <div className="flex items-center justify-between gap-2 border-t border-border/50 px-5 py-3">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <div className="flex items-center gap-2">
          {isMulti && currentIdx > 0 && (
            <Button variant="ghost" size="sm" onClick={() => setCurrentStep((s) => Math.max(0, s - 1))}>
              <ChevronLeft className="size-4" />
            </Button>
          )}
          {isMulti && currentIdx < total - 1 && (
            <Button
              size="sm"
              onClick={() => setCurrentStep((s) => Math.min(total - 1, s + 1))}
            >
              <ChevronRight className="size-4" />
            </Button>
          )}
          <Button size="sm" onClick={submit} disabled={!allAnswered}>
            {t('agent.askUserSubmit')}
          </Button>
        </div>
      </div>
    </div>
  )
}
