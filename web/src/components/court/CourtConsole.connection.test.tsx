import '@testing-library/jest-dom/vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CourtConsole } from './CourtConsole'

/**
 * 2026-10-22 用户报障复现的回归测试：
 *   「通信视图永远离线，永远什么都看不到」
 *
 * 旧实现：`/api/groups` 返回空数组 → load() throw → 顶栏渲染「离线」。
 * 新实现：空数据 = 在线 + 空态说明；只有请求真的失败才「离线」。
 */

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** jsdom 没有 canvas 2D 实现：给一个全 no-op 的 context，避免 TimelineCanvas 崩。 */
function stubCanvas2D() {
  const noop = () => {}
  const ctx = new Proxy({}, {
    get: (_t, prop) => {
      if (prop === 'canvas') return { width: 0, height: 0 }
      if (prop === 'measureText') return () => ({ width: 0 })
      if (prop === 'createLinearGradient' || prop === 'createRadialGradient') {
        return () => ({ addColorStop: noop })
      }
      return noop
    },
    set: () => true,
  })
  // @ts-expect-error jsdom 未实现 getContext
  HTMLCanvasElement.prototype.getContext = () => ctx
}

beforeEach(() => { stubCanvas2D() })
afterEach(() => { vi.unstubAllGlobals() })

describe('CourtConsole 连接状态', () => {
  it('后端在线但没有可见群组 ⇒ 显示「已连接」+ 空态原因，绝不显示「离线」', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse({ ok: true, data: { groups: [] }, error: null })))

    render(<CourtConsole />)

    const badge = await screen.findByTestId('court-conn-badge')
    await waitFor(() => expect(badge).toHaveAttribute('data-conn', 'online'))
    expect(badge).toHaveTextContent('已连接')
    expect(screen.queryByText('离线')).toBeNull()

    const reason = await screen.findByTestId('court-empty-reason')
    expect(reason).toHaveTextContent('当前账号还没有可见群组')
  })

  it('请求真的失败（HTTP 500）⇒ 才显示「离线」并给出错误原因', async () => {
    vi.stubGlobal('fetch', vi.fn(async () =>
      jsonResponse({ ok: false, data: null, error: { code: 'internal', message: 'boom' } }, 500)))

    render(<CourtConsole />)

    const badge = await screen.findByTestId('court-conn-badge')
    await waitFor(() => expect(badge).toHaveAttribute('data-conn', 'offline'))
    expect(badge).toHaveTextContent('离线')
    expect(badge.getAttribute('title')).toBe('boom')
    expect(screen.queryByTestId('court-empty-reason')).toBeNull()
  })

  it('有群组且有消息 ⇒ 已连接、无空态提示、组织树有成员', async () => {
    const msg = {
      id: 1, group_id: 'grp_x', kind: 'report', sender_type: 'agent',
      sender_id: 'court.gongbu/main', sender_name: 'court.gongbu/main',
      role: 'assistant', content: '工部奏：测试', to: ['web-1'],
      created_at: new Date().toISOString(),
    }
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.startsWith('/api/groups/grp_x/members')) {
        return jsonResponse({ ok: true, data: { members: [
          { group_id: 'grp_x', member_addr: 'web-1', status: 'alive', joined_at: '', name: 'web-1' },
          { group_id: 'grp_x', member_addr: 'agent:court.gongbu/main', status: 'alive', joined_at: '', name: 'court.gongbu/main' },
        ] }, error: null })
      }
      if (url.startsWith('/api/groups/grp_x/messages')) {
        return jsonResponse({ ok: true, data: { items: [msg], next_cursor: 1, has_more: false, channel: 'web', chat_id: 'web-1' }, error: null })
      }
      return jsonResponse({ ok: true, data: { groups: [
        { id: 'grp_x', name: '朝廷', owner: 'web-1', created_at: '', member_count: 2 },
      ] }, error: null })
    }))

    render(<CourtConsole />)

    const badge = await screen.findByTestId('court-conn-badge')
    await waitFor(() => expect(badge).toHaveAttribute('data-conn', 'online'))
    expect(badge).toHaveTextContent('已连接')
    expect(screen.queryByTestId('court-empty-reason')).toBeNull()
    await waitFor(() => expect(screen.getAllByText(/court\.gongbu\/main/).length).toBeGreaterThan(0))
  })
})
