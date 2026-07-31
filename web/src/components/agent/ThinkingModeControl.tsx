import { useEffect, useState } from 'react'

import { Slider } from '@/components/ui/slider'
import { useI18n } from '@/providers/i18n'

export type ThinkingModeValue = 'disabled' | '' | 'think' | 'think-max'

const THINKING_LABELS = ['think-', 'think', 'think+', 'think++'] as const

export function thinkingModeToStep(mode: string): number {
  switch (mode) {
    case 'disabled':
      return 0
    case 'enabled':
    case 'think':
      return 2
    case 'think-max':
      return 3
    default:
      return 1
  }
}

export function thinkingStepToMode(step: number): ThinkingModeValue {
  switch (Math.round(step)) {
    case 0:
      return 'disabled'
    case 2:
      return 'think'
    case 3:
      return 'think-max'
    default:
      return ''
  }
}

export function thinkingModeLabel(mode: string): string {
  return THINKING_LABELS[thinkingModeToStep(mode)]
}

interface ThinkingModeControlProps {
  value: string
  disabled?: boolean
  onValueCommit: (mode: ThinkingModeValue) => boolean | void | Promise<boolean | void>
  showTitle?: boolean
}

export function ThinkingModeControl({
  value,
  disabled = false,
  onValueCommit,
  showTitle = true,
}: ThinkingModeControlProps) {
  const { t } = useI18n()
  const resolvedStep = thinkingModeToStep(value)
  const [step, setStep] = useState(resolvedStep)

  useEffect(() => setStep(resolvedStep), [resolvedStep])

  return (
    <div className="min-w-0">
      {showTitle ? (
        <div className="mb-2 flex items-center justify-between gap-3 text-xs">
          <span className="text-muted-foreground">{t('settings.thinkingMode')}</span>
          <span className="font-mono text-text-secondary">{THINKING_LABELS[step]}</span>
        </div>
      ) : null}
      <div className="px-1">
        <Slider
          aria-label={t('settings.thinkingMode')}
          min={0}
          max={3}
          step={1}
          value={[step]}
          disabled={disabled}
          onValueChange={([next]) => setStep(Math.round(next ?? 1))}
          onValueCommit={([next]) => {
            const committed = Math.round(next ?? 1)
            setStep(committed)
            const mode = thinkingStepToMode(committed)
            if (thinkingModeToStep(value) !== committed || value === 'enabled') {
              void Promise.resolve(onValueCommit(mode)).then((ok) => {
                if (ok === false) setStep(resolvedStep)
              }).catch(() => setStep(resolvedStep))
            }
          }}
        />
        <div className="mt-1.5 flex justify-between font-mono text-[9px] text-text-muted">
          {THINKING_LABELS.map((label) => <span key={label}>{label}</span>)}
        </div>
      </div>
    </div>
  )
}
