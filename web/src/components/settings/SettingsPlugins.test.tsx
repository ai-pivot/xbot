import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

vi.mock('@/lib/api', () => ({ postAPI: vi.fn() }))

import { postAPI } from '@/lib/api'
import { SettingsPlugins } from './SettingsPlugins'

const mockPost = postAPI as unknown as ReturnType<typeof vi.fn>

const plugin = {
  id: 'xbot.test',
  name: 'Test Plugin',
  title: 'Test Settings',
  runtime: 'script',
  enabled: true,
  properties: {
    mode: {
      type: 'select',
      label: 'Mode',
      options: [
        { label: 'Auto', value: 'auto' },
        { label: 'Manual', value: 'manual' },
      ],
    },
    enabled: { type: 'boolean', label: 'Enabled' },
    level: { type: 'number', label: 'Level' },
  },
  values: { mode: 'auto', enabled: true, level: 3 },
}

describe('SettingsPlugins', () => {
  beforeEach(() => {
    mockPost.mockReset()
  })

  it('renders plugin config fields after load', async () => {
    mockPost.mockResolvedValue({ plugins: [plugin] })
    render(<SettingsPlugins />)
    expect(await screen.findByText('Test Plugin')).toBeInTheDocument()
    expect(screen.getByText('Mode')).toBeInTheDocument()
    expect(screen.getByText('Enabled')).toBeInTheDocument()
    expect(screen.getByText('Level')).toBeInTheDocument()
  })

  it('filters config fields by search query', async () => {
    mockPost.mockResolvedValue({ plugins: [plugin] })
    render(<SettingsPlugins />)
    await screen.findByText('Test Plugin')
    fireEvent.change(screen.getByPlaceholderText('搜索插件配置项…'), {
      target: { value: 'Mode' },
    })
    expect(screen.getByText('Mode')).toBeInTheDocument()
    expect(screen.queryByText('Level')).not.toBeInTheDocument()
  })

  it('persists a config change via plugin_config_set', async () => {
    mockPost.mockResolvedValue({ plugins: [plugin] })
    render(<SettingsPlugins />)
    await screen.findByText('Test Plugin')
    // 切换 boolean 开关。
    const toggle = screen.getByRole('switch', { name: 'Enabled' })
    fireEvent.click(toggle)
    await vi.waitFor(() => {
      expect(mockPost).toHaveBeenCalledWith('/api/rpc', {
        method: 'plugin_config_set',
        params: { id: 'xbot.test', key: 'enabled', value: false },
      })
    })
  })
})
