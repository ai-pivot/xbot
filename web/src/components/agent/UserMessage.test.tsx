import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { UserMessage } from './UserMessage'

// Use the i18n key directly — the test environment may be zh-CN or en,
// so we query by title attribute instead of localized label text.

describe('UserMessage — inline edit mode (Spec C §2)', () => {
  it('renders plain content with markdown', () => {
    renderWithProviders(
      <UserMessage content="Hello world" />,
    )
    expect(screen.getByText('Hello world')).toBeInTheDocument()
  })

  it('shows pencil edit button when onRewind and onStartEdit are provided', () => {
    const onRewind = vi.fn()
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={onRewind}
        onStartEdit={onStartEdit}
      />,
    )

    // The edit button has a title attribute from i18n; find by button role
    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))
    expect(editBtn).toBeDefined()
  })

  it('does not show edit button when onRewind is not provided', () => {
    renderWithProviders(
      <UserMessage content="Hello" />,
    )

    const buttons = screen.queryAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))
    expect(editBtn).toBeUndefined()
  })

  it('disables edit button when editDisabled is true', () => {
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={vi.fn()}
        onStartEdit={onStartEdit}
        editDisabled={true}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil')) as HTMLButtonElement
    expect(editBtn).toBeDefined()
    expect(editBtn.disabled).toBe(true)
  })

  it('calls onStartEdit when edit button is clicked', () => {
    const onStartEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Hello"
        onRewind={vi.fn()}
        onStartEdit={onStartEdit}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))!
    fireEvent.click(editBtn)
    expect(onStartEdit).toHaveBeenCalledTimes(1)
  })

  it('shows textarea and confirm/cancel buttons when in edit mode', () => {
    renderWithProviders(
      <UserMessage
        content="Original text"
        onRewind={vi.fn()}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={vi.fn()}
      />,
    )

    // Should have a textarea with the original content
    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    expect(textarea.value).toBe('Original text')

    // Should have confirm (check) and cancel (x) buttons
    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))
    const xBtn = buttons.find((b) => b.querySelector('svg.lucide-x'))
    expect(checkBtn).toBeDefined()
    expect(xBtn).toBeDefined()
  })

  it('calls onRewind with edited content when confirm is clicked', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Original text"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'Edited text' } })

    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))!
    fireEvent.click(checkBtn)

    expect(onRewind).toHaveBeenCalledWith('Edited text')
  })

  it('calls onRewind with original content when confirm is clicked without changes', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Same text"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const buttons = screen.getAllByRole('button')
    const checkBtn = buttons.find((b) => b.querySelector('svg.lucide-check'))!
    fireEvent.click(checkBtn)

    // Rewind should be called even when content is unchanged — the user
    // confirmed the rewind action by clicking Check, not Cancel (X).
    expect(onRewind).toHaveBeenCalledWith('Same text')
  })

  it('calls onEndEdit and restores original when cancel is clicked', () => {
    const onRewind = vi.fn()
    const onEndEdit = vi.fn()

    renderWithProviders(
      <UserMessage
        content="Original"
        onRewind={onRewind}
        isEditing={true}
        onStartEdit={vi.fn()}
        onEndEdit={onEndEdit}
      />,
    )

    const textarea = screen.getByRole('textbox') as HTMLTextAreaElement
    fireEvent.change(textarea, { target: { value: 'Changed' } })

    const buttons = screen.getAllByRole('button')
    const xBtn = buttons.find((b) => b.querySelector('svg.lucide-x'))!
    fireEvent.click(xBtn)

    expect(onRewind).not.toHaveBeenCalled()
    expect(onEndEdit).toHaveBeenCalledTimes(1)
  })
})
