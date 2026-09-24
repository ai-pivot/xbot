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

/**
 * ⛔ React #185 复现/守护（2026-09-24 生产崩溃：「输入框语音输入、频繁改文字必崩」）。
 *
 * 崩溃链（bundle 坐标 `index-CxWS9lE3.js:58:424` **精确对应**旧 `update` 闭包）：
 *   commit 期（passive effect）派发**真实文档变更** → 编辑器 `emit('update')`
 *   → 监听器**同步 setState**（`getText()+':'+selection.from` 每次都是新字符串 ⇒
 *     React 永不短路）→ 该 setState 落在 commit 期 flush 内 = React 的**嵌套更新**
 *   → 下一次 commit 再派发 ⇒ 嵌套计数 > 50 ⇒ `Maximum update depth exceeded`。
 * 语音输入（高频改字 + 高频光标移动）只是把这个闭环推到极限。
 *
 * 修复后监听器**只标脏 + 排一帧**（`frameScheduler`，每帧至多一次、值不变不通知），
 * 事件回调里不含任何 setState ⇒ 嵌套链断开。
 */
function CommitPhaseDispatchHarness({
  maxDispatches,
  ws,
  onReady,
}: {
  maxDispatches: number
  ws: WSConnection
  onReady: (e: NonNullable<ReturnType<typeof useEditor>>) => void
}) {
  const editor = useEditor({
    extensions: [
      StarterKit.configure({ heading: false, horizontalRule: false }),
      Markdown.configure({ html: false, tightLists: true, linkify: false, breaks: true }),
    ],
    content: '',
  })
  useCompletion({ editor, ws, cwd: '/repo' })
  const readyRef = useRef(onReady)
  readyRef.current = onReady
  useEffect(() => {
    if (editor) readyRef.current(editor)
  }, [editor])
  // ⛔ **无依赖 effect**（= 生产里"每次 commit 都跑的 effect 里调 editor.commands.*"）
  // 只派发**一次**真实文档变更；链是否续下去完全取决于**监听器有没有在事件回调里
  // 同步 setState**：
  //   修复前：派发 → emit('update') → setTextContent → 一次新提交 → 本 effect 再跑
  //           → 再派发 …… 每次嵌套一层 ⇒ 50 层后 React 抛 #185。
  //   修复后：派发 → 监听器只排一帧（回调里没有 setState）⇒ 不产生新提交 ⇒ 链断。
  // 上界 `maxDispatches` 只为让修复后的用例有限结束（超限即停，不再派发）。
  const dispatchCount = useRef(0)
  useEffect(() => {
    if (!editor) return
    if (dispatchCount.current >= maxDispatches) return
    dispatchCount.current += 1
    editor.commands.insertContent('x')
  })
  return editor ? <EditorContent editor={editor} /> : null
}

describe('⛔ 编辑器事件回调里绝不同步 setState（React #185 根因守护）', () => {
  it('commit 期派发事务的自持链：不得出现 Maximum update depth exceeded（修复前必红）', async () => {
    const consoleErrors: string[] = []
    const errSpy = vi.spyOn(console, 'error').mockImplementation((...args: unknown[]) => {
      consoleErrors.push(args.map(String).join(' '))
    })
    let thrown: unknown = null
    try {
      render(<CommitPhaseDispatchHarness maxDispatches={60} ws={makeWS()} onReady={() => {}} />)
      // 让被动 effect / 帧调度跑完（嵌套更新若存在会在这一步爆出来）
      await act(async () => { await new Promise((r) => setTimeout(r, 80)) })
    } catch (e) {
      thrown = e
    } finally {
      errSpy.mockRestore()
    }

    const blob = `${thrown ?? ''} ${consoleErrors.join(' ')}`
    expect(thrown, `React 在 commit 期派发 + 监听器同步 setState 的链上抛错了：#185 → ${blob.slice(0, 300)}`).toBeNull()
    expect(blob).not.toMatch(/Maximum update depth exceeded|Minified React error #185/)
  })
})
