import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { normalizeCanonicalSessionTree, normalizeSessionTree, useSessionStoreImpl } from './useSessionStore'
import {
  lastSeqCache,
  loadSessionTreeCache,
  progressSnapshotCache,
  SESSION_TREE_CACHE_KEY,
  sessionCacheKey,
} from '@/lib/webCache'
import type { SessionInfo, WSMessage } from '@/types/shared'

let sessionHandler: ((event: { channel?: string; chat_id?: string; session_key?: string; action?: string; role?: string; instance?: string; parent_id?: string; removed?: boolean }) => void) | null = null
let messageHandler: ((event: WSMessage) => void) | null = null

const wsMocks = vi.hoisted(() => ({
  rpc: vi.fn(),
}))

vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    connected: true,
    subscribe: vi.fn(),
    disconnect: vi.fn(),
    rpc: wsMocks.rpc,
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
  wsMocks.rpc.mockReset()
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
      questions: [{ question: 'Continue?', options: ['yes', 'no'], multiSelect: false, allowOther: false }],
    })
  })

  it('propagates multi_select / allow_other from the backend snake_case fields', async () => {
    // The backend serializes AskUserQuestion as `multi_select` / `allow_other`
    // (protocol/events.go JSON tags). The frontend AskUserPrompt uses camelCase
    // (multiSelect / allowOther) — the parse layer MUST map them, otherwise
    // AskUserPanel never renders the multi-select checkboxes / Other toggle.
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
          request_id: 'request-2',
          questions: [
            { question: 'Pick', options: ['a', 'b'], multi_select: true },
            { question: 'Color', options: ['red'], allow_other: true },
          ],
        },
      })
    })

    const prompt = result.current.askUserPrompts.get('cli:/repo')
    expect(prompt?.questions).toEqual([
      { question: 'Pick', options: ['a', 'b'], multiSelect: true, allowOther: false },
      { question: 'Color', options: ['red'], multiSelect: false, allowOther: true },
    ])
  })

  it('keeps options when a question has no question text (only allow_other + options)', async () => {
    // Real incident: the LLM emitted a question with NO `question` field but
    // `allow_other` + a list of options. The old parse layer did
    // `if (!question) continue`, dropping the whole question — the options
    // vanished from the panel. A question with options must survive even when
    // the prompt text is empty (the panel renders options and skips the title).
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
          request_id: 'request-no-question',
          questions: [
            { allow_other: true, options: ['Fix A+B', 'Fix A only', 'No change yet'] },
          ],
        },
      })
    })

    const prompt = result.current.askUserPrompts.get('cli:/repo')
    expect(prompt?.questions).toEqual([
      { question: '', options: ['Fix A+B', 'Fix A only', 'No change yet'], multiSelect: false, allowOther: true },
    ])
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

  it('keeps a running SubAgent visible across refresh when backend has not persisted it', async () => {
    // One-shot SubAgents are destroyed (interactiveSubAgents deleted + DB CASCADE)
    // immediately on completion. The 500ms canonical refresh may therefore return
    // a tree that does NOT contain the SubAgent. The transient entry must survive
    // so the running SubAgent stays visible in the sidebar.
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: 'web-chat-1',
                channel: 'web',
                label: 'My Chat',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            chats: [
              {
                chat_id: 'web-chat-1',
                channel: 'web',
                label: 'My Chat',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            orphan_subagents: [],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })

    // SubAgent starts (web channel, UUID chat_id, with session_key — the real one-shot path)
    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'web',
        chat_id: 'web-chat-1',
        session_key: 'web:web-chat-1/explore:oneshot-1',
        role: 'explore',
        instance: 'oneshot-1',
        parent_id: 'web-chat-1',
      })
      await Promise.resolve()
      await Promise.resolve()
    })

    // Transient SubAgent should be visible immediately
    expect(result.current.subAgents).toHaveLength(1)
    expect(result.current.subAgents[0]?.running).toBe(true)
    expect(result.current.sessions[0].children).toHaveLength(1)
    expect(result.current.sessions[0].children?.[0].running).toBe(true)

    // 500ms canonical refresh fires — backend tree does NOT contain the SubAgent
    // (one-shot destroyed, tenant not yet persisted). The transient must survive.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })

    // BUG: SubAgent disappears after refresh because mergeStatus doesn't carry
    // over running status for SubAgents (they're not in executingSessionsRef).
    expect(result.current.subAgents).toHaveLength(1)
    expect(result.current.subAgents[0]?.running).toBe(true)
  })

  it('keeps a running SubAgent visible when canonical refresh reports it as idle', async () => {
    // The backend's IsProcessingByChannel checks chatCancelCh — but one-shot
    // SubAgents don't register there. So /api/session-tree may return the
    // SubAgent row with running=false even while it's actively running.
    // mergeStatus must carry over the SSE-driven running=true.
    let includeChild = false
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: 'web-chat-1',
                channel: 'web',
                label: 'My Chat',
                last_active: '2026-07-08T00:00:00Z',
                children: includeChild ? [
                  {
                    chat_id: 'web:web-chat-1/explore:oneshot-1',
                    full_key: 'web:web-chat-1/explore:oneshot-1',
                    channel: 'agent',
                    type: 'agent',
                    label: 'explore',
                    role: 'explore',
                    instance: 'oneshot-1',
                    parent_channel: 'web',
                    parent_chat_id: 'web-chat-1',
                    running: false,
                    last_active: '2026-07-08T00:00:01Z',
                  },
                ] : undefined,
              },
            ],
            chats: [
              {
                chat_id: 'web-chat-1',
                channel: 'web',
                label: 'My Chat',
                last_active: '2026-07-08T00:00:00Z',
              },
            ],
            orphan_subagents: [],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())

    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })

    vi.useFakeTimers()
    // SubAgent starts
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'web',
        chat_id: 'web-chat-1',
        session_key: 'web:web-chat-1/explore:oneshot-1',
        role: 'explore',
        instance: 'oneshot-1',
        parent_id: 'web-chat-1',
      })
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(result.current.subAgents[0]?.running).toBe(true)

    // Refresh returns the SubAgent with running=false (backend can't detect
    // one-shot SubAgent processing via IsProcessingByChannel)
    includeChild = true
    await act(async () => {
      await vi.advanceTimersByTimeAsync(500)
    })

    // BUG: mergeStatus doesn't carry running for SubAgents not in executingSessionsRef
    expect(result.current.subAgents[0]?.running).toBe(true)
    expect(result.current.sessions[0].children?.[0]?.running).toBe(true)
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

    expect(result.current.subAgents[0]?.running).toBe(false)
    expect(result.current.subAgents[0]?.status).toBe('idle')
    // SubAgent is retained as idle (not removed) — it stays in the sidebar
    // after completion. Removal is handled by the background tree refresh.
    expect(result.current.sessions[0].children ?? []).toHaveLength(1)
    expect(result.current.sessions[0].children?.[0]?.running).toBe(false)
  })

  it('uses session_key to stop only the matching SubAgent', async () => {
    const child = (parentChatID: string): SessionInfo => ({
      chatID: `cli:${parentChatID}/review:1`,
      channel: 'agent',
      label: 'review/1',
      lastActive: '2026-07-08T00:00:01Z',
      preview: '',
      status: 'idle',
      isCurrent: false,
      type: 'agent',
      role: 'review',
      instance: '1',
      parentChannel: 'cli',
      parentChatID,
      fullKey: `cli:${parentChatID}/review:1`,
      agentChatID: `cli:${parentChatID}/review:1`,
      running: false,
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
    // The localStorage cache seeds STRUCTURE only — volatile running state is
    // stripped on load (a cached busy must not resurrect as stale busy after a
    // page reload). Running state comes from the SSE subagent_started events,
    // matching production where the sidebar learns subagent state live.
    localStorage.setItem('xbot_session_tree', JSON.stringify({
      version: 1,
      sessions: [parent('/repo-a:Agent-main', childA), parent('/repo-b:Agent-main', childB)],
      subAgents: [childA, childB],
    }))
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => undefined)))

    const { result, unmount } = renderHook(() => useSessionStoreImpl())
    expect(result.current.subAgents).toHaveLength(2)

    // Bring both subagents to running via the canonical SSE path — this also
    // timestamps the executing map (mergeStatus trusts SSE busy for the trust
    // window; HTTP corrects past it).
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: '/repo-a:Agent-main',
        session_key: childA.chatID,
        role: 'review',
        instance: '1',
      })
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'cli',
        chat_id: '/repo-b:Agent-main',
        session_key: childB.chatID,
        role: 'review',
        instance: '1',
      })
    })
    expect(result.current.subAgents.find((a) => a.chatID === childA.chatID)?.running).toBe(true)
    expect(result.current.subAgents.find((a) => a.chatID === childB.chatID)?.running).toBe(true)

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

    // childA is stopped via session_key → marked idle (not removed);
    // childB remains running. Both stay in the list.
    expect(result.current.subAgents.map((agent) => agent.chatID)).toEqual([childA.chatID, childB.chatID])
    expect(result.current.subAgents.find((a) => a.chatID === childA.chatID)?.running).toBe(false)
    expect(result.current.subAgents.find((a) => a.chatID === childB.chatID)?.running).toBe(true)
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

  it('createSession defaults the new session model to the current active session model', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      if (url === '/api/chats') {
        // createSession sends { label, model }; the session-tree refresh does not.
        if ('model' in body) {
          return {
            ok: true,
            json: async () => ({ ok: true, data: { chat_id: 'new-chat' }, error: null }),
          } as Response
        }
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [{
              chat_id: 'current-chat',
              channel: 'web',
              label: 'current',
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
    })
    vi.stubGlobal('fetch', fetchMock)
    wsMocks.rpc.mockResolvedValue({ model: 'gpt-4o', subscription_id: 'sub-1', subscription_name: 'Sub 1' })

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.sessions).toHaveLength(1))
    expect(result.current.activeSession).toEqual({ channel: 'web', chatID: 'current-chat' })

    let chatID: string | null = null
    await act(async () => {
      chatID = await result.current.createSession('my-new-session')
    })
    expect(chatID).toBe('new-chat')

    const createCall = fetchMock.mock.calls.find(([_input, init]) => {
      const body = init?.body ? JSON.parse(String(init.body)) : {}
      return body?.label === 'my-new-session'
    })
    expect(createCall).toBeDefined()
    const createBody = JSON.parse(String(createCall?.[1]?.body))
    expect(createBody.label).toBe('my-new-session')
    expect(createBody.model).toBe('gpt-4o')
    // Model-subscription integration: the inherited (subscription_id, model) pair
    // travels together — never a bare model name.
    expect(createBody.subscription_id).toBe('sub-1')
  })

  it('createSession prefers an explicit model param over the current session model', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/chats') {
        const body = init?.body ? JSON.parse(String(init.body)) : {}
        if (body?.model !== undefined) {
          return {
            ok: true,
            json: async () => ({ ok: true, data: { chat_id: 'new-chat' }, error: null }),
          } as Response
        }
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [{
              chat_id: 'current-chat',
              channel: 'web',
              label: 'current',
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
    })
    vi.stubGlobal('fetch', fetchMock)
    wsMocks.rpc.mockResolvedValue({ model: 'current-session-model', subscription_id: 'sub-1' })

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.sessions).toHaveLength(1))

    let chatID: string | null = null
    await act(async () => {
      chatID = await result.current.createSession(undefined, undefined, 'explicit-model')
    })
    expect(chatID).toBe('new-chat')

    const createCall = fetchMock.mock.calls.find(([_input, init]) => {
      try {
        const body = init?.body ? JSON.parse(String(init.body)) : {}
        return body?.model === 'explicit-model'
      } catch {
        return false
      }
    })
    expect(createCall).toBeDefined()
    const createBody = JSON.parse(String(createCall?.[1]?.body))
    expect(createBody.model).toBe('explicit-model')
    // Explicit model without a subscriptionId → empty subscription_id (the
    // backend resolves the owning subscription exactly once).
    expect(createBody.subscription_id).toBe('')
  })

  it('createSession omits the model when the current session model is unavailable', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/chats') {
        const body = init?.body ? JSON.parse(String(init.body)) : {}
        if (body?.model !== undefined) {
          return {
            ok: true,
            json: async () => ({ ok: true, data: { chat_id: 'new-chat' }, error: null }),
          } as Response
        }
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [{
              chat_id: 'current-chat',
              channel: 'web',
              label: 'current',
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
    })
    vi.stubGlobal('fetch', fetchMock)
    // get_context_usage rejects → non-fatal, model stays empty → backend falls back to Balance tier.
    wsMocks.rpc.mockRejectedValue(new Error('not connected'))

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => expect(result.current.sessions).toHaveLength(1))

    let chatID: string | null = null
    await act(async () => {
      chatID = await result.current.createSession()
    })
    expect(chatID).toBe('new-chat')

    const createCall = fetchMock.mock.calls.find(([_input, init]) => {
      try {
        const body = init?.body ? JSON.parse(String(init.body)) : {}
        return body?.label === '' || body?.model !== undefined
      } catch {
        return false
      }
    })
    expect(createCall).toBeDefined()
    const createBody = JSON.parse(String(createCall?.[1]?.body))
    expect(createBody.model).toBe('')
  })
})

