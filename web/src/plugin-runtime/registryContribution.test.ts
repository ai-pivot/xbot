/**
 * 动态贡献点的【累加】语义（REPRO）。
 *
 * 根因：`registerContribution` 曾这样注册：
 *
 *	registry.registerPlugin({ ...manifest, contributes: [c] }, exports)
 *
 * 而 `registerPlugin` 对同 id 会**先 unregisterPlugin 再注册** —— 于是每次
 * 注册都等于「整表替换」，**只有最后一个贡献点存活**。
 *
 * 后果（真实事故）：genui 插件 activate 里依次注册 d1(uiMode:'genui')、
 * d2(tool:'display_html')；任何在其后注册的贡献点都会把两个 messageRenderer
 * 冲掉 → `renderTool` 找不到渲染器 → **静默 fallback 到 ToolCallBlock**
 * （页面只剩「引数/出力」JSON，且 console 无任何报错）。
 *
 * 断言：连续注册的多个贡献点必须**全部保留**，互不覆盖。
 */
import { describe, expect, it } from 'vitest'

import type { ShareRendererContribution } from '@/plugin-api'

import { ContributionRegistry } from './registry'

const manifest = {
  id: 'p1',
  name: 'P1',
  version: '1.0.0',
  contributes: [],
} as never

describe('动态贡献点累加语义', () => {
  it('REPRO: 连续注册的多个贡献点必须全部保留（不得相互覆盖）', async () => {
    const reg = new ContributionRegistry()
    await reg.registerPlugin(manifest, {})

    // 模拟 genui 的 activate：先两个 messageRenderer，再一个 shareRenderer。
    reg.addContribution('p1', {
      kind: 'messageRenderer',
      id: 'r-genui',
      priority: 100,
      matches: { uiMode: 'genui' },
      render: () => null,
    } as never)
    reg.addContribution('p1', {
      kind: 'messageRenderer',
      id: 'r-legacy',
      priority: 50,
      matches: { tool: 'display_html' },
      render: () => null,
    } as never)
    reg.addContribution('p1', {
      kind: 'shareRenderer',
      id: 's-genui',
      contentType: 'xbot.genui/tsx',
      render: () => null,
    } as unknown as ShareRendererContribution)

    // 关键：先后注册的三个贡献点都要在，谁也不能把谁挤掉。
    expect(reg.listAllRenderers().map((r) => r.renderer.id).sort()).toEqual(['r-genui', 'r-legacy'])
    expect(reg.listAllShareRenderers().map((r) => r.renderer.id)).toEqual(['s-genui'])
  })
})
