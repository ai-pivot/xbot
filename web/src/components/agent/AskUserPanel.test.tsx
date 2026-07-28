import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { AskUserPanel } from './AskUserPanel'

vi.mock('@/providers/i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('AskUserPanel', () => {
  it('disables every response control while history rewind is pending', () => {
    const onRespond = vi.fn()
    const onCancel = vi.fn()
    render(
      <AskUserPanel
        prompt={{
          requestId: 'ask-1',
          questions: [
            { question: 'Choose?', options: ['yes', 'no'] },
            { question: 'Why?' },
          ],
        }}
        onRespond={onRespond}
        onCancel={onCancel}
        disabled
      />,
    )

    expect(screen.getByRole('button', { name: 'yes' })).toBeDisabled()
    expect(screen.getByRole('textbox')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'common.cancel' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'agent.askUserSubmit' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'common.cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'agent.askUserSubmit' }))
    expect(onRespond).not.toHaveBeenCalled()
    expect(onCancel).not.toHaveBeenCalled()
  })
})
