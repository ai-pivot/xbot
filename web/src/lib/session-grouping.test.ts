/**
 * Unit tests for the pure session grouping/sort helpers (Spec 3 §3.2).
 *
 * Covers categories, starred-first + lastActive-desc ordering, and fixed
 * group ordering for status/time.
 */
import { describe, expect, it } from 'vitest'
import {
  groupSessions,
  isSubAgentSession,
  parseAgentChatID,
  sessionKey,
  sortSessions,
  type SessionGroup,
} from '@/lib/session-grouping'
import type { SessionInfo } from '@/types/shared'

function mk(p: Partial<SessionInfo> & { chatID: string }): SessionInfo {
  return {
    chatID: p.chatID,
    channel: p.channel ?? 'web',
    label: p.label ?? p.chatID,
    lastActive: p.lastActive ?? '2026-06-26T10:00:00Z',
    createdAt: p.createdAt,
    sortOrder: p.sortOrder,
    preview: p.preview ?? '',
    status: p.status ?? 'idle',
    isCurrent: p.isCurrent ?? false,
    type: p.type,
    parentChatID: p.parentChatID,
    parentChannel: p.parentChannel,
  }
}

describe('sortSessions', () => {
  it('puts starred first, then sorts by createdAt asc within each tier', () => {
    const sessions = [
      mk({ chatID: 'a', lastActive: '2026-06-26T08:00:00Z', createdAt: '2026-06-26T08:00:00Z' }),
      mk({ chatID: 'b', lastActive: '2026-06-26T09:00:00Z', createdAt: '2026-06-26T09:00:00Z' }),
      mk({ chatID: 'c', lastActive: '2026-06-26T07:00:00Z', createdAt: '2026-06-26T07:00:00Z' }),
    ]
    const sorted = sortSessions(sessions, [sessionKey(sessions[0])])
    // Starred 'a' first, then by createdAt asc: c (07:00) → b (09:00)
    expect(sorted.map((s) => s.chatID)).toEqual(['a', 'c', 'b'])
  })

  it('with no starred, sorts purely by createdAt asc', () => {
    const sessions = [
      mk({ chatID: 'a', lastActive: '2026-06-01T00:00:00Z', createdAt: '2026-06-01T00:00:00Z' }),
      mk({ chatID: 'b', lastActive: '2026-06-02T00:00:00Z', createdAt: '2026-06-02T00:00:00Z' }),
      mk({ chatID: 'c', lastActive: '2026-05-30T00:00:00Z', createdAt: '2026-05-30T00:00:00Z' }),
    ]
    const sorted = sortSessions(sessions, [])
    // createdAt asc: c (05-30) → a (06-01) → b (06-02)
    expect(sorted.map((s) => s.chatID)).toEqual(['c', 'a', 'b'])
  })

  it('sortOrder (custom) takes priority over createdAt', () => {
    const sessions = [
      mk({ chatID: 'a', createdAt: '2026-06-01T00:00:00Z', sortOrder: 3 }),
      mk({ chatID: 'b', createdAt: '2026-06-02T00:00:00Z', sortOrder: 1 }),
      mk({ chatID: 'c', createdAt: '2026-06-03T00:00:00Z', sortOrder: 2 }),
    ]
    const sorted = sortSessions(sessions, [])
    // sortOrder asc: b (1) → c (2) → a (3)
    expect(sorted.map((s) => s.chatID)).toEqual(['b', 'c', 'a'])
  })

  it('sortOrder=0 (unset) falls back to createdAt', () => {
    const sessions = [
      mk({ chatID: 'a', createdAt: '2026-06-02T00:00:00Z', sortOrder: 0 }),
      mk({ chatID: 'b', createdAt: '2026-06-01T00:00:00Z', sortOrder: 0 }),
      mk({ chatID: 'c', createdAt: '2026-06-03T00:00:00Z', sortOrder: 2 }),
    ]
    const sorted = sortSessions(sessions, [])
    // c has sortOrder=2, a/b have 0 → c first (only one with >0), then a/b by createdAt
    // Actually: only c has sortOrder>0, so c goes first, then a/b by createdAt
    expect(sorted.map((s) => s.chatID)).toEqual(['c', 'b', 'a'])
  })
})

