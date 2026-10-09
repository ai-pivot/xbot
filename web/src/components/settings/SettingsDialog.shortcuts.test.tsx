import { useState } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import { TooltipProvider } from '@/components/ui/tooltip'
import { SettingsDialog, type SettingsCategory } from './SettingsDialog'
import { SETTINGS_SYNCED_EVENT } from '@/lib/userSettings'
import { UI_MODE_STORAGE_KEY } from '@/hooks/useUIMode'

vi.mock('@/hooks/useAuth', () => ({ useAuth: () => ({ user: { username: 'test' }, logout: vi.fn() }) }))
vi.mock('./SettingsAppearance', () => ({ SettingsAppearance: () => null }))
vi.mock('@/lib/userSettings', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/userSettings')>(), syncSettingToServer: vi.fn(),
}))

function Harness({ initialSection }: { initialSection?: SettingsCategory }) {
  const [open, setOpen] = useState(true)
  return <SettingsDialog open={open} onOpenChange={setOpen} initialSection={initialSection} />
}

function setup(initialSection: SettingsCategory = 'shortcuts') {
  return renderWithProviders(<MemoryRouter><TooltipProvider><Harness initialSection={initialSection} /></TooltipProvider></MemoryRouter>)
}

beforeEach(() => localStorage.removeItem(UI_MODE_STORAGE_KEY))
afterEach(() => {
  localStorage.removeItem('xbot-session-shortcuts')
  localStorage.removeItem(UI_MODE_STORAGE_KEY)
  vi.restoreAllMocks()
})

describe('shortcut recording in the settings dialog', () => {
  it('offers a dedicated shortcuts category and keeps interaction settings separate', async () => {
    setup('interaction')
    const dialog = await screen.findByRole('dialog')
    const nav = dialog.querySelector('nav')!
    expect(within(dialog).getByRole('heading', { name: i18n.t('settings.codeWordWrap') })).toBeInTheDocument()
    expect(within(dialog).getByRole('heading', { name: i18n.t('settings.sendKeyMode') })).toBeInTheDocument()
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    const shortcuts = within(nav).getByRole('button', { name: i18n.t('settings.nav.shortcuts') })
    fireEvent.click(shortcuts)
    expect(shortcuts).toHaveAttribute('aria-current', 'true')
    expect(within(dialog).getAllByRole('textbox')).toHaveLength(6)
    expect(within(dialog).queryByRole('heading', { name: i18n.t('settings.codeWordWrap') })).not.toBeInTheDocument()
    fireEvent.click(within(nav).getByRole('button', { name: i18n.t('settings.nav.interaction') }))
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('opens the shortcuts category directly and preserves saved bindings', async () => {
    localStorage.setItem('xbot-session-shortcuts', JSON.stringify({ newSession: 'f8', star: 'meta+s' }))
    setup()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('textbox', { name: i18n.t('settings.shortcuts.newSession') })).toHaveValue('F8')
    expect(within(dialog.querySelector('nav')!).getByRole('button', { name: i18n.t('settings.nav.shortcuts') })).toHaveAttribute('aria-current', 'true')
    expect(JSON.parse(localStorage.getItem('xbot-session-shortcuts')!)).toEqual({ newSession: 'f8', star: 'meta+s' })
  })

  it.each(['auto', 'mobile'])('hides shortcut settings and direct navigation in %s mobile mode', async (mode) => {
    const matchMedia = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation(query => ({
      ...matchMedia(query), matches: query === '(max-width: 767px)',
    }))
    localStorage.setItem(UI_MODE_STORAGE_KEY, mode)
    const saved = JSON.stringify({ newSession: 'f8', star: 'meta+s' })
    localStorage.setItem('xbot-session-shortcuts', saved)
    setup()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog.querySelector('nav')!).queryByRole('button', { name: i18n.t('settings.nav.shortcuts') })).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('heading', { name: i18n.t('settings.codeWordWrap') })).toBeInTheDocument()
    expect(localStorage.getItem('xbot-session-shortcuts')).toBe(saved)
  })

  it('hides the active shortcut category on mode changes without losing desktop bindings', async () => {
    const saved = JSON.stringify({ newSession: 'f8' })
    localStorage.setItem('xbot-session-shortcuts', saved)
    setup()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getAllByRole('textbox')).toHaveLength(6)
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'mobile')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    expect(within(dialog.querySelector('nav')!).queryByRole('button', { name: i18n.t('settings.nav.shortcuts') })).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    expect(within(dialog).getByRole('heading', { name: i18n.t('settings.codeWordWrap') })).toBeInTheDocument()
    act(() => {
      localStorage.setItem(UI_MODE_STORAGE_KEY, 'desktop')
      window.dispatchEvent(new CustomEvent(SETTINGS_SYNCED_EVENT))
    })
    expect(within(dialog).getByRole('textbox', { name: i18n.t('settings.shortcuts.newSession') })).toHaveValue('F8')
    expect(localStorage.getItem('xbot-session-shortcuts')).toBe(saved)
  })

  it('Escape cancels recording without dismissing the settings dialog', async () => {
    setup()
    const input = await screen.findByRole('textbox', { name: i18n.t('common.rename') })
    const record = within(input.closest('[data-shortcut-action]') as HTMLElement).getByRole('button', { name: i18n.t('settings.shortcuts.record') })
    fireEvent.click(record)
    expect(record).toHaveAttribute('aria-pressed', 'true')
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.getByRole('dialog')).toHaveAttribute('data-state', 'open')
    expect(record).toHaveAttribute('aria-pressed', 'false')
    expect(input).toHaveValue('F2')
  })

  it('Escape cancels a typed draft before the next Escape closes settings', async () => {
    setup()
    const input = await screen.findByRole('textbox', { name: i18n.t('common.rename') })
    fireEvent.change(input, { target: { value: 'F8' } })
    fireEvent.keyDown(input, { key: 'Escape' })
    expect(screen.getByRole('dialog')).toHaveAttribute('data-state', 'open')
    expect(input).toHaveValue('F2')
    fireEvent.keyDown(input, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })
})
