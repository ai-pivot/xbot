// @vitest-environment jsdom
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MarkdownRenderer } from './MarkdownRenderer'
import { ImageLightboxHost, openLightbox } from './Lightbox'

vi.mock('@/lib/previewConnection', () => ({
  previewEnabled: () => true,
  getPreviewToken: () => 'secret',
  getPreviewBackendUrl: () => 'https://backend.example',
  backendUrl: (src: string) => `https://backend.example${src}`,
}))
const fetcher = vi.fn()
beforeEach(() => {
  fetcher.mockReset()
  vi.stubGlobal('fetch', fetcher)
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn(() => 'blob:protected')
    static revokeObjectURL = vi.fn()
  })
})
afterEach(() => vi.unstubAllGlobals())

describe('protected Markdown and Lightbox consumers', () => {
  it('does not mount protected img while pending, loads Blob, and keeps external links unchanged', async () => {
    let resolve!: (response: Response) => void
    fetcher.mockImplementation(() => new Promise(done => { resolve = done }))
    const { container } = render(<MarkdownRenderer content={'![photo](/api/files/a)\n\n[external](https://example.com/api/a)'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('a')).toHaveAttribute('href', 'https://example.com/api/a')
    await act(async () => { resolve(new Response('image')) })
    await waitFor(() => expect(container.querySelector('img')).toHaveAttribute('src', 'blob:protected'))
    expect(container.innerHTML).not.toContain('secret')
  })
  it('uses on-demand bounded fetch for protected download links, never a raw API href', async () => {
    fetcher.mockResolvedValue(new Response('file'))
    const { container } = render(<MarkdownRenderer content={'[file](/api/files/a)'} />)
    const link = container.querySelector('a')!
    expect(link).toHaveAttribute('href', '#')
    expect(fetcher).not.toHaveBeenCalled()
    fireEvent.click(link)
    await waitFor(() => expect(link).toHaveAttribute('href', 'blob:protected'))
    expect(link).toHaveAttribute('download')
  })
  it('keeps unauthorized resource URLs out of image fallback links', async () => {
    fetcher.mockResolvedValue(new Response('no', { status: 401 }))
    const { container } = render(<MarkdownRenderer content={'![photo](/api/files/a)'} />)
    await waitFor(() => expect(container.textContent).toContain('401'))
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('a')).toBeNull()
  })
  it('lightbox independently fetches canonical references and revokes on close', async () => {
    fetcher.mockResolvedValue(new Response('image'))
    const { getByRole, queryByRole } = render(<ImageLightboxHost />)
    act(() => openLightbox('/api/files/a', 'photo'))
    expect(queryByRole('img')).toBeNull()
    await waitFor(() => expect(getByRole('img')).toHaveAttribute('src', 'blob:protected'))
    expect(getByRole('link')).toHaveAttribute('href', 'blob:protected')
    fireEvent.click(getByRole('button', { name: 'close' }))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:protected')
    expect(queryByRole('dialog')).toBeNull()
  })
})
