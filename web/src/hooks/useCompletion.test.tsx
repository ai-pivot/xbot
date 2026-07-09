import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createRef } from 'react'

import { WEB_LOCAL_COMMANDS, useCompletion } from './useCompletion'
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
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ ok: true, commands }), {
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

function makeTextarea(value: string): React.RefObject<HTMLTextAreaElement | null> {
  const ref = createRef<HTMLTextAreaElement>()
  Object.defineProperty(ref, 'current', {
    value: {
      selectionStart: value.length,
      focus: vi.fn(),
      setSelectionRange: vi.fn(),
    },
  })
  return ref
}

function keyEvent(key: string) {
  return {
    key,
    shiftKey: false,
    ctrlKey: false,
    metaKey: false,
    preventDefault: vi.fn(),
  } as unknown as React.KeyboardEvent<HTMLTextAreaElement>
}

describe('useCompletion', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('offers /new and completes it with Tab', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/n')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/n',
        setValue,
        textareaRef,
        ws: makeWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/new']))

    const e = keyEvent('Tab')
    act(() => {
      expect(result.current.handleKeyDown(e)).toBe(true)
    })
    expect(e.preventDefault).toHaveBeenCalled()
    expect(setValue).toHaveBeenCalledWith('/new ')
  })

  it('keeps the Web local command manifest explicit', () => {
    expect(WEB_LOCAL_COMMANDS.map((cmd) => cmd.name)).toEqual([
      '/cancel', '/channel', '/chat', '/clear', '/commands', '/compress', '/context', '/exit',
      '/help', '/list-sessions', '/llm', '/models', '/new', '/palette', '/plugin', '/quit',
      '/rename', '/rewind', '/search', '/sessions', '/set-llm', '/set-model', '/settings',
      '/setup', '/ss', '/su', '/tasks', '/unset-llm', '/update', '/usage', '/user',
    ])
  })

  it('offers local /new before remote command RPC is available', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/n')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/n',
        setValue,
        textareaRef,
        ws: makeDisconnectedWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/new']))
  })

  it('replaces leading whitespace when completing slash commands like TUI', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('  /n')
    const { result } = renderHook(() =>
      useCompletion({
        value: '  /n',
        setValue,
        textareaRef,
        ws: makeDisconnectedWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/new']))

    act(() => {
      expect(result.current.handleKeyDown(keyEvent('Tab'))).toBe(true)
    })

    expect(setValue).toHaveBeenCalledWith('/new ')
  })

  it('adds Web local commands when the RPC list is incomplete', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/r')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/r',
        setValue,
        textareaRef,
        ws: makeWSWithCommands([{ name: '/help', description: 'help' }]),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/rename', '/rewind']))
  })

  it('adds the Web local /tasks command', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/t')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/t',
        setValue,
        textareaRef,
        ws: makeWSWithCommands([{ name: '/help', description: 'help' }]),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/tasks']))
  })

  it('uses aliases from the TUI command list', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/t')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/t',
        setValue,
        textareaRef,
        ws: makeWSWithCommands([{ name: '/tasks', aliases: ['/todo'], description: 'tasks' }]),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toContain('/todo'))
  })

  it('offers TUI commands that are not handled locally by Web', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/cl')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/cl',
        setValue,
        textareaRef,
        ws: makeWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.candidates.map((c) => c.label)).toEqual(['/clear']))
  })

  it('does not use Enter for slash command completion', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('/n')
    const { result } = renderHook(() =>
      useCompletion({
        value: '/n',
        setValue,
        textareaRef,
        ws: makeWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.visible).toBe(true))

    const e = keyEvent('Enter')
    act(() => {
      expect(result.current.handleKeyDown(e)).toBe(false)
    })
    expect(e.preventDefault).not.toHaveBeenCalled()
    expect(setValue).not.toHaveBeenCalled()
  })

  it('does not trigger file completion for @ inside a word', async () => {
    const setValue = vi.fn()
    const textareaRef = makeTextarea('email@example')
    const { result } = renderHook(() =>
      useCompletion({
        value: 'email@example',
        setValue,
        textareaRef,
        ws: makeWS(),
        cwd: '/repo',
      }),
    )

    await waitFor(() => expect(result.current.triggerType).toBeNull())
    expect(result.current.visible).toBe(false)
  })

  it('does not show stale file completions after the @ trigger is removed', async () => {
    vi.useFakeTimers()
    const setValue = vi.fn()
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: true,
      json: async () => ({ entries: [{ name: 'README.md', isDir: false }] }),
    })))
    const { result, rerender } = renderHook(
      ({ value, textareaRef }) =>
        useCompletion({
          value,
          setValue,
          textareaRef,
          ws: makeDisconnectedWS(),
          cwd: '/repo',
        }),
      { initialProps: { value: '@R', textareaRef: makeTextarea('@R') } },
    )

    await act(async () => {
      vi.advanceTimersByTime(150)
      await Promise.resolve()
    })

    rerender({ value: '', textareaRef: makeTextarea('') })

    await act(async () => {
      await Promise.resolve()
      await Promise.resolve()
    })

    expect(result.current.visible).toBe(false)
    expect(result.current.candidates).toEqual([])
  })
})
