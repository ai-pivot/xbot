import '@testing-library/jest-dom/vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { CourtConsole } from './CourtConsole'
import { ORG_VISIBLE_LIMIT } from './sources'

/**
 * P1/P2 的 DOM 级验收：
 *   P1 —— 左栏默认只出「当前会话的直属子代理」（≤ 20），历史折叠，搜索可用；
 *   P2 —— 空态收成单栏、无回放条、顶栏不再并排两行说明（都收进「?」弹层）。
 */

const CURRENT = 'chat_cur'

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}
const ok = (data: unknown) => jsonResponse({ ok: true, data, error: null })

function agentNode(
  role: string,
  parent: string,
  over: Record<string, unknown> = {},
) {
  const instance = (over.instance as string | undefined) ?? 'main'
  return {
    chat_id: `agent/${role}/main`,
    channel: 'agent',
    full_key: `web:${parent}/${role}${instance === 'main' ? '' : `:${instance}`}`,
    label: role,
    role,
    instance: 'main',
    last_active: '2026-10-23T00:00:00Z',
    preview: '',
    running: false,
    status: 'idle',
    parent_chat_id: parent,
    parent_channel: 'web',
    ...over,
  }
}

/** 生产形状：1 当前会话 + 10 直属 + 12 explore + 3 unloaded + 40 历史 + 8 孤儿。 */
function prodShapedTree() {
  const depts = ['court.gongbu', 'court.hubu', 'court.hanlin', 'court.ducha', 'court.bingbu', 'court.sili', 'court.menxia']
  const proj = ['proj.xbot.lead', 'proj.xbot.dev', 'proj.xbot.qa']
  const children = [
    ...depts.map((r) => agentNode(r, CURRENT)),
    ...proj.map((r) => agentNode(r, CURRENT)),
    ...Array.from({ length: 12 }, (_, i) => agentNode('explore', CURRENT, {
      chat_id: `agent/explore/main#${i}`, instance: `x${i}`, label: 'explore',
    })),
    ...Array.from({ length: 3 }, (_, i) => agentNode('explore', CURRENT, {
      chat_id: `agent/explore/unloaded#${i}`, instance: `u${i}`, status: 'unloaded',
    })),
  ]
  return {
    sessions: [
      {
        chat_id: CURRENT, channel: 'web', label: '首辅', last_active: '2026-10-23T00:10:00Z',
        preview: '', running: true, status: 'running', is_current: true, children,
      },
      ...Array.from({ length: 40 }, (_, i) => ({
        chat_id: `chat_hist_${i}`, channel: 'web', label: `历史会话 ${i}`,
        last_active: '2026-09-01T00:00:00Z', preview: '', running: false, status: 'idle',
        is_current: false, children: [],
      })),
    ],
    orphan_subagents: Array.from({ length: 8 }, (_, i) => agentNode('explore', `chat_deleted_${i}`, {
      chat_id: `agent/explore/gone#${i}`, instance: `g${i}`, historical: true,
    })),
  }
}

const MESSAGES = [
  {
    message_id: 1, kind: 'report', text: '',
    payload: { summary: '户部：磁盘水位 71%' },
    sender: { type: 'agent', id: 'agent:court.hubu/main', name: 'court.hubu' },
    created_at: new Date().toISOString(), direction: 'in',
  },
]

function stubFetch(tree: unknown, messages: unknown[] = MESSAGES) {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/api/session-tree')) return ok(tree)
    if (url.includes('/api/messages')) return ok({ items: messages, has_more: false, next_cursor: 0 })
    if (url.includes('/api/sse')) return new Response('', { status: 200 })
    return ok({ groups: [] })
  }))
}

afterEach(() => { vi.unstubAllGlobals() })

