import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
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
import { changeLocale } from '@/i18n'
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
    fireEvent.change(screen.getByPlaceholderText(/搜索插件配置项…|Search plugin settings…/), {
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

/**
 * schema 文本走**插件自有文案表**（plugin.json 的 `web.i18n`）。
 *
 * 契约：`label` / `description` 可以写该插件文案表里的 **key** —— 宿主用**该插件的表** +
 * 当前语言解析；**不是 key**（历史插件的裸字符串）或**该插件没有表** ⇒ **原样透传**
 * （向后兼容，零 hack）。表来自既有 RPC `web_plugin_list`（不新增 RPC），解析器与插件
 * 运行时的 `ctx.i18n` 同一份实现（`createPluginI18n`）。
 */
describe('SettingsPlugins · schema 文案走插件表（web.i18n）', () => {
  const TABLE = {
    'zh-CN': { 'config.mode.label': '模式', 'config.mode.description': '运行模式说明' },
    en: { 'config.mode.label': 'Mode', 'config.mode.description': 'How the mode works' },
    ja: { 'config.mode.label': 'モード', 'config.mode.description': 'モードの説明' },
  }
  const keyedPlugin = {
    id: 'xbot.keyed',
    name: 'Keyed Plugin',
    title: 'Keyed',
    runtime: 'script',
    enabled: true,
    properties: {
      // key —— 命中插件表 ⇒ 按宿主语言解析
      mode: { type: 'string', label: 'config.mode.label', description: 'config.mode.description' },
      // 裸字符串 —— 非 key ⇒ 原样透传
      raw: { type: 'string', label: 'Raw label', description: 'Raw description' },
    },
    values: {},
  }

  /** 两个既有 RPC：plugin_config（schema）+ web_plugin_list（清单里的 web.i18n 表）。 */
  function mockRpc(decls: Array<{ id: string; i18n?: Record<string, unknown> }>) {
    mockPost.mockImplementation(
      async (_url: string, args: { method?: string }) =>
        args?.method === 'web_plugin_list' ? { plugins: decls } : { plugins: [keyedPlugin] },
    )
  }

  afterEach(() => changeLocale('zh-CN'))

  it('宿主 en：key 命中插件表 ⇒ 显示英文', async () => {
    changeLocale('en')
    mockRpc([{ id: 'xbot.keyed', i18n: TABLE }])
    render(<SettingsPlugins />)
    expect(await screen.findByText('Mode')).toBeInTheDocument()
    expect(screen.getByText('How the mode works')).toBeInTheDocument()
  })

  it('宿主 zh-CN：key 命中插件表 ⇒ 显示中文', async () => {
    changeLocale('zh-CN')
    mockRpc([{ id: 'xbot.keyed', i18n: TABLE }])
    render(<SettingsPlugins />)
    expect(await screen.findByText('模式')).toBeInTheDocument()
    expect(screen.getByText('运行模式说明')).toBeInTheDocument()
  })

  it('非 key 的裸字符串原样透传（向后兼容历史插件）', async () => {
    changeLocale('en')
    mockRpc([{ id: 'xbot.keyed', i18n: TABLE }])
    render(<SettingsPlugins />)
    await screen.findByText('Mode')
    expect(screen.getByText('Raw label')).toBeInTheDocument()
    expect(screen.getByText('Raw description')).toBeInTheDocument()
  })

  it('插件没有 i18n 表 ⇒ 原样透传、不报错', async () => {
    changeLocale('en')
    mockRpc([{ id: 'xbot.keyed' }])
    render(<SettingsPlugins />)
    // 无表 ⇒ 不做任何替换（key 也照原样显示），配置面板照常渲染。
    expect(await screen.findByText('config.mode.label')).toBeInTheDocument()
    expect(screen.getByText('Raw label')).toBeInTheDocument()
  })

  it('web_plugin_list 失败 ⇒ 配置仍渲染（降级为原样透传）', async () => {
    changeLocale('en')
    mockPost.mockImplementation(async (_url: string, args: { method?: string }) => {
      if (args?.method === 'web_plugin_list') throw new Error('rpc down')
      return { plugins: [keyedPlugin] }
    })
    render(<SettingsPlugins />)
    // 清单 RPC 挂掉不连坐配置面板：插件照常渲染，key 原样透传（不崩、不卡 loading）。
    expect(await screen.findByText('Keyed Plugin')).toBeInTheDocument()
    expect(screen.getByText('config.mode.label')).toBeInTheDocument()
    expect(screen.getByText('Raw label')).toBeInTheDocument()
  })
})
