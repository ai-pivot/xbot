import '@testing-library/jest-dom/vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreviewGate } from './PreviewGate'
import { clearPreviewConnection, getPreviewToken, PREVIEW_BACKEND_STORAGE_KEY } from '@/lib/previewConnection'
vi.mock('@/i18n', () => ({ default: { language: 'en', resolvedLanguage: 'en' } }))

beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => { values.set(key, value) },
    removeItem: (key: string) => { values.delete(key) },
  })
})
afterEach(() => { cleanup(); clearPreviewConnection(); vi.restoreAllMocks(); vi.unstubAllGlobals() })
const publicConfig = () => new Response(JSON.stringify({ ok: true, data: { invite_only: false }, error: null }))

describe('preview provider boundary', () => {
  it('shows real editable localhost without mounting providers or making requests', () => {
    const child = vi.fn(() => <div>business app</div>)
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const Child = child
    render(<PreviewGate><Child /></PreviewGate>)
    expect(screen.getByLabelText('Backend address')).toHaveValue('http://localhost:8082')
    expect(child).not.toHaveBeenCalled()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(screen.getByText('Change backend')).toBeVisible()
  })
  it('saves only address, never unlocks after public reachability success', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(publicConfig()))
    render(<PreviewGate><div>business app</div></PreviewGate>)
    fireEvent.change(screen.getByLabelText('Backend address'), { target: { value: 'https://backend/base/' } })
    fireEvent.click(screen.getByText('Save address'))
    expect(localStorage.getItem(PREVIEW_BACKEND_STORAGE_KEY)).toBe('https://backend/base')
    fireEvent.click(screen.getByText('Test connection'))
    await screen.findByText(/Public backend endpoint reached/)
    expect(screen.queryByText('business app')).toBeNull()
    expect(getPreviewToken()).toBe('')
  })
  it('cancels on edit and ignores a stale successful result', async () => {
    let resolve: (response: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise<Response>(r => { resolve = r })))
    render(<PreviewGate><div>business app</div></PreviewGate>)
    fireEvent.click(screen.getByText('Test connection'))
    fireEvent.change(screen.getByLabelText('Backend address'), { target: { value: 'https://different' } })
    await act(async () => { resolve(publicConfig()) })
    expect(screen.queryByText(/Public backend endpoint reached/)).toBeNull()
    expect(screen.getByText('No connection test yet. No model requests will be made.')).toBeVisible()
  })
  it('requires danger consent and valid token verification before mounting children', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({
      ok: true, data: { user_id: 'u', origin: window.location.origin, expires_at: new Date(Date.now() + 60_000).toISOString() }, error: null,
    }))))
    render(<PreviewGate><div>business app</div></PreviewGate>)
    fireEvent.change(screen.getByLabelText('Short-lived preview token'), { target: { value: 'memory-secret' } })
    expect(screen.getByText('Verify & connect')).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox'))
    fireEvent.click(screen.getByText('Verify & connect'))
    await waitFor(() => expect(screen.getByText('business app')).toBeVisible())
    expect(getPreviewToken()).toBe('memory-secret')
    expect(localStorage.getItem(PREVIEW_BACKEND_STORAGE_KEY)).toBeNull()
    expect(screen.getByText('Change backend')).toBeVisible()
  })
})
