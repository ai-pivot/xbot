/**
 * 宿主启动器的语言接线（wiring 守卫）。
 *
 * 单测分别覆盖了两个 hook 的实现（`viewRegistrySync.test.ts` / `localeEvent.test.ts`），
 * 那两处即使全绿，**只要启动器忘了调用**，用户看到的功能依旧不存在 ——
 * 本文件守住「宿主真的把语言反应挂上去了」：
 *  ① `PluginRuntimeBootstrap` 语言变化 ⇒ 广播 `i18n.localeChanged`（插件可订阅）；
 *  ② 启动即挂载 view→登记表 同步（面板/tab 标题随语言重算）。
 *
 * 回归背景（2026-09-19 用户实测：「改了语言插件没动态变化」）：
 * 文案解析是「调用时读语言」，但没有任何订阅/重算 ⇒ 画面定格旧语言。
 */
import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import i18n from '@/i18n'
import type { ViewContribution } from '@/plugin-api'

import { panelRegistry } from './panelRegistry'

const { runtimeMock, views } = vi.hoisted(() => {
  const views: Array<{ pluginId: string; view: unknown }> = []
  const emit = vi.fn()
  return {
    views,
    runtimeMock: {
      events: { emit },
      emit,
      listAllViews: () => views,
      registry: { manifestOf: () => undefined },
      subscribeViews: () => () => {},
      activateBuiltin: async () => ({ ok: true }),
      activate: async () => ({ ok: true }),
      deactivate: () => true,
      notifyPluginConfigChanged: () => {},
    },
  }
})

// 启动器依赖（WS / 会话 / runtime）全部用稳定 stub —— 只验证语言接线本身。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtimeMock,
  PluginRuntimeProvider: ({ children }: { children?: unknown }) => children,
}))
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({ rpc: async () => ({ plugins: [] }), onMessage: () => () => {} }),
}))
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({ activeSession: null }),
}))

import { PluginRuntimeBootstrap } from './usePluginRuntimeHost'

beforeEach(async () => {
  await i18n.changeLanguage('zh-CN')
  runtimeMock.emit.mockClear()
  views.splice(0, views.length)
  for (const p of panelRegistry.listPanels()) panelRegistry.unregisterPanel(p.id)
})

afterEach(async () => {
  vi.restoreAllMocks()
  await i18n.changeLanguage('zh-CN')
})

describe('PluginRuntimeBootstrap：语言反应接线', () => {
  it('语言变化 ⇒ 广播 i18n.localeChanged（插件据此刷新文案）', async () => {
    render(<PluginRuntimeBootstrap />)

    await act(async () => {
      await i18n.changeLanguage('ja')
    })

    expect(runtimeMock.emit).toHaveBeenCalledWith('i18n.localeChanged', { locale: 'ja' })
  })

  it('启动即挂载登记表同步（view 贡献点被登记，语言变化后重算）', async () => {
    views.push({
      pluginId: 'x',
      view: {
        kind: 'view',
        id: 'x.panel',
        container: 'right_sidebar',
        title: 'title.key',
        icon: 'blocks',
      } as unknown as ViewContribution,
    })

    render(<PluginRuntimeBootstrap />)
    expect(panelRegistry.getPanel('x.panel')?.title).toBe('title.key')
  })
})
