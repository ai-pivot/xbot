import { act, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEditor, EditorContent } from '@tiptap/react'
import StarterKit from '@tiptap/starter-kit'
import { Placeholder } from '@tiptap/extension-placeholder'
import { Markdown } from 'tiptap-markdown'
import { useEffect, useRef } from 'react'

import { WEB_LOCAL_COMMANDS, useCompletion, type CompletionState, type CompletionKeyEvent } from './useCompletion'
import type { WSConnection } from '@/types/ws'

function makeWS(): WSConnection {
  stubCommands([
    { name: '/new', description: 'new session' },
    { name: '/clear', description: 'clear session' },
    { name: '/rewind', description: 'rewind' },
    { name: '/sessions', aliases: ['/ss'], description: 'sessions' },
  ])
  return {
    connected: true,
    rpc: vi.fn(),
  } as unknown as WSConnection
}

function makeWSWithCommands(commands: unknown[]): WSConnection {
  stubCommands(commands)
  return {
    connected: true,
    rpc: vi.fn(),
  } as unknown as WSConnection
}

function stubCommands(commands: unknown[]): void {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true, data: commands, error: null }), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })))
}

function makeDisconnectedWS(): WSConnection {
  return {
    connected: false,
    rpc: vi.fn(),
  } as unknown as WSConnection
}

function keyEvent(key: string) {
  return {
    key,
    shiftKey: false,
    ctrlKey: false,
    metaKey: false,
    isComposing: false,
    preventDefault: vi.fn(),
  } as unknown as CompletionKeyEvent
}

/** Test harness: renders a real tiptap editor and exposes editor + completion via refs */
function TestHarness({
  content,
  ws,
  cwd,
  onReady,
}: {
  content: string
  ws: WSConnection
  cwd: string
  onReady: (editor: ReturnType<typeof useEditor>, completion: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }) => void
}) {
  const editor = useEditor({
    extensions: [
      StarterKit.configure({ heading: false, horizontalRule: false }),
      Placeholder.configure({ placeholder: 'test' }),
      Markdown.configure({ html: false, tightLists: true, linkify: false, breaks: true }),
    ],
    content,
  })
  const completion = useCompletion({ editor, ws, cwd })
  const readyRef = useRef(onReady)
  readyRef.current = onReady

  useEffect(() => {
    if (editor) {
      // Set cursor to end of content
      const endPos = editor.state.doc.content.size
      editor.commands.setTextSelection(endPos)
      readyRef.current(editor, completion)
    }
  })

  return editor ? <EditorContent editor={editor} /> : null
}

describe('useCompletion', () => {
  beforeEach(() => {
    vi.useRealTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('keeps the Web local command manifest explicit', () => {
    expect(WEB_LOCAL_COMMANDS.map((cmd) => cmd.name)).toEqual([
      '/cancel', '/channel', '/chat', '/clear', '/commands', '/compress', '/context', '/exit',
      '/help', '/list-sessions', '/llm', '/models', '/new', '/palette', '/plugin', '/quit',
      '/rename', '/rewind', '/search', '/sessions', '/set-llm', '/set-model', '/settings',
      '/setup', '/ss', '/su', '/tasks', '/unset-llm', '/update', '/usage', '/user',
    ])
  })

  it('offers /new and completes it with Tab', async () => {
    let editorRef: ReturnType<typeof useEditor> | null = null
    let completionRef: CompletionState & { handleKeyDown: (e: import('@/hooks/useCompletion').CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/n"
        ws={makeWS()}
        cwd="/repo"
        onReady={(e, c) => { editorRef = e; completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toEqual(['/new'])
    })

    const e = keyEvent('Tab')
    act(() => {
      expect(completionRef.handleKeyDown(e)).toBe(true)
    })
    expect(e.preventDefault).toHaveBeenCalled()
    // After completion, the editor should contain "/new " (with trailing space)
    await waitFor(() => {
      expect(editorRef?.getText()).toBe('/new ')
    })
  })

  it('offers local /new before remote command RPC is available', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/n"
        ws={makeDisconnectedWS()}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toEqual(['/new'])
    })
  })

  it('adds Web local commands when the RPC list is incomplete', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/r"
        ws={makeWSWithCommands([{ name: '/help', description: 'help' }])}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toEqual(['/rename', '/rewind'])
    })
  })

  it('adds the Web local /tasks command', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/t"
        ws={makeWSWithCommands([{ name: '/help', description: 'help' }])}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toEqual(['/tasks'])
    })
  })

  it('uses aliases from the TUI command list', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/t"
        ws={makeWSWithCommands([{ name: '/tasks', aliases: ['/todo'], description: 'tasks' }])}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toContain('/todo')
    })
  })

  it('offers TUI commands that are not handled locally by Web', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/cl"
        ws={makeWS()}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.candidates.map((c) => c.label)).toEqual(['/clear'])
    })
  })

  it('does not use Enter for slash command completion', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="/n"
        ws={makeWS()}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    await waitFor(() => {
      expect(completionRef.visible).toBe(true)
    })

    const e = keyEvent('Enter')
    act(() => {
      expect(completionRef.handleKeyDown(e)).toBe(false)
    })
    expect(e.preventDefault).not.toHaveBeenCalled()
  })

  it('does not trigger file completion for @ inside a word', async () => {
    let completionRef: CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean } = null as unknown as CompletionState & { handleKeyDown: (e: CompletionKeyEvent) => boolean }

    render(
      <TestHarness
        content="email@example"
        ws={makeWS()}
        cwd="/repo"
        onReady={(_e, c) => { completionRef = c }}
      />,
    )

    // Give time for potential async fetches
    await new Promise((r) => setTimeout(r, 200))

    expect(completionRef.triggerType).toBeNull()
    expect(completionRef.visible).toBe(false)
  })
})
