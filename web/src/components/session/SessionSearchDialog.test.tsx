import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SessionSearchDialog, openSessionSearch } from './SessionSearchDialog'

vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const state = vi.hoisted(() => ({ sessions: [
  { chatID: 'one', channel: 'web', label: 'Alpha', preview: 'keyboard lock discussion', updatedAt: '2026-10-17' },
  { chatID: 'two', channel: 'cli', label: 'Beta', preview: 'deployment notes', updatedAt: '2026-10-16' },
], hasMore: false, loading: false, loadMore: vi.fn() }))
vi.mock('@/hooks/useSessionStore', () => ({ useSessionStore: () => state }))
afterEach(cleanup)
function mount() {
  const select = vi.fn()
  render(<SessionSearchDialog onSelect={select} />)
  act(() => openSessionSearch())
  return select
}
describe('centered session search', () => {
  it('opens from the shared entry and matches previews across channels', () => {
    mount()
    expect(screen.getByRole('dialog')).toBeTruthy()
    const input = screen.getByRole('combobox')
    expect(document.activeElement).toBe(input)
    fireEvent.change(input, { target: { value: 'DEPLOYMENT' } })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option').textContent).toContain('Beta')
  })
  it('selects with arrows and Enter, then closes', () => {
    const select = mount()
    const input = screen.getByRole('combobox')
    fireEvent.keyDown(input, { key: 'ArrowDown' })
    const label = screen.getAllByRole('option').find(row => row.getAttribute('aria-selected') === 'true')!.textContent
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(select).toHaveBeenCalledOnce()
    expect(label).toContain(select.mock.calls[0][0].label)
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it('opens via Command Shift F and does not trigger while composing', () => {
    render(<SessionSearchDialog onSelect={vi.fn()} />)
    fireEvent.keyDown(window, { code: 'KeyF', metaKey: true, shiftKey: true, isComposing: true })
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.keyDown(window, { code: 'KeyF', metaKey: true, shiftKey: true })
    expect(screen.getByRole('dialog')).toBeTruthy()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
  })
  it('shows an empty state and selects on click', () => {
    const select = mount()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'not-found' } })
    expect(screen.getByRole('status').textContent).toBe('session.quickSearchEmpty')
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Alpha' } })
    fireEvent.click(screen.getByRole('option'))
    expect(select.mock.calls[0][0].chatID).toBe('one')
  })
})
