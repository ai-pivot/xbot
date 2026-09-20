/**
 * i18next initialization (Spec 1 设计系统基础).
 *
 * Resources: zh-CN (default/fallback) + en. Language is persisted via
 * localStorage key 'xbot-locale' (Spec 7 renamed from the legacy 'xbot-language'
 * key; the legacy key is migrated once then removed) and falls back to the
 * browser language, then zh-CN.
 */
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import zhCN from './zh-CN'
import en from './en'
import ja from './ja'
import type { Locale } from '@/types/shared'
import { syncSettingToServer, SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

export const LOCALE_STORAGE_KEY = 'xbot-locale'
/** Legacy key used before the Spec 7 rename; migrated on read for continuity. */
const LEGACY_LOCALE_STORAGE_KEY = 'xbot-language'
export const DEFAULT_LOCALE: Locale = 'zh-CN'

export const resources = {
  'zh-CN': { translation: zhCN },
  en: { translation: en },
  ja: { translation: ja },
} as const

export const supportedLocales: Locale[] = ['zh-CN', 'en', 'ja']

function detectInitialLocale(): Locale {
  try {
    const saved = localStorage.getItem(LOCALE_STORAGE_KEY)
    if (saved === 'zh-CN' || saved === 'en' || saved === 'ja') return saved
    // Migrate the legacy 'xbot-language' key once: copy to the new key, then
    // remove the stale entry so both no longer coexist.
    const legacy = localStorage.getItem(LEGACY_LOCALE_STORAGE_KEY)
    if (legacy === 'zh-CN' || legacy === 'en' || legacy === 'ja') {
      try {
        localStorage.setItem(LOCALE_STORAGE_KEY, legacy)
        localStorage.removeItem(LEGACY_LOCALE_STORAGE_KEY)
      } catch { /* ignore */ }
      return legacy
    }
  } catch { /* ignore */ }
  try {
    const nav = navigator.language.toLowerCase()
    if (nav.startsWith('zh')) return 'zh-CN'
    if (nav.startsWith('ja')) return 'ja'
    if (nav.startsWith('en')) return 'en'
  } catch { /* ignore */ }
  return DEFAULT_LOCALE
}

if (!i18n.isInitialized) {
  void i18n.use(initReactI18next).init({
    resources,
    lng: detectInitialLocale(),
    fallbackLng: DEFAULT_LOCALE,
    supportedLngs: supportedLocales,
    interpolation: { escapeValue: false },
    returnNull: false,
    react: { useSuspense: false },
  })
}

/** Keep <html lang> in sync with the active locale (a11y + hyphenation +
 * screen-reader pronunciation). index.html ships lang="zh-CN"; this corrects
 * it on boot and on every switch. */
function applyHtmlLang(locale: Locale): void {
  try {
    if (document.documentElement.lang !== locale) document.documentElement.lang = locale
  } catch { /* ignore */ }
}
applyHtmlLang(detectInitialLocale())

/** Persist + switch the active language. */
export function changeLocale(locale: Locale): void {
  void i18n.changeLanguage(locale)
  applyHtmlLang(locale)
  try {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale)
    syncSettingToServer(LOCALE_STORAGE_KEY, locale)
    // Drop the legacy key on any explicit change so it doesn't linger.
    localStorage.removeItem(LEGACY_LOCALE_STORAGE_KEY)
  } catch { /* ignore */ }
}

export function getLocale(): Locale {
  return (i18n.language as Locale) || DEFAULT_LOCALE
}

/**
 * 订阅宿主语言切换（**唯一 seam**）。
 *
 * 谁需要：所有「**在调用/渲染时解析文案、但自身没有 React i18n 订阅**」的消费方 ——
 * 插件 view 登记表（panelRegistry/layoutRegistry/usePluginViewPanels 的标题解析）、
 * 模块级插件清单、URL 加载的插件视图（独立 bundle，用 `ctx.i18n.t()` 取值）。
 *
 * 契约：
 *  - `@/i18n` 是宿主语言的**唯一权威**；需要跟随语言的模块订阅这里，
 *    **禁止各自 `i18n.on('languageChanged')`**（事件名/退订语义只在一处维护）；
 *  - 回调在语言**已生效后**触发（i18next 的 languageChanged 在 setLng 之后 emit），
 *    回调内直接读 `i18n.language` 即为新值；
 *  - 返回值是退订函数（组件 useEffect cleanup 直接用）。
 */
export function onLocaleChanged(listener: (locale: Locale) => void): () => void {
  const handler = (lng: string): void => {
    listener((lng as Locale) || DEFAULT_LOCALE)
  }
  i18n.on('languageChanged', handler)
  return () => {
    i18n.off('languageChanged', handler)
  }
}

// Re-read locale from localStorage when server sync updates the value.
if (typeof window !== 'undefined') {
  window.addEventListener(SETTINGS_SYNCED_EVENT, () => {
    const locale = detectInitialLocale()
    if (i18n.language !== locale) {
      void i18n.changeLanguage(locale)
    }
  })
}

export default i18n
