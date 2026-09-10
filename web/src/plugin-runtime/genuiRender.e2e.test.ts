/**
 * 端到端复现：genui 插件 activate 之后，`uiMode='genui'` 的工具必须能被
 * `renderTool` 命中并渲染成面板 —— 而不是静默 fallback 成「引数/输出」JSON。
 *
 * 这是用户报告现象的精确复现（"只能看到参数和输出的 html，看不到渲染，
 * console 也没报错"）：renderTool 返回 null → ToolRender 走默认 ToolCallBlock。
 *
 * 根因（已修）：动态贡献点是「整表替换」而非「追加」，最后一个注册的
 * （shareRenderer）把前面的 messageRenderer 全部冲掉。
 */
import { describe, expect, it, vi } from 'vitest'

import { PluginRuntime, matchesTool } from './index'
import type { PluginRuntimeHost } from './index'

vi.mock('@/lib/api', () => ({ postAPI: vi.fn(async () => ({})) }))

// 模块加载器无法在 vitest 里 import /plugins/...（HTTP 路径）—— 直接喂真实
// genui 模块，使 runtime 走完整的 activateModule 路径（真实注册逻辑）。
vi.mock('./loader', async (orig) => {
  // ⚠️ genui 模块在【import 时】就读 window.React / window.__xbot_ui__（模块顶层
  // 常量），注入必须早于 import。这个 factory 是 hoisted 的（早于测试体），
  // 所以在此内联注入，而不是在测试体里调 injectWindow()。
  const w = window as unknown as Record<string, unknown>
  w.React = {
    createElement: (type: unknown, props: unknown, ...children: unknown[]) => ({ type, props, children }),
    useState: (init: unknown) => [typeof init === 'function' ? (init as () => unknown)() : init, () => {}],
  }
  w.__xbot_ui__ = { SandboxedUI: (props: unknown) => ({ type: 'SandboxedUI', props }) }
  const actual = await orig<typeof import('./loader')>()
  const genui = await import('@/plugins/genui/index')
  return { ...actual, loadPluginModule: vi.fn(async () => genui as unknown as never) }
})

/** 真实 genui 模块依赖的 window 注入（宿主在 AppShell 里做的事）。 */
function injectWindow() {
  const w = window as unknown as Record<string, unknown>
  w.React = {
    createElement: (type: unknown, props: unknown, ...children: unknown[]) => ({ type, props, children }),
    useState: (init: unknown) => [typeof init === 'function' ? (init as () => unknown)() : init, () => {}],
  }
  w.__xbot_ui__ = { SandboxedUI: (props: unknown) => ({ type: 'SandboxedUI', props }) }
}

function makeHost(): PluginRuntimeHost {
  return {
    moduleBaseUrl: () => '/plugins/xbot.genui/web',
    loadViewComponent: vi.fn(async () => null),
    ui: {},
    rpcTransport: { call: vi.fn(async () => []) },
    getSession: () => ({}) as never,
    getMessagesRaw: () => [],
    mountView: () => () => {},
    mountRenderer: () => () => {},
    mountShareRenderer: () => () => {},
    mountCommand: () => () => {},
  } as unknown as PluginRuntimeHost
}

describe('REPRO: genui 面板渲染（uiMode=genui 必须命中 renderer）', () => {
  it('activate 之后 renderTool 能命中 genui 渲染器，且 shareRenderer 不会把它冲掉', async () => {
    injectWindow()
    const rt = new PluginRuntime(makeHost())

    // 走真实 activate 路径（内部会注册 messageRenderer ×2 + shareRenderer ×1）。
    const manifest = {
      id: 'xbot.genui',
      name: 'GenUI',
      version: '1.0.0',
      permissions: ['ui', 'share'],
      contributes: [],
    } as never
    const res = await rt.activate(manifest, '/plugins/xbot.genui/web/index.js')
    expect(res.ok, res.error).toBe(true)

    // 三个贡献点必须共存：两个 messageRenderer（uiMode / display_html）+ 一个 shareRenderer。
    const renderers = rt.registry.listAllRenderers()
    const ids = renderers.map((r) => r.renderer.id)
    expect(ids).toContain('xbot.genui.renderer')
    expect(ids).toContain('xbot.genui.legacy-display-html')
    expect(rt.registry.listAllShareRenderers().map((r) => r.renderer.id)).toContain('xbot.genui.share')

    // 核心断言：带 uiMode='genui' 的工具必须被匹配（用户看到的失败点）。
    const genuiRenderer = renderers.find((r) => r.renderer.id === 'xbot.genui.renderer')!.renderer
    expect(matchesTool(genuiRenderer.matches, { name: 'display_html', uiMode: 'genui' } as never)).toBe(true)

    const out = rt.renderTool(
      { name: 'display_html', uiMode: 'genui', detail: 'function App(){ return <div/> }' } as never,
      { chatID: '' } as never,
    )
    expect(out).not.toBeNull()
  })
})
