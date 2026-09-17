/**
 * 回归测试：动态 import 失败必须 console.warn（含 pluginId + moduleURL）。
 *
 * 背景：插件 web 产物缺失（404）/ 网络失败 / 语法错误时，import() 的失败
 * 此前只在 console.error 里一闪而过或直接静默 —— 用户看到的是"插件没出现"，
 * 却没有任何可检索线索。现在 PluginRuntime.activate 的失败分支 warn 带
 * pluginId + moduleURL，并保留既有返回值通道（{ ok: false, error }）。
 *
 * 判别力：删掉 index.ts catch 里的 console.warn（改回静默/仅 error）→ 必红。
 */
import { describe, expect, it, vi } from 'vitest'

import type { PluginManifest } from '@/plugin-api'
import type { PluginRuntimeHost } from './index'

import { PluginRuntime } from './index'

function makeHost(): PluginRuntimeHost {
  return {
    moduleBaseUrl: () => '/plugins/x',
    loadViewComponent: vi.fn(async () => null),
    ui: {},
    rpcTransport: { call: vi.fn(async () => []) },
    getSession: () => ({}) as never,
    getMessagesRaw: () => [],
    mountView: () => () => {},
    mountRenderer: () => () => {},
    mountCommand: () => () => {},
  } as unknown as PluginRuntimeHost
}

const manifest: PluginManifest = {
  id: 'xbot.demo-plugin',
  name: 'Demo',
  version: '1.0.0',
  entry: 'index.js',
  contributes: [],
}

describe('插件模块加载失败', () => {
  it('import 失败 → console.warn 含 pluginId + moduleURL，且返回 ok:false', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const rt = new PluginRuntime(makeHost())
      // 该路径在测试环境不存在 → import 必然失败（模拟插件产物缺失）。
      const badUrl = '/plugins/xbot.demo-plugin/web/index.js?load-failure-test=1'
      const res = await rt.activate(manifest, badUrl)
      expect(res.ok).toBe(false)
      expect(res.error).toBeTruthy()

      const matching = warn.mock.calls.find(
        (c) => String(c[0]).includes('加载插件模块失败') && String(c[0]).includes('xbot.demo-plugin'),
      )
      expect(matching, 'import 失败必须 warn 出 pluginId').toBeTruthy()
      expect(String(matching![0])).toContain('moduleURL=')
      expect(String(matching![0])).toContain(badUrl)
    } finally {
      warn.mockRestore()
    }
  })
})
