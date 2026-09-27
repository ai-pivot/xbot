/**
 * GroupList — the 群组 tab: which agents share a group, and editing that.
 *
 * The contract under test: members resolve to real session rows (so the user
 * sees agents, not opaque keys), members whose session is gone are flagged
 * instead of hidden, and every edit goes through the RPC whose response is
 * adopted as the authority.
 */
import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { GroupList } from './GroupList'
import { renderWithProviders } from '@/test-utils'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { PeerGroup } from '@/types/shared'

const api = vi.hoisted(() => ({
  fetchPeerGroups: vi.fn(),
  createPeerGroup: vi.fn(),
  deletePeerGroup: vi.fn(),
  joinPeerGroup: vi.fn(),
  leavePeerGroup: vi.fn(),
}))

vi.mock('@/components/agent/api', () => api)

const sessions = [
  {
    chatID: '/repo',
    channel: 'cli',
    label: '/repo',
    lastActive: '2026-09-01T00:00:00Z',
    preview: '',
    status: 'idle',
    isCurrent: true,
    children: [
      {
        chatID: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        label: 'default',
        lastActive: '2026-09-01T00:00:01Z',
        preview: '',
        status: 'idle',
        isCurrent: false,
        type: 'agent',
        role: 'review',
        instance: '1',
        parentChannel: 'cli',
        parentChatID: '/repo',
        agentChatID: 'cli:/repo:Agent-main/review:1',
      },
    ],
  },
]

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): SessionStore =>
    ({
      sessions,
      groups: [],
      sortedSessions: [],
      subAgents: [],
      activeChannel: null,
      collapsedGroups: new Set<string>(),
      starredIds: [],
      unreadIds: [],
      loading: false,
    }) as unknown as SessionStore,
}))

function group(id: string, members: { session_key: string; name: string }[]): PeerGroup {
  return { id, members }
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('GroupList', () => {
  it('shows each group with its members resolved to real sessions', async () => {
    api.fetchPeerGroups.mockResolvedValue([
      group('dev-team', [
        { session_key: 'cli:/repo', name: '/repo' },
        { session_key: 'agent:cli:/repo:Agent-main/review:1', name: 'review/1' },
      ]),
    ])

    renderWithProviders(<GroupList />)

    await waitFor(() => expect(screen.getByTestId('group-card')).toBeInTheDocument())
    const members = screen.getAllByTestId('group-member')
    expect(members).toHaveLength(2)
    // The main session shows its label; the SubAgent resolves to role/instance.
    expect(screen.getByText('/repo')).toBeInTheDocument()
    expect(screen.getByText('review/1')).toBeInTheDocument()
    // No stale flags: both sessions exist in the store.
    expect(members.every((m) => m.getAttribute('data-stale') === null)).toBe(true)
  })

  it('flags a member whose session no longer exists (instead of hiding it)', async () => {
    api.fetchPeerGroups.mockResolvedValue([
      group('dev-team', [
        { session_key: 'cli:/repo', name: '/repo' },
        { session_key: 'web:deleted-chat', name: 'ghost' },
      ]),
    ])

    renderWithProviders(<GroupList />)

    await waitFor(() => expect(screen.getAllByTestId('group-member')).toHaveLength(2))
    const stale = screen.getAllByTestId('group-member').filter((m) => m.getAttribute('data-stale') === 'true')
    expect(stale).toHaveLength(1)
    expect(stale[0].textContent).toMatch(/ghost|deleted-chat/)
  })

  it('adds a session through the picker (top-level sessions only — a SubAgent has no waker of its own)', async () => {
    api.fetchPeerGroups.mockResolvedValue([group('dev-team', [])])
    api.joinPeerGroup.mockResolvedValue([group('dev-team', [{ session_key: 'cli:/repo', name: '/repo' }])])

    renderWithProviders(<GroupList />)
    await waitFor(() => expect(screen.getByTestId('group-add-member')).toBeInTheDocument())

    fireEvent.click(screen.getByTestId('group-add-member'))
    // Only the top-level session is offered: the SubAgent row (review/1) belongs
    // to its parent's session and cannot be woken on its own.
    const candidates = await screen.findAllByTestId('group-candidate')
    expect(candidates).toHaveLength(1)
    expect(candidates[0].textContent).not.toMatch(/review\/1/)

    fireEvent.click(candidates[0])
    await waitFor(() => expect(api.joinPeerGroup).toHaveBeenCalledWith('dev-team', 'cli:/repo', '/repo'))
    // The returned list is adopted (member now rendered).
    await waitFor(() => expect(screen.getAllByTestId('group-member')).toHaveLength(1))
  })

  it('removes a member (the group is dropped once empty, exactly like the store does)', async () => {
    api.fetchPeerGroups.mockResolvedValue([group('dev-team', [{ session_key: 'cli:/repo', name: '/repo' }])])
    // Realistic response: the store drops a group with no members left.
    api.leavePeerGroup.mockResolvedValue([])

    renderWithProviders(<GroupList />)
    await waitFor(() => expect(screen.getByTestId('group-member-remove')).toBeInTheDocument())

    fireEvent.click(screen.getByTestId('group-member-remove'))
    await waitFor(() => expect(api.leavePeerGroup).toHaveBeenCalledWith('dev-team', 'cli:/repo'))
    await waitFor(() => expect(screen.queryByTestId('group-card')).not.toBeInTheDocument())
  })

  it('creates a group, and rejects an invalid name before calling the backend', async () => {
    api.fetchPeerGroups.mockResolvedValue([])
    api.createPeerGroup.mockResolvedValue([group('my-team', [])])

    renderWithProviders(<GroupList />)
    await waitFor(() => expect(screen.getByText(/No groups yet|还没有群组/)).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /New group|新建群组/ }))
    const input = screen.getByPlaceholderText(/Group name|群组名/)
    fireEvent.change(input, { target: { value: 'bad name!' } })
    fireEvent.click(screen.getByRole('button', { name: /Create|创建/ }))
    expect(api.createPeerGroup).not.toHaveBeenCalled()
    expect(screen.getByText(/Invalid name|名称不合法/)).toBeInTheDocument()

    fireEvent.change(input, { target: { value: 'my-team' } })
    fireEvent.click(screen.getByRole('button', { name: /Create|创建/ }))
    await waitFor(() => expect(api.createPeerGroup).toHaveBeenCalledWith('my-team'))
    await waitFor(() => expect(screen.getByText('my-team')).toBeInTheDocument())
  })

  it('deletes a group only after confirmation', async () => {
    api.fetchPeerGroups.mockResolvedValue([group('dev-team', [])])
    api.deletePeerGroup.mockResolvedValue([])

    renderWithProviders(<GroupList />)
    await waitFor(() => expect(screen.getByTestId('group-card')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /Delete group|删除群组/ }))
    // Confirmation dialog first — a destructive action never fires on one click.
    expect(api.deletePeerGroup).not.toHaveBeenCalled()
    fireEvent.click(await screen.findByRole('button', { name: /Delete group|删除群组/ }))

    await waitFor(() => expect(api.deletePeerGroup).toHaveBeenCalledWith('dev-team'))
  })
})
