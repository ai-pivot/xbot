import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchPreviewResource, isProtectedPreviewResource, PREVIEW_RESOURCE_MAX_BYTES } from './previewResource'

vi.mock('./previewConnection', () => ({
  previewEnabled: () => true,
  getPreviewToken: () => 'secret-token',
  backendUrl: (path: string) => `https://backend.example/xbot${path}`,
}))
afterEach(() => vi.unstubAllGlobals())

describe('protected preview resources', () => {
  it('only authorizes canonical API paths, never user URLs', async () => {
    const fetcher = vi.fn()
    vi.stubGlobal('fetch', fetcher)
    for (const src of ['https://evil.example/api/a', '//evil.example/api/a', '/plugins/a', '/api/../../outside', '/api/\\evil']) {
      await expect(fetchPreviewResource(src, new AbortController().signal)).rejects.toThrow()
    }
    expect(fetcher).not.toHaveBeenCalled()
    expect(isProtectedPreviewResource('/api/files/a')).toBe(true)
  })
  it('uses bearer header, omits cookies, refuses ALL redirects and never puts token in URL', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('image', { headers: { 'Content-Type': 'image/png' } }))
    vi.stubGlobal('fetch', fetcher)
    const signal = new AbortController().signal
    const blob = await fetchPreviewResource('/api/files/a?inline=1', signal)
    expect(blob.type).toBe('image/png')
    expect(await blob.text()).toBe('image')
    expect(fetcher).toHaveBeenCalledWith('https://backend.example/xbot/api/files/a?inline=1', {
      headers: { Authorization: 'Bearer secret-token' }, credentials: 'omit', redirect: 'error', signal,
    })
    expect(fetcher.mock.calls[0][0]).not.toContain('secret-token')
  })
  it('does not retry a redirect without authorization or forward it to third parties', async () => {
    const fetcher = vi.fn().mockRejectedValue(new TypeError('redirect blocked'))
    vi.stubGlobal('fetch', fetcher)
    await expect(fetchPreviewResource('/api/files/a', new AbortController().signal)).rejects.toThrow('redirect blocked')
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
  it('reports unauthorized responses', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Unauthorized', { status: 401 })))
    await expect(fetchPreviewResource('/api/files/a', new AbortController().signal)).rejects.toThrow('401')
  })
  it('rejects oversized Content-Length before reading', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('small', { headers: { 'Content-Length': String(PREVIEW_RESOURCE_MAX_BYTES + 1) } })))
    await expect(fetchPreviewResource('/api/files/a', new AbortController().signal)).rejects.toThrow('32 MiB')
  })
  it('enforces the streaming budget even without Content-Length', async () => {
    const cancel = vi.fn()
    const stream = new ReadableStream({
      start(controller) { controller.enqueue(new Uint8Array(PREVIEW_RESOURCE_MAX_BYTES + 1)) }, cancel,
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(stream)))
    await expect(fetchPreviewResource('/api/files/a', new AbortController().signal)).rejects.toThrow('32 MiB')
    expect(cancel).toHaveBeenCalled()
  })
})
