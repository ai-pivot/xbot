import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { normalizeCanonicalSessionTree, normalizeSessionTree, useSessionStoreImpl } from './useSessionStore'
import {
  lastSeqCache,
  messagesCache,
  progressSnapshotCache,
  SESSION_TREE_CACHE_KEY,
  sessionCacheKey,
} from '@/lib/webCache'
import type { SessionInfo, WSMessage } from '@/types/shared'

let sessionHandler: ((event: { channel?: string; chat_id?: string; session_key?: string; action?: string; role?: string; instance?: string; parent_id?: string }) => void) | null = null
let messageHandler: ((event: WSMessage) => void) | null = null

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    connected: true,
    subscribe: vi.fn(),
    disconnect: vi.fn(),
    rpc: vi.fn(),
    onSession: vi.fn((handler) => {
      sessionHandler = handler
      return vi.fn()
    }),
    onMessage: vi.fn((handler) => {
      messageHandler = handler
      return vi.fn()
    }),
    chatID: null,
    channel: null,
  }),
}))

vi.mock('@/lib/api', () => ({
  postAPI: async (endpoint: string, body: Record<string, unknown> = {}) => {
    let target = endpoint
    if (endpoint === '/api/session-tree') {
      let response = await fetch('/api/chats', { method: 'POST', body: JSON.stringify(body) })
      if (!response.ok) response = await fetch('/api/session-tree', { method: 'POST', body: JSON.stringify(body) })
      const raw = await response.json()
      const data = raw.data ?? raw
      return {
        sessions: data.sessions ?? data.chats ?? [],
        orphan_subagents: data.orphan_subagents ?? [],
      }
    }
    if (endpoint === '/api/chats/create') target = '/api/chats'
    if (endpoint.endsWith('/switch')) {
      const channel = typeof body.channel === 'string' ? body.channel : 'web'
      target = `${endpoint}?channel=${encodeURIComponent(channel)}`
    }
    const response = await fetch(target, { method: 'POST', body: JSON.stringify(body) })
    if (!response.ok) throw new Error(`request failed: ${response.status}`)
    const raw = await response.json()
    return raw.data ?? raw
  },
}))

