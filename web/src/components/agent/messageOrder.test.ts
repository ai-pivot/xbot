import { describe, expect, it } from 'vitest'

import { bindTurnIDs, orderMessageRows } from '@/components/agent/messageOrder'
import type { ChatMessage } from '@/types/shared'

function msg(over: Partial<ChatMessage>): ChatMessage {
  return {
    id: `m-${Math.random().toString(36).slice(2, 8)}`,
    role: 'assistant',
    content: '',
    iterations: [],
    timestamp: '',
    isPartial: false,
    turnID: 0,
    ...over,
  }
}

describe('bindTurnIDs', () => {
  it('binds assistant rows to the nearest preceding turn', () => {
    const rows = [
      msg({ id: 'u3', role: 'user', turnID: 3 }),
      msg({ id: 'a3', role: 'assistant', turnID: 0 }), // text event lost turn_id
      msg({ id: 'u4', role: 'user', turnID: 4 }),
      msg({ id: 'a4', role: 'assistant', turnID: 4 }),
    ]
    const bound = bindTurnIDs(rows)
    expect(bound[1].turnID).toBe(3)
  })

  it('binds user rows to the nearest following turn (legacy echo)', () => {
    const rows = [
      msg({ id: 'u-old', role: 'user', turnID: 0, persisted: true }), // legacy echo
      msg({ id: 'a5', role: 'assistant', turnID: 5 }),
      msg({ id: 'u5', role: 'user', turnID: 5 }),
    ]
    const bound = bindTurnIDs(rows)
    expect(bound[0].turnID).toBe(5)
  })

  it('binds an optimistic user at the end to the last known turn when no following turn exists', () => {
    // Optimistic user with turn_started lost: no following turn_id>0 in the
    // committed list — it must NOT be bound to a stale old turn. It stays 0
    // and orderMessageRows pins it at the bottom.
    const rows = [
      msg({ id: 'u3', role: 'user', turnID: 3 }),
      msg({ id: 'a3', role: 'assistant', turnID: 3 }),
      msg({ id: 'u-new', role: 'user', turnID: 0, persisted: false, sending: true }),
    ]
    const bound = bindTurnIDs(rows)
    expect(bound[2].turnID).toBe(0)
  })

  it('leaves a fully turn-less session untouched', () => {
    const rows = [
      msg({ id: 'u0', role: 'user', turnID: 0, persisted: true }),
      msg({ id: 'a0', role: 'assistant', turnID: 0, persisted: true }),
    ]
    const bound = bindTurnIDs(rows)
    expect(bound.every((m) => m.turnID === 0)).toBe(true)
  })

  it('binds a persisted user row with NO following turn to the nearest PRECEDING turn (long SSE-gap reload)', () => {
    // SSE disconnected for a long time; on reconnect the reload keeps a
    // user_echo (persisted=true, turnID=0, no following turn in the committed
    // list — its turn_started was lost). Without the prevTurn fallback this
    // row keeps turnID=0 and sortKey pins it at the TOP, breaking order.
    const rows = [
      msg({ id: 'u1', role: 'user', turnID: 1 }),
      msg({ id: 'a1', role: 'assistant', turnID: 1 }),
      msg({ id: 'u2', role: 'user', turnID: 2 }),
      msg({ id: 'a2', role: 'assistant', turnID: 2 }),
      msg({ id: 'echo-u3', role: 'user', turnID: 0, persisted: true }), // new user, no turn yet
    ]
    const bound = bindTurnIDs(rows)
    expect(bound[4].turnID).toBe(2) // binds to the last known turn, not 0
  })

  it('keeps an optimistic user (persisted=false) at turnID=0 (bottom placeholder)', () => {
    const rows = [
      msg({ id: 'u1', role: 'user', turnID: 1 }),
      msg({ id: 'a1', role: 'assistant', turnID: 1 }),
      msg({ id: 'opt-u', role: 'user', turnID: 0, persisted: false, sending: true }),
    ]
    const bound = bindTurnIDs(rows)
    expect(bound[2].turnID).toBe(0)
  })
})

describe('orderMessageRows', () => {
  it('sorts by turn_id ascending (R2: larger turn_id never above smaller)', () => {
    // SSE out-of-order: turn 5 committed before turn 4.
    const rows = [
      msg({ id: 'a5', role: 'assistant', turnID: 5 }),
      msg({ id: 'a4', role: 'assistant', turnID: 4 }),
      msg({ id: 'u4', role: 'user', turnID: 4 }),
      msg({ id: 'u5', role: 'user', turnID: 5 }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['u4', 'a4', 'u5', 'a5'])
  })

  it('puts user before assistant within the same turn', () => {
    const rows = [
      msg({ id: 'a3', role: 'assistant', turnID: 3 }),
      msg({ id: 'u3', role: 'user', turnID: 3 }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['u3', 'a3'])
  })

  it('pins legacy turnID=0 persisted rows at the top', () => {
    const rows = [
      msg({ id: 'a5', role: 'assistant', turnID: 5 }),
      msg({ id: 'legacy', role: 'user', turnID: 0, persisted: true }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['legacy', 'a5'])
  })

  it('pins optimistic/live turnID=0 rows at the bottom', () => {
    const rows = [
      msg({ id: 'a5', role: 'assistant', turnID: 5 }),
      msg({ id: 'opt-user', role: 'user', turnID: 0, persisted: false, sending: true }),
      msg({ id: 'live', role: 'assistant', turnID: 0, isPartial: true }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['a5', 'opt-user', 'live'])
  })

  it('keeps same-turn assistants in input order (iteration order)', () => {
    const rows = [
      msg({ id: 'a3-iter1', role: 'assistant', turnID: 3, iterations: [{ iteration: 1, thinking: '', reasoning: '', tools: [], toolCount: 0 }] }),
      msg({ id: 'a3-iter2', role: 'assistant', turnID: 3, iterations: [{ iteration: 2, thinking: '', reasoning: '', tools: [], toolCount: 0 }] }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['a3-iter1', 'a3-iter2'])
  })

  it('is stable for identical keys (input order preserved)', () => {
    const rows = [
      msg({ id: 'x1', role: 'assistant', turnID: 2 }),
      msg({ id: 'x2', role: 'assistant', turnID: 2 }),
      msg({ id: 'x3', role: 'assistant', turnID: 2 }),
    ]
    const ordered = orderMessageRows(rows)
    expect(ordered.map((m) => m.id)).toEqual(['x1', 'x2', 'x3'])
  })
})
