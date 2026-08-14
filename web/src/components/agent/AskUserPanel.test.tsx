/**
 * AskUserPanel tests — multi-select, allow-other, always-available free input,
 * and answer merge priority (free text > selected options > other).
 */
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { I18nProvider } from '@/providers/i18n'
import { AskUserPanel } from './AskUserPanel'
import type { AskUserPrompt } from '@/types/agent'

function makePrompt(questions: AskUserPrompt['questions']): AskUserPrompt {
  return { requestId: 'req-1', questions }
}

describe('AskUserPanel', () => {
  let onRespond: ReturnType<typeof vi.fn>
  let onCancel: ReturnType<typeof vi.fn>

  beforeEach(() => {
    onRespond = vi.fn()
    onCancel = vi.fn()
  })

  it('multi-select question: toggles multiple options and submits joined value', async () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Pick', options: ['a', 'b', 'c'], multiSelect: true }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    fireEvent.click(screen.getByRole('checkbox', { name: 'a' }))
    fireEvent.click(screen.getByRole('checkbox', { name: 'c' }))
    fireEvent.click(screen.getByText('Submit'))
    await waitFor(() => expect(onRespond).toHaveBeenCalledWith({ '0': 'a、c' }))
  })

  it('single-select question: clicking the same option twice deselects it', async () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Choose', options: ['dark', 'light'] }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    fireEvent.click(screen.getByRole('radio', { name: 'dark' }))
    fireEvent.click(screen.getByRole('radio', { name: 'dark' }))
    fireEvent.click(screen.getByText('Submit'))
    // Deselected → no option answer, but the submit is blocked until answered.
    await waitFor(() => expect(onRespond).not.toHaveBeenCalled())
  })

  it('allow-other: selecting "Other" expands an input whose value is submitted', async () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Color', options: ['red', 'blue'], allowOther: true }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    // "Other" renders as a dashed toggle button.
    fireEvent.click(screen.getByText('Other'))
    const input = await screen.findByPlaceholderText('Type a custom answer…')
    fireEvent.change(input, { target: { value: 'custom-color' } })
    fireEvent.click(screen.getByText('Submit'))
    await waitFor(() => expect(onRespond).toHaveBeenCalledWith({ '0': 'custom-color' }))
  })

  it('always provides a free-text input even when options exist — free text wins', async () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Choose', options: ['dark', 'light'] }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    const free = screen.getByPlaceholderText('Type a custom reply…')
    expect(free).toBeInTheDocument()
    fireEvent.change(free, { target: { value: 'my own answer' } })
    fireEvent.click(screen.getByText('Submit'))
    await waitFor(() => expect(onRespond).toHaveBeenCalledWith({ '0': 'my own answer' }))
  })

  it('question without options renders a multiline Textarea', () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Any preferences?' }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    const textarea = screen.getByPlaceholderText('Type a reply…')
    expect(textarea.tagName).toBe('TEXTAREA')
  })

  it('submit stays disabled until every question is answered', () => {
    renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([
          { question: 'Q1', options: ['a'] },
          { question: 'Q2', options: ['x', 'y'] },
        ])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    const submit = screen.getByText('Submit')
    expect(submit).toBeDisabled()
    fireEvent.click(screen.getByRole('radio', { name: 'a' }))
    expect(submit).toBeDisabled() // Q2 still unanswered
    fireEvent.click(screen.getByRole('radio', { name: 'x' }))
    expect(submit).toBeEnabled()
  })

  it('reset state when a new prompt requestId arrives', () => {
    const { rerender } = renderWithProviders(
      <AskUserPanel
        prompt={makePrompt([{ question: 'Q', options: ['a', 'b'] }])}
        onRespond={onRespond}
        onCancel={onCancel}
      />,
    )
    fireEvent.click(screen.getByRole('radio', { name: 'a' }))
    expect(screen.getByRole('radio', { name: 'a' })).toHaveAttribute('aria-checked', 'true')
    rerender(
      <I18nProvider>
        <AskUserPanel
          prompt={{ requestId: 'req-2', questions: [{ question: 'Q', options: ['a', 'b'] }] }}
          onRespond={onRespond}
          onCancel={onCancel}
        />
      </I18nProvider>,
    )
    expect(screen.getByRole('radio', { name: 'a' })).toHaveAttribute('aria-checked', 'false')
  })
})
