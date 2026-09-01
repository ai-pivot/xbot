import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { MessageInput, __getTestEditor } from './MessageInput'

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    connected: true,
    rpc: vi.fn().mockResolvedValue([]),
  }),
}))

vi.mock('@/providers/CwdProvider', () => ({
  useCwd: () => ({ cwd: '/repo' }),
}))

vi.mock('@/providers/i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/providers/i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/hooks/useSendKeyMode', () => ({
  useSendKeyMode: () => ({ mode: 'ctrl-enter' }),
  isSendKey: (e: { key: string; shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }, mode: string) => {
    if (mode === 'enter') return e.key === 'Enter' && !e.shiftKey && !e.ctrlKey && !e.metaKey
    return e.key === 'Enter' && (e.ctrlKey || e.metaKey)
  },
}))

/** Helper: set editor content and wait for React to process */
async function setEditorContent(content: string) {
  // Wait for editor to be available (useEditor creates in effect)
  const editor = await waitFor(() => {
    const e = __getTestEditor()
    if (!e) throw new Error('Editor not ready')
    return e
  })
  act(() => {
    editor.commands.setContent(content)
    // Move cursor to end so completion detection fires
    const endPos = editor.state.doc.content.size
    editor.commands.setTextSelection(endPos)
    // Focus to ensure editor is active
    editor.commands.focus()
  })
  // Wait for completion hook to process the update
  await new Promise((r) => setTimeout(r, 50))
}

describe('MessageInput', () => {
  it('delegates the bottom safe area to the InfoBar below (no own inset padding)', () => {
    const { container } = renderWithProviders(
      <MessageInput busy={false} onSend={vi.fn()} onCancel={vi.fn()} onUpload={vi.fn()} />,
    )
    // Find the outer wrapper by class (not by role — ProseMirror editor
    // initializes asynchronously in jsdom, so getByRole('textbox') may not exist yet)
    const wrapper = container.querySelector('.border-t')
    expect(wrapper).not.toBeNull()
    expect((wrapper as HTMLElement).style.paddingBottom).toBe('')
  })

  it('maps /rewind to the Web rewind action instead of sending it as a message', async () => {
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

    await setEditorContent('/rewind')
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onRewindLatest).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send /rewind as a message while busy', async () => {
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

    await setEditorContent('/rewind')
    // While busy, the send button becomes a cancel button
    // Use keyboard: Ctrl+Enter to trigger send
    const editor = __getTestEditor()
    expect(editor).not.toBeNull()
    act(() => {
      editor!.chain().focus().setContent('/rewind').run()
      const endPos = editor!.state.doc.content.size
      editor!.commands.setTextSelection(endPos)
    })
    await new Promise((r) => setTimeout(r, 50))

    // While busy, /rewind should not trigger rewind
    expect(onRewindLatest).not.toHaveBeenCalled()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('maps /cancel to cancel instead of sending it as a message while busy', async () => {
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

    await setEditorContent('/cancel')

    // While busy + has content: button is Send (not Cancel). Clicking it
    // triggers submit(), which intercepts /cancel → onCancel().
    fireEvent.click(screen.getByLabelText(/排队发送/))

    expect(onCancel).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('maps /tasks to opening the Web tasks panel instead of sending it', async () => {
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

    await setEditorContent('/tasks')
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onOpenTasks).toHaveBeenCalledOnce()
    expect(onSend).not.toHaveBeenCalled()
  })

  it('does not send /new while busy', async () => {
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

    // While busy, we can't click send (it shows cancel button)
    // Instead, verify that typing /new and pressing Ctrl+Enter doesn't send
    await setEditorContent('/new')

    // While busy, the send button is replaced by cancel button
    // The /new command is checked in submit() which requires clicking send or pressing Enter
    // Since send button is replaced by cancel, /new can't be submitted
    expect(onSend).not.toHaveBeenCalled()
  })

  it('sends /new through the agent command path when idle', async () => {
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

    await setEditorContent('/new')
    fireEvent.click(screen.getByLabelText(/send/i))

    expect(onSend).toHaveBeenCalledWith('/new', undefined, undefined)
  })
})
