import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import React from 'react'

import { useSoundFeedback } from '../hooks/useSoundFeedback'
import { useSnapshot } from '../hooks/useSnapshot'
import { NotificationProvider, useNotificationContext } from '../contexts/NotificationContext'
import type { Message } from '../types'

// ═══════════════════════════════════════════════════════════════════════════════
// 1. useSoundFeedback
// ═══════════════════════════════════════════════════════════════════════════════

describe('useSoundFeedback', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('returns default config (enabled: false, volume: 0.5)', () => {
    const { result } = renderHook(() => useSoundFeedback())
    expect(result.current.config.enabled).toBe(false)
    expect(result.current.config.volume).toBe(0.5)
  })

  it('toggleEnabled toggles enabled state', () => {
    const { result } = renderHook(() => useSoundFeedback())
    expect(result.current.config.enabled).toBe(false)
    act(() => { result.current.toggleEnabled() })
    expect(result.current.config.enabled).toBe(true)
    act(() => { result.current.toggleEnabled() })
    expect(result.current.config.enabled).toBe(false)
  })

  it('updateConfig updates specified fields', () => {
    const { result } = renderHook(() => useSoundFeedback())
    act(() => {
      result.current.updateConfig({ volume: 0.8, sentSound: 'chime' })
    })
    expect(result.current.config.volume).toBe(0.8)
    expect(result.current.config.sentSound).toBe('chime')
    expect(result.current.config.enabled).toBe(false) // unchanged
  })

  it('persists config to localStorage', () => {
    const { result } = renderHook(() => useSoundFeedback())
    act(() => { result.current.toggleEnabled() })
    const stored = JSON.parse(localStorage.getItem('xbot-sound-config') || '{}')
    expect(stored.enabled).toBe(true)
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 2. useSnapshot
// ═══════════════════════════════════════════════════════════════════════════════

describe('useSnapshot', () => {
  const mockMessage: Message = { id: 'msg-1', type: 'user', content: 'Test snapshot content', ts: Date.now() }

  beforeEach(() => {
    const mockCtx = {
      fillRect: vi.fn(),
      strokeRect: vi.fn(),
      fillText: vi.fn(),
      measureText: vi.fn().mockReturnValue({ width: 50 }),
      font: '',
      fillStyle: '',
      strokeStyle: '',
      lineWidth: 0,
    }
    HTMLCanvasElement.prototype.getContext = vi.fn().mockReturnValue(mockCtx)
    HTMLCanvasElement.prototype.toBlob = vi.fn().mockImplementation(
      (callback: (blob: Blob | null) => void) => {
        callback(new Blob(['test'], { type: 'image/png' }))
      }
    )
    // ClipboardItem must be a real constructor since source uses `new ClipboardItem(...)`
    globalThis.ClipboardItem = class MockClipboardItem {
      data: Record<string, Blob>
      constructor(data: Record<string, Blob>) { this.data = data }
    } as unknown as typeof ClipboardItem
    Object.assign(navigator, {
      clipboard: { write: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('starts with snapshotting=false and snapshotError=null', () => {
    const { result } = renderHook(() => useSnapshot())
    expect(result.current.snapshotting).toBe(false)
    expect(result.current.snapshotError).toBeNull()
  })

  it('sets error when canvas is not available', async () => {
    HTMLCanvasElement.prototype.getContext = vi.fn().mockReturnValue(null)
    const { result } = renderHook(() => useSnapshot())
    let success: boolean | undefined
    await act(async () => {
      success = await result.current.takeSnapshot(mockMessage)
    })
    expect(success).toBe(false)
    expect(result.current.snapshotError).toBe('Canvas not supported')
  })

  it('returns true on successful snapshot', async () => {
    const { result } = renderHook(() => useSnapshot())
    let success: boolean | undefined
    await act(async () => {
      success = await result.current.takeSnapshot(mockMessage)
    })
    expect(success).toBe(true)
    expect(result.current.snapshotError).toBeNull()
  })

  it('resets snapshotting to false after completion', async () => {
    const { result } = renderHook(() => useSnapshot())
    expect(result.current.snapshotting).toBe(false)
    await act(async () => {
      await result.current.takeSnapshot(mockMessage)
    })
    expect(result.current.snapshotting).toBe(false)
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 3. NotificationContext
// ═══════════════════════════════════════════════════════════════════════════════

const notifWrapper = ({ children }: { children: React.ReactNode }) => (
  <NotificationProvider>{children}</NotificationProvider>
)

describe('NotificationContext', () => {
  it('provides context when wrapped with NotificationProvider', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    expect(result.current).toBeTruthy()
    expect(result.current.notifications).toEqual([])
  })

  it('adds a notification and updates unreadCount', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      result.current.addNotification({ type: 'message', title: 'Test', body: 'Body' })
    })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.unreadCount).toBe(1)
  })

  it('marks a notification as read', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      result.current.addNotification({ type: 'message', title: 'Test', body: 'Body' })
    })
    const id = result.current.notifications[0].id
    act(() => { result.current.markAsRead(id) })
    expect(result.current.notifications[0].read).toBe(true)
    expect(result.current.unreadCount).toBe(0)
  })

  it('marks all notifications as read', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      result.current.addNotification({ type: 'message', title: 'A', body: 'a' })
      result.current.addNotification({ type: 'reply', title: 'B', body: 'b' })
    })
    expect(result.current.unreadCount).toBe(2)
    act(() => { result.current.markAllRead() })
    expect(result.current.notifications.every(n => n.read)).toBe(true)
    expect(result.current.unreadCount).toBe(0)
  })

  it('clears all notifications', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      result.current.addNotification({ type: 'message', title: 'Test', body: 'Body' })
    })
    expect(result.current.notifications).toHaveLength(1)
    act(() => { result.current.clearNotifications() })
    expect(result.current.notifications).toHaveLength(0)
  })

  it('removes a specific notification', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      result.current.addNotification({ type: 'message', title: 'A', body: 'a' })
      result.current.addNotification({ type: 'reply', title: 'B', body: 'b' })
    })
    // Notifications are prepended, so [0] is 'B' (last added)
    const id = result.current.notifications[0].id
    act(() => { result.current.removeNotification(id) })
    expect(result.current.notifications).toHaveLength(1)
    expect(result.current.notifications[0].title).toBe('A')
  })

  it('truncates to 100 notifications', () => {
    const { result } = renderHook(() => useNotificationContext(), { wrapper: notifWrapper })
    act(() => {
      for (let i = 0; i < 110; i++) {
        result.current.addNotification({ type: 'system', title: `N${i}`, body: `B${i}` })
      }
    })
    expect(result.current.notifications).toHaveLength(100)
  })
})
