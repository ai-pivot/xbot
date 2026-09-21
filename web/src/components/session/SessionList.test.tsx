import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { SessionList } from './SessionList'
import type { SessionInfo } from '@/types/shared'

vi.mock('@/components/ui/scroll-area', () => ({
  ScrollArea: ({ children, className }: { children: React.ReactNode; className?: string }) => (
    <div className={className}>{children}</div>
  ),
}))

function session(overrides: Partial<SessionInfo> & { chatID: string; channel: string; label: string }): SessionInfo {
  return {
    chatID: overrides.chatID,
    channel: overrides.channel,
    label: overrides.label,
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? (overrides.type === 'agent' ? 'running' : 'idle'),
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    role: overrides.role,
    instance: overrides.instance,
    parentChatID: overrides.parentChatID,
    parentChannel: overrides.parentChannel,
    running: overrides.running ?? overrides.type === 'agent',
    historical: overrides.historical,
    synthetic: overrides.synthetic,
    children: overrides.children,
  }
}

describe('SessionList', () => {
  it('renders SubAgents under their parent session instead of normal rows', () => {
    const child = session({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      role: 'review',
      instance: '1',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [child],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search=""
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByText('Agent-main')).toBeInTheDocument()
    expect(screen.getByText('review/1')).toBeInTheDocument()
    expect(screen.getByTitle('review/1').closest('[role="button"]')).toHaveStyle({ marginLeft: '1rem' })
    expect(screen.getByText('review/1').closest('[role="button"]')?.querySelector('svg')).not.toBeNull()
  })

  it('searches matching SubAgents by showing their parent with the child row', () => {
    const child = session({
      chatID: 'cli:/repo:Agent-main/code-review:1',
      channel: 'agent',
      label: 'code-review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [child],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search="code-review"
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByText('Agent-main')).toBeInTheDocument()
    expect(screen.getByText('code-review/1')).toBeInTheDocument()
  })

  it('renders nested SubAgents recursively under their owner', () => {
    const fix = session({
      chatID: 'agent:cli:/repo:Agent-main/review:1/fix:2',
      channel: 'agent',
      label: 'fix/2',
      type: 'agent',
      parentChannel: 'agent',
      parentChatID: 'cli:/repo:Agent-main/review:1',
    })
    const review = session({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
      children: [fix],
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [review],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search=""
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByText('Agent-main')).toBeInTheDocument()
    expect(screen.getByText('review/1')).toBeInTheDocument()
    expect(screen.getByText('fix/2')).toBeInTheDocument()
  })

  it('renders backend-attached SubAgent children without rebuilding parent links from a flat list', () => {
    const review = session({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [review],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search=""
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByText('Agent-main')).toBeInTheDocument()
    expect(screen.getByText('review/1')).toBeInTheDocument()
  })

  it('hides synthetic parents and their SubAgents from the active list', () => {
    const child = session({
      chatID: 'cli:/repo:Agent-deleted/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-deleted',
    })
    const parent = session({
      chatID: '/repo:Agent-deleted',
      channel: 'cli',
      label: 'Agent-deleted',
      type: 'main',
      synthetic: true,
      children: [child],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search=""
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.queryByText('Agent-deleted')).not.toBeInTheDocument()
    expect(screen.queryByText('review/1')).not.toBeInTheDocument()
  })

  it('searches nested SubAgents by keeping the ancestor chain visible', () => {
    const fix = session({
      chatID: 'agent:cli:/repo:Agent-main/review:1/fix:2',
      channel: 'agent',
      label: 'fix/2',
      type: 'agent',
      parentChannel: 'agent',
      parentChatID: 'cli:/repo:Agent-main/review:1',
    })
    const review = session({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
      children: [fix],
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [review],
    })

    renderWithProviders(
      <SessionList
        sessions={[parent]}
        groups={[{ key: 'today', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search="fix"
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn()}
      />,
    )

    expect(screen.getByText('Agent-main')).toBeInTheDocument()
    expect(screen.getByText('review/1')).toBeInTheDocument()
    expect(screen.getByText('fix/2')).toBeInTheDocument()
  })

  // REPRO（用户报告："删除会话的时候弹窗内容有问题，看上去是占位符没有实际被
  // 替换掉"）。根因：i18n `session.deleteConfirm` 的占位符是 `{{username}}`，
  // 而唯一调用点传的是 `{ name }` —— i18next 取不到 username，把模板原样渲染：
  // `确定删除会话「{{username}}」吗？此操作不可撤销。`
  it('删除确认弹窗必须插入会话名，且不得残留 {{...}} 占位符（REPRO）', async () => {
    const s = session({ chatID: 'web:chat-1', channel: 'web', label: 'My Session', type: 'main' })

    renderWithProviders(
      <SessionList
        sessions={[s]}
        groups={[{ key: 'today', sessions: [s] }]}
        sortedSessions={[s]}
        category="time"
        collapsedGroups={new Set()}
        onToggleGroup={vi.fn()}
        starredIds={[]}
        unreadIds={[]}
        activeSession={null}
        search=""
        subAgents={[]}
        onSelect={vi.fn()}
        onToggleStar={vi.fn()}
        onRename={vi.fn()}
        onDelete={vi.fn().mockResolvedValue(true)}
      />,
    )

    // 右键会话行 → 菜单里点「删除」→ 打开确认弹窗。
    fireEvent.contextMenu(screen.getByText('My Session'))
    fireEvent.click(await screen.findByRole('menuitem', { name: /Delete|删除/ }))

    const dialog = await screen.findByRole('alertdialog')
    // 会话名必须被真正插入（修复前这里是字面量 "{{username}}"）。
    expect(dialog.textContent).toContain('My Session')
    expect(dialog.textContent ?? '').not.toMatch(/\{\{|\}\}/)
  })
})

// 需求（用户）：「项目会话列表可以折叠/展开」。组头是**受控**的（open/onToggle 来自
// store）—— 状态提升后才能持久化 + 支持「全部折叠/展开」；折叠键必须含 category。
describe('project group collapse', () => {
  it('toggles through the store and hides the rows while collapsed', () => {
    const onToggleGroup = vi.fn()
    const repo = session({ chatID: '/repo:Agent-main', channel: 'cli', label: 'Agent-main', type: 'main' })
    const baseProps = {
      sessions: [repo],
      groups: [{ key: '/repo', sessions: [repo] }],
      sortedSessions: [repo],
      category: 'path' as const,
      starredIds: [],
      unreadIds: [],
      activeSession: null,
      search: '',
      subAgents: [],
      onSelect: vi.fn(),
      onToggleStar: vi.fn(),
      onRename: vi.fn(),
      onDelete: vi.fn(),
    }

    // 默认展开：组头 aria-expanded=true，会话行可见，tooltip 是完整路径。
    const { rerender } = renderWithProviders(
      <SessionList {...baseProps} collapsedGroups={new Set()} onToggleGroup={onToggleGroup} />,
    )
    const title = screen.getByTitle('/repo')
    expect(title.closest('button')).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('Agent-main')).toBeInTheDocument()

    // 点组头 → 折叠键（含 category）交给 store。
    fireEvent.click(title)
    expect(onToggleGroup).toHaveBeenCalledWith('path:/repo')

    // 折叠态：组头 aria-expanded=false。内容被裁到 0 高这一层由 E2E 的几何判据
    // 守护 —— jsdom 没有布局，断言 AnimatedCollapse 的挂载语义会与浏览器不一致。
    rerender(<SessionList {...baseProps} collapsedGroups={new Set(['path:/repo'])} onToggleGroup={onToggleGroup} />)
    expect(screen.getByTitle('/repo').closest('button')).toHaveAttribute('aria-expanded', 'false')
  })
})
