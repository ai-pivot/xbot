import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MessageItem } from './MessageItem'
import type { ChatMessage } from '@/types/agent'

describe('MessageItem', () => {
  it('renders rewind action below user messages', () => {
    const message: ChatMessage = {
      id: 'u1',
      role: 'user',
      content: 'rewind here',
      iterations: [],
      timestamp: '2026-07-08T00:00:00Z',
      isPartial: false,
      turnID: 0,
    }
    const onRewind = vi.fn()

    renderWithProviders(
      <MessageItem
        message={message}
        liveProgress={null}
        collapseLevel="all"
        onRewind={onRewind}
      />,
    )

    fireEvent.click(screen.getByLabelText('rewind'))
    expect(onRewind).toHaveBeenCalledWith(message)
  })

  it('renders empty LLM responses as a visible warning', () => {
    renderWithProviders(
      <MessageItem
        message={{
          id: 'a1',
          role: 'assistant',
          content: '(empty response)',
          iterations: [],
          timestamp: '',
          isPartial: false,
          turnID: 0,
        }}
        liveProgress={null}
        collapseLevel="minimal"
      />,
    )

    expect(screen.getByText(/LLM returned no text/)).toBeInTheDocument()
    expect(screen.queryByText('(no text output)')).not.toBeInTheDocument()
    expect(screen.queryByText('(empty response)')).not.toBeInTheDocument()
  })
})
