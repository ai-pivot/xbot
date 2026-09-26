import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// 面板与服务端只经 postAPI('/api/rpc', {method, params}) 通信。
const postAPIMock = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api', () => ({ postAPI: postAPIMock }))

// usePwaInstall 依赖浏览器 SW API —— About 面板始终渲染 PWA 区块，mock 掉。
const usePwaInstallMock = vi.hoisted(() => vi.fn())
vi.mock('@/hooks/usePwaInstall', () => ({ usePwaInstall: usePwaInstallMock }))

import { renderWithProviders } from '@/test-utils'
import { SettingsAbout } from './SettingsAbout'

/** get_system_info 的标准响应（systemd 托管的 release 构建）。 */
function sysInfo(over: Record<string, unknown> = {}) {
  return {
    version: 'v1.2.3',
    commit: 'abc1234',
    buildTime: '2026-09-25T10:00:00Z',
    channel: 'stable',
    goVersion: 'go1.26.0',
    os: 'linux',
    arch: 'amd64',
    exePath: '/home/u/.local/bin/xbot-cli',
    managedBy: 'systemd',
    devBuild: false,
    ...over,
  }
}

function mockRPC(handlers: Record<string, unknown>) {
  postAPIMock.mockImplementation((_endpoint: string, body: { method: string }) => {
    if (body?.method in handlers) return Promise.resolve(handlers[body.method])
    return Promise.resolve({})
  })
}

