import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom'
import { renderWithProviders } from '@/test-utils'
import type { SessionInfo } from '@/types/shared'
import { SessionList } from './SessionList'
import { SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import i18n from '@/i18n'
import { UI_MODE_STORAGE_KEY } from '@/hooks/useUIMode'

vi.mock('@/components/ui/scroll-area', () => ({
  ScrollArea: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

const current: SessionInfo = {
  chatID: 'current', channel: 'web', label: 'Current session', preview: '',
  lastActive: '2026-10-09T00:00:00Z', status: 'idle', isCurrent: true,
}
const other: SessionInfo = { ...current, chatID: 'other', channel: 'cli', label: 'Other session', isCurrent: false }

function setup(overrides: Partial<React.ComponentProps<typeof SessionList>> = {}) {
  const props = {
    sessions: [current, other], groups: [{ key: 'today', sessions: [current, other] }],
    sortedSessions: [current, other], category: 'time' as const,
    collapsedGroups: new Set<string>(), onToggleGroup: vi.fn(), starredIds: [], unreadIds: [],
    activeSession: { channel: 'web', chatID: 'current' }, search: '', subAgents: [],
    onSelect: vi.fn(), onToggleStar: vi.fn(), onRename: vi.fn().mockResolvedValue(true),
    onFork: vi.fn().mockResolvedValue('fork'), onDelete: vi.fn().mockResolvedValue(true), onExport: vi.fn(),
    ...overrides,
  }
  return { props, ...renderWithProviders(<SessionList {...props} />) }
}

function shortcut(target: Window | Document | Element, key: string, options: KeyboardEventInit = {}) {
  const event = new KeyboardEvent('keydown', {
    key, ctrlKey: key !== 'F2', altKey: key !== 'F2', bubbles: true, cancelable: true, ...options,
  })
  fireEvent(target, event)
  return event
}

beforeEach(() => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue('MacIntel')
  localStorage.removeItem('xbot-session-shortcuts')
  localStorage.removeItem('xbot-session-shortcuts-windows')
  localStorage.removeItem(UI_MODE_STORAGE_KEY)
})
afterEach(() => {
  localStorage.removeItem('xbot-session-shortcuts')
  localStorage.removeItem('xbot-session-shortcuts-windows')
  localStorage.removeItem(UI_MODE_STORAGE_KEY)
  vi.restoreAllMocks()
})

describe('session action shortcuts', () => {
  it.each(['current', 'row', 'menu'])('Windows browser-tab binding opens the %s target and preserves Control+N', async scope => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    let target: Window | Element = window
    if (scope === 'row') target = row
    if (scope === 'menu') {
      fireEvent.contextMenu(row)
      const menu = await screen.findByRole('menu')
      target = menu
      const item = within(menu).getByRole('menuitem', { name: i18n.t('session.openInTab') })
      expect(item.querySelector('[data-slot="context-menu-shortcut"]')).toHaveTextContent('Ctrl+Shift+2')
    }
    expect(shortcut(target, 'n', { altKey: false }).defaultPrevented).toBe(false)
    expect(open).not.toHaveBeenCalled()
    expect(shortcut(target, '@', { code: 'Digit2', altKey: false, shiftKey: true }).defaultPrevented).toBe(true)
    expect(open).toHaveBeenCalledOnce()
    expect(new URL(open.mock.calls[0][0] as string).searchParams.get('session')).toBe(scope === 'current' ? 'web:current' : 'cli:other')
    expect(open.mock.calls[0].slice(1)).toEqual(['_blank', 'noopener'])
    expect(props.onSelect).not.toHaveBeenCalled()
  })

  it.each(['current', 'row', 'menu'])('Windows star binding acts on the %s target', async scope => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    let target: Window | Element = window
    if (scope === 'row') target = row
    if (scope === 'menu') {
      fireEvent.contextMenu(row)
      target = await screen.findByRole('menu')
    }
    shortcut(target, 's', { code: 'KeyS' })
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith(scope === 'current' ? 'web:current' : 'cli:other')
  })

  it('Windows delete still requires confirmation and ignores shortcuts on mobile', async () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Win32')
    const { props } = setup()
    shortcut(window, 'Backspace')
    const dialog = await screen.findByRole('alertdialog')
    expect(props.onDelete).not.toHaveBeenCalled()
    fireEvent.keyDown(dialog, { key: 'Enter' })
    await waitFor(() => expect(props.onDelete).toHaveBeenCalledExactlyOnceWith('current', 'web'))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new StorageEvent('storage', { key: UI_MODE_STORAGE_KEY }))
    })
    expect(shortcut(window, 'Backspace').defaultPrevented).toBe(false)
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it.each(['s', 'F2', 'f', 'e', 'Backspace'])('ignores %s on mobile in the current session, row, and menu', async (key) => {
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    expect(shortcut(window, key).defaultPrevented).toBe(false)
    expect(shortcut(row, key).defaultPrevented).toBe(false)
    fireEvent.contextMenu(row)
    const menu = await screen.findByRole('menu')
    expect(menu.querySelectorAll('[data-slot="context-menu-shortcut"]')).toHaveLength(0)
    expect(shortcut(menu, key).defaultPrevented).toBe(false)
    expect(props.onToggleStar).not.toHaveBeenCalled()
    expect(props.onFork).not.toHaveBeenCalled()
    expect(props.onExport).not.toHaveBeenCalled()
    expect(props.onDelete).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(menu).toBeInTheDocument()
  })

  it('keeps ordinary mobile selection, menu actions, and delete confirmation working', async () => {
    localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    fireEvent.keyDown(row, { key: 'Enter' })
    expect(props.onSelect).toHaveBeenCalledExactlyOnceWith('other', 'cli')
    fireEvent.click(within(row as HTMLElement).getByRole('button', { name: i18n.t('session.star') }))
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith('cli:other')
    fireEvent.contextMenu(row)
    fireEvent.click(await screen.findByRole('menuitem', { name: i18n.t('common.delete') }))
    expect(props.onDelete).not.toHaveBeenCalled()
    fireEvent.keyDown(await screen.findByRole('alertdialog'), { key: 'Enter' })
    await waitFor(() => expect(props.onDelete).toHaveBeenCalledExactlyOnceWith('other', 'cli'))
  })

  it('removes desktop listeners on switching to mobile and restores them on switching back', () => {
    const saved = JSON.stringify({ star: 'f8' })
    localStorage.setItem('xbot-session-shortcuts', saved)
    const { props } = setup()
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    const row = screen.getByText('Other session').closest('[role="button"]')!
    expect(shortcut(window, 'F8', { ctrlKey: false, altKey: false }).defaultPrevented).toBe(false)
    expect(shortcut(row, 'F8', { ctrlKey: false, altKey: false }).defaultPrevented).toBe(false)
    expect(props.onToggleStar).not.toHaveBeenCalled()
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'desktop')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    fireEvent.keyDown(window, { key: 'F8' })
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith('web:current')
    expect(localStorage.getItem('xbot-session-shortcuts')).toBe(saved)
  })

  it('uses a saved custom shortcut in the current session, row, and menu', async () => {
    localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ star: 'f8' }))
    const { props } = setup()
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'F8' })
    expect(props.onToggleStar).toHaveBeenLastCalledWith('web:current')
    const row = screen.getByText('Other session').closest('[role="button"]')!
    fireEvent.keyDown(row, { key: 'F8' })
    expect(props.onToggleStar).toHaveBeenLastCalledWith('cli:other')
    fireEvent.contextMenu(row)
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByText('F8')).toBeInTheDocument()
    fireEvent.keyDown(menu, { key: 'F8' })
    expect(props.onToggleStar).toHaveBeenCalledTimes(3)
  })

  it('does not trigger a disabled binding or advertise it in the menu', async () => {
    localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ star: null }))
    const { props } = setup()
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const menu = await screen.findByRole('menu')
    expect(menu.querySelectorAll('[data-slot="context-menu-shortcut"]')).toHaveLength(5)
  })

  it('updates existing listeners and menu labels when preferences change', async () => {
    const { props } = setup()
    act(() => {
      localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ star: 'f8' }))
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
    fireEvent.keyDown(window, { key: 'F8' })
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith('web:current')
    fireEvent.contextMenu(screen.getByText('Other session'))
    const menu = await screen.findByRole('menu')
    expect(within(menu).getByText('F8')).toBeInTheDocument()
  })

  it.each([{ ctrlKey: true }, { ctrlKey: false, metaKey: true }])('no longer opens a browser tab from the replaced shortcut (%o)', (modifier) => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    setup()
    expect(shortcut(window, 'o', modifier).defaultPrevented).toBe(false)
    expect(shortcut(window, 'n', modifier).defaultPrevented).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })

  it('keeps opening a session in a browser tab from its menu and shows Control+N', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    setup()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const item = await screen.findByRole('menuitem', { name: i18n.t('session.openInTab') })
    expect(item.querySelector('[data-slot="context-menu-shortcut"]')).toHaveTextContent(/N/)
    fireEvent.click(item)
    const url = new URL(open.mock.calls[0][0] as string)
    expect(url.searchParams.get('session')).toBe('cli:other')
    expect(open.mock.calls[0].slice(1)).toEqual(['_blank', 'noopener'])
  })

  it.each(['current', 'row', 'menu'])('Control+N opens the %s target exactly once without changing selection', async (scope) => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    let target: Window | Element = window
    if (scope === 'row') target = row
    if (scope === 'menu') {
      fireEvent.contextMenu(row)
      target = await screen.findByRole('menu')
    }
    expect(shortcut(target, 'n', { altKey: false }).defaultPrevented).toBe(true)
    expect(open).toHaveBeenCalledOnce()
    expect(new URL(open.mock.calls[0][0] as string).searchParams.get('session')).toBe(scope === 'current' ? 'web:current' : 'cli:other')
    expect(open.mock.calls[0].slice(1)).toEqual(['_blank', 'noopener'])
    expect(props.onSelect).not.toHaveBeenCalled()
    if (scope === 'menu') await waitFor(() => expect(screen.queryByRole('menu')).not.toBeInTheDocument())
  })

  it('updates and disables the browser-tab binding without affecting session creation', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ openInBrowserTab: 'f9', newSession: 'f8' }))
    setup()
    expect(shortcut(window, 'n', { altKey: false }).defaultPrevented).toBe(false)
    fireEvent.keyDown(window, { key: 'F9' })
    expect(open).toHaveBeenCalledOnce()
    act(() => {
      localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ openInBrowserTab: null, newSession: 'f8' }))
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    fireEvent.keyDown(window, { key: 'F9' })
    expect(open).toHaveBeenCalledOnce()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const item = await screen.findByRole('menuitem', { name: i18n.t('session.openInTab') })
    expect(item.querySelector('[data-slot="context-menu-shortcut"]')).toBeNull()
  })

  it.each([{ isComposing: true }, { keyCode: 229 }, { repeat: true }])('ignores unsafe Control+N events (%o)', options => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    setup()
    expect(shortcut(window, 'n', { altKey: false, ...options }).defaultPrevented).toBe(false)
    const handled = new KeyboardEvent('keydown', { key: 'n', ctrlKey: true, cancelable: true })
    handled.preventDefault()
    fireEvent(window, handled)
    expect(open).not.toHaveBeenCalled()
  })

  it('restores the browser-tab shortcut after returning from mobile without changing preferences', () => {
    const saved = JSON.stringify({ openInBrowserTab: 'f9', newSession: 'f8' })
    localStorage.setItem('xbot-session-shortcuts', saved)
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    setup()
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    fireEvent.keyDown(window, { key: 'F9' })
    expect(open).not.toHaveBeenCalled()
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'desktop')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    fireEvent.keyDown(window, { key: 'F9' })
    expect(open).toHaveBeenCalledOnce()
    expect(localStorage.getItem('xbot-session-shortcuts')).toBe(saved)
  })

  it.each([{ synthetic: true }, { type: 'agent' as const }])('allows opening a read-only current session but not mutating it (%o)', flags => {
    const target = { ...current, ...flags }
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const { props } = setup({ sessions: [target], groups: [{ key: 'today', sessions: [target] }], sortedSessions: [target] })
    expect(shortcut(window, 'n', { altKey: false }).defaultPrevented).toBe(true)
    expect(open).toHaveBeenCalledOnce()
    for (const key of ['s', 'F2', 'f', 'e', 'Backspace']) shortcut(window, key)
    expect(props.onToggleStar).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it.each(['current', 'row', 'menu', 'input', 'overlay', 'hidden', 'multiselect'])('does not open a browser tab from excluded %s scopes', async (scope) => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    if (['current', 'row', 'menu'].includes(scope)) localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
    const { container } = setup({ multiSelectMode: scope === 'multiselect' })
    let target: Window | Element = window
    if (scope === 'row') target = screen.getByText('Other session').closest('[role="button"]')!
    if (scope === 'menu') {
      fireEvent.contextMenu(screen.getByText('Other session'))
      target = await screen.findByRole('menu')
    }
    if (scope === 'input') {
      target = document.createElement('input')
      container.appendChild(target)
    }
    if (scope === 'overlay') {
      const overlay = document.createElement('div')
      overlay.setAttribute('role', 'dialog')
      container.appendChild(overlay)
    }
    if (scope === 'hidden') container.style.display = 'none'
    expect(shortcut(target, 'n', { altKey: false }).defaultPrevented).toBe(false)
    expect(open).not.toHaveBeenCalled()
  })

  it('stars the focused row rather than the active session, exactly once', () => {
    const { props } = setup()
    const row = screen.getByText('Other session').closest('[role="button"]')!
    act(() => (row as HTMLElement).focus())
    shortcut(row, 's')
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith('cli:other')
    expect(props.onSelect).not.toHaveBeenCalled()
  })

  it('opens rename with F2 and saves the targeted session with Enter', async () => {
    const { props } = setup()
    shortcut(screen.getByText('Other session').closest('[role="button"]')!, 'F2')
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    expect(input).toHaveValue('Other session')
    fireEvent.change(input, { target: { value: 'Renamed' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(props.onRename).toHaveBeenCalledExactlyOnceWith('other', 'cli', 'Renamed'))
  })

  it('forks only after confirming the new name', async () => {
    const { props } = setup()
    shortcut(window, 'f')
    const dialog = await screen.findByRole('dialog')
    expect(props.onFork).not.toHaveBeenCalled()
    const input = within(dialog).getByRole('textbox')
    fireEvent.change(input, { target: { value: 'My fork' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(props.onFork).toHaveBeenCalledExactlyOnceWith('current', 'web', 'My fork'))
  })

  it.each(['native', 'openai', 'codex'])('exports only after choosing %s', async (format) => {
    const { props } = setup()
    shortcut(window, 'e')
    const dialog = await screen.findByRole('dialog')
    expect(props.onExport).not.toHaveBeenCalled()
    const select = within(dialog).getByRole('combobox')
    fireEvent.change(select, { target: { value: format } })
    fireEvent.click(within(dialog).getByRole('button', { name: /Export|导出/ }))
    expect(props.onExport).toHaveBeenCalledExactlyOnceWith(current, format)
  })

  it('opens delete confirmation, keeps Escape safe, and confirms with Enter', async () => {
    const { props } = setup()
    shortcut(window, 'Backspace')
    const dialog = await screen.findByRole('alertdialog')
    expect(props.onDelete).not.toHaveBeenCalled()
    fireEvent.keyDown(dialog, { key: 'Escape' })
    await waitFor(() => expect(dialog).not.toBeInTheDocument())
    expect(props.onDelete).not.toHaveBeenCalled()
    shortcut(window, 'Backspace')
    fireEvent.keyDown(await screen.findByRole('alertdialog'), { key: 'Enter' })
    await waitFor(() => expect(props.onDelete).toHaveBeenCalledExactlyOnceWith('current', 'web'))
  })

  it('focuses Cancel when another deletion opens during the previous closing animation', async () => {
    const style = document.createElement('style')
    style.textContent = '[role="alertdialog"][data-state="closed"] { animation-name: close-dialog; }'
    document.head.appendChild(style)
    try {
      const { props } = setup()
      shortcut(window, 'Backspace')
      const dialog = await screen.findByRole('alertdialog')
      const cancel = within(dialog).getByRole('button', { name: /Cancel|取消/ })
      expect(cancel).toHaveFocus()
      fireEvent.keyDown(cancel, { key: 'Enter' })
      await waitFor(() => expect(dialog).toHaveAttribute('data-state', 'closed'))
      const otherRow = screen.getByText('Other session').closest('[role="button"]')!
      act(() => (otherRow as HTMLElement).focus())
      shortcut(otherRow, 'Backspace')
      await waitFor(() => expect(dialog).toHaveAttribute('data-state', 'open'))
      expect(cancel).toHaveFocus()
      fireEvent.keyDown(document.activeElement!, { key: 'Enter' })
      await waitFor(() => expect(props.onDelete).toHaveBeenLastCalledWith('other', 'cli'))
    } finally {
      style.remove()
    }
  })

  it.each(['F2', 'f', 'e'])('restores the field focus when %s reopens during a closing animation', async (key) => {
    const style = document.createElement('style')
    style.textContent = '[role="dialog"][data-state="closed"] { animation-name: close-dialog; }'
    document.head.appendChild(style)
    try {
      setup()
      shortcut(window, key)
      const dialog = await screen.findByRole('dialog')
      fireEvent.keyDown(dialog, { key:'Escape' })
      await waitFor(() => expect(dialog).toHaveAttribute('data-state','closed'))
      const row = screen.getByText('Other session').closest('[role="button"]')!
      act(() => (row as HTMLElement).focus())
      shortcut(row,key)
      await waitFor(() => expect(dialog).toHaveAttribute('data-state','open'))
      expect(within(dialog).getByRole(key === 'e' ? 'combobox' : 'textbox')).toHaveFocus()
    } finally {
      style.remove()
    }
  })

  it('shows shortcuts on the six targeted menu actions and acts on the menu target', async () => {
    const { props } = setup()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const menu = await screen.findByRole('menu')
    expect(menu.querySelectorAll('[data-slot="context-menu-shortcut"]')).toHaveLength(6)
    shortcut(menu, 's')
    expect(props.onToggleStar).toHaveBeenCalledExactlyOnceWith('cli:other')
    await waitFor(() => expect(menu).not.toBeInTheDocument())
  })

  it('opens the export submenu from its menu shortcut', async () => {
    const { props } = setup()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const menu = await screen.findByRole('menu')
    shortcut(menu, 'e')
    fireEvent.click(await screen.findByRole('menuitem', { name: /OpenAI/ }))
    expect(props.onExport).toHaveBeenCalledExactlyOnceWith(other, 'openai')
  })

  it.each(['F2', 'f', 'Backspace'])('opens a targeted dialog from the menu with %s', async (key) => {
    setup()
    fireEvent.contextMenu(screen.getByText('Other session'))
    const menu = await screen.findByRole('menu')
    shortcut(menu, key)
    const dialog = await screen.findByRole(key === 'Backspace' ? 'alertdialog' : 'dialog')
    if (key === 'Backspace') expect(dialog).toHaveTextContent('Other session')
    else expect(within(dialog).getByRole('textbox')).toHaveValue(key === 'F2' ? 'Other session' : 'Other session fork')
    await waitFor(() => expect(menu).not.toBeInTheDocument())
  })

  it('ignores shortcuts in a hidden session panel', () => {
    const { props, container } = setup()
    container.style.display = 'none'
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
  })

  it.each(['F2', 'f'])('ignores IME and held Enter and avoids duplicate submissions for %s', async (key) => {
    let finish!: (result: boolean | string) => void
    const pending = vi.fn(() => new Promise<boolean | string>((resolve) => { finish = resolve }))
    setup(key === 'F2'
      ? { onRename: () => pending().then((result) => result === true) }
      : { onFork: () => pending().then((result) => typeof result === 'string' ? result : null) })
    shortcut(window, key)
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByRole('textbox')
    for (const options of [{ isComposing: true }, { keyCode: 229 }, { repeat: true }]) {
      fireEvent.keyDown(input, { key: 'Enter', ...options })
    }
    expect(pending).not.toHaveBeenCalled()
    fireEvent.keyDown(input, { key: 'Enter' })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(pending).toHaveBeenCalledOnce()
    await act(async () => finish(key === 'F2' ? true : 'fork'))
    await waitFor(() => expect(dialog).not.toBeInTheDocument())
  })

  it.each(['input', 'textarea', 'select', 'editable'])('does not steal shortcuts from %s', (kind) => {
    const { props, container } = setup()
    const editor = document.createElement(kind === 'editable' ? 'div' : kind)
    if (kind === 'editable') {
      editor.contentEditable = 'true'
      editor.setAttribute('contenteditable', 'true')
      editor.innerHTML = '<p>Draft</p>'
    }
    container.appendChild(editor)
    for (const key of ['o', 's', 'F2', 'f', 'e', 'Backspace']) {
      expect(shortcut(editor.firstElementChild ?? editor, key).defaultPrevented).toBe(false)
    }
    expect(props.onToggleStar).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it.each([{ isComposing: true }, { keyCode: 229 }, { repeat: true }])('ignores unsafe key events (%o)', (options) => {
    const { props } = setup()
    shortcut(window, 's', options)
    shortcut(screen.getByText('Other session').closest('[role="button"]')!, 's', options)
    expect(props.onToggleStar).not.toHaveBeenCalled()
  })

  it('ignores already handled events, unrelated menus, and dialogs', () => {
    const { props, container } = setup()
    const handled = new KeyboardEvent('keydown', { key: 's', ctrlKey: true, altKey: true, cancelable: true })
    handled.preventDefault()
    fireEvent(window, handled)
    for (const role of ['menu', 'dialog', 'alertdialog']) {
      const overlay = document.createElement('div')
      overlay.setAttribute('role', role)
      container.appendChild(overlay)
      shortcut(window, 's')
      overlay.remove()
    }
    expect(props.onToggleStar).not.toHaveBeenCalled()
  })

  it('does not apply single-session shortcuts in multi-select mode or without a target', () => {
    const { props, unmount } = setup({ multiSelectMode: true })
    shortcut(screen.getByText('Other session').closest('[role="button"]')!, 's')
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
    unmount()
    const noTarget = setup({ activeSession: null })
    shortcut(window, 's')
    expect(noTarget.props.onToggleStar).not.toHaveBeenCalled()
  })

  it('only offers open-in-tab for subagents, not destructive shortcuts', async () => {
    const child: SessionInfo = { ...other, chatID: 'child', channel: 'agent', label: 'Child', type: 'agent' }
    const parent = { ...current, children: [child] }
    const { props } = setup({ sessions: [parent], groups: [{ key: 'today', sessions: [parent] }], sortedSessions: [parent] })
    const row = screen.getByText('Child').closest('[role="button"]')!
    for (const key of ['s', 'F2', 'f', 'e', 'Backspace']) shortcut(row, key)
    expect(props.onToggleStar).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    expect(shortcut(row, 'o').defaultPrevented).toBe(false)
    expect(open).not.toHaveBeenCalled()
    expect(shortcut(row, 'n', { altKey: false }).defaultPrevented).toBe(true)
    expect(new URL(open.mock.calls[0][0] as string).searchParams.get('session')).toBe('agent:child')
    fireEvent.contextMenu(row)
    const menu = await screen.findByRole('menu')
    expect(menu.querySelectorAll('[data-slot="context-menu-shortcut"]')).toHaveLength(1)
    shortcut(menu, 'n', { altKey: false })
    expect(open).toHaveBeenCalledTimes(2)
    fireEvent.contextMenu(row)
    fireEvent.click(await screen.findByRole('menuitem', { name: i18n.t('session.openInTab') }))
    expect(open).toHaveBeenCalledTimes(3)
    expect(new URL(open.mock.calls[2][0] as string).searchParams.get('session')).toBe('agent:child')
  })

  it('does not run optional operations when unavailable and removes the listener on unmount', () => {
    const { props, unmount } = setup({ onFork: undefined, onExport: undefined })
    expect(shortcut(window, 'f').defaultPrevented).toBe(false)
    expect(shortcut(window, 'e').defaultPrevented).toBe(false)
    unmount()
    shortcut(window, 's')
    expect(props.onToggleStar).not.toHaveBeenCalled()
  })
})
