import { afterEach, describe, expect, it, vi } from 'vitest'

import { APIError, postAPI } from './api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('postAPI', () => {
  it('sends JSON POST requests and unwraps response data', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      ok: true,
      data: { value: 42 },
      error: null,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(postAPI<{ value: number }>('/api/example', { name: 'xbot' }))
      .resolves.toEqual({ value: 42 })
    expect(fetchMock).toHaveBeenCalledWith('/api/example', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ name: 'xbot' }),
      headers: expect.objectContaining({ 'Content-Type': 'application/json' }),
    }))
  })

  it('throws the structured server error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ok: false,
      data: null,
      error: { code: 'not_found', message: 'missing session' },
    }), { status: 404, headers: { 'Content-Type': 'application/json' } })))

    const error = await postAPI('/api/example').catch((reason: unknown) => reason)
    expect(error).toBeInstanceOf(APIError)
    expect(error).toMatchObject({
      message: 'missing session',
      code: 'not_found',
      status: 404,
    })
  })

  it('accepts null data in a successful response', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({
      ok: true,
      data: null,
      error: null,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })))

    await expect(postAPI<null>('/api/example')).resolves.toBeNull()
  })
})