describe('P1 · 组织树数据源（DOM）', () => {
  it('默认只渲染直属子代理，行数 ≤ 20，并给出「N 个直属」', async () => {
    stubFetch(prodShapedTree())
    const { container } = render(<CourtConsole />)

    await waitFor(() => expect(screen.getByTestId('court-conn-badge')).toHaveAttribute('data-conn', 'online'))
    await waitFor(() => expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(10))

    const rows = [...container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]')]
    expect(rows.length).toBeLessThanOrEqual(ORG_VISIBLE_LIMIT)
    expect(screen.getByTestId('org-count')).toHaveTextContent('10 个直属')
    // 历史/一次性 explore/unloaded 都不在默认视图里
    const names = rows.map((r) => r.getAttribute('data-lane'))
    expect(names.some((n) => n?.includes('explore'))).toBe(false)
    expect(names.some((n) => n?.includes('unloaded'))).toBe(false)
  })

  it('按 role 命名空间分组（court / proj.xbot），组头可折叠', async () => {
    stubFetch(prodShapedTree())
    const { container } = render(<CourtConsole />)
    await waitFor(() => expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(10))

    const groups = [...container.querySelectorAll('[data-testid="org-group"]')]
    expect(groups.map((g) => g.getAttribute('data-group'))).toEqual(['court', 'proj.xbot'])

    const courtHeader = groups[0].querySelector('button')!
    expect(courtHeader).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(courtHeader)
    expect(courtHeader).toHaveAttribute('aria-expanded', 'false')
    // 收起 court 组后只剩 proj.xbot 的 3 行
    expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(3)
  })

  it('历史默认折叠，点「显示历史」才出现', async () => {
    stubFetch(prodShapedTree())
    const { container } = render(<CourtConsole />)
    await waitFor(() => expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(10))

    const toggle = await screen.findByTestId('org-history-toggle')
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveTextContent('显示历史（60）') // 40 历史会话 + 12 explore + 8 孤儿
    expect(screen.queryByTestId('org-history')).toBeNull()

    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(container.querySelectorAll('[data-testid="org-history"] [data-lane]').length).toBe(60)
  })

  it('搜索框能过滤部门（大小写不敏感）', async () => {
    stubFetch(prodShapedTree())
    const { container } = render(<CourtConsole />)
    await waitFor(() => expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(10))

    fireEvent.change(screen.getByTestId('org-search'), { target: { value: 'HUBU' } })
    await waitFor(() => expect(container.querySelectorAll('[data-testid="org-tree-list"] [data-lane]').length).toBe(1))
    expect(container.querySelector('[data-testid="org-tree-list"] [data-lane]')?.getAttribute('data-lane')).toBe('agent:court.hubu/main')

    fireEvent.change(screen.getByTestId('org-search'), { target: { value: '不存在的部门' } })
    await waitFor(() => expect(screen.getByTestId('org-tree-empty')).toHaveTextContent('没有匹配的部门'))
  })
})

describe('P2 · 界面降噪（DOM）', () => {
  it('空态是单栏：不渲染组织树 / 右详情栏，也不渲染回放条', async () => {
    stubFetch({ sessions: [], orphan_subagents: [] }, [])
    const { container } = render(<CourtConsole />)

    await waitFor(() => expect(screen.getByTestId('court-conn-badge')).toHaveAttribute('data-conn', 'online'))
    expect(await screen.findByTestId('court-empty-single')).toBeInTheDocument()
    expect(container.querySelector('aside')).toBeNull()
    expect(screen.queryByTestId('thread-empty')).toBeNull()
    expect(container.querySelector('[aria-label="回放时间"]')).toBeNull()
  })

  it('顶栏不再并排两行说明；解释收进「?」弹层', async () => {
    stubFetch({ sessions: [], orphan_subagents: [] }, [])
    render(<CourtConsole />)
    await waitFor(() => expect(screen.getByTestId('court-conn-badge')).toHaveAttribute('data-conn', 'online'))

    expect(document.body.textContent).not.toContain('这是什么：')
    expect(document.body.textContent).not.toContain('活动流 · 消息流向 · 对话线程 · 组织树 · 回放')
    expect(screen.queryByTestId('court-intro')).toBeNull()

    const help = screen.getByTestId('court-help-toggle')
    expect(help).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByTestId('court-help-popover')).toBeNull()
    fireEvent.click(help)
    const popover = await screen.findByTestId('court-help-popover')
    expect(popover).toHaveTextContent('这是什么')
    expect(popover).toHaveTextContent('当前会话的直属子代理')
  })

  it('有事件时回放条才出现', async () => {
    stubFetch(prodShapedTree())
    const { container } = render(<CourtConsole />)
    await waitFor(() => expect(screen.getByTestId('court-conn-badge')).toHaveAttribute('data-conn', 'online'))
    await waitFor(() => expect(container.querySelector('[aria-label="回放时间"]')).not.toBeNull())
  })
})
