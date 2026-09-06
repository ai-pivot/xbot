import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { SessionItem } from './SessionItem'
import type { SessionInfo } from '@/types/shared'

function session(overrides: Partial<SessionInfo>): SessionInfo {
  return {
    chatID: overrides.chatID ?? '/repo:Agent-main',
    channel: overrides.channel ?? 'cli',
    label: overrides.label ?? 'Agent-main',
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? 'idle',
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    role: overrides.role,
    instance: overrides.instance,
    parentChatID: overrides.parentChatID,
    parentChannel: overrides.parentChannel,
    running: overrides.running ?? false,
    historical: overrides.historical,
    synthetic: overrides.synthetic,
    children: overrides.children,
  }
}

const baseProps = {
  starred: false,
  unread: false,
  active: false,
  onSelect: vi.fn(),
  onToggleStar: vi.fn(),
  onRename: vi.fn(),
  onDelete: vi.fn(),
}

// REPRO: 用户报告 web 端点击 fork 会话（ContextMenu 的"分叉会话/Fork"项）
// 完全无反应——无 RPC、无交互。组件级复现：SessionItem 渲染的 ContextMenu
// 必须包含 Fork 项（onFork 提供时）且点击触发 onFork。
describe('SessionItem fork', () => {
  it('onFork 提供时右键菜单渲染 Fork 项，点击触发 onFork', async () => {
    const onFork = vi.fn()
    const s = session({})
    renderWithProviders(
      <SessionItem
        {...baseProps}
        session={s}
        onFork={onFork}
      />,
    )

    // 右键会话项打开 ContextMenu。
    const item = screen.getByText('Agent-main').closest('[data-slot="context-menu-trigger"]') ?? screen.getByText('Agent-main')
    fireEvent.contextMenu(item)

    // 菜单出现 Fork 项（zh-CN '分叉会话' / en 'Fork'）。
    const forkEntry = await screen.findByRole('menuitem', { name: /分叉会话|Fork/ })
    expect(forkEntry).toBeInTheDocument()

    // 点击 Fork 项必须调用 onFork（用户报告：点击无任何反应）。
    fireEvent.click(forkEntry)
    await waitFor(() => {
      expect(onFork).toHaveBeenCalledWith(s)
    })
  })

  it('onFork 未提供时不渲染 Fork 项（可选 prop 契约）', async () => {
    renderWithProviders(
      <SessionItem
        {...baseProps}
        session={session({})}
      />,
    )
    const item = screen.getByText('Agent-main').closest('[data-slot="context-menu-trigger"]') ?? screen.getByText('Agent-main')
    fireEvent.contextMenu(item)
    await waitFor(() => {
      expect(screen.getByRole('menuitem', { name: /重命名|Rename/ })).toBeInTheDocument()
    })
    // 无 onFork → 菜单里没有 Fork 项。
    const forkEntries = screen.queryAllByRole('menuitem', { name: /分叉会话|Fork/ })
    expect(forkEntries).toHaveLength(0)
  })
})
