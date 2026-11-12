import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ImmersiveModeButton } from './ImmersiveModeButton'

vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

let fullscreen: Element | null
const lock = vi.fn<(codes: string[]) => Promise<void>>()
const unlock = vi.fn()
const enter = vi.fn<() => Promise<void>>()
const exit = vi.fn<() => Promise<void>>()
function change(element: Element | null) {
  fullscreen = element
  document.dispatchEvent(new Event('fullscreenchange'))
}
function button() { return screen.getByRole('button') }
async function start() {
  fireEvent.click(button())
  await waitFor(() => expect(button().getAttribute('aria-pressed')).toBe('true'))
  await waitFor(() => expect(button().hasAttribute('disabled')).toBe(false))
}

beforeEach(() => {
  fullscreen = null
  lock.mockReset().mockResolvedValue(undefined)
  unlock.mockReset()
  enter.mockReset().mockImplementation(async () => { change(document.documentElement) })
  exit.mockReset().mockImplementation(async () => { change(null) })
  Object.defineProperty(document, 'fullscreenElement', { configurable: true, get: () => fullscreen })
  Object.defineProperty(document, 'fullscreenEnabled', { configurable: true, get: () => true })
  vi.stubGlobal('navigator', Object.assign(Object.create(navigator), { keyboard: { lock, unlock } }))
  Object.defineProperty(document.documentElement, 'requestFullscreen', { configurable: true, value: enter })
  Object.defineProperty(document, 'exitFullscreen', { configurable: true, value: exit })
})
afterEach(() => {
  cleanup()
  delete (document.documentElement as Partial<HTMLElement>).requestFullscreen
  delete (document as Partial<Document>).exitFullscreen
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  Reflect.deleteProperty(document, 'fullscreenElement')
  Reflect.deleteProperty(document, 'fullscreenEnabled')
})

describe('immersive mode', () => {
  it('enters from a click, locks shortcuts except Escape, and exits via its button', async () => {
    render(<ImmersiveModeButton />)
    expect(enter).not.toHaveBeenCalled()
    await start()
    expect(lock).toHaveBeenCalledWith(expect.arrayContaining(['KeyN', 'KeyW', 'Tab', 'F2']))
    expect(lock.mock.calls[0][0]).not.toContain('Escape')
    expect(button().getAttribute('aria-label')).toBe('layout.immersiveExit')
    fireEvent.click(button())
    await waitFor(() => expect(button().getAttribute('aria-pressed')).toBe('false'))
    expect(exit).toHaveBeenCalledOnce()
    expect(unlock).toHaveBeenCalledOnce()
  })

  it('retains fullscreen with a visible fallback when keyboard lock is unsupported', async () => {
    vi.stubGlobal('navigator', Object.create(Object.getPrototypeOf(navigator)))
    render(<ImmersiveModeButton />)
    await start()
    expect(screen.getByRole('status').textContent).toBe('layout.immersiveNoLock')
    expect(exit).not.toHaveBeenCalled()
  })

  it('retains fullscreen when keyboard permission is denied', async () => {
    lock.mockRejectedValue(new DOMException('Denied', 'NotAllowedError'))
    render(<ImmersiveModeButton />)
    await start()
    expect(screen.getByRole('status').textContent).toBe('layout.immersiveNoLock')
    expect(fullscreen).toBe(document.documentElement)
  })

  it('reports fullscreen failure without requesting a lock', async () => {
    enter.mockRejectedValue(new Error('Denied'))
    render(<ImmersiveModeButton />)
    fireEvent.click(button())
    await waitFor(() => expect(screen.getByRole('status').textContent).toBe('layout.immersiveFailed'))
    expect(button().getAttribute('aria-pressed')).toBe('false')
    expect(button().hasAttribute('disabled')).toBe(false)
    expect(lock).not.toHaveBeenCalled()
  })

  it('disables the entry if fullscreen is unavailable', () => {
    vi.spyOn(document, 'fullscreenEnabled', 'get').mockReturnValue(false)
    render(<ImmersiveModeButton />)
    expect(button().hasAttribute('disabled')).toBe(true)
    expect(button().title).toBe('layout.immersiveUnavailable')
  })

  it('releases the lock and resets the button after browser-initiated exit', async () => {
    render(<ImmersiveModeButton />)
    await start()
    act(() => change(null))
    expect(unlock).toHaveBeenCalledOnce()
    expect(button().getAttribute('aria-pressed')).toBe('false')
  })

  it('releases a late permission grant after exiting fullscreen', async () => {
    let resolve!: () => void
    lock.mockReturnValue(new Promise<void>(done => { resolve = done }))
    render(<ImmersiveModeButton />)
    fireEvent.click(button())
    await waitFor(() => expect(lock).toHaveBeenCalledOnce())
    act(() => change(null))
    await act(async () => { resolve() })
    expect(unlock).toHaveBeenCalledTimes(2)
    expect(button().getAttribute('aria-pressed')).toBe('false')
  })

  it('cleans up fullscreen and keyboard lock on unmount', async () => {
    const view = render(<ImmersiveModeButton />)
    await start()
    view.unmount()
    expect(unlock).toHaveBeenCalledOnce()
    expect(exit).toHaveBeenCalledOnce()
  })

  it('does not lock the keyboard after unmount during a pending fullscreen request', async () => {
    let resolve!: () => void
    enter.mockImplementation(() => new Promise<void>(done => { resolve = () => { change(document.documentElement); done() } }))
    const view = render(<ImmersiveModeButton />)
    fireEvent.click(button())
    fireEvent.click(button())
    expect(enter).toHaveBeenCalledOnce()
    view.unmount()
    await act(async () => { resolve() })
    expect(lock).not.toHaveBeenCalled()
    expect(fullscreen).toBeNull()
  })
})
