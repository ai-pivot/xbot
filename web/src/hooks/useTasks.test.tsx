import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useTasks } from './useTasks'
import type { WSConnection } from '@/types/ws'

describe('useTasks', () => {
  it('drops stale task responses after switching sessions', async () => {
    let resolveOldCron!: (value: unknown[]) => void
    let resolveOldBg!: (value: unknown[]) => void
    const oldCron = new Promise<unknown[]>((resolve) => { resolveOldCron = resolve })
    const oldBg = new Promise<unknown[]>((resolve) => { resolveOldBg = resolve })
    const rpc = vi.fn()
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      const isOld = url.includes('chat_id=a')
      const isCron = url.startsWith('/api/tasks')
      const value = isOld
        ? await (isCron ? oldCron : oldBg)
        : isCron
          ? [{ id: 'new-cron', message: 'new', channel: 'web', chatID: 'b' }]
          : [{ id: 'new-bg', command: 'new', status: 'running' }]
      return new Response(JSON.stringify({ ok: true, tasks: value }), {
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

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/tasks?channel=web&chat_id=a', expect.anything()))
    rerender({ chatID: 'b' })
    await waitFor(() => expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron']))
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])

    await act(async () => {
      resolveOldCron([{ id: 'old-cron', message: 'old', channel: 'web', chatID: 'a' }])
      resolveOldBg([{ id: 'old-bg', command: 'old', status: 'running' }])
      await Promise.resolve()
    })

    expect(result.current.cronTasks.map((t) => t.id)).toEqual(['new-cron'])
    expect(result.current.bgTasks.map((t) => t.id)).toEqual(['new-bg'])
  })
})
