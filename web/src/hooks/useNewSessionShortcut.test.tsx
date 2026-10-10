import { act, fireEvent, render, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNewSessionShortcut } from './useNewSessionShortcut'
import { SESSION_SHORTCUT_STORAGE_KEY, useSessionShortcuts } from './useSessionShortcuts'
import { SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'

vi.mock('@/lib/userSettings', async importOriginal => ({
  ...await importOriginal<typeof import('@/lib/userSettings')>(),
  syncSettingToServer: vi.fn(),
}))

const save = (overrides: Record<string, string | null>) =>
  localStorage.setItem(SESSION_SHORTCUT_STORAGE_KEY, JSON.stringify(overrides))
const press = (target: Window | Element = window, options: KeyboardEventInit = {}) => {
  const event = new KeyboardEvent('keydown', { key: 'F8', bubbles: true, cancelable: true, ...options })
  fireEvent(target, event)
  return event
}

beforeEach(() => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue('MacIntel')
  save({ newSession: 'f8' })
})
afterEach(() => {
  localStorage.removeItem(SESSION_SHORTCUT_STORAGE_KEY)
  localStorage.removeItem('xbot-session-shortcuts-windows')
  vi.restoreAllMocks()
})

describe('global new-session shortcut', () => {
  it('uses the Windows creation default without consuming the browser-tab or native browser keys', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    expect(press(window, { key: 'n', ctrlKey: true }).defaultPrevented).toBe(false)
    expect(press(window, { key: '@', code: 'Digit2', ctrlKey: true, shiftKey: true }).defaultPrevented).toBe(false)
    await act(async () => {
      expect(press(window, { key: 'n', code: 'KeyN', ctrlKey: true, altKey: true }).defaultPrevented).toBe(true)
    })
    expect(create).toHaveBeenCalledOnce()
  })

  it('creates without a session target and does not consume other actions', () => {
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    expect(press().defaultPrevented).toBe(true)
    expect(create).toHaveBeenCalledOnce()
    expect(press(window, { key: 'F2' }).defaultPrevented).toBe(false)
    expect(press(window, { key: 'n', ctrlKey: true }).defaultPrevented).toBe(false)
    expect(create).toHaveBeenCalledOnce()
  })

  it('runs before the focused row can select itself for a modified Enter', () => {
    save({ newSession: 'ctrl+enter' })
    const create = vi.fn()
    const select = vi.fn()
    function Row() {
      useNewSessionShortcut(create)
      return <div data-session-row="web:other" role="button" tabIndex={0} onKeyDown={select}>Other</div>
    }
    const { getByRole } = render(<Row />)
    press(getByRole('button'), { key: 'Enter', ctrlKey: true })
    expect(create).toHaveBeenCalledOnce()
    expect(select).not.toHaveBeenCalled()
  })

  it.each(['input', 'textarea', 'select', 'editable', 'textbox', 'monaco-editor', 'xterm'])('ignores the %s editor', kind => {
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    const editor = document.createElement(['input', 'textarea', 'select'].includes(kind) ? kind : 'div')
    if (kind === 'editable') editor.setAttribute('contenteditable', 'true')
    if (kind === 'textbox') editor.setAttribute('role', 'textbox')
    editor.className = kind
    document.body.appendChild(editor)
    try {
      expect(press(editor).defaultPrevented).toBe(false)
      expect(create).not.toHaveBeenCalled()
    } finally { editor.remove() }
  })

  it.each(['menu', 'dialog', 'alertdialog'])('ignores an open %s but not its closing animation', async role => {
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    const overlay = document.createElement('div')
    overlay.setAttribute('role', role)
    document.body.appendChild(overlay)
    try {
      expect(press().defaultPrevented).toBe(false)
      expect(create).not.toHaveBeenCalled()
      overlay.dataset.state = 'closed'
      await act(async () => { press() })
      expect(create).toHaveBeenCalledOnce()
    } finally { overlay.remove() }
  })

  it.each([{ isComposing: true }, { keyCode: 229 }, { repeat: true }])('ignores IME and repeat (%o)', options => {
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    expect(press(window, options).defaultPrevented).toBe(false)
    expect(create).not.toHaveBeenCalled()
  })

  it('ignores handled events and removes its listener when disabled or unmounted', () => {
    const create = vi.fn()
    const { rerender, unmount } = renderHook(({ enabled }) => useNewSessionShortcut(create, enabled), { initialProps: { enabled: true } })
    const event = new KeyboardEvent('keydown', { key: 'F8', cancelable: true })
    event.preventDefault()
    fireEvent(window, event)
    rerender({ enabled: false })
    press()
    rerender({ enabled: true })
    unmount()
    press()
    expect(create).not.toHaveBeenCalled()
  })

  it('allows only one pending creation and releases it when creation fails', async () => {
    let finish!: (id: null) => void
    const create = vi.fn(() => new Promise<null>(resolve => { finish = resolve }))
    renderHook(() => useNewSessionShortcut(create))
    press()
    press()
    expect(create).toHaveBeenCalledOnce()
    await act(async () => { finish(null) })
    press()
    expect(create).toHaveBeenCalledTimes(2)
    await act(async () => { finish(null) })
  })

  it('keeps custom Control and Command distinct', async () => {
    save({ newSession: 'ctrl+shift+n' })
    const create = vi.fn()
    renderHook(() => useNewSessionShortcut(create))
    expect(press(window, { key: 'n', metaKey: true, shiftKey: true }).defaultPrevented).toBe(false)
    await act(async () => { press(window, { key: 'n', ctrlKey: true, shiftKey: true }) })
    expect(create).toHaveBeenCalledOnce()
  })

  it('updates live after local edits, server hydration, and cross-tab storage events', async () => {
    const create = vi.fn()
    const { result } = renderHook(() => {
      useNewSessionShortcut(create)
      return useSessionShortcuts()
    })
    act(() => result.current.setBinding('newSession', 'f9'))
    expect(press().defaultPrevented).toBe(false)
    await act(async () => { press(window, { key: 'F9' }) })
    act(() => {
      save({ newSession: 'f10' })
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    expect(press(window, { key: 'F9' }).defaultPrevented).toBe(false)
    await act(async () => { press(window, { key: 'F10' }) })
    act(() => {
      save({ newSession: 'f11' })
      window.dispatchEvent(new StorageEvent('storage', { key: SESSION_SHORTCUT_STORAGE_KEY }))
    })
    await act(async () => { press(window, { key: 'F11' }) })
    expect(create).toHaveBeenCalledTimes(3)
  })

  it.each([
    [{ openInTab: 'f8' }, 'f8'],
    [{ openInTab: null }, null],
    [{ openInTab: 'f8', newSession: 'f9' }, 'f9'],
    [{ openInTab: 'f8', newSession: null }, null],
  ])('reads legacy overrides without rewriting them and prefers the new key (%o)', (overrides, binding) => {
    save(overrides)
    const raw = localStorage.getItem(SESSION_SHORTCUT_STORAGE_KEY)
    const { result } = renderHook(() => useSessionShortcuts())
    expect(result.current.bindings.newSession).toBe(binding)
    expect(localStorage.getItem(SESSION_SHORTCUT_STORAGE_KEY)).toBe(raw)
  })
})
