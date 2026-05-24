import { useState, type ReactNode } from 'react'
import { useTranslation, type I18nKey } from '../i18n'
import { IconChat, IconPaperclip, IconKeyboard } from './Icons'

const ONBOARDING_KEY = 'xbot-onboarding-done'

interface Step {
  titleKey: I18nKey
  descKey: I18nKey
  icon: ReactNode
}

const steps: Step[] = [
  { titleKey: 'onboardingStep1Title', descKey: 'onboardingStep1Desc', icon: <IconChat /> },
  { titleKey: 'onboardingStep2Title', descKey: 'onboardingStep2Desc', icon: <IconPaperclip /> },
  { titleKey: 'onboardingStep3Title', descKey: 'onboardingStep3Desc', icon: <IconKeyboard /> },
]

export default function OnboardingTip() {
  const { t } = useTranslation()
  const [visible, setVisible] = useState(() => {
    try {
      return !localStorage.getItem(ONBOARDING_KEY)
    } catch {
      return false
    }
  })
  const [currentStep, setCurrentStep] = useState(0)

  const finish = () => {
    try {
      localStorage.setItem(ONBOARDING_KEY, '1')
    } catch { /* ignore */ }
    setVisible(false)
  }

  const next = () => {
    if (currentStep < steps.length - 1) {
      setCurrentStep(prev => prev + 1)
    } else {
      finish()
    }
  }

  const prev = () => {
    if (currentStep > 0) {
      setCurrentStep(prev => prev - 1)
    }
  }

  if (!visible) return null

  const step = steps[currentStep]
  const isLast = currentStep === steps.length - 1
  const isFirst = currentStep === 0

  return (
    <div className="onboarding-overlay" data-testid="onboarding-overlay" onKeyDown={(e) => { if (e.key === 'Escape') finish() }}>
      <div className="onboarding-card" role="dialog" aria-modal="true" aria-label={t('onboardingTip')}>
        {/* Step indicator */}
        <div className="onboarding-step-indicator">
          {steps.map((_, idx) => (
            <div
              key={idx}
              className={`onboarding-step-dot ${idx === currentStep ? 'active' : ''} ${idx < currentStep ? 'done' : ''}`}
            />
          ))}
        </div>

        {/* Step content */}
        <div className="onboarding-step-content" data-testid={`onboarding-step-${currentStep}`}>
          <div className="onboarding-step-icon">{step.icon}</div>
          <h3 className="onboarding-step-title">
            {t(step.titleKey)}
          </h3>
          <p className="onboarding-step-desc">
            {t(step.descKey)}
          </p>
        </div>

        {/* Actions */}
        <div className="onboarding-actions">
          <button
            className="onboarding-btn onboarding-btn-skip"
            onClick={finish}
            data-testid="onboarding-skip"
          >
            {t('onboardingDismiss')}
          </button>
          {!isFirst && (
            <button
              className="onboarding-btn onboarding-btn-prev"
              onClick={prev}
              data-testid="onboarding-prev"
            >
              {t('onboardingPrev')}
            </button>
          )}
          <button
            className="onboarding-btn onboarding-btn-next"
            onClick={next}
            data-testid={isLast ? 'onboarding-done' : 'onboarding-next'}
          >
            {isLast ? t('onboardingDone') : t('onboardingNext')}
          </button>
        </div>
      </div>
    </div>
  )
}
