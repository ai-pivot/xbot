import { useRef, useCallback, useEffect, useState } from 'react'
import { useTranslation } from '../i18n'

interface AskUserQuestion {
  question: string
  options?: string[]
}

interface AskUserData {
  questions: AskUserQuestion[]
  answers: Record<string, string>
  currentQ: number
}

interface AskUserPanelProps {
  askUser: AskUserData
  onSubmit: (answers: Record<string, string>) => void
  onCancel: (answers: Record<string, string>) => void
}

export default function AskUserPanel({ askUser, onSubmit, onCancel }: AskUserPanelProps) {
  const [currentQ, setCurrentQ] = useState(askUser.currentQ)
  const [answers, setAnswers] = useState<Record<string, string>>(askUser.answers)
  const inputRef = useRef<HTMLInputElement>(null)
  const { t } = useTranslation()

  // Auto-focus the input when question changes
  useEffect(() => {
    const q = askUser.questions[currentQ]
    if (q && !q.options) {
      setTimeout(() => inputRef.current?.focus(), 0)
    }
  }, [currentQ, askUser.questions])

  const submitAnswer = useCallback((value: string) => {
    if (!value.trim()) return
    const newAnswers = { ...answers, [currentQ]: value.trim() }
    if (currentQ < askUser.questions.length - 1) {
      setAnswers(newAnswers)
      setCurrentQ(prev => prev + 1)
      setTimeout(() => inputRef.current?.focus(), 0)
    } else {
      onSubmit(newAnswers)
    }
  }, [answers, currentQ, askUser.questions.length, onSubmit])

  // Escape key to cancel
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onCancel(answers)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [answers, onCancel])

  const currentQuestion = askUser.questions[currentQ]
  if (!currentQuestion) return null

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 askuser-backdrop" role="dialog" aria-modal="true" aria-label={t('agentNeedsInput')} onClick={(e) => {
      if (e.target === e.currentTarget) {
        onCancel(answers)
      }
    }}>
      <div className="bg-slate-800 border border-slate-600 rounded-2xl shadow-2xl max-w-lg w-full mx-4 askuser-panel">
        <div className="px-5 py-4 border-b border-slate-700 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-white flex items-center gap-2">
            <span className="text-lg">🤔</span>
            {t('agentNeedsInput')}
          </h3>
          <span className="text-xs text-slate-400">
            {currentQ + 1} / {askUser.questions.length}
          </span>
        </div>
        <div className="px-5 py-4">
          <p className="text-sm text-slate-200 mb-4">{currentQuestion.question}</p>
          {currentQuestion.options && currentQuestion.options!.length > 0 ? (
            <div className="space-y-2">
              {askUser.questions[currentQ].options!.map((opt, i) => (
                <button
                  key={i}
                  onClick={() => submitAnswer(opt)}
                  className="w-full text-left px-4 py-2.5 rounded-lg border border-slate-600 text-sm text-slate-200 hover:bg-blue-500/10 hover:border-blue-500/50 transition-colors"
                  aria-label={opt}
                >
                  {opt}
                </button>
              ))}
            </div>
          ) : (
            <div className="flex gap-2">
              <input
                type="text"
                ref={inputRef}
                autoFocus
                placeholder={t('inputAnswer')}
                className="flex-1 px-3 py-2 bg-slate-700 border border-slate-600 rounded-lg text-sm text-white placeholder-slate-400 focus:outline-none focus:border-blue-500"
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    submitAnswer((e.target as HTMLInputElement).value)
                  }
                }}
              />
              <button
                onClick={() => submitAnswer(inputRef.current?.value || '')}
                className="px-4 py-2 bg-blue-600 hover:bg-blue-500 text-white text-sm rounded-lg transition-colors" aria-label={t('submit')}
              >
                {t('submit')}
              </button>
            </div>
          )}
        </div>
        <div className="px-5 py-3 border-t border-slate-700 flex justify-between items-center">
          {currentQ > 0 ? (
            <button
              onClick={() => setCurrentQ(prev => prev - 1)}
              className="text-xs text-slate-400 hover:text-white transition-colors" aria-label={t('previousQuestion')}
            >
              {t('previousQuestion')}
            </button>
          ) : (
            <div />
          )}
          <button
            onClick={() => onCancel(answers)}
            className="text-xs text-red-400 hover:text-red-300 transition-colors" aria-label={t('cancel')}
          >
            {t('cancel')}
          </button>
        </div>
      </div>
    </div>
  )
}
