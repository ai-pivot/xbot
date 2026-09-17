import { act, renderHook, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => {
  const connectionHandlers = new Set<(connected: boolean) => void>()
  const conn = {
    connected: false,
    onConnectionChange: vi.fn((handler: (connected: boolean) => void) => {
      connectionHandlers.add(handler)
      handler(conn.connected)
      return () => connectionHandlers.delete(handler)
    }),
  }
  return {
    conn,
    connectionHandlers,
    listSubscriptions: vi.fn(),
    listAllModelEntries: vi.fn(),
    getUserThinkingMode: vi.fn(),
    getLLMConcurrency: vi.fn(),
    getSettings: vi.fn(),
  }
})

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => mocks.conn,
}))

vi.mock('@/components/agent/api', () => ({
  listSubscriptions: mocks.listSubscriptions,
  listAllModelEntries: mocks.listAllModelEntries,
  getUserThinkingMode: mocks.getUserThinkingMode,
  getLLMConcurrency: mocks.getLLMConcurrency,
  getSettings: mocks.getSettings,
  addSubscription: vi.fn(),
  updateSubscription: vi.fn(),
  removeSubscription: vi.fn(),
  setDefaultSubscription: vi.fn(),
  setSubscriptionEnabled: vi.fn(),
  refreshModelEntries: vi.fn(),
  updatePerModelConfig: vi.fn(),
  setModelEnabled: vi.fn(),
  removeModel: vi.fn(),
  upsertModel: vi.fn(),
  setUserThinkingMode: vi.fn(),
  setLLMConcurrency: vi.fn(),
  setSetting: vi.fn(),
  isMaskedAPIKey: vi.fn(() => false),
}))

import { useLLMSettings } from './useLLMSettings'

describe('useLLMSettings', () => {
  beforeEach(() => {
    mocks.conn.connected = false
    mocks.connectionHandlers.clear()
    vi.clearAllMocks()
    mocks.listSubscriptions.mockResolvedValue([
      { id: 'sub-1', name: 'Test', provider: 'openai', enabled: true },
    ])
    mocks.listAllModelEntries.mockResolvedValue([
      { sub_id: 'sub-1', sub_name: 'Test', model: 'model-1', status: 'normal' },
    ])
    mocks.getUserThinkingMode.mockResolvedValue('')
    mocks.getLLMConcurrency.mockResolvedValue(4)
    mocks.getSettings.mockResolvedValue({ tier_vanguard: 'sub-1|model-1' })
  })

  it('loads REST-backed settings without an active SSE session', async () => {
    const { result } = renderHook(() => useLLMSettings())

    await waitFor(() => expect(result.current.loading).toBe(false))

    expect(result.current.error).toBeNull()
    expect(result.current.data.subscriptions).toHaveLength(1)
    expect(result.current.data.modelEntries).toHaveLength(1)
    expect(result.current.data.llmConcurrency).toBe(4)
    expect(mocks.listSubscriptions).toHaveBeenCalledTimes(1)
  })

  it('reloads once when an SSE connection is later established', async () => {
    renderHook(() => useLLMSettings())
    await waitFor(() => expect(mocks.listSubscriptions).toHaveBeenCalledTimes(1))

    act(() => {
      mocks.conn.connected = true
      mocks.connectionHandlers.forEach((handler) => handler(true))
    })

    await waitFor(() => expect(mocks.listSubscriptions).toHaveBeenCalledTimes(2))
  })
})
