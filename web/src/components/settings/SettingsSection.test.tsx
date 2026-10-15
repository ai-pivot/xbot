import { fireEvent, render, screen, within } from '@testing-library/react'
import '@testing-library/jest-dom/vitest'
import { describe, expect, it, vi } from 'vitest'
import { SettingsSection } from './SettingsSection'

describe('SettingsSection', () => {
  it('preserves the description and body controls without header actions', () => {
    render(<SettingsSection title="Group" description="Description"><button>Control</button></SettingsSection>)
    const heading = screen.getByRole('heading', { name: 'Group' })
    expect(screen.getByRole('region', { name: 'Group' })).toContainElement(heading)
    expect(heading.closest('header')).toContainElement(screen.getByText('Description'))
    expect(within(heading.closest('header')!).queryByRole('button')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Control' })).toBeVisible()
  })

  it('keeps header actions usable and outside the body controls', () => {
    const reset = vi.fn()
    render(<SettingsSection title="Group" actions={<button onClick={reset}>Reset</button>}><input aria-label="Binding" /></SettingsSection>)
    const header = screen.getByRole('heading', { name: 'Group' }).closest('header')!
    fireEvent.click(within(header).getByRole('button', { name: 'Reset' }))
    expect(reset).toHaveBeenCalledOnce()
    expect(header).not.toContainElement(screen.getByRole('textbox', { name: 'Binding' }))
  })
})
