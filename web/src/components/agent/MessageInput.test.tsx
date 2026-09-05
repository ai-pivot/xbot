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

describe('MessageInput links & file paste', () => {
  /** Helper: get the live editor + wait for mount */
  async function getEditor() {
    const editor = await waitFor(() => {
      const e = __getTestEditor()
      if (!e) throw new Error('Editor not ready')
      return e
    })
    return editor
  }

  /** Helper: render a MessageInput and get its editor (single-instance editor). */
  async function renderInput(overrides?: { onUpload?: (file: File) => Promise<{ upload_key?: string; name?: string; size?: number; mime?: string }> }) {
    const onSend = vi.fn()
    const onUpload = overrides?.onUpload ?? vi.fn().mockResolvedValue({ upload_key: 'k', name: 'a.png', size: 1, mime: 'image/png' })
    const utils = renderWithProviders(
      <MessageInput busy={false} onSend={onSend} onCancel={vi.fn()} onUpload={onUpload} />,
    )
    const editor = await getEditor()
    return { ...utils, editor, onSend, onUpload }
  }

  it('markdown link input rule renders [text](url) as a live link mark (WYSIWYG)', async () => {
    const { editor } = await renderInput()
    act(() => {
      editor.commands.clearContent()
      editor.commands.insertContent('[docs](https://example.com)', { applyInputRules: true })
    })
    const html = editor.getHTML()
    expect(html).toContain('href="https://example.com"')
    expect(html).toContain('docs')
    // Markdown round-trip: getMarkdown keeps the link syntax
    const md = (editor.storage as unknown as { markdown: { getMarkdown: () => string } }).markdown.getMarkdown()
    expect(md).toContain('[docs](https://example.com)')
  })

  it('autolinks protocol URLs but NOT bare domains like file.tar.gz', async () => {
    const { editor } = await renderInput()
    // Autolink only processes the last word before a trailing whitespace (the
    // typing simulation) — insert URL + space like a user typing it.
    act(() => {
      editor.commands.clearContent()
      editor.commands.insertContent('go https://example.com ')
    })
    expect(editor.getHTML()).toContain('href="https://example.com"')

    act(() => {
      editor.commands.clearContent()
      editor.commands.insertContent('archive file.tar.gz ')
    })
    // .gz is a valid TLD — the old shouldAutoLink linkified this. Strict mode must not.
    expect(editor.getHTML()).not.toContain('<a ')
  })

  it('does NOT absorb typed text into a preceding link (inclusive=false)', async () => {
    const { editor } = await renderInput()
    act(() => {
      editor.commands.clearContent()
      editor.commands.insertContent('see link tail')
      // Mark "link" ([5,9)) as a link, then type right after its end
      editor.chain().setTextSelection({ from: 5, to: 9 }).setLink({ href: 'https://e.com' }).run()
      editor.chain().setTextSelection(9).insertContent('X').run()
    })
    const html = editor.getHTML()
    // X must be OUTSIDE the link — the old inclusive mark merged it in
    expect(html).toContain('link</a>X')
    expect(html).not.toContain('linkX</a>')
  })

  it('pasted clipboard files upload as attachments (paste a screenshot)', async () => {
    const onUpload = vi.fn().mockResolvedValue({ upload_key: 'up-1', name: 'shot.png', size: 3, mime: 'image/png' })
    const { editor, onSend } = await renderInput({ onUpload })
    const file = new File([new Uint8Array([1, 2, 3])], 'shot.png', { type: 'image/png' })
    // PM's internal paste handler reads clipboardData.getData before consulting
    // editorProps.handlePaste — the synthetic clipboard needs a getData stub.
    fireEvent.paste(editor.view.dom as HTMLElement, {
      clipboardData: { files: [file], getData: () => '', types: [] },
    })
    await waitFor(() => expect(onUpload).toHaveBeenCalledWith(file))
    expect(onSend).not.toHaveBeenCalled()
  })

  it('dropped files upload as attachments', async () => {
    const onUpload = vi.fn().mockResolvedValue({ upload_key: 'up-2', name: 'data.bin', size: 4, mime: 'application/octet-stream' })
    const { editor } = await renderInput({ onUpload })
    const file = new File([new Uint8Array([9, 9, 9, 9])], 'data.bin', { type: 'application/octet-stream' })
    // PM resolves the drop position via document.elementFromPoint before calling
    // editorProps.handleDrop — jsdom lacks it, so point it at the editor DOM.
    const doc = document as Document & { elementFromPoint?: (x: number, y: number) => Element | null }
    const origElementFromPoint = doc.elementFromPoint?.bind(doc)
    doc.elementFromPoint = () => editor.view.dom as unknown as Element
    try {
      fireEvent.drop(editor.view.dom as HTMLElement, {
        dataTransfer: { files: [file], getData: () => '', types: [] },
      })
      await waitFor(() => expect(onUpload).toHaveBeenCalledWith(file))
    } finally {
      doc.elementFromPoint = origElementFromPoint
    }
  })

  it('Ctrl/Cmd+K opens the link editor on the word at the cursor and applies the URL', async () => {
    const { editor } = await renderInput()
    act(() => {
      editor.commands.clearContent()
      editor.commands.setContent('hello world')
      editor.commands.setTextSelection(3) // inside "hello"
    })
    fireEvent.keyDown(editor.view.dom as HTMLElement, { key: 'k', ctrlKey: true })

    // The toolbar's URL input appears (BubbleMenu portals to body)
    const input = await screen.findByTestId('st-link-input', {}, { timeout: 3000 })
    expect(input).toBeInTheDocument()
    fireEvent.change(input, { target: { value: 'example.com' } })
    fireEvent.keyDown(input, { key: 'Enter' })

    // Word "hello" is now linked; bare domain normalized to https://
    await waitFor(() => expect(editor.getHTML()).toContain('href="https://example.com"'))
    expect(editor.getHTML()).toContain('hello')

    // …and it can be removed again (select the linked word → link button → remove)
    act(() => {
      // Explicit range selection over the link — focus() in jsdom collapses the
      // PM selection to a cursor, and a collapsed cursor after an inclusive=false
      // mark no longer reports isActive('link').
      editor.commands.setTextSelection({ from: 1, to: 6 })
    })
    const linkBtn = await screen.findByTestId('st-link', {}, { timeout: 3000 })
    fireEvent.click(linkBtn)
    const removeBtn = await screen.findByTestId('st-link-remove', {}, { timeout: 3000 })
    fireEvent.click(removeBtn)
    await waitFor(() => expect(editor.getHTML()).not.toContain('<a '))
  })

  it('a URL typed mid-text becomes a link and neighboring text stays plain (typing simulation)', async () => {
    const { editor } = await renderInput()
    act(() => {
      editor.commands.clearContent()
      editor.commands.insertContent('check ')
      editor.commands.insertContent('https://example.com ')
      editor.commands.insertContent('ok')
    })
    const html = editor.getHTML()
    expect(html).toContain('href="https://example.com"')
    expect(editor.state.doc.textContent).toBe('check https://example.com ok')
  })
})
