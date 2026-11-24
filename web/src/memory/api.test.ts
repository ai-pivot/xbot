/**
 * memory/api.test.ts —— 回归测试：长期记忆列表必须从统一信封的 data.items 里取。
 *
 * 2026-11-17 的真实事故：后端返回 {ok:true, data:{items:[...]}}，fetchMemories
 * 却读顶层 body.items ⇒ 永远是 undefined ⇒ 面板恒显示"没有记忆。"，而当时所有
 * 单测都通过（它们 mock 掉了 fetchMemories 本身，从没验证过真实解包）。
 * 这里直接喂真实响应体，钉死解包路径。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

const backendFetch = vi.fn()
vi.mock('@/lib/api', async () => {
  const actual = await vi.importActual<typeof import('@/lib/api')>('@/lib/api')
  return {
    ...actual,
    backendFetch: (...args: unknown[]) => backendFetch(...args),
    postAPI: vi.fn(),
  }
})

import { fetchMemories } from './api'

const item = (id: number) => ({
  id,
  type: 'fact',
  content: `memory ${id}`,
  keywords: '',
  tags: '',
  importance: 0.5,
  scope: 'global',
  created_at: '2026-11-17T09:00:00Z',
})

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('fetchMemories envelope handling', () => {
  beforeEach(() => backendFetch.mockReset())

  it('从 {ok,data:{items}} 信封里取出条目（真实服务端形态）', async () => {
    backendFetch.mockResolvedValue(
      jsonResponse({ ok: true, data: { items: [item(1), item(2)], total: 2 }, error: null }),
    )
    const items = await fetchMemories({ channel: 'web', chat_id: 'chat_x' }, { type: 'all', limit: 200 })
    expect(items.map((i) => i.id)).toEqual([1, 2])
  })

  it('兼容 data 直接是数组的形态', async () => {
    backendFetch.mockResolvedValue(jsonResponse({ ok: true, data: [item(3)], error: null }))
    const items = await fetchMemories({ channel: 'web', chat_id: 'chat_x' })
    expect(items.map((i) => i.id)).toEqual([3])
  })

  it('空列表返回 []，不是 undefined', async () => {
    backendFetch.mockResolvedValue(jsonResponse({ ok: true, data: { items: [] }, error: null }))
    await expect(fetchMemories({ channel: 'web', chat_id: 'chat_x' })).resolves.toEqual([])
  })

  it('服务端错误信封 → 抛出带 message 的错误（面板会显示）', async () => {
    backendFetch.mockResolvedValue(
      jsonResponse({ ok: false, data: null, error: { code: 'memory_unavailable', message: 'no provider' } }, 200),
    )
    await expect(fetchMemories({ channel: 'web', chat_id: 'chat_x' })).rejects.toThrow('no provider')
  })

  it('HTTP 503 + 错误信封同样抛出（不再静默空列表）', async () => {
    backendFetch.mockResolvedValue(
      jsonResponse({ ok: false, data: null, error: { message: 'memory panel unavailable' } }, 503),
    )
    await expect(fetchMemories({ channel: 'web', chat_id: 'chat_x' })).rejects.toThrow('memory panel unavailable')
  })
})
