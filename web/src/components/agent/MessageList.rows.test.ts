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
  it('BUG: inserts the live assistant AFTER the current turn optimistic user (turnID=0) — not after the previous assistant', () => {
    // Previous turn complete; the new user message is optimistic with turnID=0
    // because turn_started was lost and bindLastUserToTurn never ran.
    const messages: ChatMessage[] = [
      msg('u-prev', 'user', 5),
      msg('a-prev', 'assistant', 5),
      msg('u-new', 'user', 0, { persisted: false }),
    ]
    const live: ChatMessage = {
      id: 'live-new', role: 'assistant', content: 'streaming new reply',
      iterations: [], timestamp: '', isPartial: true, turnID: 6,
    }
    const rows = buildMessageRows(messages, live)
    const ids = rows.map((r) => r.id)
    // The live assistant MUST render after the new user message.
    expect(ids.indexOf('live-new')).toBeGreaterThan(ids.indexOf('u-new'))
    expect(ids[ids.length - 1]).toBe('live-new')
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
