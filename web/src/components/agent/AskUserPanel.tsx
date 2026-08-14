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
 *     AskUserResponse; multi-select values are joined with "、".
 */
import { useEffect, useMemo, useState } from 'react'
import { Check, HelpCircle } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Checkbox } from '@/components/ui/checkbox'
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

  // Reset local state whenever a new prompt arrives.
  useEffect(() => {
    setSelected({})
    setOtherInputs({})
    setFreeInputs({})
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

  return (
    <div className="mx-auto my-3 w-full max-w-2xl shrink-0 rounded-xl border border-border bg-card shadow-sm">
      {/* Header */}
      <div className="flex items-center gap-2 rounded-t-xl border-b border-border/70 bg-bg-tertiary/50 px-4 py-2.5 text-sm font-medium text-text-primary">
        <HelpCircle className="size-4 text-accent" />
        <span>{t('agent.askUserTitle')}</span>
        {prompt.questions.length > 1 && (
          <span className="ml-auto text-xs font-normal text-text-muted">
            {prompt.questions.length} {t('agent.askUserQuestionsCount') ?? 'questions'}
          </span>
        )}
      </div>

      <div className="max-h-[55vh] overflow-y-auto px-4 py-3">
        <div className="flex flex-col gap-4">
          {prompt.questions.map((q, i) => {
            const key = String(i)
            // allowOther appends an "Other" toggle automatically — the LLM only
            // declares allowOther=true, it does not include "Other" in options.
            const options = q.allowOther
              ? [...(q.options ?? []), t('agent.askUserOther')]
              : q.options
            const hasOptions = (options?.length ?? 0) > 0
            const multi = Boolean(q.multiSelect) && hasOptions
            const picks = selected[key] ?? []
            const otherSelected = q.allowOther && picks.includes(t('agent.askUserOther'))
            return (
              <div key={i} className="flex flex-col gap-2">
                {/* Question title */}
                <label className="flex items-start gap-2 text-sm text-text-primary">
                  {prompt.questions.length > 1 && (
                    <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-accent/10 text-xs font-semibold text-accent">
                      {i + 1}
                    </span>
                  )}
                  <span className="min-w-0 flex-1">
                    <MarkdownRenderer content={q.question} />
                  </span>
                </label>

                {/* Options */}
                {hasOptions && (
                  <div className="flex flex-wrap items-center gap-2 pl-7">
                    {options!.map((opt) => {
                      const isOther = q.allowOther && opt === t('agent.askUserOther')
                      const selectedOpt = picks.includes(opt)
                      if (isOther) {
                        // "Other" toggle — expands a custom input below.
                        return (
                          <button
                            key={opt}
                            type="button"
                            onClick={() => toggleOption(i, opt, multi)}
                            className={cn(
                              'flex items-center gap-1.5 rounded-lg border border-dashed px-3 py-1.5 text-sm transition-colors',
                              otherSelected
                                ? 'border-accent bg-accent/10 text-text-primary'
                                : 'border-border text-text-secondary hover:bg-bg-tertiary',
                            )}
                          >
                            {opt}
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
                            onClick={() => toggleOption(i, opt, true)}
                            className={cn(
                              'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
                              selectedOpt
                                ? 'border-accent bg-accent/10 text-text-primary'
                                : 'border-border text-text-secondary hover:bg-bg-tertiary',
                            )}
                          >
                            <Checkbox
                              checked={selectedOpt}
                              onCheckedChange={() => toggleOption(i, opt, true)}
                              className="pointer-events-none"
                            />
                            {opt}
                          </button>
                        )
                      }
                      return (
                        <button
                          key={opt}
                          type="button"
                          role="radio"
                          aria-checked={selectedOpt}
                          onClick={() => toggleOption(i, opt, false)}
                          className={cn(
                            'flex items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
                            selectedOpt
                              ? 'border-accent bg-accent/10 text-text-primary'
                              : 'border-border text-text-secondary hover:bg-bg-tertiary',
                          )}
                        >
                          <span
                            className={cn(
                              'flex size-4 items-center justify-center rounded-full border transition-colors',
                              selectedOpt ? 'border-accent' : 'border-input',
                            )}
                          >
                            {selectedOpt && <Check className="size-3 text-accent" strokeWidth={3} />}
                          </span>
                          {opt}
                        </button>
                      )
                    })}
                    {multi && (
                      <span className="text-[11px] text-text-muted">{t('agent.askUserMultiHint')}</span>
                    )}
                  </div>
                )}

                {/* Other custom input (expands when "Other" selected) */}
                {otherSelected && (
                  <div className="pl-7">
                    <Input
                      autoFocus
                      value={otherInputs[key] ?? ''}
                      onChange={(e) =>
                        setOtherInputs((prev) => ({ ...prev, [key]: e.target.value }))
                      }
                      onKeyDown={(e) => {
                        // IME guard: Enter while composing (Chinese/Japanese
                        // candidate selection) must NEVER submit — that would
                        // fire mid-typing. isComposing is set by the browser
                        // during IME composition.
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

                {/* Free-text input — ALWAYS present, whatever the options are */}
                <div className="flex flex-col gap-1 pl-7">
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
                        // IME guard — Enter while composing must not submit.
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
                        // IME guard — Ctrl+Enter while composing must not submit.
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
            )
          })}
        </div>
      </div>

      {/* Footer actions */}
      <div className="flex items-center justify-end gap-2 rounded-b-xl border-t border-border/70 bg-bg-tertiary/50 px-4 py-2.5">
        <Button variant="ghost" size="sm" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
        <Button size="sm" onClick={submit} disabled={!allAnswered}>
          {t('agent.askUserSubmit')}
        </Button>
      </div>
    </div>
  )
}
