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

/** 排序主键（turnID 维度）—— **不分配数组**。
 *  原实现每行返回 `[turnID, roleRank]` 元组，而 orderMessageRows 每个流式帧都
 *  对**全表**调用 ⇒ 每帧 N 个数组分配（代价 ∝ 已加载历史总量，2026-09-13
 *  「长历史也会卡」）。改成两个标量取值。 */
function sortTurnKey(m: ChatMessage): number {
  if (m.turnID > 0) return m.turnID
  // 命令回复（standalone 段）：**按构造无 turn**，必须渲染在最底部（turn 行之后）。
  // 它有 `persisted: true`（integrate 的 committed 映射），若不单独判定会落到
  // "早起 legacy 行"分支（-1）→ 命令输出跑到会话**顶部**（用户看到的仍是"没有输出"）。
  // 注意：判定放在 `turnID === 0` 之内，**字段保持 0** —— 属性测试 P4/P5 用
  // `turnID > 0` 识别 turn 行（模型约定：0 = 无 turn 的独立消息）。
  // 命令行的「时间锚点」：插回它发生的那一刻 —— anchor+0.5 落在该 turn 的所有行
  // **之后**、下一个 turn **之前**（turnID 保持 0 只是虚拟键的回落依据，排序键用
  // 小数锚点，绝不与真实 turn 行等键）。无锚点（旧数据/锚点 turn 已被裁剪）回落沉底。
  if (m.standalone) {
    return m.anchorTurnID !== undefined ? m.anchorTurnID + 0.5 : Number.MAX_SAFE_INTEGER
  }
  // turnID=0 residue (undeducible):
  //  - isPartial (live streaming) or persisted=false (optimistic send): the
  //    newest content — must render at the BOTTOM (below all committed rows).
  //  - persisted=true with no derivable turn (early legacy rows): the oldest
  //    content — renders at the TOP.
  const bottom = m.isPartial || m.persisted === false
  return bottom ? Number.MAX_SAFE_INTEGER : -1
}

/** 角色次序：同 turn 内 user(0) 先于 assistant(1)。 */
function sortRoleRank(m: ChatMessage): number {
  return m.role === 'user' ? 0 : 1
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
    if (m.turnID === 0 && !m.isPartial && !m.standalone) {
      needsBinding = true
      break
    }
  }
  if (!needsBinding) return messages
  // 只做数组浅拷贝（O(N) 指针），**只在真正需要绑定的行上再浅拷贝行对象**。
  // 原实现 `messages.map(m => ({...m}))` 每个流式帧对**全表**分配 N 个新对象 ——
  // 代价 ∝ 已加载历史总量（2026-09-13「长历史也会卡，哪怕新 turn 很小」）。
  const result = messages.slice()
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
    if (m.turnID > 0 || m.isPartial || m.standalone) continue // live rows: snapshot turnID wins；standalone: 按构造无 turn（不绑定）
    let bound = 0
    if (m.role === 'assistant' && prevTurn[i] > 0) {
      bound = prevTurn[i]
    } else if (m.role === 'user') {
      // Users bind to the nearest FOLLOWING turn (the turn they triggered).
      // This applies to optimistic rows too: buildMessageRows runs binding on
      // [messages, live] together, so an optimistic user whose reply is
      // streaming (live, turnID=2) binds to 2 and sorts user-before-assistant
      // — "reply below my user msg" (linear consistency).
      if (nextTurn[i] > 0) {
        bound = nextTurn[i]
      } else if (m.persisted !== false && prevTurn[i] > 0) {
        // Persisted user_echo with NO following turn (its turn_started was
        // lost / AskUser answer): bind to the nearest PRECEDING turn — keeps
        // it in turn order instead of pinning at the top. Optimistic rows
        // (persisted=false) stay 0 → sorted to the bottom (awaiting their own
        // turn_started).
        bound = prevTurn[i]
      }
    }
    if (bound > 0) result[i] = { ...m, turnID: bound }
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
  // Detect order violations in O(N)（只做标量比较，零分配）；一处逆序才排序。
  let prevTurn = sortTurnKey(messages[0])
  let prevRank = sortRoleRank(messages[0])
  for (let i = 1; i < messages.length; i++) {
    const turn = sortTurnKey(messages[i])
    const rank = sortRoleRank(messages[i])
    if (turn < prevTurn || (turn === prevTurn && rank < prevRank)) {
      // Out of order — do the stable sort.
      return [...messages].sort((a, b) => {
        const at = sortTurnKey(a)
        const bt = sortTurnKey(b)
        if (at !== bt) return at - bt
        const ar = sortRoleRank(a)
        const br = sortRoleRank(b)
        if (ar !== br) return ar - br
        return 0 // stable — keep input order for identical keys
      })
    }
    prevTurn = turn
    prevRank = rank
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
        if (currIter === prevIter) {
          // Duplicate iteration record (dual-server double-write / replayed
          // delta) — NOT a gap. Mirrors the store-layer guards
          // (continuousIterations / hasIterationGap / assertIterationContinuity):
          // a duplicate must never be reported as LOST iteration history.
          continue
        }
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
