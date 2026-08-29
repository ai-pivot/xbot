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
  // SkillManagerPanel/PluginManagerPanel 用 rpc.call（skill_list 等）；
  // SessionStatsPanel 用 get_session_usage_stats（iteration_history v59 聚合）。
  // 按方法分发，保持 skill_list → [] 兼容旧断言。
  const rpcCall = vi.fn((method: string) => {
    if (method === 'get_session_usage_stats') {
      return Promise.resolve({
        iteration_count: 3,
        turn_count: 2,
        input_tokens: 12300,
        output_tokens: 4500,
        cached_tokens: 8000,
        llm_total_ms: 12345,
        avg_ttft_ms: 850,
        avg_tpot_ms: 40,
        avg_tokens_per_sec: 25,
        first_iteration_at: '2026-08-29 10:00:00',
        last_iteration_at: '2026-08-29 11:00:00',
        last_prompt_tokens: 5000,
        last_completion_tokens: 300,
        current_model: 'glm-5.2',
        session_created_at: '2026-08-29 09:00:00',
        session_last_active: '2026-08-29 11:00:00',
        by_model: [
          {
            model: 'glm-5.2',
            iterations: 3,
            turns: 2,
            input_tokens: 12300,
            output_tokens: 4500,
            cached_tokens: 8000,
            avg_ttft_ms: 850,
            avg_tpot_ms: 40,
          },
        ],
        recent_iterations: [
          {
            turn_id: 2,
            iteration: 1,
            input_tokens: 6150,
            output_tokens: 2250,
            cached_tokens: 4000,
            ttft_ms: 900,
            tpot_ms: 42,
            tokens_per_sec: 24,
            total_ms: 6000,
            model: 'glm-5.2',
            created_at: '2026-08-29 11:00:00',
          },
        ],
      })
    }
    return Promise.resolve([])
  })
  // SessionStatsPanel 的聚合数据 mock（对齐 TenantUsageStats JSON）。
  const statsMock = { loaded: true }
  return { runtimeMock: { loadViewComponent, rpc: { call: rpcCall } }, statsMock }
})
// mock usePluginRuntime：loadViewComponent 返回捕获 props 的组件。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtimeMock,
}))
// SkillManagerPanel 用 useI18n().t('sidebar.skills') 渲染标题，mock 掉让 t 原样返回 key。
vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
// SessionStatsPanel 用 useSessionStore().activeSession 解析当前会话。
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({ activeSession: { channel: 'web', chatID: 'web-1' } }),
}))

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

    it('xbot.session-stats.panel 渲染出 SessionStatsPanel（聚合数据可见）', async () => {
      render(<PluginView pluginId="xbot.session-stats" view={makeView('xbot.session-stats.panel', 'builtin:xbot.session-stats.panel')} />)
      // 头部标题 + RPC 聚合数据（12,300 → 12.3k；命中率 8000/12300 → 65.0%）。
      expect(screen.getByText('统计')).toBeTruthy()
      expect(await screen.findByText('12.3k')).toBeTruthy()
      expect(screen.getByText('65.0%')).toBeTruthy()
      expect(screen.getByText('glm-5.2')).toBeTruthy()
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
