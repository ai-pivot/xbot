import { describe, it, expect, vi } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useNetworkStatus } from '../hooks/useNetworkStatus'
import { useKeyboardShortcuts } from '../hooks/useKeyboardShortcuts'

// ─── useNetworkStatus ───

describe('useNetworkStatus', () => {
  it('returns correct initial state with navigator.onLine=true', () => {
    const { result } = renderHook(() => useNetworkStatus(true, false, false))
    expect(result.current.online).toBe(true)
    expect(result.current.wsConnected).toBe(true)
    expect(result.current.wsReconnecting).toBe(false)
    expect(result.current.serverStopped).toBe(false)
  })

  it('returns correct initial state with navigator.onLine=false', () => {
    Object.defineProperty(navigator, 'onLine', { value: false, writable: true, configurable: true })
    const { result } = renderHook(() => useNetworkStatus(false, true, true))
    expect(result.current.online).toBe(false)
    expect(result.current.wsConnected).toBe(false)
    expect(result.current.wsReconnecting).toBe(true)
    expect(result.current.serverStopped).toBe(true)
    // Restore
    Object.defineProperty(navigator, 'onLine', { value: true, writable: true, configurable: true })
  })

  it('returns wsConnected and wsReconnecting from params', () => {
    const { result } = renderHook(() => useNetworkStatus(false, true, false))
    expect(result.current.wsConnected).toBe(false)
    expect(result.current.wsReconnecting).toBe(true)
    expect(result.current.serverStopped).toBe(false)
  })

  it('updates online to true on window online event', () => {
    Object.defineProperty(navigator, 'onLine', { value: false, writable: true, configurable: true })
    const { result } = renderHook(() => useNetworkStatus(true, false, false))
    expect(result.current.online).toBe(false)
    act(() => { window.dispatchEvent(new Event('online')) })
    expect(result.current.online).toBe(true)
    // Restore
    Object.defineProperty(navigator, 'onLine', { value: true, writable: true, configurable: true })
  })

  it('updates online to false on window offline event', () => {
    const { result } = renderHook(() => useNetworkStatus(true, false, false))
    expect(result.current.online).toBe(true)
    act(() => { window.dispatchEvent(new Event('offline')) })
    expect(result.current.online).toBe(false)
  })

  it('cleans up event listeners on unmount', () => {
    const { unmount } = renderHook(() => useNetworkStatus(true, false, false))
    unmount()
    // After unmount, dispatching events should not throw
    act(() => { window.dispatchEvent(new Event('online')) })
    act(() => { window.dispatchEvent(new Event('offline')) })
  })
})

// ─── useKeyboardShortcuts ───

describe('useKeyboardShortcuts', () => {
  it('triggers handler on key match', () => {
    const handler = vi.fn()
    renderHook(() => useKeyboardShortcuts([{ key: 'k', handler }]))
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', bubbles: true }))
    })
    expect(handler).toHaveBeenCalledOnce()
  })

  it('does not trigger when enabled=false', () => {
    const handler = vi.fn()
    renderHook(() => useKeyboardShortcuts([{ key: 'k', handler, enabled: false }]))
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', bubbles: true }))
    })
    expect(handler).not.toHaveBeenCalled()
  })

  it('first match wins — only first handler called', () => {
    const handler1 = vi.fn()
    const handler2 = vi.fn()
    renderHook(() => useKeyboardShortcuts([
      { key: 'k', handler: handler1 },
      { key: 'k', handler: handler2 },
    ]))
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', bubbles: true }))
    })
    expect(handler1).toHaveBeenCalledOnce()
    expect(handler2).not.toHaveBeenCalled()
  })

  it('ctrl:true requires Ctrl key', () => {
    const handler = vi.fn()
    renderHook(() => useKeyboardShortcuts([{ key: 'k', ctrl: true, handler }]))
    // Without Ctrl
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', bubbles: true }))
    })
    expect(handler).not.toHaveBeenCalled()
    // With Ctrl
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', ctrlKey: true, bubbles: true }))
    })
    expect(handler).toHaveBeenCalledOnce()
  })

  it('ctrl:true works with meta key (Cmd)', () => {
    const handler = vi.fn()
    renderHook(() => useKeyboardShortcuts([{ key: 'k', ctrl: true, handler }]))
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))
    })
    expect(handler).toHaveBeenCalledOnce()
  })

  it('cleans up listener on unmount', () => {
    const handler = vi.fn()
    const { unmount } = renderHook(() => useKeyboardShortcuts([{ key: 'k', handler }]))
    unmount()
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', bubbles: true }))
    })
    expect(handler).not.toHaveBeenCalled()
  })
})
