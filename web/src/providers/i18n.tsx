/**
 * I18nProvider — exposes locale / setLocale / t to the app (Spec 1).
 *
 * react-i18next is initialized globally in '@/i18n'; this provider keeps the
 * locale prop in sync with i18next's languageChanged events and offers a
 * thin, typed setLocale() that persists the choice.
 */
import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { changeLocale } from '@/i18n'
import type { Locale } from '@/types/shared'

export interface I18nContextValue {
  locale: Locale
  setLocale: (locale: Locale) => void
  t: (key: string, params?: Record<string, string | number>) => string
}

const I18nContext = createContext<I18nContextValue | undefined>(undefined)

export function I18nProvider({ children }: { children: ReactNode }) {
  const { t, i18n } = useTranslation()

  const setLocale = useCallback((locale: Locale) => {
    changeLocale(locale)
  }, [])

  // i18n.language updates synchronously on languageChanged; react-i18next
  // subscribes to that event and re-renders, so reading it here is reactive.
  const locale = (i18n.language as Locale) || 'zh-CN'

  const value = useMemo<I18nContextValue>(
    () => ({ locale, setLocale, t }),
    [locale, setLocale, t],
  )

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}

export { I18nContext }

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext)
  if (!ctx) {
    throw new Error('useI18n must be used within an <I18nProvider>')
  }
  return ctx
}
