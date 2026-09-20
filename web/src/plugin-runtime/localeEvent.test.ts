/**
 * 宿主语言变化的 React seam 契约：
 *  - `useLocale()` —— 语言变化触发订阅组件重渲染（PluginView 用它换 key）；
 *  - `usePluginLocaleEvent()` —— 宿主 → 插件的 `i18n.localeChanged` 广播
 *    （插件经 `ctx.events.on('i18n.localeChanged', …)` 订阅）。
 *
 * 回归背景（2026-09-19 用户实测：「改了语言插件没动态变化」）：
 * 文案解析是「调用时读语言」，但**没有订阅就没有重渲染/重算**⇒ 画面定格旧语言。
 * 修复前这里的广播用例必红（没有任何地方广播该事件）。
 */
import { act, renderHook } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import i18n from '@/i18n'

import { useLocale, usePluginLocaleEvent } from './useLocale'

afterEach(async () => {
  vi.restoreAllMocks()
  await i18n.changeLanguage('zh-CN')
})

describe('useLocale：宿主语言的 React 订阅', () => {
  it('初始值 = 当前宿主语言；语言变化 ⇒ 重渲染并给出新值', async () => {
    await i18n.changeLanguage('zh-CN')
    const { result } = renderHook(() => useLocale())
    expect(result.current).toBe('zh-CN')

    await act(async () => {
      await i18n.changeLanguage('ja')
    })
    expect(result.current).toBe('ja')
  })
})

describe('usePluginLocaleEvent：i18n.localeChanged 广播', () => {
  it('语言变化 ⇒ 广播 { locale }（插件据此刷新文案）', async () => {
    const emit = vi.fn()
    renderHook(() => usePluginLocaleEvent({ emit }))

    await act(async () => {
      await i18n.changeLanguage('en')
    })

    expect(emit).toHaveBeenCalledTimes(1)
    expect(emit).toHaveBeenCalledWith('i18n.localeChanged', { locale: 'en' })

    await act(async () => {
      await i18n.changeLanguage('ja')
    })
    expect(emit).toHaveBeenLastCalledWith('i18n.localeChanged', { locale: 'ja' })
  })

  it('卸载后不再广播（退订彻底，不泄漏监听）', async () => {
    const emit = vi.fn()
    const { unmount } = renderHook(() => usePluginLocaleEvent({ emit }))
    unmount()

    await act(async () => {
      await i18n.changeLanguage('en')
    })
    expect(emit).not.toHaveBeenCalled()
  })
})
