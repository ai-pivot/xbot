/**
 * useLLMSettings 跨实例同步（复现：设置里 add/update LLM 后，会话 LLM 选择栏不更新）。
 *
 * 架构事实：
 *   - AgentPanel 用 useLLMSettings() 喂会话 LLM 选择栏（ModelSelector 的
 *     subscriptions / modelEntries）；
 *   - SettingsDialog 也用一个 useLLMSettings() 实例做订阅/模型的增删改；
 *   - 两个实例各自持有 state，mutation 只 await load() 自己那份 → 选择栏陈旧，
 *     必须刷新页面才更新（用户报告的 bug）。
 *
 * 期望：任一实例改完服务端 LLM 配置后，所有实例（含选择栏那份）都同步刷新，
 * 无需刷新页面（"两边数据统一"）。
 */
import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { WSConnection } from '@/types/ws'
import { useLLMSettings } from './useLLMSettings'

const connection = vi.hoisted(() => ({ current: undefined as unknown }))

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => connection.current,
}))

interface FakeSubscription {
  id: string
  name: string
  provider: string
  base_url: string
  api_key: string
  model: string
  active?: boolean
  max_output_tokens?: number
  thinking_mode?: string
  api_type?: string
}

/** 最小可用的假服务端：订阅存在数组里，add/update 就地改。 */
function makeServer() {
  const subs: FakeSubscription[] = []
  const entries: Array<Record<string, unknown>> = []
  const rpc = vi.fn(async (method: string, params?: Record<string, unknown>) => {
    switch (method) {
      case 'list_subscriptions':
        return subs.map((s) => ({ ...s }))
      case 'list_all_model_entries':
        return entries.map((e) => ({ ...e }))
      case 'get_user_thinking_mode':
        return ''
      case 'get_llm_concurrency':
        return 0
      case 'get_settings':
        return {}
      case 'add_subscription': {
        const sub = params?.sub as FakeSubscription
        subs.push({ ...sub, id: `sub-${subs.length + 1}` })
        return null
      }
      case 'update_subscription': {
        const id = params?.id as string
        const sub = params?.sub as FakeSubscription
        const idx = subs.findIndex((s) => s.id === id)
        if (idx >= 0) subs[idx] = { ...subs[idx], ...sub, id }
        return null
      }
      default:
        return null
    }
  })
  return { subs, entries, rpc }
}

/** 两个独立实例：bar = 会话 LLM 选择栏的数据源；dialog = 设置面板的数据源。 */
function useBarAndDialog() {
  return { bar: useLLMSettings(), dialog: useLLMSettings() }
}

function useTwo(server: { rpc: unknown }) {
  connection.current = {
    connected: true,
    rpc: server.rpc,
    // master 的 useLLMSettings 通过 onConnectionChange 订阅连接变化
    // （SSE 重连后重新拉取）；假连接必须提供该方法，否则 hook 抛
    // TypeError: conn.onConnectionChange is not a function。
    onConnectionChange: () => () => {},
  } as unknown as WSConnection
  return renderHook(useBarAndDialog)
}

describe('useLLMSettings 跨实例同步', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })

  it('设置实例新增订阅后，选择栏实例必须同步看到（无需刷新页面）', async () => {
    const server = makeServer()
    const { result } = useTwo(server)

    await waitFor(() => expect(result.current.bar.loading).toBe(false))
    await waitFor(() => expect(result.current.dialog.loading).toBe(false))
    expect(result.current.bar.data.subscriptions).toHaveLength(0)

    await act(async () => {
      await result.current.dialog.addSubscription({
        name: 'New Sub',
        provider: 'openai',
        base_url: 'https://example.test/v1',
        api_key: 'sk-test',
        model: 'model-a',
      })
    })

    // 触发方自己当然要更新
    expect(result.current.dialog.data.subscriptions).toHaveLength(1)
    // 另一个实例（= 会话 LLM 选择栏的数据源）也必须更新 —— 这就是用户报的 bug
    await waitFor(() => expect(result.current.bar.data.subscriptions).toHaveLength(1))
  })

  it('设置实例更新订阅后，选择栏实例必须看到新值', async () => {
    const server = makeServer()
    server.subs.push({
      id: 'sub-1',
      name: 'Old Name',
      provider: 'openai',
      base_url: 'https://example.test/v1',
      api_key: 'sk-test',
      model: 'model-a',
    })
    const { result } = useTwo(server)

    await waitFor(() => expect(result.current.bar.data.subscriptions).toHaveLength(1))
    expect(result.current.bar.data.subscriptions[0].name).toBe('Old Name')

    await act(async () => {
      await result.current.dialog.updateSubscription('sub-1', {
        name: 'Renamed',
        provider: 'openai',
        base_url: 'https://example.test/v1',
        api_key: 'sk-test',
        model: 'model-b',
      })
    })

    await waitFor(() => expect(result.current.bar.data.subscriptions[0].name).toBe('Renamed'))
    expect(result.current.bar.data.subscriptions[0].model).toBe('model-b')
  })
})
