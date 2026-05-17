import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { useNotification } from '../hooks/useNotification'
import { useTabManager } from '../hooks/useTabManager'

// ─── useNotification ───

describe('useNotification', () => {
  const originalNotification = globalThis.Notification

  beforeEach(() => {
    vi.clearAllMocks()
  })

  afterEach(() => {
    // Restore Notification
    Object.defineProperty(globalThis, 'Notification', {
      value: originalNotification,
      writable: true,
      configurable: true,
    })
    Object.defineProperty(document, 'hidden', {
      value: false,
      writable: true,
      configurable: true,
    })
  })

  it('returns "unsupported" when Notification API not available', () => {
    // @ts-expect-error - intentionally removing Notification
    delete globalThis.Notification
    const { result } = renderHook(() => useNotification())
    expect(result.current.permission).toBe('unsupported')
  })

  it('returns default permission when Notification API available', () => {
    Object.defineProperty(globalThis, 'Notification', {
      value: function Notification() {},
      writable: true,
      configurable: true,
    })
    Object.defineProperty(globalThis.Notification, 'permission', {
      value: 'default',
      writable: false,
      configurable: true,
    })
    const { result } = renderHook(() => useNotification())
    expect(result.current.permission).toBe('default')
  })

  it('detects background state via visibilitychange', () => {
    Object.defineProperty(globalThis, 'Notification', {
      value: function Notification() {},
      writable: true,
      configurable: true,
    })
    Object.defineProperty(globalThis.Notification, 'permission', {
      value: 'granted',
      writable: false,
      configurable: true,
    })

    const { result } = renderHook(() => useNotification())
    expect(result.current.isBackground).toBe(false)

    act(() => {
      Object.defineProperty(document, 'hidden', { value: true, writable: true, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(result.current.isBackground).toBe(true)
  })

  it('requestPermission returns false when unsupported', async () => {
    // @ts-expect-error - intentionally removing Notification
    delete globalThis.Notification
    const { result } = renderHook(() => useNotification())
    const granted = await result.current.requestPermission()
    expect(granted).toBe(false)
  })

  it('notify() is no-op when permission not granted', () => {
    Object.defineProperty(globalThis, 'Notification', {
      value: vi.fn(),
      writable: true,
      configurable: true,
    })
    Object.defineProperty(globalThis.Notification, 'permission', {
      value: 'denied',
      writable: false,
      configurable: true,
    })

    const { result } = renderHook(() => useNotification())
    result.current.notify('Test', {})
    expect(globalThis.Notification).not.toHaveBeenCalled()
  })

  it('notify() creates Notification when permission granted and background', () => {
    const mockNotification = vi.fn(() => ({
      onclick: null,
      close: vi.fn(),
    }))
    Object.defineProperty(globalThis, 'Notification', {
      value: mockNotification,
      writable: true,
      configurable: true,
    })
    Object.defineProperty(globalThis.Notification, 'permission', {
      value: 'granted',
      writable: false,
      configurable: true,
    })

    const { result } = renderHook(() => useNotification())

    // Simulate background
    act(() => {
      Object.defineProperty(document, 'hidden', { value: true, writable: true, configurable: true })
      document.dispatchEvent(new Event('visibilitychange'))
    })

    result.current.notify('New message', { body: 'Hello' })
    expect(mockNotification).toHaveBeenCalledWith('New message', expect.objectContaining({ body: 'Hello' }))
  })
})

// ─── useTabManager ───

describe('useTabManager', () => {
  const mockSwitch = vi.fn()
  const mockNew = vi.fn()

  beforeEach(() => {
    vi.clearAllMocks()
    sessionStorage.clear()
  })

  it('initializes with empty tabs from sessionStorage', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    expect(result.current.tabs).toEqual([])
    expect(result.current.activeTabId).toBe('')
  })

  it('opens a new tab', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
    })
    expect(result.current.tabs).toHaveLength(1)
    expect(result.current.tabs[0].chatId).toBe('chat-1')
    expect(result.current.activeTabId).toBe('chat-1')
  })

  it('does not add duplicate tab', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
      result.current.openTab('chat-1', 'Session 1')
    })
    expect(result.current.tabs).toHaveLength(1)
  })

  it('switches active tab', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
      result.current.openTab('chat-2', 'Session 2')
    })
    act(() => {
      result.current.switchTab('chat-1')
    })
    expect(result.current.activeTabId).toBe('chat-1')
    expect(mockSwitch).toHaveBeenCalledWith('chat-1')
  })

  it('closes a tab and switches to adjacent', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
      result.current.openTab('chat-2', 'Session 2')
      result.current.openTab('chat-3', 'Session 3')
    })
    // Active tab is chat-3
    act(() => {
      result.current.closeTab('chat-3')
    })
    // Should switch to adjacent tab (chat-2)
    expect(result.current.tabs).toHaveLength(2)
    expect(mockSwitch).toHaveBeenCalledWith('chat-2')
  })

  it('calls onNewChat when last tab is closed', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
    })
    act(() => {
      result.current.closeTab('chat-1')
    })
    expect(result.current.tabs).toHaveLength(0)
    expect(mockNew).toHaveBeenCalled()
  })

  it('renames a tab', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Old name')
    })
    act(() => {
      result.current.renameTab('chat-1', 'New name')
    })
    expect(result.current.tabs[0].label).toBe('New name')
  })

  it('persists tabs to sessionStorage', () => {
    const { result } = renderHook(() => useTabManager(mockSwitch, mockNew))
    act(() => {
      result.current.openTab('chat-1', 'Session 1')
    })
    const stored = JSON.parse(sessionStorage.getItem('xbot-open-tabs') || '[]')
    expect(stored).toHaveLength(1)
    expect(stored[0].chatId).toBe('chat-1')
  })
})
