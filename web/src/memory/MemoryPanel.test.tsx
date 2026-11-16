/**
 * MemoryPanel 测试 —— 面板能列出 / 删除 / 编辑，且没有会话时给出明确提示。
 *
 * 这个面板的存在意义：之前记忆只能靠 memory_* 工具从对话里操作，UI 上看不到
 * 注入到上下文里的内容，也删不掉垃圾行（实测有 importance=24440 的乱码条目
 * 霸榜，把正常记忆挤出列表）。
 */
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { MemoryItem } from './api'

const fetchMemories = vi.fn()
const updateMemoryItem = vi.fn()
const deleteMemoryItem = vi.fn()

vi.mock('./api', () => ({
  fetchMemories: (...args: unknown[]) => fetchMemories(...args),
  updateMemoryItem: (...args: unknown[]) => updateMemoryItem(...args),
  deleteMemoryItem: (...args: unknown[]) => deleteMemoryItem(...args),
}))

const activeSession = { channel: 'web', chatID: 'chat_test' }
vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => ({ activeSession }),
}))

import { MemoryPanel } from './MemoryPanel'

function item(over: Partial<MemoryItem> = {}): MemoryItem {
  return {
    id: 1,
    type: 'fact',
    content: '用户的周跑量是 14 km',
    keywords: '跑量',
    tags: 'health',
    importance: 0.7,
    scope: 'global',
    created_at: '2026-11-11T10:00:00Z',
    ...over,
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(window, 'confirm').mockReturnValue(true)
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('MemoryPanel', () => {
  it('列出记忆：类型徽标 + importance + 正文', async () => {
    fetchMemories.mockResolvedValue([item()])
    render(<MemoryPanel />)

    expect(await screen.findByText('用户的周跑量是 14 km')).toBeTruthy()
    // 「事实」在类型下拉里也有一项，必须限定在行内断言
    expect(within(screen.getByTestId('memory-item')).getByText('事实')).toBeTruthy()
    // 文本被 JSX 拆成多个节点（'★ ' 与 toFixed 结果），用 title 定位再断言内容
    expect(screen.getByTitle('importance').textContent).toContain('0.70')
    expect(fetchMemories).toHaveBeenCalledWith(
      { channel: 'web', chat_id: 'chat_test' },
      { type: 'all', limit: 200 },
    )
  })

  it('删除：二次确认后调 deleteMemoryItem 并重新拉列表', async () => {
    fetchMemories.mockResolvedValue([item()])
    deleteMemoryItem.mockResolvedValue(undefined)
    render(<MemoryPanel />)

    await screen.findByText('用户的周跑量是 14 km')
    fireEvent.click(screen.getByTestId('memory-delete'))

    await waitFor(() => {
      expect(deleteMemoryItem).toHaveBeenCalledWith({ channel: 'web', chat_id: 'chat_test' }, 1)
    })
    // 初次加载 + 删除后刷新
    await waitFor(() => expect(fetchMemories).toHaveBeenCalledTimes(2))
  })

  it('取消确认框时不删除', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    fetchMemories.mockResolvedValue([item()])
    render(<MemoryPanel />)

    await screen.findByText('用户的周跑量是 14 km')
    fireEvent.click(screen.getByTestId('memory-delete'))

    expect(deleteMemoryItem).not.toHaveBeenCalled()
  })

  it('编辑：改正文后保存，走 updateMemoryItem', async () => {
    fetchMemories.mockResolvedValue([item()])
    updateMemoryItem.mockResolvedValue(undefined)
    render(<MemoryPanel />)

    await screen.findByText('用户的周跑量是 14 km')
    fireEvent.click(screen.getByText('编辑'))

    const ta = screen.getByTestId('memory-edit-content') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '用户的周跑量是 14 km（更新）' } })
    fireEvent.click(screen.getByTestId('memory-save'))

    await waitFor(() => {
      expect(updateMemoryItem).toHaveBeenCalledWith(
        { channel: 'web', chat_id: 'chat_test' },
        expect.objectContaining({ id: 1, content: '用户的周跑量是 14 km（更新）', importance: 0.7 }),
      )
    })
  })

  it('没有会话时提示，且不发请求', async () => {
    activeSession.chatID = ''
    render(<MemoryPanel />)

    expect(await screen.findByText(/先选中一个会话/)).toBeTruthy()
    expect(fetchMemories).not.toHaveBeenCalled()
    activeSession.chatID = 'chat_test'
  })
})