describe('groupSessions', () => {
  it("'time' returns a single group when all sessions are in the same time bucket", () => {
    const sessions = [
      mk({ chatID: 'a', lastActive: '2026-06-26T08:00:00Z' }),
      mk({ chatID: 'b', lastActive: '2026-06-26T09:00:00Z' }),
    ]
    const groups = groupSessions(sessions, 'time', [sessionKey(sessions[0])])
    // Both sessions are in the 'earlier' bucket since the dates are in the past
    expect(groups).toHaveLength(1)
    expect(groups[0].sessions.map((s) => s.chatID)).toEqual(['a', 'b'])
  })

  it("'status' groups in fixed order running→idle→error and skips empties", () => {
    const sessions = [
      mk({ chatID: 'a', status: 'idle' }),
      mk({ chatID: 'b', status: 'running' }),
      mk({ chatID: 'c', status: 'error' }),
    ]
    const groups = groupSessions(sessions, 'status', [])
    expect(groups.map((g) => g.key)).toEqual(['running', 'idle', 'error'])
  })

  it("'time' groups today/yesterday/earlier", () => {
    const startOfToday = new Date()
    startOfToday.setHours(0, 0, 0, 0)
    const yesterday = new Date(startOfToday)
    yesterday.setDate(yesterday.getDate() - 1)
    const earlier = new Date(startOfToday)
    earlier.setDate(earlier.getDate() - 10)
    const sessions = [
      mk({ chatID: 'today', lastActive: new Date(startOfToday.getTime() + 60_000).toISOString() }),
      mk({ chatID: 'yesterday', lastActive: new Date(yesterday.getTime() + 60_000).toISOString() }),
      mk({ chatID: 'earlier', lastActive: earlier.toISOString() }),
    ]
    const groups = groupSessions(sessions, 'time', []) as SessionGroup[]
    expect(groups.map((g) => g.key)).toEqual(['today', 'yesterday', 'earlier'])
  })

  it('starred items float to the top within their group too', () => {
    const sessions = [
      mk({ chatID: 'a', lastActive: '2026-06-26T08:00:00Z', status: 'idle' }),
      mk({ chatID: 'b', lastActive: '2026-06-26T09:00:00Z', status: 'idle' }),
    ]
    const groups = groupSessions(sessions, 'status', [sessionKey(sessions[0])])
    expect(groups[0].sessions.map((s) => s.chatID)).toEqual(['a', 'b'])
  })

  it('sessionKey includes channel to distinguish matching chat IDs', () => {
    expect(sessionKey(mk({ chatID: 'same', channel: 'web' }))).toBe('web:same')
    expect(sessionKey(mk({ chatID: 'same', channel: 'cli' }))).toBe('cli:same')
  })

  it('filters SubAgent sessions out of main groups', () => {
    const sessions = [
      mk({ chatID: 'parent', channel: 'cli' }),
      mk({ chatID: 'agent-parent/review/1', channel: 'cli', type: 'agent', parentChatID: 'parent' }),
      mk({ chatID: 'cli:parent/review:1', channel: 'agent' }),
    ]
    expect(sessions.slice(1).every(isSubAgentSession)).toBe(true)
    expect(groupSessions(sessions, 'time', [])[0].sessions.map((s) => s.chatID)).toEqual(['parent'])
  })

  it('treats historical agent tenant rows as SubAgent sessions', () => {
    const historical = mk({
      chatID: 'cli:/repo:Agent-main/review:oneshot-1',
      channel: 'agent',
      type: 'agent',
      parentChatID: '/repo:Agent-main',
      parentChannel: 'cli',
    })
    expect(isSubAgentSession(historical)).toBe(true)
    expect(sortSessions([mk({ chatID: '/repo:Agent-main', channel: 'cli' }), historical], []))
      .toHaveLength(1)
  })

  it('treats agent channel or parentChatID rows as SubAgent sessions even without type', () => {
    expect(isSubAgentSession(mk({ chatID: 'cli:/repo:Agent-main/review:1', channel: 'agent' })))
      .toBe(true)
    expect(isSubAgentSession(mk({ chatID: 'review:1', channel: 'cli', parentChatID: '/repo:Agent-main' })))
      .toBe(true)
  })

  it('treats TUI full-key rows as SubAgent sessions even without normalized fields', () => {
    const fullKeyOnly = mk({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'cli',
      label: 'default',
    })

    expect(isSubAgentSession(fullKeyOnly)).toBe(true)
    expect(sortSessions([mk({ chatID: '/repo:Agent-main', channel: 'cli' }), fullKeyOnly], []))
      .toHaveLength(1)
  })

  it('does not classify ordinary CLI path sessions as SubAgent rows', () => {
    const ordinary = mk({
      chatID: '/vePFS-Mindverse/user/intern/yihang:Agent-warm-stone',
      channel: 'cli',
    })

    expect(isSubAgentSession(ordinary)).toBe(false)
    expect(sortSessions([ordinary], []).map((s) => s.chatID)).toEqual([ordinary.chatID])
  })

  it('parses TUI SubAgent tenant keys without confusing CLI main chat names', () => {
    expect(parseAgentChatID('cli:/repo:Agent-main/review:1')).toEqual({
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
      role: 'review',
      instance: '1',
    })
    expect(parseAgentChatID('web:chat_123/explore')).toEqual({
      parentChannel: 'web',
      parentChatID: 'chat_123',
      role: 'explore',
      instance: '',
    })
    expect(parseAgentChatID('/vePFS-Mindverse/user/intern/yihang:Agent-warm-stone')).toBeNull()
  })
})

