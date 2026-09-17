import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// The panel talks to the server through postAPI('/api/rpc', {method, params}).
const postAPIMock = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api', () => ({ postAPI: postAPIMock }))

import { changeLocale } from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { SettingsChannels } from './SettingsChannels'

const feishuSchema = JSON.stringify([
  { key: 'enabled', label: 'Enabled', type: 'toggle' },
  { key: 'app_id', label: 'App ID', type: 'text' },
  { key: 'app_secret', label: 'App Secret', type: 'password' },
])

const pluginSchema = JSON.stringify([
  { key: 'enabled', label: 'Enabled', type: 'toggle' },
  { key: 'bot_token', label: 'Bot Token', type: 'password' },
])

const channelsFixture = {
  feishu: {
    enabled: 'true',
    app_id: 'cli_existing',
    app_secret: 'sekret',
    _schema: feishuSchema,
    _builtin: 'true',
  },
  telegram: {
    enabled: 'false',
    bot_token: '',
    _schema: pluginSchema,
    _builtin: 'false',
  },
}

function mockRPC(overrides: Record<string, unknown> = {}) {
  postAPIMock.mockImplementation((_endpoint: string, body: { method: string }) => {
    const method = body?.method
    if (method in overrides) return Promise.resolve(overrides[method])
    if (method === 'get_channel_config') return Promise.resolve(channelsFixture)
    if (method === 'feishu_bind_status') return Promise.resolve({ state: 'waiting' })
    return Promise.resolve({})
  })
}

describe('SettingsChannels — 渠道面板', () => {
  beforeEach(() => {
    changeLocale('zh-CN')
    postAPIMock.mockReset()
  })

  it('加载渠道列表：内置与插件渠道都渲染，并带上类型标记', async () => {
    mockRPC()
    renderWithProviders(<SettingsChannels />)

    expect(await screen.findByText(/feishu · /)).toBeInTheDocument()
    // Plugin (user-registered) channels are listed too.
    expect(screen.getByText(/telegram · /)).toBeInTheDocument()
    // Fields come from _schema (plugin + builtin use the same shape).
    expect(screen.getByLabelText('App ID')).toHaveValue('cli_existing')
    expect(screen.getByLabelText('Bot Token')).toBeInTheDocument()

    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_channel_config',
      params: {},
    })
  })

  it('开关改动后保存 → set_channel_config（含 enabled 新值）', async () => {
    mockRPC()
    renderWithProviders(<SettingsChannels />)

    // Two channels each render an "启用" label — pick the Feishu switch by id.
    const toggle = (await screen.findAllByLabelText(/^启用$/)).find(
      (el) => el.id === 'channel-enabled-feishu',
    )
    expect(toggle).toBeDefined()
    fireEvent.click(toggle!)

    const save = screen.getAllByRole('button', { name: '保存' })[0]
    await waitFor(() => expect(save).toBeEnabled())
    fireEvent.click(save)

    await waitFor(() =>
      expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
        method: 'set_channel_config',
        params: { channel: 'feishu', values: expect.objectContaining({ enabled: 'false' }) },
      }),
    )
  })

  it('未改动时保存按钮禁用（不产生无谓写入）', async () => {
    mockRPC()
    renderWithProviders(<SettingsChannels />)
    const save = (await screen.findAllByRole('button', { name: '保存' }))[0]
    expect(save).toBeDisabled()
  })

  it('一键绑定飞书：调用 feishu_bind_start 并把链接展示出来', async () => {
    const bindURL = 'https://open.feishu.cn/page/launcher?user_code=AB12-CD34'
    mockRPC({ feishu_bind_start: { url: bindURL, expires_in: 600, app_id: 'cli_existing' } })
    // jsdom 没有 window.open；这里显式返回 null（= 被拦截）以走「手动打开」兜底。
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null)
    renderWithProviders(<SettingsChannels />)

    const bindBtn = await screen.findByRole('button', { name: '一键绑定飞书智能体应用' })
    fireEvent.click(bindBtn)

    await waitFor(() =>
      expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
        method: 'feishu_bind_start',
        params: { app_id: 'cli_existing' },
      }),
    )
    expect(await screen.findByTestId('feishu-bind-url')).toHaveTextContent(bindURL)
    openSpy.mockRestore()
  })

  it('点击按钮即自动打开浏览器窗口并导航到授权链接（全新安装引导）', async () => {
    const bindURL = 'https://open.feishu.cn/page/launcher?addons=H4sIA'
    mockRPC({ feishu_bind_start: { url: bindURL, expires_in: 600, app_id: 'cli_new' } })
    const replace = vi.fn()
    const popup = { closed: false, opener: {} as unknown, location: { replace } } as unknown as Window
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(popup)

    renderWithProviders(<SettingsChannels />)
    fireEvent.click(await screen.findByTestId('feishu-bind'))

    // 窗口必须在用户手势内同步打开（否则被弹窗拦截器干掉）……
    expect(openSpy).toHaveBeenCalledWith('about:blank', '_blank')
    // ……拿到链接后导航过去，并摘掉 opener。
    await waitFor(() => expect(replace).toHaveBeenCalledWith(bindURL))
    expect(popup.opener).toBeNull()
    openSpy.mockRestore()
  })

  it('拿到链接后按钮立刻脱离「正在获取链接…」（不再永久禁用），并提供打开链接', async () => {
    const bindURL = 'https://open.feishu.cn/page/launcher?user_code=XY'
    mockRPC({ feishu_bind_start: { url: bindURL, expires_in: 600 } })
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null) // 被拦截

    renderWithProviders(<SettingsChannels />)
    fireEvent.click(await screen.findByTestId('feishu-bind'))

    const btn = await screen.findByTestId('feishu-bind')
    await waitFor(() => expect(btn).toBeEnabled())
    expect(btn).toHaveTextContent('重新生成链接')
    expect(screen.getByTestId('feishu-open-link')).toBeInTheDocument()
    expect(screen.getByTestId('feishu-popup-blocked')).toBeInTheDocument()
    openSpy.mockRestore()
  })

  it('服务端状态回到 idle（重启/被新绑定顶替）→ 复位提示且按钮可用', async () => {
    mockRPC({
      feishu_bind_start: { url: 'https://open.feishu.cn/page/launcher?user_code=Z', expires_in: 600 },
      feishu_bind_status: { state: 'idle' },
    })
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null)

    renderWithProviders(<SettingsChannels />)
    fireEvent.click(await screen.findByTestId('feishu-bind'))

    await waitFor(() => expect(screen.getByText(/授权链接已失效/)).toBeInTheDocument(), { timeout: 6000 })
    expect(screen.getByTestId('feishu-bind')).toBeEnabled()
    openSpy.mockRestore()
  })

  it('链接过期（expires_in 已过）→ 明确提示过期并允许重新生成', async () => {
    mockRPC({
      feishu_bind_start: { url: 'https://open.feishu.cn/page/launcher?user_code=Z', expires_in: 1 },
    })
    const openSpy = vi.spyOn(window, 'open').mockReturnValue(null)

    renderWithProviders(<SettingsChannels />)
    fireEvent.click(await screen.findByTestId('feishu-bind'))

    await waitFor(() => expect(screen.getByTestId('feishu-link-expired')).toBeInTheDocument(), {
      timeout: 6000,
    })
    expect(screen.getByTestId('feishu-bind')).toBeEnabled()
    openSpy.mockRestore()
  })
})
