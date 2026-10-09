import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import i18n from '@/i18n'
import { renderWithProviders as renderBase } from '@/test-utils'
import { TooltipProvider } from '@/components/ui/tooltip'
import { SETTINGS_SYNCED_EVENT, syncSettingToServer } from '@/lib/userSettings'
import { SettingsSessionShortcuts } from './SettingsSessionShortcuts'
import { commands } from '@/lib/commandRouter'

vi.mock('@/lib/userSettings', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/userSettings')>(),
  syncSettingToServer: vi.fn(),
}))

const storageKey = 'xbot-session-shortcuts'
const renderWithProviders = (children: React.ReactNode) => renderBase(<TooltipProvider>{children}</TooltipProvider>)
const field = (name: string) => screen.getByRole('textbox', { name: i18n.t(name) })
const row = (name: string) => field(name).closest('[data-shortcut-action]') as HTMLElement

beforeEach(() => localStorage.removeItem(storageKey))
afterEach(() => {
  localStorage.removeItem(storageKey)
  vi.clearAllMocks()
  commands.clear()
})

describe('editable session shortcuts', () => {
  it('renders all six editable bindings', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    expect(screen.getAllByRole('textbox')).toHaveLength(6)
    expect(field('common.rename')).toHaveValue('F2')
    expect(field('settings.shortcuts.newSession')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: i18n.t('session.openInTab') })).not.toBeInTheDocument()
  })

  it('preserves the old open-in-tab binding and stores subsequent edits under newSession', () => {
    localStorage.setItem(storageKey, JSON.stringify({ openInTab: 'ctrl+meta+n', star: 'meta+s' }))
    renderWithProviders(<SettingsSessionShortcuts />)
    const input = field('settings.shortcuts.newSession')
    expect((input as HTMLInputElement).value).toMatch(/Ctrl\+/)
    fireEvent.change(input, { target: { value: 'F8' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual({ newSession: 'f8', star: 'meta+s' })
  })

  it('places reset all beside the section heading, outside the shortcut rows', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    const heading = screen.getByRole('heading', { name: i18n.t('settings.shortcuts.title') })
    const reset = screen.getByRole('button', { name: i18n.t('settings.shortcuts.resetAll') })
    const header = heading.closest('header')
    expect(header).toContainElement(reset)
    expect(within(header!).queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('saves typed shortcuts and queues server sync', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    const input = field('common.rename')
    fireEvent.change(input, { target: { value: 'Control+Shift+R' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({ rename: 'ctrl+shift+r' })
    expect(syncSettingToServer).toHaveBeenCalledWith(storageKey, expect.any(String))
    expect(input).toHaveValue('Ctrl+Shift+R')
  })

  it('records a key without closing settings or dispatching a session action', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    const rename = row('common.rename')
    fireEvent.click(within(rename).getByRole('button', { name: i18n.t('settings.shortcuts.record') }))
    const input = field('common.rename')
    expect(input).toHaveFocus()
    fireEvent.keyDown(input, { key: 'F8', code: 'F8' })
    expect(input).toHaveValue('F8')
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({ rename: 'f8' })
  })

  it('rejects duplicate bindings and unsafe plain text keys', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    const input = field('session.star')
    fireEvent.change(input, { target: { value: 'F2' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent(i18n.t('common.rename'))
    expect(localStorage.getItem(storageKey)).toBeNull()
    fireEvent.change(input, { target: { value: 's' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent(i18n.t('settings.shortcuts.invalid'))
    expect(localStorage.getItem(storageKey)).toBeNull()
  })

  it('rejects keys already assigned to registered commands', () => {
    commands.register({ id: 'test.shortcut', title: 'Existing command', keybinding: 'ctrl+shift+k', handler: vi.fn() })
    renderWithProviders(<SettingsSessionShortcuts />)
    const input = field('common.rename')
    fireEvent.change(input, { target: { value: 'Cmd+Shift+K' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent('Existing command')
    expect(localStorage.getItem(storageKey)).toBeNull()
  })

  it('restores draft fields when resetting unchanged defaults', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    fireEvent.change(field('common.rename'), { target: { value: 'Not a key' } })
    fireEvent.click(screen.getByRole('button', { name: i18n.t('settings.shortcuts.resetAll') }))
    expect(field('common.rename')).toHaveValue('F2')
  })

  it('ignores IME and repeat recording and Escape cancels without saving', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    fireEvent.click(within(row('common.rename')).getByRole('button', { name: i18n.t('settings.shortcuts.record') }))
    const input = field('common.rename')
    for (const options of [{ isComposing: true }, { keyCode: 229 }, { repeat: true }]) {
      fireEvent.keyDown(input, { key: 'F8', ...options })
    }
    expect(localStorage.getItem(storageKey)).toBeNull()
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(input).toHaveValue('F2')
    expect(localStorage.getItem(storageKey)).toBeNull()
  })

  it('clears a binding, resets one action, and restores all defaults', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    fireEvent.pointerDown(within(row('common.rename')).getByRole('button', { name: i18n.t('settings.shortcuts.more') }), { button: 0, ctrlKey: false })
    fireEvent.click(screen.getByRole('menuitem', { name: i18n.t('settings.shortcuts.clear') }))
    expect(field('common.rename')).toHaveValue('')
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toMatchObject({ rename: null })
    fireEvent.pointerDown(within(row('common.rename')).getByRole('button', { name: i18n.t('settings.shortcuts.more') }), { button: 0, ctrlKey: false })
    fireEvent.click(screen.getByRole('menuitem', { name: i18n.t('settings.shortcuts.reset') }))
    expect(field('common.rename')).toHaveValue('F2')
    fireEvent.change(field('session.star'), { target: { value: 'F8' } })
    fireEvent.blur(field('session.star'))
    fireEvent.click(screen.getByRole('button', { name: i18n.t('settings.shortcuts.resetAll') }))
    expect(JSON.parse(localStorage.getItem(storageKey)!)).toEqual({})
  })

  it('updates mounted settings after server sync and storage events', () => {
    renderWithProviders(<SettingsSessionShortcuts />)
    act(() => {
      localStorage.setItem(storageKey, JSON.stringify({ rename: 'f8' }))
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    expect(field('common.rename')).toHaveValue('F8')
    act(() => {
      localStorage.setItem(storageKey, JSON.stringify({ rename: 'f9' }))
      window.dispatchEvent(new StorageEvent('storage', { key: storageKey }))
    })
    expect(field('common.rename')).toHaveValue('F9')
  })
})