describe('path grouping', () => {
  it('groups CLI sessions by full workDir and uses the basename as title', () => {
    const sessions = [
      mk({ chatID: '/home/user/project1:session-a', channel: 'cli' }),
      mk({ chatID: '/home/user/project1:session-b', channel: 'cli' }),
      mk({ chatID: '/home/user/project2:session-c', channel: 'cli' }),
    ]
    const groups = groupSessions(sessions, 'path', [])
    expect(groups).toHaveLength(2)
    const project1Group = groups.find((g) => g.key === '/home/user/project1')
    const project2Group = groups.find((g) => g.key === '/home/user/project2')
    expect(project1Group).toBeDefined()
    expect(project1Group!.sessions).toHaveLength(2)
    expect(project2Group).toBeDefined()
    expect(project2Group!.sessions).toHaveLength(1)
  })

  it('groups sessions without a persisted workDir into the unset bucket', () => {
    const sessions = [
      mk({ chatID: 'web-session-1', channel: 'web' }),
      mk({ chatID: 'web-session-2', channel: 'web' }),
    ]
    const groups = groupSessions(sessions, 'path', [])
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('__unset__')
  })

  it('subAgent sessions inherit parent path group', () => {
    const parent = mk({ chatID: '/home/user/project1:session-a', channel: 'cli' })
    const child = mk({
      chatID: 'cli:/home/user/project1:session-a/review:1',
      channel: 'agent',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/home/user/project1:session-a',
    })
    const groups = groupSessions([parent, child], 'path', [])
    // SubAgents are filtered out of main groups
    expect(groups).toHaveLength(1)
    expect(groups[0].key).toBe('/home/user/project1')
    expect(groups[0].sessions).toHaveLength(1)
    expect(groups[0].sessions[0].chatID).toBe('/home/user/project1:session-a')
  })
})
