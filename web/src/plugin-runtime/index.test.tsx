/**
 * PluginRuntime.loadViewComponent —— 多入口插件视图加载测试。
 *
 * 复现"diff tab 渲染成插件 panel"：loadViewComponent 曾无条件复用已激活
 * 主模块的 mod.default（= panel 组件）——多入口插件（git-fancy 的
 * diff.js/commit.js）的其他视图全部错误拿到主模块 default。模块复用只对
 * view.entry === manifest.entry（主模块入口）的视图有效。
 */
import { describe, expect, it, vi } from 'vitest'

import type { PluginManifest, ViewContribution } from '@/plugin-api'
import type { PluginRuntimeHost } from './index'

import { PluginRuntime } from './index'

const PanelComp = () => <div>panel</div>
const DiffComp = () => <div>diff</div>

function makeHost(overrides: Partial<PluginRuntimeHost> = {}): PluginRuntimeHost {
  return {
    moduleBaseUrl: () => '/plugins/x',
    loadViewComponent: vi.fn(async () => DiffComp),
    ui: {},
    rpcTransport: { call: vi.fn(async () => []) },
    getSession: () => ({}) as never,
    getMessagesRaw: () => [],
    mountView: () => () => {},
    mountRenderer: () => () => {},
    mountCommand: () => () => {},
    ...overrides,
  } as unknown as PluginRuntimeHost
}

const manifest: PluginManifest = {
  id: 'p1',
  name: 'P1',
  version: '1.0.0',
  entry: 'index.js',
  contributes: [
    { kind: 'view', id: 'p1.panel', container: 'right_sidebar', title: 'Panel', entry: 'index.js' },
    { kind: 'view', id: 'p1.diff', container: 'main', title: 'Diff', entry: 'diff.js', dynamic: true },
  ],
}

const panelView = manifest.contributes[0] as ViewContribution
const diffView = manifest.contributes[1] as ViewContribution

describe('loadViewComponent 多入口插件', () => {
  it('view.entry === 主模块 entry → 复用已激活模块的 default（单实例）', async () => {
    const host = makeHost()
    const rt = new PluginRuntime(host)
    rt.registry.registerPlugin(manifest, {})
    // 模拟 activate 后的已激活模块（default = 主入口的视图组件）。
    ;(rt as unknown as { modules: Map<string, unknown> }).modules.set('p1', { default: PanelComp })

    const comp = await rt.loadViewComponent('p1', panelView)
    expect(comp).toBe(PanelComp)
    expect(host.loadViewComponent).not.toHaveBeenCalled()
  })

  it('view.entry ≠ 主模块 entry（diff.js）→ 按 view.entry 走 host 加载，绝不返回主模块 default', async () => {
    const host = makeHost()
    const rt = new PluginRuntime(host)
    rt.registry.registerPlugin(manifest, {})
    ;(rt as unknown as { modules: Map<string, unknown> }).modules.set('p1', { default: PanelComp })

    const comp = await rt.loadViewComponent('p1', diffView)
    // 关键断言：diff 视图不得拿到主模块的 default（PanelComp）。
    expect(comp).toBe(DiffComp)
    expect(host.loadViewComponent).toHaveBeenCalledWith('p1', diffView)
  })

  it('主模块命名导出（mod[view.id]）优先于 default——多视图放主模块时单实例最优', async () => {
    const host = makeHost()
    const rt = new PluginRuntime(host)
    rt.registry.registerPlugin(manifest, {})
    ;(rt as unknown as { modules: Map<string, unknown> }).modules.set('p1', {
      default: PanelComp,
      'p1.diff': DiffComp,
    })

    const comp = await rt.loadViewComponent('p1', diffView)
    expect(comp).toBe(DiffComp)
    expect(host.loadViewComponent).not.toHaveBeenCalled()
  })

  it('未激活插件 → 直接走 host 加载（原有行为不变）', async () => {
    const host = makeHost()
    const rt = new PluginRuntime(host)
    rt.registry.registerPlugin(manifest, {})

    const comp = await rt.loadViewComponent('p1', diffView)
    expect(comp).toBe(DiffComp)
    expect(host.loadViewComponent).toHaveBeenCalledWith('p1', diffView)
  })
})
