/**
 * PluginView —— 内置视图分发回归测试。
 *
 * 回归背景：BuiltinView 的 switch 曾缺少 `xbot.skill-manager.panel` 分支，
 * 导致右侧栏「技能」tab 点击后命中 default → return null（空白、无请求、
 * 无报错）。本测试守护所有 builtin 视图都能渲染出内容。
 */
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

import { PluginView } from './PluginView'
import type { ViewContribution } from '@/plugin-api'

vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// SkillManagerPanel / PluginManagerPanel 都通过 usePluginRuntime().rpc.call 发请求，
// 测试中 mock 掉（断言的是视图能渲染，不是请求结果）。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => ({
    rpc: { call: vi.fn().mockResolvedValue([]) },
  }),
}))

function makeView(id: string, entry: string): ViewContribution {
  return {
    kind: 'view',
    id,
    container: 'right_sidebar',
    title: '面板',
    icon: 'sparkles',
    entry,
  }
}

describe('PluginView builtin 视图分发', () => {
  it('xbot.skill-manager.panel 渲染出 SkillManagerPanel（头部标题可见）', () => {
    render(<PluginView pluginId="xbot.skill-manager" view={makeView('xbot.skill-manager.panel', 'builtin:xbot.skill-manager.panel')} />)
    expect(screen.getByText('sidebar.skills')).toBeTruthy()
  })

  it('xbot.plugin-manager.panel 仍正常渲染（回归守护）', async () => {
    render(<PluginView pluginId="xbot.plugin-manager" view={makeView('xbot.plugin-manager.panel', 'builtin:xbot.plugin-manager.panel')} />)
    expect(await screen.findByText('暂无插件')).toBeTruthy()
  })

  it('未知 builtin 视图渲染 null 且不崩溃', () => {
    const { container } = render(<PluginView pluginId="unknown" view={makeView('unknown.view', 'builtin:unknown.view')} />)
    expect(container.firstChild).toBeNull()
  })
})
