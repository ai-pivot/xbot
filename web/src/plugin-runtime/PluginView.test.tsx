/**
 * PluginView —— 内置视图分发 + viewParams 透传回归测试。
 *
 * 回归背景 1（内置视图分发）：BuiltinView 的 switch 曾缺少 `xbot.skill-manager.panel` 分支，
 * 导致右侧栏「技能」tab 点击后命中 default → return null（空白、无请求、无报错）。
 * 本测试守护所有 builtin 视图都能渲染出内容。
 *
 * 回归背景 2（viewParams 透传）：动态视图（openViewTab 打开）的参数经 props 传给插件组件。
 *
 * 注意：vi.mock 工厂被 vitest 提升到模块顶部执行，工厂内引用的外部变量
 * 必须经 vi.hoisted() 定义（否则 const TDZ → 模块加载挂死，测试无输出超时）。
 * 且 mock 必须返回【稳定引用】——AsyncPluginView 的 useEffect deps 含 runtime，
 * 每次渲染返回新对象会触发 effect→setState→render 死循环（挂死 worker）。
 */
import { render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'

import type { ViewContribution } from '@/plugin-api'

const { runtimeMock } = vi.hoisted(() => {
  const loadViewComponent = vi.fn()
  const rpcCall = vi.fn().mockResolvedValue([])
  // SkillManagerPanel/PluginManagerPanel 用 rpc.call；AsyncPluginView 用 loadViewComponent。
  return { runtimeMock: { loadViewComponent, rpc: { call: rpcCall } } }
})
// mock usePluginRuntime：loadViewComponent 返回捕获 props 的组件。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtimeMock,
}))
// SkillManagerPanel 用 useI18n().t('sidebar.skills') 渲染标题，mock 掉让 t 原样返回 key。
vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

import { PluginView } from './PluginView'

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

describe('PluginView 分发', () => {
  describe('builtin 视图分发', () => {
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

  describe('viewParams 透传', () => {
    const view: ViewContribution = {
      kind: 'view',
      id: 'xbot.git-fancy.diff',
      container: 'main',
      title: 'Diff',
      entry: 'diff.js',
      dynamic: true,
    }

    it('动态视图（openViewTab 打开）把 viewParams 作为 props 传给插件组件', async () => {
      runtimeMock.loadViewComponent.mockResolvedValue((props: Record<string, unknown>) => (
        <div data-testid="captured">{JSON.stringify(props)}</div>
      ))

      render(
        <PluginView
          pluginId="xbot.git-fancy"
          view={view}
          panelParams={{ viewParams: { path: 'src/a.go', commit: 'abc1234' }, title: 'src/a.go' }}
        />,
      )

      await waitFor(() => {
        expect(screen.getByTestId('captured')).toBeInTheDocument()
      })
      expect(screen.getByTestId('captured').textContent).toBe(
        JSON.stringify({ path: 'src/a.go', commit: 'abc1234' }),
      )
    })

    it('无 viewParams 时组件收到空 props（静态视图不受影响）', async () => {
      runtimeMock.loadViewComponent.mockResolvedValue((props: Record<string, unknown>) => (
        <div data-testid="captured">{JSON.stringify(props)}</div>
      ))

      render(<PluginView pluginId="xbot.git-fancy" view={view} />)

      await waitFor(() => {
        expect(screen.getByTestId('captured')).toBeInTheDocument()
      })
      expect(screen.getByTestId('captured').textContent).toBe('{}')
    })
  })
})
