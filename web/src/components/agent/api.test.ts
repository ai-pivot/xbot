import { beforeEach, describe, expect, it, vi } from 'vitest'

import { postAPI } from '@/lib/api'
import { continueInteractiveSession, fetchCwd, fetchSessionSubscription, isMaskedAPIKey, rewindHistory } from './api'
import type { Subscription } from '@/types/shared'

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn(),
}))

const postAPIMock = vi.mocked(postAPI)

beforeEach(() => {
  postAPIMock.mockReset()
})

describe('agent REST API', () => {
  it('reads idle session CWD from the authoritative status endpoint', async () => {
    postAPIMock.mockResolvedValue({ cwd: '/workspace/project' })

    await expect(fetchCwd({ channel: 'cli', chatID: '/workspace:Agent-main' })).resolves.toEqual({
      dir: '/workspace/project',
    })
    expect(postAPIMock).toHaveBeenCalledWith('/api/session/status', {
      channel: 'cli',
      chat_id: '/workspace:Agent-main',
    })
  })

  it('requests the selected session subscription', async () => {
    postAPIMock.mockResolvedValue({
      subscription_id: 'sub-a',
      model: 'model-a',
    })

    await expect(
      fetchSessionSubscription({
        channel: 'agent',
        chatID: 'web:web-1/review:1',
      }),
    ).resolves.toEqual({ subscription_id: 'sub-a', model: 'model-a' })
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_session_subscription',
      params: { channel: 'agent', chat_id: 'web:web-1/review:1' },
    })
  })

  it('rewinds by stable history ID without timestamp fields', async () => {
    postAPIMock.mockResolvedValue({ history_rewound: true })

    await rewindHistory({ channel: 'web', chatID: 'chat-1' }, 42)

    expect(postAPIMock).toHaveBeenCalledWith('/api/history/rewind', {
      channel: 'web',
      chat_id: 'chat-1',
      history_id: 42,
    })
  })

  it('continues an interactive Agent by canonical full key', async () => {
    const ws = { rpc: vi.fn(async () => undefined) }

    await continueInteractiveSession(ws as never, 'agent:web:chat-1/review:1/fix:2', 'continue')

    expect(ws.rpc).toHaveBeenCalledWith('continue_interactive_session', {
      full_key: 'agent:web:chat-1/review:1/fix:2',
      content: 'continue',
    })
  })
})

// ── LLM Config Tests (Spec D) ──

describe('isMaskedAPIKey', () => {
  it('detects masked API keys containing ****', () => {
    expect(isMaskedAPIKey('sk-a****')).toBe(true)
    expect(isMaskedAPIKey('sk-abc****def')).toBe(true)
    expect(isMaskedAPIKey('****')).toBe(true)
    expect(isMaskedAPIKey('sk-123456****')).toBe(true)
  })

  it('returns false for real API keys', () => {
    expect(isMaskedAPIKey('sk-abc123def456')).toBe(false)
    expect(isMaskedAPIKey('sk-proj-1234567890abcdef')).toBe(false)
    expect(isMaskedAPIKey('')).toBe(false)
    expect(isMaskedAPIKey('real-key-no-masking')).toBe(false)
  })

  it('returns false for empty and whitespace-only keys', () => {
    expect(isMaskedAPIKey('')).toBe(false)
    expect(isMaskedAPIKey('   ')).toBe(false)
  })
})

describe('System subscription protection', () => {
  const systemSub: Subscription = {
    id: 'system',
    name: 'system',
    provider: 'openai',
    base_url: 'https://api.openai.com/v1',
    api_key: 'sk-a****',
    model: 'gpt-4o',
    max_output_tokens: 0,
    max_context: 0,
    api_type: '',
    thinking_mode: '',
    per_model_configs: {},
    active: true,
    enabled: true,
    is_system: true,
  }

  const userSub: Subscription = {
    id: 'user-1',
    name: 'My OpenAI',
    provider: 'openai',
    base_url: 'https://api.openai.com/v1',
    api_key: 'sk-abc123',
    model: 'gpt-4o',
    max_output_tokens: 0,
    max_context: 0,
    api_type: '',
    thinking_mode: '',
    per_model_configs: {},
    active: false,
    enabled: true,
    is_system: false,
  }

  it('identifies system subscriptions by is_system flag', () => {
    expect(systemSub.is_system).toBe(true)
    expect(userSub.is_system).toBe(false)
  })

  it('system subscription should not be editable', () => {
    const shouldAllowEdit = (sub: Subscription) => !sub.is_system
    expect(shouldAllowEdit(systemSub)).toBe(false)
    expect(shouldAllowEdit(userSub)).toBe(true)
  })

  it('system subscription should not be deletable', () => {
    const shouldAllowDelete = (sub: Subscription) => !sub.is_system
    expect(shouldAllowDelete(systemSub)).toBe(false)
    expect(shouldAllowDelete(userSub)).toBe(true)
  })

  it('system subscription should not be toggleable', () => {
    const shouldAllowToggle = (sub: Subscription) => !sub.is_system
    expect(shouldAllowToggle(systemSub)).toBe(false)
    expect(shouldAllowToggle(userSub)).toBe(true)
  })

  it('system subscription models should be read-only', () => {
    const shouldAllowModelEdit = (sub: Subscription) => !sub.is_system
    expect(shouldAllowModelEdit(systemSub)).toBe(false)
    expect(shouldAllowModelEdit(userSub)).toBe(true)
  })
})

describe('Tier config value format', () => {
  it('should format tier value as subID|model', () => {
    const formatTierValue = (subID: string, model: string) => `${subID}|${model}`
    expect(formatTierValue('sub-1', 'gpt-4o')).toBe('sub-1|gpt-4o')
    expect(formatTierValue('system', 'gpt-4o-mini')).toBe('system|gpt-4o-mini')
  })

  it('should parse tier value back to subID and model', () => {
    const parseTierValue = (v: string): { subID: string; model: string } => {
      const [subID, model] = v.split('|')
      return { subID, model }
    }
    expect(parseTierValue('sub-1|gpt-4o')).toEqual({
      subID: 'sub-1',
      model: 'gpt-4o',
    })
    expect(parseTierValue('system|gpt-4o-mini')).toEqual({
      subID: 'system',
      model: 'gpt-4o-mini',
    })
  })
})
