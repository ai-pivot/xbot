/**
 * Message ordering — the SINGLE rendering-level guarantee for linear
 * message consistency (user requirement: "证明任何情况下的消息一致性").
 *
 * Invariants enforced here (pure functions, no side effects):
 *
 *   R1. Every rendered row has a deterministic turn_id (turnID=0 is derived
 *       from neighbors where possible; only truly undeducible rows keep 0).
 *   R2. Rows render strictly ordered by (turn_id, role): a larger turn_id
 *       NEVER renders above a smaller one. Within a turn, user(0) precedes
 *       assistant(1). Ties keep input order (stable sort) — the input order
 *       equals DB append order, which equals iteration order.
 *   R3. Within a turn, assistant rows preserve iteration order (stable sort
 *       keeps the input order, and the input order is monotonic in iteration
 *       numbers because ConvertMessagesToHistoryWithIterations merges all
 *       iterations of a turn into ONE assistant row in ascending order).
 *   R4. Continuity (turn_id monotonic, per-turn iteration contiguous) is
 *       asserted at render time for diagnostics; the backend guarantees
 *       monotonic turn_id allocation and per-turn iteration numbering.
 *
 * Rationale for sorting at render time (instead of relying on accumulation
 * order): SSE events can arrive out of order on weak networks (turn 5's text
 * event before turn 4's), reloads interleave live rows, and legacy rows may
 * lack turn_id entirely. The render layer is the single choke point — every
 * path (history reload, live append, cancel, notification, session switch)
 * funnels through buildMessageRows, so sorting here fixes ALL paths at once.
 */

import type { ChatMessage } from '@/types/shared'

/** Stable sort comparator key: (turnID, roleRank). */
function sortKey(m: ChatMessage): [number, number] {
  const roleRank = m.role === 'user' ? 0 : 1
  if (m.turnID > 0) return [m.turnID, roleRank]
  // turnID=0 residue (undeducible):
  //  - isPartial (live streaming) or persisted=false (optimistic send): the
  //    newest content — must render at the BOTTOM (below all committed rows).
  //  - persisted=true with no derivable turn (early legacy rows): the oldest
  //    content — renders at the TOP.
  const bottom = m.isPartial || m.persisted === false
  return [bottom ? Number.MAX_SAFE_INTEGER : -1, roleRank]
}

/**
 * Derive deterministic turn_id for rows that carry turnID=0.
 *
 * Rules (pure, deterministic, injective-safe):
 *  - isPartial rows (live streaming) are SKIPPED — their turnID comes from
 *    the progress snapshot (authoritative). Deriving a live row's turnID
 *    from neighbors would mis-bind it to a PREVIOUS turn, causing the live
 *    row to be deduped against that turn's committed assistant (streaming
 *    content vanishes).
 *  - assistant rows: inherit the nearest PRECEDING row's turn_id (the turn's
 *    user/assistant anchor). Handles appendAssistant commits whose text event
 *    arrived without turn_id, and legacy assistant rows.
 *  - user rows: inherit the nearest FOLLOWING row's turn_id (the user message
 *    triggers the turn that follows it). Handles legacy user_echo rows and
 *    optimistic user rows whose turn_started was lost (they bind to the next
 *    turn's id — the turn they actually triggered).
 *  - Rows that still cannot be derived (a session with NO turn_id anywhere,
 *    e.g. pre-turn-id legacy data) keep 0; orderMessageRows pins them at the
 *    top (persisted) or bottom (optimistic/live).
 *
 * O(N) — two linear passes build prev/next turn arrays.
 */
