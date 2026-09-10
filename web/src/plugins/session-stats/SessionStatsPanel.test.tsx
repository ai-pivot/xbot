/**
 * SessionStatsPanel —— 同一 view 的双形态。「统计详情」必须渲染【详情布局】，
 * 而不是侧边栏紧凑版的复刻。
 *
 * 历史 bug（用户报告）：手机端点侧边栏「统计详情」后，打开的全屏视图与侧边栏
 * 一模一样。根因是布局只按【容器宽度】判定（>= 640px 才算详情）——手机容器恒为
 * ~375px，无论怎么打开都落到紧凑形态。修复：详情形态由【打开方式】显式决定
 * （openViewTab 传 params: { mode: 'full' } → viewParams.mode === 'full'）。
 *
 * jsdom 里 ResizeObserver 是 no-op（test-setup 提供），容器宽度恒为 0 —— 正好等价
 * 于「手机窄屏」，无需额外铺 mock 就能复现。
 */
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { SessionStatsPanel } from './SessionStatsPanel'

const { runtimeMock } = vi.hoisted(() => ({
  runtimeMock: {
    rpc: { call: vi.fn(() => Promise.resolve({})) },
    ui: { openViewTab: vi.fn() },
  },
}))

vi.mock('@/plugin-runtime', () => ({ usePluginRuntime: () => runtimeMock }))
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({ activeSession: { channel: 'web', chatID: 'chat-1' } }),
}))
vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (k: string) => k }) }))
vi.mock('./sessionStats', () => ({ subscribeStatsRefresh: () => () => {} }))

beforeEach(() => {
  runtimeMock.rpc.call.mockClear()
})

describe('SessionStatsPanel 双形态', () => {
  it('无 viewParams（侧边栏）→ 紧凑布局：给「统计详情」入口，不渲染时间范围', async () => {
    render(<SessionStatsPanel />)
    expect(await screen.findByTestId('stats-open-detail')).toBeInTheDocument()
    expect(screen.queryByTestId('stats-range')).toBeNull()
  })

  it('REPRO: viewParams.mode="full"（详情）渲染详情布局，而不是紧凑版复刻', async () => {
    render(<SessionStatsPanel viewParams={{ mode: 'full' }} />)
    expect(await screen.findByTestId('stats-range')).toBeInTheDocument()
    // 详情形态下不该再出现「统计详情」自跳转入口
    expect(screen.queryByTestId('stats-open-detail')).toBeNull()
  })
})
