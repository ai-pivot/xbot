/**
 * 插件自带 i18n 的运行时解析器。
 *
 * 契约（2026-09-19 用户要求：插件 i18n 是**平台通用能力**）：
 *   - 文案随**插件清单**分发（`web.i18n: { "<locale>": { "<key>": "<text>" } }`）；
 *   - 解析顺序：当前宿主 locale（精确 → 语言前缀匹配，如 "zh-CN" 命中 "zh"）
 *     → `en` → 表里第一个可用 locale → `fallback` 参数 → key 本身；
 *   - `locale` 是**读取时的快照**（宿主切换语言后，插件再读即为新值，无需重新激活）；
 *   - 永不抛错、永不返回 undefined —— 最差情况返回 `fallback ?? key`，
 *     这样插件 UI 至少显示可读的 key，而不是空白。
 */
import type { I18nAPI } from '@/plugin-api'

/** 插件清单里的文案表形状：locale → key → text。 */
export type PluginI18nTable = Readonly<Record<string, Readonly<Record<string, string>>>>

/** 宿主当前语言的读取器（注入是为了可测：单测给固定值即可）。 */
export type LocaleReader = () => string

/** 兜底语言（与宿主 i18n 的 fallbackLng 语义一致）。 */
const FALLBACK_LOCALE = 'en'

function normalizeLocale(tag: string): string {
  return (tag || '').trim().replace(/_/g, '-')
}

/** 语言前缀（"zh-CN" → "zh"）；无 "-" 时返回空串。 */
function localePrefix(tag: string): string {
  const i = tag.indexOf('-')
  return i > 0 ? tag.slice(0, i) : ''
}

/**
 * 在表里按 key 取值，逐级回退：
 *   精确 locale → 语言前缀匹配（唯一命中） → en → 表里第一个可用 locale。
 */
function lookup(table: PluginI18nTable, locale: string, key: string): string | undefined {
  const exact = table[locale]?.[key]
  if (exact !== undefined) return exact

  const prefix = localePrefix(locale)
  if (prefix) {
    const matches = Object.keys(table).filter(
      (l) => l === prefix || l.startsWith(prefix + '-'),
    )
    for (const l of matches) {
      const v = table[l]?.[key]
      if (v !== undefined) return v
    }
  }

  const en = table[FALLBACK_LOCALE]?.[key]
  if (en !== undefined) return en

  // 表里任何语言提供的该 key 都优于"完全没有"（插件只提供部分语言时也能显示）。
  for (const l of Object.keys(table)) {
    const v = table[l]?.[key]
    if (v !== undefined) return v
  }
  return undefined
}

/**
 * 创建插件的 i18n 解析器。
 *
 * @param table  插件清单里的文案表（缺省/空表 ⇒ 永不报错，全部回退到 fallback/key）
 * @param readLocale 读取宿主当前语言（每次调用都读，语言切换即时生效）
 */
export function createPluginI18n(table: PluginI18nTable | undefined, readLocale: LocaleReader): I18nAPI {
  const t = table ?? {}
  return {
    get locale() {
      return normalizeLocale(readLocale()) || FALLBACK_LOCALE
    },
    t(key: string, fallback?: string): string {
      if (!key) return fallback ?? ''
      const hit = lookup(t, this.locale, key)
      if (hit !== undefined) return hit
      return fallback ?? key
    },
  }
}