beforeEach(() => {
  sessionHandler = null
  messageHandler = null
  const store = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: vi.fn((key: string) => store.get(key) ?? null),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  })
})

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('normalizeSessionTree', () => {
  it('keeps canonical session trees as backend-authored children only', () => {
    const tree = normalizeCanonicalSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'review',
            role: 'review',
            instance: '1',
            parent_channel: 'cli',
            parent_chat_id: '/repo:Agent-main',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
      {
        chat_id: 'cli:/repo:Agent-main/fix:1',
        channel: 'agent',
        type: 'agent',
        label: 'fix',
        parent_channel: 'cli',
        parent_chat_id: '/repo:Agent-main',
        last_active: '2026-07-08T00:00:02Z',
      },
    ] as unknown as Parameters<typeof normalizeCanonicalSessionTree>[0])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    expect(tree.mainSessions[0].children?.map((s) => s.chatID)).toEqual([
      'cli:/repo:Agent-main/review:1',
      'cli:/repo:Agent-main/fix:1',
    ])
    expect(tree.agents.map((s) => s.chatID)).toEqual([
      'cli:/repo:Agent-main/review:1',
      'cli:/repo:Agent-main/fix:1',
    ])
  })

  it('uses only top-level sessions as main rows and direct children as SubAgents', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: 'parent',
        channel: 'cli',
        label: 'parent',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'cli:parent/review:1',
            label: 'review',
            last_active: '2026-07-08T00:00:01Z',
            children: [
              {
                chat_id: 'agent:cli:parent/review:1/fix:1',
                label: 'fix',
                last_active: '2026-07-08T00:00:02Z',
              },
            ],
          },
        ],
      } as unknown as Parameters<typeof normalizeSessionTree>[0][number],
    ])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['parent'])
    expect(tree.agents.map((s) => s.chatID)).toEqual([
      'cli:parent/review:1',
      'agent:cli:parent/review:1/fix:1',
    ])
    expect(tree.agents[1].parentChannel).toBe('agent')
    expect(tree.agents[1].parentChatID).toBe('cli:parent/review:1')
  })

  it('uses backend SubAgent role instead of default labels', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'default',
            role: 'review',
            instance: '1',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])

    expect(tree.agents).toHaveLength(1)
    expect(tree.agents[0].role).toBe('review')
    expect(tree.agents[0].instance).toBe('1')
    expect(tree.agents[0].label).toBe('review/1')
    expect(tree.agents[0].agentChatID).toBe('cli:/repo:Agent-main/review:1')
  })

  it('uses role and instance instead of preview-derived SubAgent labels', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'review: checking files',
            role: 'review',
            instance: '1',
            preview: 'checking files',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])

    expect(tree.agents[0].label).toBe('review/1')
    expect(tree.agents[0].preview).toBe('checking files')
  })

  it('uses explicit backend parent fields for SubAgent placement', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'review',
            parent_channel: 'cli',
            parent_chat_id: '/old-parent',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])

    expect(tree.agents[0].parentChannel).toBe('cli')
    expect(tree.agents[0].parentChatID).toBe('/old-parent')
  })

  it('uses explicit full_key as the SubAgent identity while preserving backend parent fields', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'short-row-id',
            full_key: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'default',
            parent_channel: 'web',
            parent_chat_id: 'stale',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])

    expect(tree.agents[0].chatID).toBe('cli:/repo:Agent-main/review:1')
    expect(tree.agents[0].fullKey).toBe('cli:/repo:Agent-main/review:1')
    expect(tree.agents[0].agentChatID).toBe('cli:/repo:Agent-main/review:1')
    expect(tree.agents[0].parentChannel).toBe('web')
    expect(tree.agents[0].parentChatID).toBe('stale')
    expect(tree.agents[0].label).toBe('review/1')
  })

  it('matches nested SubAgent parents by full_key aliases', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'row-review',
            full_key: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'review',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ], [
      {
        chat_id: 'agent:cli:/repo:Agent-main/review:1/fix:2',
        channel: 'agent',
        type: 'agent',
        label: 'fix',
        parent_channel: 'agent',
        parent_chat_id: 'cli:/repo:Agent-main/review:1',
        last_active: '2026-07-08T00:00:02Z',
      },
    ])

    const review = tree.mainSessions[0].children?.[0]
    expect(review?.chatID).toBe('cli:/repo:Agent-main/review:1')
    expect(review?.children?.map((s) => s.chatID)).toEqual([
      'agent:cli:/repo:Agent-main/review:1/fix:2',
    ])
  })

  it('indexes backend-attached SubAgent children before attaching top-level nested rows', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'row-review',
            full_key: 'cli:/repo:Agent-main/review:1',
            channel: 'agent',
            type: 'agent',
            label: 'default',
            role: 'review',
            instance: '1',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
      {
        chat_id: 'agent:cli:/repo:Agent-main/review:1/fix:2',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        parent_channel: 'agent',
        parent_chat_id: 'cli:/repo:Agent-main/review:1',
        role: 'fix',
        instance: '2',
        last_active: '2026-07-08T00:00:02Z',
      },
    ])

    expect(tree.mainSessions).toHaveLength(1)
    const review = tree.mainSessions[0].children?.[0]
    expect(review?.label).toBe('review/1')
    expect(review?.children?.map((s) => [s.chatID, s.label])).toEqual([
      ['agent:cli:/repo:Agent-main/review:1/fix:2', 'fix/2'],
    ])
    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
  })

  it('attaches orphan SubAgents to an existing parent when backend returns parent metadata', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
      },
    ], [
      {
        chat_id: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        type: 'agent',
        label: 'review',
        parent_channel: 'cli',
        parent_chat_id: '/repo:Agent-main',
        last_active: '2026-07-08T00:00:01Z',
      },
    ])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    expect(tree.agents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(tree.mainSessions[0].children?.map((s) => s.label)).toEqual(['review/1'])
  })

  it('attaches CLI SubAgents by TUI session-name alias when parent metadata is short', () => {
    const tree = normalizeCanonicalSessionTree([
      {
        chat_id: '/repo/project:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
      },
    ], [
      {
        chat_id: 'cli:Agent-main/review:oneshot-1',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        parent_channel: 'cli',
        parent_chat_id: 'Agent-main',
        role: 'review',
        instance: 'oneshot-1',
        last_active: '2026-07-08T00:00:01Z',
      },
    ])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo/project:Agent-main'])
    expect(tree.mainSessions[0].children?.map((s) => [s.chatID, s.label])).toEqual([
      ['cli:Agent-main/review:oneshot-1', 'review/oneshot-1'],
    ])
    expect(tree.agents.map((s) => s.label)).toEqual(['review/oneshot-1'])
  })

  it('synthesizes a parent for historical orphan SubAgents when the parent session is absent', () => {
    const tree = normalizeSessionTree([], [
      {
        chat_id: 'cli:/repo:Agent-deleted/review:1',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        parent_channel: 'cli',
        parent_chat_id: '/repo:Agent-deleted',
        role: 'review',
        instance: '1',
        last_active: '2026-07-08T00:00:01Z',
      },
    ])

    expect(tree.mainSessions.map((s) => [s.channel, s.chatID, s.label, s.synthetic])).toEqual([
      ['cli', '/repo:Agent-deleted', 'Agent-deleted', true],
    ])
    expect(tree.agents.map((s) => [s.chatID, s.label])).toEqual([
      ['cli:/repo:Agent-deleted/review:1', 'review/1'],
    ])
  })

  it('synthesizes a canonical parent for supplemental SubAgents instead of exposing them as main sessions', () => {
    const tree = normalizeCanonicalSessionTree([], [
      {
        chat_id: 'web:chat_123/review:1',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        parent_channel: 'web',
        parent_chat_id: 'chat_123',
        role: 'review',
        instance: '1',
        last_active: '2026-07-08T00:00:01Z',
      },
    ])

    expect(tree.mainSessions.map((s) => [s.channel, s.chatID, s.synthetic])).toEqual([
      ['web', 'chat_123', true],
    ])
    expect(tree.agents.map((s) => [s.channel, s.chatID, s.label])).toEqual([
      ['agent', 'web:chat_123/review:1', 'review/1'],
    ])
  })

  it('keeps orphan SubAgents with unknown missing parents out of the main list', () => {
    const tree = normalizeSessionTree([], [
      {
        chat_id: 'agent:feishu:oc_x/review:1/fix:2',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        parent_channel: 'agent',
        parent_chat_id: 'feishu:oc_x/review:1',
        last_active: '2026-07-08T00:00:02Z',
      },
    ])

    expect(tree.mainSessions).toEqual([])
    expect(tree.agents).toEqual([])
  })

  it('attaches top-level agent rows when the full key carries parent metadata', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
      },
      {
        chat_id: 'cli:/repo:Agent-main/review:1',
        channel: 'agent',
        type: 'agent',
        label: 'default',
        last_active: '2026-07-08T00:00:00Z',
      },
    ])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    expect(tree.agents.map((s) => s.label)).toEqual(['review/1'])
  })

  it('attaches raw rows whose chatID is a full SubAgent key', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
      },
      {
        chat_id: 'cli:/repo:Agent-main/review:1',
        channel: 'cli',
        label: 'default',
        last_active: '2026-07-08T00:00:01Z',
      },
    ])

    expect(tree.mainSessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    expect(tree.agents.map((s) => [s.channel, s.chatID, s.label, s.parentChannel])).toEqual([
      ['agent', 'cli:/repo:Agent-main/review:1', 'review/1', 'cli'],
    ])
  })

  it('attaches nested SubAgents when the parent full-key row arrived with a non-agent channel', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
      },
      {
        chat_id: 'cli:/repo:Agent-main/review:1',
        channel: 'cli',
        label: 'default',
        last_active: '2026-07-08T00:00:01Z',
      },
      {
        chat_id: 'agent:cli:/repo:Agent-main/review:1/fix:2',
        channel: 'agent',
        label: 'default',
        last_active: '2026-07-08T00:00:02Z',
      },
    ])

    expect(tree.mainSessions).toHaveLength(1)
    const review = tree.mainSessions[0].children?.[0]
    expect(review?.channel).toBe('agent')
    expect(review?.chatID).toBe('cli:/repo:Agent-main/review:1')
    expect(review?.children?.map((s) => s.chatID)).toEqual([
      'agent:cli:/repo:Agent-main/review:1/fix:2',
    ])
  })

  it('keeps weak role-only SubAgent rows out of the main session list', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: 'review:1',
        channel: 'web',
        label: 'default',
        role: 'review',
        instance: '1',
        last_active: '2026-07-08T00:00:00Z',
      },
    ])

    expect(tree.mainSessions).toEqual([])
    expect(tree.agents).toEqual([])
  })

  it('uses a non-default fallback label for weak SubAgent child rows', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/repo:Agent-main',
        channel: 'cli',
        label: 'Agent-main',
        last_active: '2026-07-08T00:00:00Z',
        children: [
          {
            chat_id: 'review-1',
            channel: 'agent',
            type: 'agent',
            label: 'default',
            last_active: '2026-07-08T00:00:01Z',
          },
        ],
      },
    ])

    expect(tree.mainSessions[0].children?.map((s) => s.label)).toEqual(['review-1'])
    expect(tree.agents.map((s) => s.label)).toEqual(['review-1'])
  })

  it('shows the TUI session name for default-labeled CLI main sessions', () => {
    const tree = normalizeSessionTree([
      {
        chat_id: '/vePFS-Mindverse/user/intern/yihang:Agent-warm-stone',
        channel: 'cli',
        label: 'default',
        last_active: '2026-07-08T00:00:00Z',
      },
    ])

    expect(tree.mainSessions).toHaveLength(1)
    expect(tree.mainSessions[0].label).toBe('Agent-warm-stone')
    expect(tree.agents).toEqual([])
  })

  it('stores structured questions from a live CLI AskUser event', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [{
              chat_id: '/repo',
              channel: 'cli',
              label: 'repo',
              last_active: '2026-07-08T00:00:00Z',
              is_current: true,
            }],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    }))
    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.sessions).toHaveLength(1))

    act(() => {
      messageHandler?.({
        type: 'ask_user',
        channel: 'cli',
        chat_id: '/repo',
        progress: {
          request_id: 'request-1',
          questions: [{ question: 'Continue?', options: ['yes', 'no'] }],
        },
      })
    })

    expect(result.current.sessions[0].status).toBe('waiting_input')
    expect(result.current.askUserPrompts.get('cli:/repo')).toEqual({
      requestId: 'request-1',
      questions: [{ question: 'Continue?', options: ['yes', 'no'] }],
    })
  })

  it('sends the selected channel when renaming and deleting matching chat IDs', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              { chat_id: 'shared', channel: 'web', label: 'web', last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'shared', channel: 'cli', label: 'cli', last_active: '2026-07-08T00:00:01Z' },
            ],
          }),
        } as Response
      }
      if (url === '/api/chats/shared/rename' || url === '/api/chats/shared/delete') {
        return {
          ok: true,
          json: async () => ({ ok: true, data: {}, error: null }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.sessions).toHaveLength(2))
    const cliCacheKey = sessionCacheKey('cli', 'shared')
    messagesCache.set(cliCacheKey, [])
    lastSeqCache.set(cliCacheKey, 9)
    progressSnapshotCache.set(cliCacheKey, { phase: 'tool' })

    await act(async () => {
      expect(await result.current.renameSession('shared', 'cli', 'renamed')).toBe(true)
      expect(await result.current.deleteSession('shared', 'cli')).toBe(true)
    })

    const renameCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/rename'))
    const deleteCall = fetchMock.mock.calls.find(([input]) => String(input).endsWith('/delete'))
    expect(JSON.parse(String(renameCall?.[1]?.body))).toEqual({ channel: 'cli', label: 'renamed' })
    expect(JSON.parse(String(deleteCall?.[1]?.body))).toEqual({ channel: 'cli' })
    expect(messagesCache.has(cliCacheKey)).toBe(false)
    expect(lastSeqCache.has(cliCacheKey)).toBe(false)
    expect(progressSnapshotCache.has(cliCacheKey)).toBe(false)
  })

  it('uses /api/chats as the authoritative SubAgent tree source', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    role: 'review',
                    instance: '1',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    last_active: '2026-07-08T00:00:01Z',
                    running: true,
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(result.current.subAgents[0].running).toBe(true)
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session-tree')
    expect(fetchMock).not.toHaveBeenCalledWith('/api/subagents')
  })

  it('prefers /api/chats sessions tree over compatibility chats', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: 'stale-flat',
                channel: 'cli',
                label: 'stale flat row',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            sessions: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    role: 'review',
                    instance: '1',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
  })

  it('ignores compatibility chats when canonical sessions are present', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: 'stale-main',
                channel: 'cli',
                label: 'stale-main',
                last_active: '2026-07-08T00:00:00Z',
              },
              {
                chat_id: 'cli:/repo:Agent-main/review:1',
                channel: 'web',
                label: 'default',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
            sessions: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    role: 'review',
                    instance: '1',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.sessions[0].children?.map((s) => s.label)).toEqual(['review/1'])
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
  })

  it('ignores compatibility SubAgent rows when canonical sessions omit children', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: '/repo/project:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            chats: [
              {
                chat_id: '/repo/project:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
              {
                chat_id: 'cli:Agent-main/review:oneshot-1',
                channel: 'agent',
                type: 'agent',
                label: 'default',
                parent_channel: 'cli',
                parent_chat_id: 'Agent-main',
                role: 'review',
                instance: 'oneshot-1',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo/project:Agent-main'])
    })
    expect(result.current.sessions[0].children ?? []).toEqual([])
    expect(result.current.subAgents).toEqual([])
  })

  it('attaches orphan SubAgents returned by /api/chats', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            orphan_subagents: [
              {
                chat_id: 'cli:/repo:Agent-main/review:1',
                channel: 'agent',
                type: 'agent',
                label: 'default',
                parent_channel: 'cli',
                parent_chat_id: '/repo:Agent-main',
                role: 'review',
                instance: '1',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.sessions[0].children?.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(fetchMock).not.toHaveBeenCalledWith('/api/session-tree')
  })

  it('attaches orphan SubAgents when /api/chats returns canonical sessions', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            orphan_subagents: [
              {
                chat_id: 'cli:/repo:Agent-main/review:1',
                channel: 'agent',
                type: 'agent',
                label: 'default',
                parent_channel: 'cli',
                parent_chat_id: '/repo:Agent-main',
                role: 'review',
                instance: '1',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.sessions[0].children?.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
    expect(fetchMock).not.toHaveBeenCalledWith('/api/subagents')
  })

  it('does not attach Web-only /api/subagents rows under canonical sessions', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            subagents: [
              {
                chat_id: 'cli:/repo:Agent-main/review:1',
                channel: 'agent',
                type: 'agent',
                label: 'default',
                parent_channel: 'cli',
                parent_chat_id: '/repo:Agent-main',
                role: 'review',
                instance: '1',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.sessions[0].children ?? []).toEqual([])
    expect(result.current.subAgents).toEqual([])
    expect(fetchMock).not.toHaveBeenCalledWith('/api/subagents')
  })

  it('does not synthesize visible sessions from /api/subagents supplemental rows', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            subagents: [
              {
                chat_id: 'ordinary-main',
                channel: 'web',
                label: 'ordinary main',
                last_active: '2026-07-08T00:00:00Z',
              },
              {
                chat_id: 'web:chat_123/review:1',
                channel: 'agent',
                type: 'agent',
                label: 'default',
                parent_channel: 'web',
                parent_chat_id: 'chat_123',
                role: 'review',
                instance: '1',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions).toEqual([])
    })
    expect(result.current.subAgents).toEqual([])
    expect(fetchMock).not.toHaveBeenCalledWith('/api/subagents')
  })

  it('falls back to /api/session-tree when /api/chats is unavailable', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return { ok: false, json: async () => ({ ok: false }) } as Response
      }
      if (url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'default',
                    role: 'review',
                    instance: '1',
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.sessions[0].children?.map((s) => s.label)).toEqual(['review/1'])
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
  })

  it('preserves the selected active session across background refreshes', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: 'first',
                channel: 'cli',
                label: 'first',
                is_current: true,
                last_active: '2026-07-08T00:00:00Z',
              },
              {
                chat_id: 'second',
                channel: 'cli',
                label: 'second',
                last_active: '2026-07-08T00:00:01Z',
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/chats/second/switch?channel=cli' && init?.method === 'POST') {
        return { ok: true, json: async () => ({ ok: true, chat_id: 'second', channel: 'cli' }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'first' })
    })

    await act(async () => {
      await result.current.switchSession('second', 'cli')
    })
    expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'second' })
    const cached = JSON.parse(localStorage.getItem(SESSION_TREE_CACHE_KEY) ?? '{}') as {
      sessions?: Array<{ chatID: string; isCurrent?: boolean }>
    }
    expect(cached.sessions?.find((session) => session.chatID === 'first')?.isCurrent).toBe(false)
    expect(cached.sessions?.find((session) => session.chatID === 'second')?.isCurrent).toBe(true)

    await act(async () => {
      await result.current.refresh()
    })

    expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'second' })
    expect(result.current.sessions.find((s) => s.chatID === 'second')?.isCurrent).toBe(true)
  })

  it('keeps the latest session switch when REST responses resolve out of order', async () => {
    let resolveA!: (response: Response) => void
    let resolveB!: (response: Response) => void
    const responseA = new Promise<Response>((resolve) => { resolveA = resolve })
    const responseB = new Promise<Response>((resolve) => { resolveB = resolve })
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              { chat_id: 'initial', channel: 'cli', label: 'initial', is_current: true, last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'session-a', channel: 'cli', label: 'A', last_active: '2026-07-08T00:00:01Z' },
              { chat_id: 'session-b', channel: 'cli', label: 'B', last_active: '2026-07-08T00:00:02Z' },
            ],
          }),
        } as Response
      }
      if (url === '/api/chats/session-a/switch?channel=cli') return responseA
      if (url === '/api/chats/session-b/switch?channel=cli') return responseB
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'initial' }))

    let switchA!: Promise<void>
    let switchB!: Promise<void>
    act(() => {
      switchA = result.current.switchSession('session-a', 'cli')
      switchB = result.current.switchSession('session-b', 'cli')
    })
    await act(async () => {
      resolveB({
        ok: true,
        json: async () => ({ ok: true, data: { chat_id: 'session-b', channel: 'cli' }, error: null }),
      } as Response)
      await switchB
    })
    expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'session-b' })

    await act(async () => {
      resolveA({
        ok: true,
        json: async () => ({ ok: true, data: { chat_id: 'session-a', channel: 'cli' }, error: null }),
      } as Response)
      await switchA
    })
    expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'session-b' })
  })

  it('keeps session object identity when a background refresh returns the same tree', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: 'stable',
                channel: 'cli',
                label: 'stable',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:stable/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review/1',
                    role: 'review',
                    instance: '1',
                    parent_channel: 'cli',
                    parent_chat_id: 'stable',
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['stable'])
    })
    const firstSessions = result.current.sessions
    const firstSubAgents = result.current.subAgents

    await act(async () => {
      await result.current.refresh()
    })

    expect(result.current.sessions).toBe(firstSessions)
    expect(result.current.subAgents).toBe(firstSubAgents)
  })

  it('keeps synthesized SubAgent parents out of active session selection', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                synthetic: true,
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
              {
                chat_id: 'normal',
                channel: 'cli',
                label: 'normal',
                last_active: '2026-07-08T00:00:02Z',
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.find((s) => s.chatID === '/repo:Agent-main')?.synthetic).toBe(true)
    })
    expect(result.current.activeSession).toEqual({ channel: 'cli', chatID: 'normal' })
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:1'])
  })

  it('shows SubAgent lifecycle rows immediately before canonical refresh catches up', async () => {
    let includeChild = false
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: includeChild ? [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:runtime-1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    role: 'review',
                    instance: 'runtime-1',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    running: true,
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ] : undefined,
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })
    expect(result.current.subAgents).toEqual([])

    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: '/repo:Agent-main',
        session_key: 'cli:/repo:Agent-main/review:runtime-1',
        role: 'stale-role',
        instance: 'stale-instance',
      })
    })
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:runtime-1'])
    expect(result.current.sessions[0].children?.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:runtime-1'])
    const transient = result.current.subAgents.find((s) => s.chatID === 'cli:/repo:Agent-main/review:runtime-1')
    expect(transient?.running).toBe(true)
    expect(transient?.label).toBe('review/runtime-1')

    includeChild = true
    await act(async () => {
      await result.current.refresh()
    })

    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review:runtime-1'])
    const started = result.current.subAgents.find((s) => s.chatID === 'cli:/repo:Agent-main/review:runtime-1')
    expect(started?.running).toBe(true)
    expect(started?.label).toBe('review/runtime-1')
  })

  it('keeps short-lived SubAgent rows when delayed canonical refresh has not persisted them yet', async () => {
    let fetchCount = 0
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        fetchCount++
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
          }),
        } as Response
      }
      if (url === '/api/subagents') {
        return { ok: true, json: async () => ({ ok: true, subagents: [] }) } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['/repo:Agent-main'])
    })

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: '/repo:Agent-main',
        role: 'review',
      })
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(fetchCount).toBe(1)
    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review'])

    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })

    expect(result.current.subAgents.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review'])
    expect(result.current.sessions[0].children?.map((s) => s.chatID)).toEqual(['cli:/repo:Agent-main/review'])
  })

  it('updates existing SubAgent running state immediately on lifecycle events', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            chats: [
              {
                chat_id: '/repo:Agent-main',
                channel: 'cli',
                label: 'Agent-main',
                last_active: '2026-07-08T00:00:00Z',
                children: [
                  {
                    chat_id: 'cli:/repo:Agent-main/review:1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'review',
                    role: 'review',
                    instance: '1',
                    parent_channel: 'cli',
                    parent_chat_id: '/repo:Agent-main',
                    running: false,
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ],
              },
            ],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.subAgents[0]?.running).toBe(false)
    })

    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: '/repo:Agent-main',
        role: 'review',
        instance: '1',
      })
    })

    expect(result.current.subAgents[0]?.running).toBe(true)
    expect(result.current.sessions[0].children?.[0].status).toBe('running')

    await act(async () => {
      sessionHandler?.({
        action: 'subagent_stopped',
        channel: 'cli',
        chat_id: '/repo:Agent-main',
        role: 'review',
        instance: '1',
      })
    })

    expect(result.current.subAgents).toEqual([])
    expect(result.current.sessions[0].children ?? []).toEqual([])
  })

  it('uses session_key to stop only the matching SubAgent', async () => {
    const child = (parentChatID: string): SessionInfo => ({
      chatID: `cli:${parentChatID}/review:1`,
      channel: 'agent',
      label: 'review/1',
      lastActive: '2026-07-08T00:00:01Z',
      preview: '',
      status: 'running',
      isCurrent: false,
      type: 'agent',
      role: 'review',
      instance: '1',
      parentChannel: 'cli',
      parentChatID,
      fullKey: `cli:${parentChatID}/review:1`,
      agentChatID: `cli:${parentChatID}/review:1`,
      running: true,
      children: [],
    })
    const childA = child('/repo-a:Agent-main')
    const childB = child('/repo-b:Agent-main')
    const parent = (chatID: string, agent: SessionInfo): SessionInfo => ({
      chatID,
      channel: 'cli',
      label: chatID,
      lastActive: '2026-07-08T00:00:00Z',
      preview: '',
      status: 'idle',
      isCurrent: false,
      type: 'main',
      children: [agent],
    })
    localStorage.setItem('xbot_session_tree', JSON.stringify({
      version: 1,
      sessions: [parent('/repo-a:Agent-main', childA), parent('/repo-b:Agent-main', childB)],
      subAgents: [childA, childB],
    }))
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => undefined)))

    const { result, unmount } = renderHook(() => useSessionStoreImpl())
    expect(result.current.subAgents).toHaveLength(2)

    await act(async () => {
      sessionHandler?.({
        action: 'subagent_stopped',
        channel: 'cli',
        chat_id: '/repo-a:Agent-main',
        session_key: childA.chatID,
        role: 'review',
        instance: '1',
      })
    })

    expect(result.current.subAgents.map((agent) => agent.chatID)).toEqual([childB.chatID])
    unmount()
  })

  it('renders the cached session tree before the background refresh resolves', () => {
    localStorage.setItem('xbot_session_tree', JSON.stringify({
      version: 1,
      sessions: [{
        chatID: 'cached-chat',
        channel: 'web',
        label: 'Cached chat',
        lastActive: '2026-07-13T00:00:00Z',
        preview: 'cached preview',
        status: 'idle',
        isCurrent: true,
      }],
      subAgents: [],
    }))
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => undefined)))

    const { result, unmount } = renderHook(() => useSessionStoreImpl())

    expect(result.current.sessions.map((session) => session.chatID)).toEqual(['cached-chat'])
    expect(result.current.activeSession).toEqual({ channel: 'web', chatID: 'cached-chat' })
    unmount()
  })
})
