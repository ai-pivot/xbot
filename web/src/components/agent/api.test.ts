import { beforeEach, describe, expect, it, vi } from 'vitest'

import { postAPI } from '@/lib/api'
import { fetchCwd, fetchSessionSubscription } from './api'

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

    await expect(fetchCwd({ channel: 'cli', chatID: '/workspace:Agent-main' }))
      .resolves.toEqual({ dir: '/workspace/project' })
    expect(postAPIMock).toHaveBeenCalledWith('/api/session/status', {
      channel: 'cli',
      chat_id: '/workspace:Agent-main',
    })
  })

  it('requests the selected session subscription', async () => {
    postAPIMock.mockResolvedValue({ subscription_id: 'sub-a', model: 'model-a' })

    await expect(fetchSessionSubscription({ channel: 'agent', chatID: 'web:web-1/review:1' }))
      .resolves.toEqual({ subscription_id: 'sub-a', model: 'model-a' })
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
      method: 'get_session_subscription',
      params: { channel: 'agent', chat_id: 'web:web-1/review:1' },
    })
  })
})
