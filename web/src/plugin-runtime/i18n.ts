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
import i18n from '@/i18n'

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

/**
 * 宿主侧解析「**插件清单文本**（key 或裸文本）」的唯一 helper。
 *
 * 契约（2026-09-19，与插件运行时的 `ctx.i18n` 同源）：插件清单里凡是用户可见的文本
 * ——`contributes.configuration` 的 `label`/`description`/`section`、插件卡片
 * `name`/`description`、view 的 `title`——都允许写**该插件 `web.i18n` 表里的 key**。
 * 命中 ⇒ 按宿主当前语言取译文；**不是 key**（历史清单里的裸字符串）或**该插件没有表**
 * ⇒ **原样返回**（向后兼容，零 hack）。解析器复用 `createPluginI18n`（回退链一致）。
 *
 * @param table 该插件的文案表（`PluginManifest.i18n`）；缺省 ⇒ 原样透传
 * @param keyOrText 清单里的文本或 key；空值原样返回
 * @param readLocale 宿主语言读取器（默认读宿主 i18n；测试可注入固定值）
 */
export function resolvePluginText(
  table: PluginI18nTable | undefined,
  keyOrText: string | undefined | null,
  readLocale: LocaleReader = () => i18n.language,
): string | undefined {
  if (keyOrText === undefined || keyOrText === null || keyOrText === '') return keyOrText ?? undefined
  if (!table) return keyOrText
  return createPluginI18n(table, readLocale).t(keyOrText, keyOrText)
}

/**
 * 从插件清单里取某插件的文案表（宿主各处消费插件文本时的统一取表入口）。
 * 未激活/无 web 声明的插件 ⇒ undefined（调用方原样透传文本）。
 */
export function pluginI18nTableOf(
  manifests: { manifestOf(pluginId: string): { i18n?: PluginI18nTable } | undefined },
  pluginId: string,
): PluginI18nTable | undefined {
  return manifests.manifestOf(pluginId)?.i18n
}