describe('sidebar state reconciliation (trust window + lost events)', () => {
  const idleTreeResponse = () => ({
    ok: true,
    json: async () => ({
      ok: true,
      sessions: [
        { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
      ],
      chats: [
        { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
      ],
      orphan_subagents: [],
    }),
  }) as Response

  const subagentTreeResponse = () => ({
    ok: true,
    json: async () => ({
      ok: true,
      sessions: [
        {
          chat_id: 'web-chat-1',
          channel: 'web',
          label: 'My Chat',
          last_active: '2026-07-08T00:00:00Z',
          children: [
            {
              chat_id: 'web:web-chat-1/explore:mem-1',
              full_key: 'web:web-chat-1/explore:mem-1',
              channel: 'agent',
              type: 'agent',
              label: 'explore',
              role: 'explore',
              instance: 'mem-1',
              parent_channel: 'web',
              parent_chat_id: 'web-chat-1',
              running: false,
              last_active: '2026-07-08T00:00:01Z',
            },
          ],
        },
      ],
      chats: [
        { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
      ],
      orphan_subagents: [],
    }),
  }) as Response

  it('HTTP corrects a stale busy key after the trust window (lost idle event)', async () => {
    // User report: "明明 idle 却显示 busy" — a session(idle) lost in an SSE
    // ring eviction / disconnect window previously left executingSessionsRef
    // stuck FOREVER (an unbounded Set with no timestamp); mergeStatus forced
    // running with no correction path. Now the SSE busy hint is trusted for
    // EXECUTING_TRUST_WINDOW_MS; past the window HTTP is authoritative.
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return idleTreeResponse()
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-1' })
      await Promise.resolve()
    })
    expect(result.current.sessions[0].running).toBe(true)
    expect(result.current.sessions[0].status).toBe('running')

    // Refresh WITHIN the trust window: fresh SSE busy beats the lagging HTTP
    // idle response (chatCancelCh lags the idle event by up to one round-trip).
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.sessions[0].running).toBe(true)

    // The idle event was lost (ring eviction / disconnect window). Advance
    // past the trust window: HTTP (idle) is now authoritative — the busy key
    // is dropped and the session goes idle instead of running forever.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000)
    })
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.sessions[0].running).toBe(false)
    expect(result.current.sessions[0].status).toBe('idle')

    // The correction sticks (the key was deleted, not just masked).
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.sessions[0].running).toBe(false)
  })

  it('HTTP corrects a subagent left running by a missed subagent_stopped after the trust window', async () => {
    // User report: "subagent 被卸载了却还显示" — the old mergeStatus carried
    // running=true for agent rows UNCONDITIONALLY (one-shot agents don't
    // register chatCancelCh), so a missed subagent_stopped left the row
    // running forever. subagent_started now timestamps the executing map;
    // past the window HTTP (IsProcessingByChannel reads interactiveSubAgents
    // running state — accurate) corrects it.
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return subagentTreeResponse()
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.subAgents.map((a) => a.chatID)).toEqual(['web:web-chat-1/explore:mem-1'])
    })
    // HTTP says idle (running: false) before the started event.
    expect(result.current.subAgents[0].running).toBe(false)

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'web',
        chat_id: 'web-chat-1',
        session_key: 'web:web-chat-1/explore:mem-1',
        role: 'explore',
        instance: 'mem-1',
        parent_id: 'web-chat-1',
      })
      await Promise.resolve()
    })
    expect(result.current.subAgents[0].running).toBe(true)

    // Within the trust window: SSE-driven running wins over the (lagging) HTTP
    // idle row.
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.subAgents[0].running).toBe(true)

    // Missed subagent_stopped + past the trust window: HTTP idle is
    // authoritative — the row goes idle instead of running forever.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000)
    })
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.subAgents[0].running).toBe(false)
    expect(result.current.subAgents[0].status).toBe('idle')
  })

  it('subagent_stopped removed=true deletes the sidebar row (destroyed session)', async () => {
    // User report: "subagent 被卸载了却还显示" — destroyInteractiveSession
    // (TTL eviction / unload / spawn-failure cleanup) cascade-deletes the DB
    // tenant; removed=true makes the frontend drop the row immediately instead
    // of parking it idle until the next tree refresh (the transient TTL
    // re-attached it for up to 10 minutes).
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return idleTreeResponse()
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_started',
        channel: 'web',
        chat_id: 'web-chat-1',
        session_key: 'web:web-chat-1/explore:mem-1',
        role: 'explore',
        instance: 'mem-1',
        parent_id: 'web-chat-1',
      })
      await Promise.resolve()
    })
    expect(result.current.subAgents.map((a) => a.chatID)).toEqual(['web:web-chat-1/explore:mem-1'])
    expect(result.current.subAgents[0].running).toBe(true)

    // Destroyed (removed=true): the row must be GONE, not parked idle.
    await act(async () => {
      sessionHandler?.({
        action: 'subagent_stopped',
        channel: 'web',
        chat_id: 'web-chat-1',
        session_key: 'web:web-chat-1/explore:mem-1',
        role: 'explore',
        instance: 'mem-1',
        parent_id: 'web-chat-1',
        removed: true,
      })
      await Promise.resolve()
    })
    expect(result.current.subAgents).toHaveLength(0)
    expect(result.current.sessions[0].children ?? []).toHaveLength(0)
  })

  it('sessions-resync refreshes WITHOUT clearing intents (restart-race protection)', async () => {
    // Backend-restart race (user report: "明明 busy 侧边栏却显示 idle"): the
    // turn auto-resumes, busy is replayed via catch-up (fresh intent), reconnect
    // fires sessions-resync, and the HTTP refresh races the resume (chatCancelCh
    // not yet registered → tree says idle). The OLD design cleared the intents
    // then trusted that racy idle response — the one-shot busy event was already
    // consumed → idle forever. The intent window protects: fresh busy intent +
    // racy HTTP idle → running for 15s; the resumed turn's next state (busy
    // event / seq-gap resync / reconnect refresh) converges once the window
    // expires or HTTP catches up.
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return idleTreeResponse()
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-1' })
      await Promise.resolve()
    })
    expect(result.current.sessions[0].running).toBe(true)

    // Reconnect fires sessions-resync; the refresh races the backend resume
    // (HTTP still reports idle). The fresh busy intent (≤15s) protects the
    // sidebar — running, NOT the racy idle.
    await act(async () => {
      window.dispatchEvent(new CustomEvent('sessions-resync'))
      await vi.advanceTimersByTimeAsync(400)
    })
    expect(result.current.sessions[0].running).toBe(true)

    // The window expires → HTTP (idle) is authoritative. (Lost-idle recovery:
    // the same path corrects a stale busy whose idle event was missed.)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(16_000)
      await result.current.refresh()
    })
    expect(result.current.sessions[0].running).toBe(false)
    expect(result.current.sessions[0].status).toBe('idle')
  })

  it('backend restart resume without SSE intents converges to HTTP running (Case-3 deadlock fix)', async () => {
    // The deadlock the old mergeStatus rule created: a restart race left
    // carried idle (racy refresh) + no busy key (busy event missed during the
    // reconnect window) + HTTP running (turn resumed). The old rule
    // ("busySince===undefined && carried.status==='idle' → idle") suppressed
    // HTTP running FOREVER — the resumed session showed idle permanently
    // ("明明 busy 侧边栏却显示 idle"). Now: no intent → HTTP wins
    // unconditionally; the unconditional event chain (busy on resume,
    // seq-gap resync) keeps convergence real-time.
    let treeRunning = false
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              {
                chat_id: 'web-chat-1', channel: 'web', label: 'My Chat',
                last_active: '2026-07-08T00:00:00Z',
                running: treeRunning,
                status: treeRunning ? 'running' : undefined,
              },
            ],
            chats: [
              { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
            ],
            orphan_subagents: [],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID)).toEqual(['web-chat-1'])
    })
    // Carried idle (the restart race left the sidebar idle — no intents at all).
    expect(result.current.sessions[0].running).toBe(false)

    // The backend resumed the turn (chatCancelCh registered) — HTTP now reports
    // running. No SSE intent exists (busy missed during the reconnect window).
    // mergeStatus must take HTTP running — NOT the stale carried idle.
    treeRunning = true
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.sessions[0].running).toBe(true)
    expect(result.current.sessions[0].status).toBe('running')
  })

  it('loadSessionTreeCache strips volatile running state (busy ghost after page reload)', () => {
    // User report: "登录进入会话明明 idle 却显示 busy" — the localStorage tree
    // cache restored running:true from the previous page's state; a slow/failed
    // first refresh left the stale busy on screen. The cache is for first-paint
    // STRUCTURE only; running/waiting must come from the server.
    localStorage.setItem(SESSION_TREE_CACHE_KEY, JSON.stringify({
      version: 1,
      sessions: [{
        chatID: 'web-chat-1', channel: 'web', label: 'My Chat',
        lastActive: '2026-07-08T00:00:00Z', preview: '', status: 'running',
        isCurrent: false, type: 'main', running: true, children: [],
      }],
      subAgents: [{
        chatID: 'web:web-chat-1/explore:mem-1', channel: 'agent', label: 'explore/mem-1',
        lastActive: '2026-07-08T00:00:01Z', preview: '', status: 'waiting_input',
        isCurrent: false, type: 'agent', role: 'explore', instance: 'mem-1',
        parentChannel: 'web', parentChatID: 'web-chat-1', historical: false,
        agentChatID: 'web:web-chat-1/explore:mem-1', synthetic: false,
        running: true, status_original: undefined as never, children: [],
      } as unknown as SessionInfo],
    }))
    const tree = loadSessionTreeCache()
    if (!tree) {
      throw new Error('cached tree must load')
    }
    expect(tree.sessions[0].running).toBe(false)
    expect(tree.sessions[0].status).toBe('idle')
    expect(tree.subAgents[0].running).toBe(false)
    expect(tree.subAgents[0].status).toBe('idle')
  })

  it('identity-less agent-idle must NOT touch the active session (cancel of a background session must not corrupt other busy sessions)', async () => {
    // User report: "只要我在前端cancel一个session，会导致所有busy的session状态异常"
    // Root cause: useProgressStream's PhaseDone dispatched agent-idle with
    // `p.chat_id ?? undefined` — the inner progress payload carries no chat_id,
    // so the dispatch was IDENTITY-LESS for most PhaseDone events. useSessionStore's
    // listener had a "legacy" fallback: clear the ACTIVE session. Cancelling
    // session A (background tab) → A's PhaseDone → identity-less agent-idle →
    // the fallback idled the ACTIVE session B (busy, what the user was viewing)
    // — my fresh-idle-intent change made it authoritative for 15s (beat HTTP
    // running). The fallback is now deleted: identity-less events are DROPPED,
    // and per-session dispatches go through sessionEvents.dispatchAgentIdle
    // (chatID required at the type level).
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'web-chat-2', channel: 'web', label: 'Other Chat', last_active: '2026-07-08T00:00:00Z' },
            ],
            chats: [
              { chat_id: 'web-chat-1', channel: 'web', label: 'My Chat', last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'web-chat-2', channel: 'web', label: 'Other Chat', last_active: '2026-07-08T00:00:00Z' },
            ],
            orphan_subagents: [],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result, unmount } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID).sort()).toEqual(['web-chat-1', 'web-chat-2'])
    })

    vi.useFakeTimers()
    // Session B (web-chat-2) is BUSY — a real SSE busy event + the user is viewing it.
    await act(async () => {
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-2' })
      await Promise.resolve()
    })
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.running).toBe(true)

    // Session A (web-chat-1, background) is CANCELLED. Its useProgressStream
    // PhaseDone used to dispatch agent-idle with `p.chat_id ?? undefined` —
    // identity-less. The OLD fallback idled the ACTIVE session (web-chat-2).
    await act(async () => {
      window.dispatchEvent(new CustomEvent('agent-idle', {
        // detail carries NO chatID — the exact identity-less shape the old
        // PhaseDone dispatch produced.
      }))
      await Promise.resolve()
    })
    // B MUST stay running: identity-less events are dropped, never routed to
    // "the active session" (that guess was the cross-session pollution).
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.running).toBe(true)
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.status).toBe('running')

    // A properly-addressed agent-idle for A still works (its own session).
    await act(async () => {
      window.dispatchEvent(new CustomEvent('agent-idle', {
        detail: { chatID: 'web-chat-1', channel: 'web' },
      }))
      await Promise.resolve()
    })
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-1')?.running).toBe(false)

    // And within the intent window, a refresh with HTTP running keeps B busy
    // (the fresh busy intent beats the lagging HTTP response — the corruption
    // would previously ALSO show as B idle via HTTP winning with a fresh idle
    // intent).
    await act(async () => {
      await result.current.refresh()
    })
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.running).toBe(true)
    unmount()
  })

  it('agent-idle with an explicit chatID only clears THAT session (background busy sessions unaffected)', async () => {
    // The happy path after the fix: A's PhaseDone dispatches
    // agent-idle with A's OWN chatID (via sessionEvents.dispatchAgentIdle —
    // the panel's identity passed through handleProgressMessage). Only A
    // goes idle; B's busy intent and status are untouched.
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/api/chats' || url === '/api/session-tree') {
        return {
          ok: true,
          json: async () => ({
            ok: true,
            sessions: [
              { chat_id: 'web-chat-1', channel: 'web', label: 'A', last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'web-chat-2', channel: 'web', label: 'B', last_active: '2026-07-08T00:00:00Z' },
            ],
            chats: [
              { chat_id: 'web-chat-1', channel: 'web', label: 'A', last_active: '2026-07-08T00:00:00Z' },
              { chat_id: 'web-chat-2', channel: 'web', label: 'B', last_active: '2026-07-08T00:00:00Z' },
            ],
            orphan_subagents: [],
          }),
        } as Response
      }
      throw new Error(`unexpected fetch: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { result, unmount } = renderHook(() => useSessionStoreImpl())
    await waitFor(() => {
      expect(result.current.sessions.map((s) => s.chatID).sort()).toEqual(['web-chat-1', 'web-chat-2'])
    })

    vi.useFakeTimers()
    await act(async () => {
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-1' })
      sessionHandler?.({ action: 'busy', channel: 'web', chat_id: 'web-chat-2' })
      await Promise.resolve()
    })
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-1')?.running).toBe(true)
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.running).toBe(true)

    // A's turn ends (PhaseDone with A's identity) — only A clears.
    await act(async () => {
      window.dispatchEvent(new CustomEvent('agent-idle', {
        detail: { chatID: 'web-chat-1', channel: 'web' },
      }))
      await Promise.resolve()
    })
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-1')?.running).toBe(false)
    expect(result.current.sessions.find((s) => s.chatID === 'web-chat-2')?.running).toBe(true)
    unmount()
  })
})
