import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// useWSConnection must return a stable object across renders (the load effect
// depends on it). rpc dispatches get_settings / set_setting by method name.
const rpcMock = vi.hoisted(() => vi.fn())
const wsStub = vi.hoisted(() => ({
  connected: true,
  rpc: rpcMock as unknown,
}))
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => wsStub,
}))

import { renderWithProviders } from '@/test-utils'
import i18n from '@/i18n'
import { SettingsSearch } from './SettingsSearch'

// 断言值与当前渲染语言使用同一 i18n 实例（vitest 无 navigator ⇒ 不由浏览器语言决定）。
const t = (key: string) => i18n.t(key)

describe('SettingsSearch — Tavily API key', () => {
  beforeEach(() => {
    rpcMock.mockReset()
  })

  it('未配置时显示「未配置」且没有清除按钮', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({})
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsSearch />)
    const status = await screen.findByTestId('search-key-status')
    await waitFor(() => expect(status).toHaveAttribute('data-configured', 'false'))
    expect(status).toHaveTextContent(t('settings.search.statusMissing'))
    expect(screen.queryByTestId('tavily-api-key-clear')).toBeNull()
    expect(rpcMock).toHaveBeenCalledWith('get_settings', { namespace: 'cli', sender_id: '' })
  })

  it('服务端返回掩码占位符时视为已配置（且不回填真实密钥）', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ tavily_api_key: '***' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsSearch />)
    const status = await screen.findByTestId('search-key-status')
    await waitFor(() => expect(status).toHaveAttribute('data-configured', 'true'))
    expect(status).toHaveTextContent(t('settings.search.statusConfigured'))
    const input = screen.getByTestId('tavily-api-key-input') as HTMLInputElement
    expect(input.value).toBe('')
    expect(screen.getByTestId('tavily-api-key-clear')).toBeInTheDocument()
  })

  it('保存新密钥写 set_setting(namespace=cli)，并重新读取状态', async () => {
    let stored = ''
    rpcMock.mockImplementation((method: string, params: Record<string, string>) => {
      if (method === 'get_settings') {
        return Promise.resolve(stored ? { tavily_api_key: '***' } : {})
      }
      if (method === 'set_setting') {
        expect(params).toMatchObject({ namespace: 'cli', sender_id: '', key: 'tavily_api_key' })
        stored = params.value
        return Promise.resolve({})
      }
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsSearch />)
    const input = (await screen.findByTestId('tavily-api-key-input')) as HTMLInputElement
    fireEvent.change(input, { target: { value: 'tvly-test-key' } })
    fireEvent.click(screen.getByTestId('tavily-api-key-save'))

    await waitFor(() => {
      expect(rpcMock).toHaveBeenCalledWith('set_setting', {
        namespace: 'cli',
        sender_id: '',
        key: 'tavily_api_key',
        value: 'tvly-test-key',
      })
    })
    await waitFor(() =>
      expect(screen.getByTestId('search-key-status')).toHaveAttribute('data-configured', 'true'),
    )
    expect(screen.getByTestId('search-settings-flash')).toHaveTextContent(t('settings.search.saved'))
    expect(input.value).toBe('')
  })

  it('清除写空串（回退到 config.json），不会把掩码写回数据库', async () => {
    rpcMock.mockImplementation((method: string, params: Record<string, string>) => {
      if (method === 'get_settings') return Promise.resolve({ tavily_api_key: '***' })
      if (method === 'set_setting') {
        expect(params.value).toBe('')
        return Promise.resolve({})
      }
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsSearch />)
    const clear = await screen.findByTestId('tavily-api-key-clear')
    fireEvent.click(clear)
    await waitFor(() => {
      expect(rpcMock).toHaveBeenCalledWith('set_setting', {
        namespace: 'cli',
        sender_id: '',
        key: 'tavily_api_key',
        value: '',
      })
    })
  })

  it('空输入时保存按钮不可用（不会用空串覆盖已配置密钥）', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ tavily_api_key: '***' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsSearch />)
    const save = await screen.findByTestId('tavily-api-key-save')
    await waitFor(() => expect(save).toBeDisabled())
  })
})
