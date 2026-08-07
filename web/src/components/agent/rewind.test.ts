import { describe, expect, it } from 'vitest'

import { resolveUserMessageDBID } from './rewind'
import type { ChatMessage } from '@/types/shared'

function userMsg(partial: Partial<ChatMessage>): ChatMessage {
  return {
    id: 'x',
    role: 'user',
    content: '',
    iterations: [],
    timestamp: '2026-07-08T00:00:00Z',
    isPartial: false,
    turnID: 0,
    ...partial,
  }
}

describe('resolveUserMessageDBID', () => {
  it('resolves the DB id for an echo row from reloaded history by turnID+content', () => {
    // Rows rendered live from user_echo SSE have persisted=true but NO dbID —
    // the DB id is assigned when the agent loop persists the message, which
    // happens AFTER the echo is sent at queue-admission time.
    const echoRow = userMsg({
      id: 'echo-1',
      turnID: 7,
      content: 'hello',
      persisted: true,
      requestID: 'req-1',
    })
    // A fresh history snapshot (fetchHistory → parseHistoryMessages) carries dbID.
    const reloadRows = [
      userMsg({ id: 'db-101', turnID: 7, content: 'hello', persisted: true, dbID: 101 }),
      userMsg({ id: 'db-100', turnID: 6, content: 'earlier', persisted: true, dbID: 100 }),
    ]
    expect(resolveUserMessageDBID(reloadRows, echoRow)).toBe(101)
  })

  it('falls back to content-only matching for rows without a turnID (attachment echoes)', () => {
    // The upload-expansion echo (web_inbound.go else-branch) carries no turnID.
    const echoRow = userMsg({ id: 'echo-2', turnID: 0, content: 'file.pdf attached', persisted: true })
    const reloadRows = [
      userMsg({ id: 'db-202', turnID: 3, content: 'file.pdf attached', persisted: true, dbID: 202 }),
    ]
    expect(resolveUserMessageDBID(reloadRows, echoRow)).toBe(202)
  })

  it('returns undefined when the message is not in the fresh snapshot (genuinely not persisted)', () => {
    const echoRow = userMsg({ id: 'echo-3', turnID: 9, content: 'queued msg', persisted: true })
    expect(resolveUserMessageDBID([], echoRow)).toBeUndefined()
  })

  it('never matches assistant rows or rows without a dbID', () => {
    const echoRow = userMsg({ id: 'echo-4', turnID: 5, content: 'hi', persisted: true })
    const reloadRows = [
      {
        ...userMsg({ id: 'db-300', turnID: 5, content: 'hi', persisted: true, dbID: 300 }),
        role: 'assistant' as const,
      },
      userMsg({ id: 'echo-live', turnID: 5, content: 'hi', persisted: true }), // no dbID
    ]
    expect(resolveUserMessageDBID(reloadRows, echoRow)).toBeUndefined()
  })
})