describe('SettingsAbout（关于 → 版本/更新/重启）', () => {
  beforeEach(() => {
    postAPIMock.mockReset()
    usePwaInstallMock.mockReturnValue({
      canInstall: false,
      isInstalled: false,
      install: vi.fn(),
      updateAvailable: false,
      checkForUpdate: vi.fn().mockResolvedValue(false),
      refreshSW: vi.fn(),
      diagnostics: null,
    })
  })

  it('挂载即拉取 get_system_info 并渲染前后端版本', async () => {
    mockRPC({ get_system_info: sysInfo() })
    renderWithProviders(<SettingsAbout />)
    await waitFor(() => expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', { method: 'get_system_info', params: {} }))
    // 后端版本 + 渠道徽标 + commit
    expect(screen.getByText('v1.2.3')).toBeInTheDocument()
    expect(screen.getByText('abc1234')).toBeInTheDocument()
    expect(screen.getByText('stable')).toBeInTheDocument()
    // 前端版本（__BUILD_INFO__ 注入，测试环境 version=dev）
    expect(screen.getAllByText(/dev/i).length).toBeGreaterThan(0)
  })

  it('DEV 构建也展示具体 commit（运行时 git 探测由服务端补全）', async () => {
    mockRPC({
      get_system_info: sysInfo({
        version: 'dev',
        channel: '',
        commit: 'cbffc69 (runtime)',
        devBuild: true,
      }),
    })
    renderWithProviders(<SettingsAbout />)
    expect(await screen.findByText('cbffc69 (runtime)')).toBeInTheDocument()
    // 前后端两张版本卡都可能出现 DEV 徽标（后端 devBuild + 前端 dev 构建）
    expect(screen.getAllByText('DEV').length).toBeGreaterThan(0)
  })

  it('检查更新：有新版本时展示 当前→最新 + 更新按钮', async () => {
    mockRPC({
      get_system_info: sysInfo(),
      check_update: {
        current: 'v1.2.3',
        latest: 'v1.3.0',
        tag: 'v1.3.0',
        hasUpdate: true,
        channel: 'stable',
        url: 'https://github.com/ai-pivot/xbot/releases/v1.3.0',
        skipped: false,
        reason: '',
      },
    })
    renderWithProviders(<SettingsAbout />)
    fireEvent.click(await screen.findByRole('button', { name: /检查更新|Check for updates/ }))
    // v1.3.0 同时出现在「当前→最新」段落与「更新到」按钮 —— 用 findAllByText
    expect((await screen.findAllByText(/v1\.3\.0/)).length).toBeGreaterThan(0)
    // 更新按钮出现（带目标版本）
    expect(
      screen.getByRole('button', { name: /更新到 v1\.3\.0|Update to v1\.3\.0/ }),
    ).toBeInTheDocument()
  })

  it('从更新提醒进入时自动检查更新', async () => {
    mockRPC({
      get_system_info: sysInfo(),
      check_update: {
        current: 'v1.2.3', latest: 'v1.3.0', tag: 'v1.3.0',
        hasUpdate: true, channel: 'stable', url: '', skipped: false, reason: '',
      },
    })
    renderWithProviders(<SettingsAbout autoCheckUpdate />)
    expect(await screen.findByRole('button', { name: /更新到 v1\.3\.0|Update to v1\.3\.0/ })).toBeInTheDocument()
  })

  it('检查更新：已是最新时展示 up-to-date，不出现更新按钮', async () => {
    mockRPC({
      get_system_info: sysInfo(),
      check_update: {
        current: 'v1.2.3',
        latest: 'v1.2.3',
        tag: 'v1.2.3',
        hasUpdate: false,
        channel: 'stable',
        url: '',
        skipped: false,
        reason: '',
      },
    })
    renderWithProviders(<SettingsAbout />)
    fireEvent.click(await screen.findByRole('button', { name: /检查更新|Check for updates/ }))
    await waitFor(() =>
      expect(screen.getByText(/已是最新版本|Up to date/)).toBeInTheDocument(),
    )
    expect(screen.queryByRole('button', { name: /更新到|Update to/ })).not.toBeInTheDocument()
  })

  it('DEV 构建检查更新被跳过时展示原因（不出现更新按钮）', async () => {
    mockRPC({
      get_system_info: sysInfo({ devBuild: true, version: 'dev', channel: '' }),
      check_update: {
        current: 'dev',
        latest: '',
        tag: '',
        hasUpdate: false,
        channel: '',
        url: '',
        skipped: true,
        reason: 'dev build — update checks need a release build',
      },
    })
    renderWithProviders(<SettingsAbout />)
    fireEvent.click(await screen.findByRole('button', { name: /检查更新|Check for updates/ }))
    expect(
      await screen.findByText(/dev build — update checks need a release build/),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /更新到|Update to/ })).not.toBeInTheDocument()
  })

  it('一键更新：apply_update 成功后展示已更新组件 + 重启提示', async () => {
    mockRPC({
      get_system_info: sysInfo(),
      check_update: {
        current: 'v1.2.3',
        latest: 'v1.3.0',
        tag: 'v1.3.0',
        hasUpdate: true,
        channel: 'stable',
        url: '',
        skipped: false,
        reason: '',
      },
      apply_update: {
        newVersion: 'v1.3.0',
        tag: 'v1.3.0',
        components: ['binary', 'web', 'plugins'],
        warnings: [],
      },
    })
    renderWithProviders(<SettingsAbout />)
    fireEvent.click(await screen.findByRole('button', { name: /检查更新|Check for updates/ }))
    fireEvent.click(await screen.findByRole('button', { name: /更新到 v1\.3\.0|Update to v1\.3\.0/ }))
    expect(
      await screen.findByText(/重启服务后生效|Restart the server to apply/),
    ).toBeInTheDocument()
    expect(screen.getByText(/binary, web, plugins/)).toBeInTheDocument()
  })

  it('重启：托管方式未知（managedBy=none）展示中性提示（不假定是否自动恢复）', async () => {
    mockRPC({ get_system_info: sysInfo({ managedBy: 'none' }) })
    const { container } = renderWithProviders(<SettingsAbout />)
    // 锚定到组件自身容器 —— 避免跨测试 DOM/i18n 残留导致 flaky。
    const view = within(container)
    // 中性提示：提到进程管理器会自动恢复 + 手动启动需自行重启，不假定任何一方
    expect(
      await view.findByText(/进程管理器|process manager/i),
    ).toBeInTheDocument()
    expect(
      view.getByText(/手动启动|manually started/i),
    ).toBeInTheDocument()
    // 不再出现「不会自动恢复」这种绝对化断言
    expect(
      view.queryByText(/不会自动恢复|will NOT come back/i),
    ).not.toBeInTheDocument()
    // 点击重启 → 出现确认描述 + 确认按钮（两步确认，防误触）
    fireEvent.click(view.getByRole('button', { name: /重启服务|Restart server/ }))
    expect(
      view.getByText(/进行中的任务会被中断|In-flight turns are interrupted/i),
    ).toBeInTheDocument()
    expect(
      view.getByRole('button', { name: /确认重启|Confirm restart/ }),
    ).toBeInTheDocument()
  })

  it('重启：检测到托管（systemd/supervisord）展示托管提示（中性，按其策略恢复）', async () => {
    mockRPC({ get_system_info: sysInfo({ managedBy: 'supervisord' }) })
    const { container } = renderWithProviders(<SettingsAbout />)
    // 锚定到组件自身容器 —— 全量套件里同文档的其它残留 DOM 会让 text 查询误命中（曾 flaky 红灯）。
    const view = within(container)
    // 托管提示：检测到 manager 名 + 按其策略恢复（不承诺"一定自动重启"）
    await waitFor(() =>
      expect(view.getByText(/supervisord/)).toBeInTheDocument(),
    )
    expect(
      view.getByText(/按其策略|per that manager/i),
    ).toBeInTheDocument()
    // 不显示"手动启动需自行重启"的未知托管提示
    expect(
      view.queryByText(/手动启动的服务需要您重新启动|must be restarted by hand/i),
    ).not.toBeInTheDocument()
  })

  it('重启：确认后调用 restart_server（两步确认防误触）', async () => {
    const calls: string[] = []
    postAPIMock.mockImplementation((_endpoint: string, body: { method: string }) => {
      calls.push(body?.method ?? '')
      if (body?.method === 'get_system_info') return Promise.resolve(sysInfo())
      if (body?.method === 'restart_server') return Promise.resolve({ managedBy: 'systemd' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsAbout />)
    // 第一步：点击「重启服务」→ 出现确认描述（不直接触发 RPC）
    fireEvent.click(await screen.findByRole('button', { name: /重启服务|Restart server/ }))
    expect(calls).not.toContain('restart_server')
    // 第二步：确认 → restart_server 被调用
    fireEvent.click(screen.getByRole('button', { name: /确认重启|Confirm restart/ }))
    await waitFor(() => expect(calls).toContain('restart_server'))
    // 重启后轮询 get_system_info 恢复版本（restart_server 之后还有 get_system_info 调用）。
    // 组件在 restart 后有 1.2s 初始延迟 + 1.5s 轮询间隔，waitFor 需覆盖该窗口。
    await waitFor(
      () => {
        const infoCalls = calls.filter((m) => m === 'get_system_info').length
        expect(infoCalls).toBeGreaterThanOrEqual(2) // 挂载 1 次 + 重启后恢复 1 次
      },
      { timeout: 6000 },
    )
  })
})