export function bindTurnIDs(messages: ChatMessage[]): ChatMessage[] {
  if (messages.length === 0) return messages
  // Fast path: no committed (isPartial=false) row carries turnID=0 — every
  // row already has its authoritative turn_id. Return the input array as-is
  // (zero copy, zero scan) so the streaming hot path (committed rows unchanged
  // between frames) does not allocate per frame.
  let needsBinding = false
  for (const m of messages) {
    if (m.turnID === 0 && !m.isPartial) {
      needsBinding = true
      break
    }
  }
  if (!needsBinding) return messages
  const result = messages.map((m) => ({ ...m }))
  const n = result.length
  // prevTurn[i] = nearest turn_id>0 at or before i (assistant anchor).
  const prevTurn = new Array<number>(n).fill(0)
  let prev = 0
  for (let i = 0; i < n; i++) {
    if (result[i].turnID > 0) prev = result[i].turnID
    prevTurn[i] = prev
  }
  // nextTurn[i] = nearest turn_id>0 at or after i (user anchor).
  const nextTurn = new Array<number>(n).fill(0)
  let next = 0
  for (let i = n - 1; i >= 0; i--) {
    if (result[i].turnID > 0) next = result[i].turnID
    nextTurn[i] = next
  }
  for (let i = 0; i < n; i++) {
    const m = result[i]
    if (m.turnID > 0 || m.isPartial) continue // live rows: snapshot turnID wins
    if (m.role === 'assistant' && prevTurn[i] > 0) {
      m.turnID = prevTurn[i]
    } else if (m.role === 'user') {
      // Optimistic rows (persisted=false) stay 0 — they are unbound sends
      // awaiting their own turn_started; orderMessageRows pins them at the
      // BOTTOM (newest). Persisted user rows (history echoes) bind:
      //  - to the nearest FOLLOWING turn (the turn they triggered), else
      //  - to the nearest PRECEDING turn (a user_echo whose turn_started was
      //    lost and whose turn already ended — e.g. after a long SSE gap the
      //    reload keeps the echo above the watermark and no following turn
      //    exists in the committed list). Without the prevTurn fallback these
      //    rows keep turnID=0 and sort to the TOP, recreating the "user msgs
      //    all at the bottom/top after SSE reconnect" ordering bug.
      if (m.persisted === false) continue
      if (nextTurn[i] > 0) {
        m.turnID = nextTurn[i]
      } else if (prevTurn[i] > 0) {
        m.turnID = prevTurn[i]
      }
    }
  }
  return result
}

/**
 * Stable sort rows by (turn_id, role). R2: larger turn_id never renders above
 * a smaller one; within a turn user precedes assistant. R3: ties keep input
 * order (= DB append order = iteration order).
 *
 * Fast path: if the array is already ordered (the common case — DB append
 * order is turn-monotonic and the committed rows were sorted in a previous
 * frame), return the input array as-is (zero copy). Only an out-of-order
 * array (SSE reorder, mis-bound rows) pays the O(N log N) sort.
 */
export function orderMessageRows(messages: ChatMessage[]): ChatMessage[] {
  if (messages.length < 2) return messages
  // Detect order violations in O(N); a single inversion triggers the sort.
  let prevKey = sortKey(messages[0])
  for (let i = 1; i < messages.length; i++) {
    const key = sortKey(messages[i])
    if (key[0] < prevKey[0] || (key[0] === prevKey[0] && key[1] < prevKey[1])) {
      // Out of order — do the stable sort.
      return [...messages].sort((a, b) => {
        const ak = sortKey(a)
        const bk = sortKey(b)
        if (ak[0] !== bk[0]) return ak[0] - bk[0]
        if (ak[1] !== bk[1]) return ak[1] - bk[1]
        return 0 // stable — keep input order for identical keys
      })
    }
    prevKey = key
  }
  return messages // already ordered — zero copy
}

/**
 * Assert the render invariants (diagnostic only — never blocks rendering):
 *  - turn_id strictly non-decreasing among turn_id>0 rows (a regression means
 *    either the backend allocated out of order or a row was mis-bound).
 *  - within each turn, the union of assistant iteration numbers is contiguous
 *    (no gap). A gap indicates lost iteration history (backend restart/cancel);
 *    continuousIterations already hides the non-contiguous tail at render time.
 */
export function assertRowConsistency(rows: ChatMessage[]): void {
  let lastTurn = 0
  for (const row of rows) {
    if (row.turnID > 0) {
      if (lastTurn > 0 && row.turnID < lastTurn) {
        console.error('[ROW_ORDER_INVARIANT] turn_id decreased', {
          prev: lastTurn,
          next: row.turnID,
          role: row.role,
          id: row.id,
        })
      }
      if (row.turnID > lastTurn) lastTurn = row.turnID
    }
    if (row.role === 'assistant' && row.iterations.length > 1) {
      for (let i = 1; i < row.iterations.length; i++) {
        const prevIter = row.iterations[i - 1].iteration
        const currIter = row.iterations[i].iteration
        if (currIter !== prevIter + 1) {
          console.error('[ITER_GAP_INVARIANT] iteration gap within a turn', {
            turnID: row.turnID,
            prev: prevIter,
            next: currIter,
            id: row.id,
          })
          break
        }
      }
    }
  }
}
