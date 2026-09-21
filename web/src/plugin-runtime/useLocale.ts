/**
 * useLocale —— 宿主语言的 React 订阅（语言变化 ⇒ 该组件重渲染）。
 *
 * 为什么需要它：文案解析本身是「调用时读语言」（`ctx.i18n`/`i18n.t` 都读
 * `i18n.language` 快照），但**解析结果要进入画面，必须有一次重渲染**。
 *  - 宿主组件用 `useI18n()`（React context）⇒ Provider 变化即重渲染 ✓；
 *  - **URL 加载的插件视图**（独立 bundle，模块级单例 + `ctx.i18n.t()`）与
 *    **模块级内置清单**（import 时求值）⇒ 没有任何订阅 ⇒ 画面停在旧语言 ✗。
 *
 * 本 hook 是后者的唯一 React 入口：返回值**只作为依赖 / `key` 使用**，
 * 绝不参与文案解析（解析永远读 `@/i18n` 单例 / 插件 `ctx.i18n`）——
 * 不引入第二套 i18n 机制，也不要求插件改代码。
 */
import { useEffect, useState } from 'react'

import i18n, { onLocaleChanged } from '@/i18n'
import type { Locale } from '@/types/shared'

/** 当前宿主语言；宿主切换语言时订阅组件自动重渲染。 */
export function useLocale(): Locale {
  const [locale, setLocale] = useState<Locale>(() => (i18n.language as Locale) || 'zh-CN')
  useEffect(() => onLocaleChanged(setLocale), [])
  return locale
}

/** 广播语言变化所需的最小事件总线视图（PluginEventBus 结构上满足）。 */
export interface LocaleEventEmitter {
  emit(name: 'i18n.localeChanged', payload: { locale: Locale }): void
}

/**
 * 宿主 → 插件的语言变化广播（`i18n.localeChanged`，事件表见
 * `@/plugin-api` 的 `EventMap`）。
 *
 * 单一广播点：宿主 PluginRuntime 启动器挂一次；插件用
 * `ctx.events.on('i18n.localeChanged', ({ locale }) => …)` 订阅（需 `events` 权限）。
 * 广播在语言**已生效后**发出（回调在 i18next 的 languageChanged 之后）。
 */
export function usePluginLocaleEvent(events: LocaleEventEmitter): void {
  useEffect(() => onLocaleChanged((locale) => events.emit('i18n.localeChanged', { locale })), [events])
}
