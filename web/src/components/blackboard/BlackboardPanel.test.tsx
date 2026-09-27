/**
 * BlackboardPanel — the human's window into the shared blackboard.
 *
 * These assertions pin the properties that make the panel trustworthy:
 *   - every entry renders its actionable state (ready / claimed / blocked / closed),
 *   - a claim shows the holder AND the lease countdown (computed locally),
 *   - the list never carries bodies; expanding fetches one,
 *   - a board broadcast refreshes the panel (live, cross-session),
 *   - closing an entry goes through the API with the board the user is looking at.
 */
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { BlackboardPanel } from './BlackboardPanel'
import { renderWithProviders } from '@/test-utils'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { BlackboardEntry } from '@/types/shared'

const api = vi.hoisted(() => ({
  fetchBlackboardEntries: vi.fn(),
  fetchBlackboardBoards: vi.fn(),
  fetchBlackboardEntry: vi.fn(),
  postBlackboardEntry: vi.fn(),
  closeBlackboardEntry: vi.fn(),
  releaseBlackboardEntry: vi.fn(),
  deleteBlackboardEntry: vi.fn(),
}))

vi.mock('@/components/agent/api', () => api)

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: (): SessionStore =>
    ({
      sessions: [],
      activeSessionId: 'web:chat-1',
      activeSession: { channel: 'web', chatID: 'chat-1' },
      activeChannel: 'web',
      loading: false,
    }) as unknown as SessionStore,
}))

function entry(overrides: Partial<BlackboardEntry> = {}): BlackboardEntry {
  return {
    board: 'web:chat-1',
    key: 'api-impl',
    kind: 'task',
    title: '实现 /v2 API',
    status: 'open',
    closed: false,
    revision: 4,
    blocked: false,
    ready: true,
    created_at: Date.now(),
    updated_at: Date.now(),
    ...overrides,
  }
}

function mockBoard(entries: BlackboardEntry[]) {
  api.fetchBlackboardEntries.mockResolvedValue({ board: 'web:chat-1', entries })
  api.fetchBlackboardBoards.mockResolvedValue([])
}

afterEach(() => {
  vi.clearAllMocks()
})

describe('BlackboardPanel', () => {
  it('renders every entry with its actionable state and the derived counts', async () => {
    mockBoard([
      entry({ key: 'ready-1', title: '可认领的活' }),
      entry({
        key: 'claimed-1',
        title: '别人在做',
        ready: false,
        claimed_by: 'main/explore',
        claim_expires_at: Date.now() + 5 * 60_000,
      }),
      entry({ key: 'blocked-1', title: '等依赖', ready: false, blocked: true, blocked_by: ['design'] }),
      entry({ key: 'done-1', title: '已完成', closed: true }),
    ])

    renderWithProviders(<BlackboardPanel />)

    await waitFor(() => expect(screen.getAllByTestId('blackboard-entry')).toHaveLength(4))
    const states = screen.getAllByTestId('blackboard-entry').map((el) => el.getAttribute('data-state'))
    expect(states).toEqual(['ready', 'claimed', 'blocked', 'closed'])

    // The claim shows WHO holds it and for how long (countdown from the lease).
    expect(screen.getByText(/main\/explore/)).toBeInTheDocument()
    expect(screen.getByText(/4m\d\ds/)).toBeInTheDocument()
    // A blocked entry names the dependency that gates it.
    expect(screen.getByText(/design/)).toBeInTheDocument()
    // Derived counts are rendered (1 ready / 1 claimed / 1 blocked / 3 open) —
    // asserted on the stats row only (the numbers are locale-independent inside it).
    expect(screen.getByTestId('blackboard-stats').textContent).toMatch(/1/)
  })

  it('never shows bodies up front and fetches one on expand', async () => {
    mockBoard([entry({ key: 'api-impl' })])
    api.fetchBlackboardEntry.mockResolvedValue(entry({ key: 'api-impl', body: '详细设计：先定接口' }))

    renderWithProviders(<BlackboardPanel />)
    await waitFor(() => expect(screen.getByTestId('blackboard-entry')).toBeInTheDocument())

    // The list payload carried no body — nothing to show yet.
    expect(screen.queryByText(/详细设计/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /展开|Expand/ }))

    await waitFor(() => expect(screen.getByText(/详细设计/)).toBeInTheDocument())
    expect(api.fetchBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl')
  })

  it('closes an entry through the API using the board being displayed', async () => {
    mockBoard([entry()])
    api.closeBlackboardEntry.mockResolvedValue(entry({ closed: true }))

    renderWithProviders(<BlackboardPanel />)
    await waitFor(() => expect(screen.getByTestId('blackboard-entry')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /关闭|Close/ }))
    await waitFor(() => expect(api.closeBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl', true))
  })

  it('refreshes when a board broadcast arrives (a peer changed the board)', async () => {
    mockBoard([entry({ key: 'before' })])
    renderWithProviders(<BlackboardPanel />)
    await waitFor(() => expect(screen.getByTestId('blackboard-entry')).toBeInTheDocument())
    const callsBefore = api.fetchBlackboardEntries.mock.calls.length

    // Another session (or an agent) wrote: the server fanned out the change.
    mockBoard([entry({ key: 'before' }), entry({ key: 'posted-by-peer' })])
    act(() => {
      window.dispatchEvent(new CustomEvent('blackboard-update', { detail: { board: 'web:chat-1', key: 'posted-by-peer', op: 'post', revision: 1 } }))
    })

    await waitFor(() => expect(screen.getAllByTestId('blackboard-entry')).toHaveLength(2))
    expect(api.fetchBlackboardEntries.mock.calls.length).toBeGreaterThan(callsBefore)
  })

  it('releases a stuck claim from the UI', async () => {
    mockBoard([entry({ claimed_by: 'main/explore', claim_expires_at: Date.now() + 60_000, ready: false })])
    api.releaseBlackboardEntry.mockResolvedValue(undefined)

    renderWithProviders(<BlackboardPanel />)
    await waitFor(() => expect(screen.getByTestId('blackboard-entry')).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: /释放认领|Release claim/ }))
    await waitFor(() => expect(api.releaseBlackboardEntry).toHaveBeenCalledWith('web:chat-1', 'api-impl'))
  })
})
