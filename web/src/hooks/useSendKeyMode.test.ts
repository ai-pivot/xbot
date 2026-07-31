import { renderHook, act } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useSendKeyMode, isSendKey } from './useSendKeyMode'

describe('useSendKeyMode', () => {
  beforeEach(() => {
    const store = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => store.set(key, value),
      removeItem: (key: string) => store.delete(key),
    })
  })

  it('defaults to ctrl-enter mode', () => {
    const { result } = renderHook(() => useSendKeyMode())
    expect(result.current.mode).toBe('ctrl-enter')
  })

  it('reads persisted mode from localStorage', () => {
    localStorage.setItem('xbot-send-key-mode', 'enter')
    const { result } = renderHook(() => useSendKeyMode())
    expect(result.current.mode).toBe('enter')
  })

  it('setMode persists to localStorage', () => {
    const { result } = renderHook(() => useSendKeyMode())
    act(() => result.current.setMode('enter'))
    expect(localStorage.getItem('xbot-send-key-mode')).toBe('enter')
  })
})

describe('isSendKey', () => {
  const mk = (key: string, mods: Partial<{ shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }> = {}) =>
    ({ key, ...mods }) as React.KeyboardEvent

  it('ctrl-enter mode: Ctrl+Enter sends', () => {
    expect(isSendKey(mk('Enter', { ctrlKey: true }), 'ctrl-enter')).toBe(true)
  })

  it('ctrl-enter mode: plain Enter does not send', () => {
    expect(isSendKey(mk('Enter'), 'ctrl-enter')).toBe(false)
  })

  it('ctrl-enter mode: Shift+Ctrl+Enter does not send', () => {
    expect(isSendKey(mk('Enter', { ctrlKey: true, shiftKey: true }), 'ctrl-enter')).toBe(false)
  })

  it('enter mode: plain Enter sends', () => {
    expect(isSendKey(mk('Enter'), 'enter')).toBe(true)
  })

  it('enter mode: Shift+Enter does not send', () => {
    expect(isSendKey(mk('Enter', { shiftKey: true }), 'enter')).toBe(false)
  })

  it('enter mode: Ctrl+Enter does not send', () => {
    expect(isSendKey(mk('Enter', { ctrlKey: true }), 'enter')).toBe(false)
  })

  it('non-Enter key never sends', () => {
    expect(isSendKey(mk('a', { ctrlKey: true }), 'ctrl-enter')).toBe(false)
    expect(isSendKey(mk('a'), 'enter')).toBe(false)
  })
})
