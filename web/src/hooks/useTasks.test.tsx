import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useTasks } from './useTasks'
import type { WSConnection } from '@/types/ws'

describe('useTasks', () => {
  it('drops stale task responses after switching sessions', async () => {
    let resolveOld!: (value: { tasks: unknown[]; background_tasks: unknown[] }) => void
    const oldStatus = new Promise<{ tasks: unknown[]; background_tasks: unknown[] }>((resolve) => { resolveOld = resolve })
    const rpc = vi.fn()
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body ?? '{}')) as { chat_id?: string }
      const data = body.chat_id === 'a'
        ? await oldStatus
        : {
            tasks: [{ id: 'new-cron', message: 'new', channel: 'web', chatID: 'b' }],
            background_tasks: [{ id: 'new-bg', command: 'new', status: 'running' }],
          }
      return new Response(JSON.stringify({ ok: true, data, error: null }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const ws = { connected: true, rpc } as unknown as WSConnection

    const { result, rerender } = renderHook(
      ({ chatID }) => useTasks(ws, { channel: 'web', chatID }),
      { initialProps: { chatID: 'a' } },
    )

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/session/status', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ channel: 'web', chat_id: 'a' }),
    })))
    rerender({ chatID: 'b' })
    await waitFor(() => expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron']))
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])

    await act(async () => {
      resolveOld({
        tasks: [{ id: 'old-cron', message: 'old', channel: 'web', chatID: 'a' }],
        background_tasks: [{ id: 'old-bg', command: 'old', status: 'running' }],
      })
      await Promise.resolve()
    })

    expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron'])
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])
  })
})
