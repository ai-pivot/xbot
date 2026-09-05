import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

// vi.hoisted：useEffect [conn] 依赖引用稳定；rpc 按方法名分发（get_settings/
// set_setting 都走 ws.rpc —— api.ts 的 getSettings/setSetting helpers）。
const rpcMock = vi.hoisted(() => vi.fn())
const wsStub = vi.hoisted(() => ({
  connected: true,
  rpc: rpcMock as unknown,
}))
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => wsStub,
}))

import { renderWithProviders } from '@/test-utils'
import { SettingsAgent } from './SettingsAgent'

describe('SettingsAgent — allow_self_compact 开关', () => {
  beforeEach(() => {
    rpcMock.mockReset()
  })

  it('挂载时读 get_settings 并反映服务端值（on）', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ allow_self_compact: 'true' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsAgent />)
    const sw = await screen.findByRole('switch')
    await waitFor(() => expect(sw).toHaveAttribute('data-state', 'checked'))
    expect(rpcMock).toHaveBeenCalledWith('get_settings', { namespace: 'cli', sender_id: '' })
  })

  it('DB 无值时反映 config.json 注入的默认值（off）', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ allow_self_compact: 'false' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsAgent />)
    const sw = await screen.findByRole('switch')
    await waitFor(() => expect(sw).toHaveAttribute('data-state', 'unchecked'))
  })

  it('切换开关写 set_setting（乐观更新 + 新值）', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ allow_self_compact: 'false' })
      return Promise.resolve({})
    })
    renderWithProviders(<SettingsAgent />)
    const sw = await screen.findByRole('switch')
    // loaded 后 disabled 解除。
    await waitFor(() => expect(sw).not.toBeDisabled())
    fireEvent.click(sw)
    await waitFor(() => {
      expect(rpcMock).toHaveBeenCalledWith('set_setting', {
        namespace: 'cli',
        sender_id: '',
        key: 'allow_self_compact',
        value: 'true',
      })
    })
    await waitFor(() => expect(sw).toHaveAttribute('data-state', 'checked'))
  })

  it('写失败时回滚开关状态', async () => {
    rpcMock.mockImplementation((method: string) => {
      if (method === 'get_settings') return Promise.resolve({ allow_self_compact: 'true' })
      return Promise.reject(new Error('rpc down'))
    })
    renderWithProviders(<SettingsAgent />)
    const sw = await screen.findByRole('switch')
    await waitFor(() => expect(sw).not.toBeDisabled())
    fireEvent.click(sw) // 关 → RPC 失败 → 回滚回 on
    await waitFor(() => expect(sw).toHaveAttribute('data-state', 'checked'))
  })
})
