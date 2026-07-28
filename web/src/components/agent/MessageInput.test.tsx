import { fireEvent, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MessageInput } from './MessageInput'

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    connected: true,
    rpc: vi.fn().mockResolvedValue([]),
  }),
}))

vi.mock('@/providers/CwdProvider', () => ({
  useCwd: () => ({ cwd: '/repo' }),
}))

describe('MessageInput', () => {
  it('maps /rewind to the Web rewind action instead of sending it as a message', () => {
    const onSend = vi.fn()
    const onRewindLatest = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={false}
        onSend={onSend}
        onCancel={vi.fn()}
        onRewindLatest={onRewindLatest}
        onUpload={vi.fn()}
      />,
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: '/rewind' } })
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onRewindLatest).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send /rewind as a message while busy', () => {
    const onSend = vi.fn()
    const onRewindLatest = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={true}
        onSend={onSend}
        onCancel={vi.fn()}
        onRewindLatest={onRewindLatest}
        onUpload={vi.fn()}
      />,
    )

    const textbox = screen.getByRole('textbox')
    fireEvent.change(textbox, { target: { value: '/rewind' } })
    fireEvent.keyDown(textbox, { key: 'Enter', ctrlKey: true })

    expect(onRewindLatest).not.toHaveBeenCalled()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('maps /cancel to cancel instead of sending it as a message while busy', () => {
    const onSend = vi.fn()
    const onCancel = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={true}
        onSend={onSend}
        onCancel={onCancel}
        onRewindLatest={vi.fn()}
        onUpload={vi.fn()}
      />,
    )

    const textbox = screen.getByRole('textbox')
    fireEvent.change(textbox, { target: { value: '/cancel' } })
    fireEvent.keyDown(textbox, { key: 'Enter', ctrlKey: true })

    expect(onCancel).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('maps /tasks to opening the Web tasks panel instead of sending it', () => {
    const onSend = vi.fn()
    const onOpenTasks = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={false}
        onSend={onSend}
        onCancel={vi.fn()}
        onRewindLatest={vi.fn()}
        onOpenTasks={onOpenTasks}
        onUpload={vi.fn()}
      />,
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: '/tasks' } })
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onOpenTasks).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send /new while busy', () => {
    const onSend = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={true}
        onSend={onSend}
        onCancel={vi.fn()}
        onRewindLatest={vi.fn()}
        onUpload={vi.fn()}
      />,
    )

    const textbox = screen.getByRole('textbox')
    fireEvent.change(textbox, { target: { value: '/new' } })
    fireEvent.keyDown(textbox, { key: 'Enter', ctrlKey: true })

    expect(onSend).not.toHaveBeenCalled()
  })

  it('sends /new through the agent command path when idle', () => {
    const onSend = vi.fn()

    renderWithProviders(
      <MessageInput
        busy={false}
        onSend={onSend}
        onCancel={vi.fn()}
        onRewindLatest={vi.fn()}
        onUpload={vi.fn()}
      />,
    )

    fireEvent.change(screen.getByRole('textbox'), { target: { value: '/new' } })
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onSend).toHaveBeenCalledWith('/new', undefined)
  })
})
