import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useTasks } from './useTasks'
import type { WSConnection } from '@/types/ws'

describe('useTasks', () => {
  it('drops stale task responses after switching sessions', async () => {
    let resolveOldCron!: (value: { tasks: unknown[] }) => void
    let resolveOldBg!: (value: { background_tasks: unknown[] }) => void
    const oldCron = new Promise<{ tasks: unknown[] }>((resolve) => { resolveOldCron = resolve })
    const oldBg = new Promise<{ background_tasks: unknown[] }>((resolve) => { resolveOldBg = resolve })
    const rpc = vi.fn()
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const body = JSON.parse(String(init?.body ?? '{}')) as { chat_id?: string }
      const isOld = body.chat_id === 'a'
      if (url === '/api/cron/list') {
        const data = isOld
          ? await oldCron
          : { tasks: [{ id: 'new-cron', message: 'new', channel: 'web', chatID: 'b' }] }
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200, headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url === '/api/tasks/list') {
        const data = isOld
          ? await oldBg
          : { background_tasks: [{ id: 'new-bg', command: 'new', status: 'running' }] }
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200, headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response(JSON.stringify({ ok: true, data: null, error: null }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const ws = { connected: true, rpc } as unknown as WSConnection

    const { result, rerender } = renderHook(
      ({ chatID }) => useTasks(ws, { channel: 'web', chatID }),
      { initialProps: { chatID: 'a' } },
    )

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/cron/list', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ channel: 'web', chat_id: 'a' }),
    })))
    rerender({ chatID: 'b' })
    await waitFor(() => expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron']))
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])

    await act(async () => {
      resolveOldCron({ tasks: [{ id: 'old-cron', message: 'old', channel: 'web', chatID: 'a' }] })
      resolveOldBg({ background_tasks: [{ id: 'old-bg', command: 'old', status: 'running' }] })
      await Promise.resolve()
    })

    expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron'])
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])
  })
})
