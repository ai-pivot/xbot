import { useEffect, useState } from 'react'

import { Slider } from '@/components/ui/slider'
import { useI18n } from '@/providers/i18n'

export type ThinkingModeValue = 'disabled' | '' | 'think' | 'think-max'

/** 协议/内部值标签（ASCII，供非展示用途；UI 展示一律走 thinkingStepLabel）。 */
const THINKING_LABELS = ['think-', 'think', 'think+', 'think++'] as const

/** i18n key（按档位显示，用户可见文案不得硬编码 ASCII —— 中文界面里显示 "think" 就是没做 i18n）。 */
const THINKING_STEP_KEYS = [
  'settings.thinkingStepOff',
  'settings.thinkingStepOn',
  'settings.thinkingStepPlus',
  'settings.thinkingStepMax',
] as const

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

/** 档位 → 本地化显示文案（UI 唯一来源；中文/日文界面不得再出现 ASCII "think"）。 */
export function thinkingStepLabel(t: (key: string) => string, step: number): string {
  return t(THINKING_STEP_KEYS[Math.min(Math.max(step, 0), THINKING_STEP_KEYS.length - 1)])
}

/** 模式值 → 本地化显示文案（供 ModelSelector 触发按钮等展示点使用）。 */
export function thinkingModeLabelI18n(t: (key: string) => string, mode: string): string {
  return thinkingStepLabel(t, thinkingModeToStep(mode))
}

/** 兼容旧导出：ASCII 标签（仅协议/调试用途；展示请用 thinkingModeLabelI18n）。 */
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
          <span className="text-text-secondary">{thinkingStepLabel(t, step)}</span>
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
          {THINKING_STEP_KEYS.map((key, i) => <span key={key}>{thinkingStepLabel(t, i)}</span>)}
        </div>
      </div>
    </div>
  )
}
