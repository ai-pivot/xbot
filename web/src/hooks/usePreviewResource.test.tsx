// @vitest-environment jsdom
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { usePreviewResource } from './usePreviewResource'
import { fetchPreviewResource } from '@/lib/previewResource'

vi.mock('@/lib/previewConnection', () => ({ getPreviewBackendUrl: () => 'https://backend.example', getPreviewToken: () => 'secret' }))
vi.mock('@/lib/previewResource', () => ({
  isProtectedPreviewResource: (src: string) => src.startsWith('/api/'),
  fetchPreviewResource: vi.fn(),
}))
const fetcher = vi.mocked(fetchPreviewResource)
beforeEach(() => {
  fetcher.mockReset()
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn(() => 'blob:protected')
    static revokeObjectURL = vi.fn()
  })
})
afterEach(() => vi.unstubAllGlobals())

describe('usePreviewResource', () => {
  it('never exposes backend URL before Blob is ready; revokes and aborts on unmount', async () => {
    fetcher.mockResolvedValue(new Blob(['image']))
    const { result, unmount } = renderHook(() => usePreviewResource('/api/files/a'))
    expect(result.current.src).toBeUndefined()
    await waitFor(() => expect(result.current.src).toBe('blob:protected'))
    const signal = fetcher.mock.calls[0][1]
    unmount()
    expect(signal.aborted).toBe(true)
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:protected')
  })
  it('aborts stale loads and does not allocate a URL for their late completion', async () => {
    let resolve!: (blob: Blob) => void
    fetcher.mockImplementationOnce(() => new Promise(done => { resolve = done }))
    fetcher.mockResolvedValueOnce(new Blob(['new']))
    const { result, rerender, unmount } = renderHook(({ src }) => usePreviewResource(src), { initialProps: { src: '/api/a' } })
    const firstSignal = fetcher.mock.calls[0][1]
    rerender({ src: '/api/b' })
    expect(result.current.src).toBeUndefined()
    expect(firstSignal.aborted).toBe(true)
    await act(async () => { resolve(new Blob(['old'])) })
    await waitFor(() => expect(result.current.src).toBe('blob:protected'))
    expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
    unmount()
  })
  it('keeps external links unchanged and never fetches them', () => {
    const { result } = renderHook(() => usePreviewResource('https://external.example/image.png'))
    expect(result.current.src).toBe('https://external.example/image.png')
    expect(fetcher).not.toHaveBeenCalled()
  })
  it('keeps failed protected URLs out of image attributes', async () => {
    fetcher.mockRejectedValue(new Error('Unauthorized'))
    const { result } = renderHook(() => usePreviewResource('/api/a'))
    await waitFor(() => expect(result.current.error).toBe('Unauthorized'))
    expect(result.current.src).toBeUndefined()
  })
})
