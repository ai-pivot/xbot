import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
        groups={[{ key: 'all', sessions: [parent] }]}
        sortedSessions={[parent]}
        category="all"
        starredIds={[]}
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
})
