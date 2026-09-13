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
  })
})
