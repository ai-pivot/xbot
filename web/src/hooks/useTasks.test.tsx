import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useTasks } from './useTasks'
import type { WSConnection } from '@/types/ws'

describe('useTasks', () => {
  it('normalizes backend snake_case cron fields (cron_expr/one_shot/...) into camelCase', async () => {
    // 后端 CronJob（storage/sqlite/cron.go）json tag 是 snake_case：
    // cron_expr/every_seconds/delay_seconds/one_shot/next_run/created_at/chat_id。
    // 曾经前端直接用原始数据 → task.cronExpr/oneShot 全 undefined →
    // 气泡调度行永远显示空 + 1× 标记丢失。normalizeCronTask 负责映射。
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/cron/list') {
        const data = {
          tasks: [{
            id: 'cron-1',
            message: '每天早上检查集群状态',
            channel: 'web',
            chat_id: 'chat-a',
            cron_expr: '0 9 * * *',
            one_shot: true,
            next_run: '2026-09-01T09:00:00Z',
            created_at: '2026-08-31T09:00:00Z',
          }, {
            id: 'cron-2',
            message: '每 30 秒轮询',
            channel: 'web',
            chat_id: 'chat-a',
            every_seconds: 30,
            one_shot: false,
          }],
        }
        return new Response(JSON.stringify({ ok: true, data, error: null }), {
          status: 200, headers: { 'Content-Type': 'application/json' },
        })
      }
      if (url === '/api/tasks/list') {
        return new Response(JSON.stringify({ ok: true, data: { background_tasks: [] }, error: null }), {
          status: 200, headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response(JSON.stringify({ ok: true, data: null, error: null }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const ws = { connected: true, rpc: vi.fn() } as unknown as WSConnection

    const { result } = renderHook(
      () => useTasks(ws, { channel: 'web', chatID: 'chat-a' }),
    )

    await waitFor(() => expect(result.current.cronTasks).toHaveLength(2))
    const [a, b] = result.current.cronTasks
    expect(a.cronExpr).toBe('0 9 * * *')
    expect(a.oneShot).toBe(true)
    expect(a.chatID).toBe('chat-a')
    expect(a.nextRun).toBe('2026-09-01T09:00:00Z')
    expect(a.createdAt).toBe('2026-08-31T09:00:00Z')
    expect(b.everySeconds).toBe(30)
    expect(b.oneShot).toBe(false)
    vi.unstubAllGlobals()
  })

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
