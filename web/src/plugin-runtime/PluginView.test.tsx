/**
 * PluginView —— viewParams 透传测试（动态视图的参数经 props 传给插件组件）。
 *
 * 注意：vi.mock 工厂被 vitest 提升到模块顶部执行，工厂内引用的外部变量
 * 必须经 vi.hoisted() 定义（否则 const TDZ → 模块加载挂死，测试无输出超时）。
 */
import { render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'

import type { ViewContribution } from '@/plugin-api'

const { runtimeMock } = vi.hoisted(() => {
  const loadViewComponent = vi.fn()
  // 必须返回稳定引用：AsyncPluginView 的 useEffect deps 含 runtime，
  // 每次渲染返回新对象会触发 effect→setState→render 死循环（挂死 worker）。
  // 真实实现 usePluginRuntime = useContext(...)，天然返回稳定 context value。
  return { runtimeMock: { loadViewComponent } }
})
// mock usePluginRuntime：loadViewComponent 返回捕获 props 的组件。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtimeMock,
}))

import { PluginView } from './PluginView'

const view: ViewContribution = {
  kind: 'view',
  id: 'xbot.git-fancy.diff',
  container: 'main',
  title: 'Diff',
  entry: 'diff.js',
  dynamic: true,
}

describe('PluginView viewParams 透传', () => {
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
