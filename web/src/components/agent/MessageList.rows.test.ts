/**
 * Unit tests for buildMessageRows — the turn-aware row builder that merges
 * committed messages with the live streaming assistant.
 *
 * Regression: when turn_started is lost (SSE drop/coalesce), the optimistic
 * user row keeps turnID=0 (bindLastUserToTurn never ran). The live assistant
 * for the NEW turn must still be positioned AFTER that user message, NOT
 * after the previous turn's assistant — otherwise the reply renders inside
 * the previous turn ("agent 回复开始在上一个 agent turn 的开头渲染，
 * user msg 后面什么都没有").
 */
import { describe, expect, it } from 'vitest'

import { buildMessageRows } from '@/components/agent/MessageList'
import type { ChatMessage } from '@/types/agent'

function msg(id: string, role: 'user' | 'assistant', turnID: number, extra: Partial<ChatMessage> = {}): ChatMessage {
  return {
    id,
    role,
    content: `content-${id}`,
    iterations: [],
    timestamp: '',
    isPartial: false,
    turnID,
    ...extra,
  }
}

describe('buildMessageRows turn-boundary live insertion', () => {
  it('orders the live assistant after its own turn user (REST-bound turnID, no heuristics)', () => {
    // The new user message is bound to turn 6 via the REST response turn_id
    // (sendMessage binds resp.turn_id) — deterministic ordering by turnID
    // places it and its live assistant after the previous turn, user first.
    const messages: ChatMessage[] = [
      msg('u-prev', 'user', 5),
      msg('a-prev', 'assistant', 5),
      msg('u-new', 'user', 6, { persisted: false }),
    ]
    const live: ChatMessage = {
      id: 'live-new', role: 'assistant', content: 'streaming new reply',
      iterations: [], timestamp: '', isPartial: true, turnID: 6,
    }
    const rows = buildMessageRows(messages, live)
    const ids = rows.map((r) => r.id)
    // The live assistant MUST render after the new user message (same turn:
    // user before assistant), and never inside the previous turn.
    expect(ids.indexOf('live-new')).toBeGreaterThan(ids.indexOf('u-new'))
    expect(ids.indexOf('u-new')).toBeGreaterThan(ids.indexOf('a-prev'))
  })

  it('sorts an unbound optimistic user (turnID=0) LAST — deterministic, no fallback', () => {
    // turnID=0 rows are unclassified (unbound optimistic / legacy) and sort
    // last by the deterministic ordering key. In the normal flow the REST
    // response binds resp.turn_id before the live assistant exists, so this
    // only occurs when the response itself is lost.
    const messages: ChatMessage[] = [
      msg('u-prev', 'user', 5),
      msg('a-prev', 'assistant', 5),
      msg('u-new', 'user', 0, { persisted: false }),
    ]
    const rows = buildMessageRows(messages, null)
    const ids = rows.map((r) => r.id)
    expect(ids).toEqual(['u-prev', 'a-prev', 'u-new'])
  })

  it('inserts live assistant after a bound same-turn user when turn_started arrived normally', () => {
    const messages: ChatMessage[] = [
      msg('u-prev', 'user', 5),
      msg('a-prev', 'assistant', 5),
      msg('u-new', 'user', 6, { persisted: false }),
    ]
    const live: ChatMessage = {
      id: 'live-new', role: 'assistant', content: 'streaming', iterations: [], timestamp: '', isPartial: true, turnID: 6,
    }
    const rows = buildMessageRows(messages, live)
    const ids = rows.map((r) => r.id)
    expect(ids.indexOf('live-new')).toBeGreaterThan(ids.indexOf('u-new'))
    expect(ids[ids.length - 1]).toBe('live-new')
  })

  it('merges into a committed same-turn assistant instead of inserting a duplicate', () => {
    const messages: ChatMessage[] = [
      msg('u1', 'user', 1),
      msg('a1', 'assistant', 1),
    ]
    const live: ChatMessage = {
      id: 'live-1', role: 'assistant', content: 'streaming', iterations: [], timestamp: '', isPartial: true, turnID: 1,
    }
    const rows = buildMessageRows(messages, live)
    expect(rows).toHaveLength(2)
    expect(rows.some((r) => r.id === 'live-1')).toBe(false)
  })
})
