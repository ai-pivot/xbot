/**
 * Minimal i18n framework — Provider + Hook
 *
 * Design: key→string mapping with optional {varName} template substitution.
 * No external dependencies. Type-safe via I18nKey from zh-CN.ts.
 */
import { createContext, useContext, useState, useCallback, type ReactNode } from 'react'
import zhCN, { type I18nKey } from './zh-CN'
export type { I18nKey }
import en from './en'

// ── Locale map ──

const locales = { 'zh-CN': zhCN, en } as const
export type Locale = keyof typeof locales
export const DEFAULT_LOCALE: Locale = 'en'

// ── Template substitution ──

export function interpolate(template: string, params?: Record<string, string | number>): string {
  if (!params) return template
  return template.replace(/\{(\w+)\}/g, (_, key) =>
    params[key] !== undefined ? String(params[key]) : `{${key}}`
  )
}

// ── Context ──

interface I18nContextValue {
  locale: Locale
  setLocale: (locale: Locale) => void
}

const I18nContext = createContext<I18nContextValue>({
  locale: DEFAULT_LOCALE,
  setLocale: () => {},
})

// ── Provider ──

const STORAGE_KEY = 'xbot-language'

export function detectBrowserLocale(): Locale {
  try {
    const navLang = navigator.language.toLowerCase()
    if (navLang.startsWith('zh')) return 'zh-CN'
  } catch { /* ignore */ }
  return DEFAULT_LOCALE
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => {
    try {
      const saved = localStorage.getItem(STORAGE_KEY)
      if (saved && saved in locales) return saved as Locale
    } catch { /* ignore */ }
    // Fall back to browser language, then DEFAULT_LOCALE
    return detectBrowserLocale()
  })

  const setLocale = useCallback((newLocale: Locale) => {
    setLocaleState(newLocale)
    try {
      localStorage.setItem(STORAGE_KEY, newLocale)
    } catch { /* ignore */ }
  }, [])

  return (
    <I18nContext.Provider value={{ locale, setLocale }}>
      {children}
    </I18nContext.Provider>
  )
}

// ── Hook ──

export function useTranslation() {
  const { locale, setLocale } = useContext(I18nContext)

  const t = useCallback(
    (key: I18nKey, params?: Record<string, string | number>): string => {
      const dict = locales[locale] ?? locales[DEFAULT_LOCALE]
      const template = dict[key] ?? locales[DEFAULT_LOCALE][key] ?? key
      return interpolate(template, params)
    },
    [locale],
  )

  return { t, locale, setLocale }
}

// ── Non-React helper (for class components like ErrorBoundary) ──

export function getTranslation(locale?: Locale) {
  const loc = locale ?? (() => {
    try {
      const saved = localStorage.getItem(STORAGE_KEY)
      if (saved && saved in locales) return saved as Locale
    } catch { /* ignore */ }
    return detectBrowserLocale()
  })()
  const dict = locales[loc] ?? locales[DEFAULT_LOCALE]
  return (key: I18nKey, params?: Record<string, string | number>): string => {
    const template = dict[key] ?? locales[DEFAULT_LOCALE][key] ?? key
    return interpolate(template, params)
  }
}
