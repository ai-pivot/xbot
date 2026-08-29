import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'

vi.mock('@/lib/api', () => ({ postAPI: vi.fn() }))

// SettingsPlugins 监听 web_plugin_config_changed（SSE 广播轻量同步）——
// 测试环境无 WSProvider，mock 稳定引用（vi.hoisted：useEffect [ws] 依赖引用稳定）。
const wsStub = vi.hoisted(() => ({ onMessage: () => () => {} }))
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => wsStub,
}))

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

const pluginWithRange = {
  ...plugin,
  properties: {
    ...plugin.properties,
    glassOpacity: { type: 'number', label: '玻璃不透明度', minimum: 0, maximum: 1 },
  },
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

  it('does NOT re-fetch the plugin list after a config change (no panel reload / slider interruption)', async () => {
    // 旧实现：setValue 成功后 onSaved() → load() 全量重拉 → 面板闪 loading
    // + plugins 引用全换 → 拖动中的滑条被打断（"刷新看板"体验差）。
    // 修复：本地乐观更新 + SSE 广播轻量合并，不重拉。
    mockPost.mockResolvedValue({ plugins: [plugin] })
    render(<SettingsPlugins />)
    await screen.findByText('Test Plugin')
    const fetchCount = () =>
      mockPost.mock.calls.filter(([, args]) => (args as { method?: string })?.method === 'plugin_config').length
    expect(fetchCount()).toBe(1)

    const input = screen.getByLabelText('Level') as HTMLInputElement
    fireEvent.change(input, { target: { value: '7' } })
    fireEvent.blur(input)
    await vi.waitFor(() => {
      expect(mockPost).toHaveBeenCalledWith('/api/rpc', {
        method: 'plugin_config_set',
        params: { id: 'xbot.test', key: 'level', value: 7 },
      })
    })
    // set 成功后不再重拉 plugin_config（无第二次全量加载）。
    expect(fetchCount()).toBe(1)
    // loading 骨架不闪（面板不进入加载态）。
    expect(screen.queryByText('加载插件配置…')).not.toBeInTheDocument()
  })

  it('number input stays editable — local draft keeps typed value until blur (mobile regression)', async () => {
    // 旧实现：受控 input 只挂 onBlur 无 onChange —— React 重渲染把 DOM 值
    // 弹回 props value，手机端数字改不了。修复：本地 draft 受控 + blur 提交。
    mockPost.mockResolvedValue({ plugins: [plugin] })
    render(<SettingsPlugins />)
    await screen.findByText('Test Plugin')
    const input = screen.getByLabelText('Level') as HTMLInputElement
    expect(input.value).toBe('3')
    fireEvent.change(input, { target: { value: '7' } })
    // draft 受控 —— 输入立即反映，不被重置弹回 '3'。
    expect(input.value).toBe('7')
    fireEvent.blur(input)
    await vi.waitFor(() => {
      expect(mockPost).toHaveBeenCalledWith('/api/rpc', {
        method: 'plugin_config_set',
        params: { id: 'xbot.test', key: 'level', value: 7 },
      })
    })
  })

  it('renders a slider + numeric input for ranged number props', async () => {
    mockPost.mockResolvedValue({ plugins: [pluginWithRange] })
    render(<SettingsPlugins />)
    await screen.findByText('Test Plugin')
    // 滑条（radix Slider thumb role="slider"，aria-label 转发）+ 数字输入框。
    const slider = screen.getByRole('slider', { name: '玻璃不透明度' })
    expect(slider).toBeInTheDocument()
    // 数字输入框（type=number → role=spinbutton，同样带 aria-label）。
    const input = screen.getByRole('spinbutton', { name: '玻璃不透明度' }) as HTMLInputElement
    expect(input.value).toBe('')
    // 无范围属性（Level）不渲染滑条。
    expect(screen.queryByRole('slider', { name: 'Level' })).not.toBeInTheDocument()
  })
})
