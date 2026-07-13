import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { fetchCwd, fetchSessionSubscription } from './api'

// Mock global fetch
const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

beforeEach(() => {
  fetchMock.mockReset()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('agent REST API', () => {
  it('reads idle session CWD from the cwd endpoint', async () => {
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({ cwd: '/workspace/project' }),
    })

    const result = await fetchCwd({ channel: 'cli', chatID: '/workspace:Agent-main' })
    expect(result).toEqual({ cwd: '/workspace/project' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('/api/cwd')
    expect(url).toContain('channel=cli')
    expect(url).toContain('chat_id=')
  })

  it('requests the selected session subscription', async () => {
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({ subscription_id: 'sub-a', model: 'model-a' }),
    })

    const result = await fetchSessionSubscription({ channel: 'agent', chatID: 'web:web-1/review:1' })
    expect(result).toEqual({ subscription_id: 'sub-a', model: 'model-a' })
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toContain('/api/session-subscription')
    expect(url).toContain('channel=agent')
  })
})
