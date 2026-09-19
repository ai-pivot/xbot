/**
 * PluginManagerPanel 卡片名的 i18n。
 *
 * `plugin_status`（后端）下发的是清单里的**字面** `name`；卡片名允许是**该插件
 * `web.i18n` 表里的 key** —— 表来自既有 RPC `web_plugin_list`（不新增 RPC），
 * 宿主用 `resolvePluginText` 按当前语言解析；非 key / 无表 ⇒ 原样透传。
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

import { changeLocale } from '@/i18n'

const { runtimeMock } = vi.hoisted(() => ({
  // 稳定引用：面板的 refresh useCallback deps 含 runtime。
  runtimeMock: { rpc: { call: vi.fn() } },
}))

vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtimeMock,
  useOptionalPluginRuntime: () => runtimeMock,
}))

import { PluginManagerPanel } from './PluginManagerPanel'

const TABLE = {
  'zh-CN': { 'manifest.name': 'Git 面板' },
  en: { 'manifest.name': 'Git Fancy' },
}

/** 两个既有 RPC：plugin_status（插件列表）+ web_plugin_list（清单里的 web.i18n 表）。 */
function mockRpc(opts: { names: Array<{ id: string; name: string }>; tables?: Record<string, unknown> }) {
  runtimeMock.rpc.call.mockImplementation(async (method: string) => {
    if (method === 'plugin_status') {
      return { plugins: opts.names.map((n) => ({ ...n, version: '1.0.0', state: 'active', runtime: 'stdio' })) }
    }
    if (method === 'web_plugin_list') {
      return { plugins: Object.entries(opts.tables ?? {}).map(([id, i18n]) => ({ id, i18n })) }
    }
    return {}
  })
}

describe('PluginManagerPanel · 卡片名走插件表（web.i18n）', () => {
  beforeEach(() => {
    runtimeMock.rpc.call.mockReset()
  })

  afterEach(() => {
    changeLocale('zh-CN')
  })

  it('宿主 en：name 是插件表里的 key ⇒ 显示英文', async () => {
    changeLocale('en')
    mockRpc({ names: [{ id: 'xbot.git-fancy', name: 'manifest.name' }], tables: { 'xbot.git-fancy': TABLE } })
    render(<PluginManagerPanel />)
    expect(await screen.findByText('Git Fancy')).toBeInTheDocument()
    expect(screen.queryByText('manifest.name')).not.toBeInTheDocument()
  })

  it('宿主 zh-CN：key ⇒ 中文；非 key 的名字原样透传', async () => {
    changeLocale('zh-CN')
    mockRpc({
      names: [
        { id: 'xbot.git-fancy', name: 'manifest.name' },
        { id: 'xbot.plain', name: 'Plain Plugin' },
      ],
      tables: { 'xbot.git-fancy': TABLE },
    })
    render(<PluginManagerPanel />)
    expect(await screen.findByText('Git 面板')).toBeInTheDocument()
    expect(screen.getByText('Plain Plugin')).toBeInTheDocument()
  })

  it('插件没有 i18n 表 ⇒ 名字原样透传、不报错', async () => {
    changeLocale('en')
    mockRpc({ names: [{ id: 'xbot.plain', name: 'Plain Plugin' }] })
    render(<PluginManagerPanel />)
    expect(await screen.findByText('Plain Plugin')).toBeInTheDocument()
  })

  it('web_plugin_list 失败 ⇒ 列表仍渲染（降级为原样透传）', async () => {
    changeLocale('en')
    runtimeMock.rpc.call.mockImplementation(async (method: string) => {
      if (method === 'web_plugin_list') throw new Error('rpc down')
      return { plugins: [{ id: 'xbot.git-fancy', name: 'Git Fancy', version: '1.0.0', state: 'active', runtime: 'stdio' }] }
    })
    render(<PluginManagerPanel />)
    expect(await screen.findByText('Git Fancy')).toBeInTheDocument()
  })
})
