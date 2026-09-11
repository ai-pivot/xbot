import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MessageItem } from './MessageItem'
import type { ChatMessage } from '@/types/agent'
import { EMPTY_LIVE_PROGRESS } from '@/types/agent'

describe('MessageItem', () => {
  it('renders edit action below user messages and calls onStartEdit', () => {
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
    const onStartEdit = vi.fn()

    renderWithProviders(
      <MessageItem
        message={message}
        liveProgress={null}
        collapseLevel="all"
        onRewind={onRewind}
        onStartEdit={onStartEdit}
      />,
    )

    // Find the pencil button by its SVG icon
    const buttons = screen.getAllByRole('button')
    const editBtn = buttons.find((b) => b.querySelector('svg.lucide-pencil'))
    expect(editBtn).toBeDefined()
    fireEvent.click(editBtn!)
    expect(onStartEdit).toHaveBeenCalledTimes(1)
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

  it.each(['pending', 'generating', 'running'] as const)(
    'does not show the generic thinking indicator while a tool is %s',
    (status) => {
      const { container } = renderWithProviders(
        <MessageItem
          message={{
            id: 'live-tool',
            role: 'assistant',
            content: '',
            iterations: [],
            timestamp: '',
            isPartial: true,
            turnID: 0,
          }}
          liveProgress={{
            ...EMPTY_LIVE_PROGRESS,
            streaming: true,
            activeTools: [{
              name: 'Shell',
              label: 'Shell',
              status,
              elapsedMs: 0,
              summary: '',
              detail: '',
              args: '',
              toolHints: '',
            }],
          }}
          collapseLevel="minimal"
        />,
      )

      expect(container.querySelectorAll('.sweep-text')).toHaveLength(1)
      expect(container.querySelector('.sweep-text')).toHaveTextContent('Shell')
    },
  )
})
