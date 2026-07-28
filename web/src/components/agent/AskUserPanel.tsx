/**
 * AskUserPanel — renders an active ask_user prompt and collects answers
 * (Spec 4 §3.8).
 *
 * For each question: if `options` is provided, render option buttons (single
 * select); otherwise render a free-text input. The answers are collected by
 * question index (string keys, matching the backend's AskUserResponse). A
 * cancel affordance mirrors the agent's own cancel path.
 */
import { useEffect, useState } from 'react'
import { HelpCircle } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { MarkdownRenderer } from '@/components/agent/MarkdownRenderer'
import { useI18n } from '@/providers/i18n'
import type { AskUserPrompt } from '@/types/agent'
import { cn } from '@/lib/utils'

interface AskUserPanelProps {
  prompt: AskUserPrompt
  onRespond: (answers: Record<string, string>) => void
  onCancel: () => void
}

export function AskUserPanel({ prompt, onRespond, onCancel }: AskUserPanelProps) {
  const { t } = useI18n()
  const [answers, setAnswers] = useState<Record<string, string>>({})
  const [textInputs, setTextInputs] = useState<Record<string, string>>({})

  // Reset local state whenever a new prompt arrives.
  useEffect(() => {
    setAnswers({})
    setTextInputs({})
  }, [prompt.requestId])

  const setOption = (index: number, value: string) => {
    setAnswers((prev) => ({ ...prev, [String(index)]: value }))
  }

  const submit = () => {
    const merged: Record<string, string> = { ...answers }
    for (const [k, v] of Object.entries(textInputs)) {
      if (v.trim()) merged[k] = v.trim()
    }
    onRespond(merged)
  }

  const allAnswered = prompt.questions.every((_, i) => {
    const key = String(i)
    return Boolean(answers[key] || textInputs[key]?.trim())
  })

  return (
    <div className="mx-auto my-3 w-full max-w-2xl shrink-0 max-h-[60vh] overflow-y-auto rounded-lg border border-border bg-card p-4 shadow-sm">
      <div className="mb-3 flex items-center gap-2 text-sm font-medium text-text-primary">
        <HelpCircle className="size-4 text-accent" />
        <span>{t('agent.askUserTitle')}</span>
      </div>
      <div className="flex flex-col gap-4">
        {prompt.questions.map((q, i) => (
          <div key={i} className="flex flex-col gap-2">
            <label className="text-sm text-text-primary">
              {prompt.questions.length > 1 ? `${i + 1}. ` : ''}
              <MarkdownRenderer content={q.question} />
            </label>
            {q.options && q.options.length > 0 ? (
              <div className="flex flex-wrap gap-2">
                {q.options.map((opt) => {
                  const selected = answers[String(i)] === opt
                  return (
                    <button
                      key={opt}
                      type="button"
                      onClick={() => setOption(i, opt)}
                      className={cn(
                        'rounded-md border px-3 py-1.5 text-sm transition-colors',
                        selected
                          ? 'border-accent bg-accent/10 text-text-primary'
                          : 'border-border text-text-secondary hover:bg-bg-tertiary',
                      )}
                    >
                      {opt}
                    </button>
                  )
                })}
              </div>
            ) : (
              <Input
                value={textInputs[String(i)] ?? ''}
                onChange={(e) =>
                  setTextInputs((prev) => ({ ...prev, [String(i)]: e.target.value }))
                }
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey && allAnswered) {
                    e.preventDefault()
                    submit()
                  }
                }}
                placeholder={t('agent.askUserPlaceholder')}
                className="max-w-xl"
              />
            )}
          </div>
        ))}
      </div>
      <div className="mt-4 flex justify-end gap-2">
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
