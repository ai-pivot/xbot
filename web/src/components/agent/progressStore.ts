/**
 * External store for live Agent progress (Spec 3 — 流式数据模型与 Store 重写).
 *
 * Core design (mirrors TUI's progress state machine):
 *
 * 1. **stream-only patch** — stream_content events (phase==='' && iteration===0)
 *    only patch StreamContent/ReasoningStreamContent/StreamingTools to `current`,
 *    never replace the entire snapshot. This prevents the "text disappears on
 *    structured event arrival" bug.
 *
 * 2. **carry-forward** — when a structured event (progress_structured) arrives,
 *    stream-only fields (streamContent, reasoningStreamContent, streamingTools)
 *    are preserved from the current state; structured fields (phase, iteration,
 *    activeTools, completedTools) are replaced.
 *
 * 3. **semantic snapshot + log** — ProgressEvent.Seq is the monotonic log ID.
 *    IterationHistory is accepted only from the backend; the client never
 *    synthesizes completed iterations while advancing the current snapshot.
 *
 * 4. **tool dedup** — generating-status tools are never deduped (each call shows
 *    independently). running/done/error tools are deduped by name+label.
 *
 * Performance: requestAnimationFrame throttling coalesces many mutations into
 * at most one notify per frame. flush() produces a shallow-copied top-level
 * object so useSyncExternalStore's referential equality check detects changes.
 */
import {
  EMPTY_PROGRESS_SNAPSHOT,
  type ProgressSnapshot,
  type WebToolProgress,
  type WebIteration,
  type TodoItem,
  type WebSubAgentProgress,
  type TokenUsageInfo,
} from '@/types/shared'
import type { ProgressEvent } from '@/types/shared'

type Listener = () => void
type Mutator = (draft: ProgressSnapshot) => void

/**
 * Append new iterations (Delta Push: 0-1 entries) to iterationHistory,
 * deduplicating by iteration number. Creates a new array reference so
 * immer detects the change (push on draft arrays can be unreliable when
 * the source is a shared constant like EMPTY_PROGRESS_SNAPSHOT).
 */
function appendIterations(draft: ProgressSnapshot, incoming: WebIteration[]) {
  const newIters = incoming.filter(
    (iter) => !draft.iterationHistory.some((i) => i.iteration === iter.iteration),
  )
  if (newIters.length > 0) {
    draft.iterationHistory = [...draft.iterationHistory, ...newIters]
    assertIterationContinuity(draft.iterationHistory)
  }
}

/**
 * Assert that iteration numbers are strictly sequential (1, 2, 3, ...).
 * A gap (e.g. 1 → 148) indicates a bug — typically iteration history was lost
 * during a backend restart + cancel, or the DB Detail was incomplete.
 * Non-blocking: logs a console.error with diagnostic context.
 */
export function assertIterationContinuity(iters: WebIteration[]) {
  if (iters.length < 2) return
  for (let i = 1; i < iters.length; i++) {
    const prev = iters[i - 1].iteration
    const curr = iters[i].iteration
    if (curr !== prev + 1) {
      console.error(
        `[ITERATION_GAP] Iteration continuity broken: ${prev} → ${curr} (expected ${prev + 1}). ` +
          `Total iterations: ${iters.length}. ` +
          `This indicates lost iteration history — check backend restart + cancel handling.`,
        { iterations: iters.map((it) => it.iteration) },
      )
      break // report first gap only
    }
  }
}

/**
 * Return the longest CONTIGUOUS prefix of iterations, preserving the input
 * order (1, 2, 3, ...).
 *
 * Linear-consistency guarantee: whatever is RENDERED must be a valid,
 * contiguous iteration sequence — a gap (e.g. 1 → 3, iteration 2's delta lost
 * on a weak network before restoreActiveProgress backfills it) would render
 * iteration 3 in place of 2, violating iteration order. Rendering only the
 * contiguous prefix keeps the visible history linear; the missing iterations
 * are backfilled by restoreActiveProgress (SSE seq-gap recovery) and the
 * hidden tail reappears once contiguous.
 *
 * NOTE: the input order is PRESERVED (no sorting). Sorting would reorder
 * iterations after a reconnect/replace and make old iterations appear to
 * jump to the "latest progress" area (visual duplication). appendIterations
 * already appends in arrival order, which equals iteration order.
 */
export function continuousIterations(iters: WebIteration[]): WebIteration[] {
  if (iters.length < 2) return iters
  if (iters[0].iteration !== 1) return [] // no valid start — render nothing
  const out: WebIteration[] = [iters[0]]
  for (let i = 1; i < iters.length; i++) {
    const prev = out[out.length - 1]
    const curr = iters[i]
    if (curr.iteration !== prev.iteration + 1) break
    out.push(curr)
  }
  return out
}

// ── exported helpers (used by useProgressStream) ──────────────────────────

/** Detect a stream-only event: no phase/iteration, has stream fields. */
export function isStreamOnly(payload: ProgressEvent): boolean {
  const hasStreamFields =
    payload.stream_content !== undefined ||
    payload.reasoning_stream_content !== undefined ||
    payload.streaming_tools !== undefined
  if (!hasStreamFields) return false
  const noPhase = !payload.phase || payload.phase === ''
  const noIteration = !payload.iteration || payload.iteration === 0
  return noPhase && noIteration
}

/** Normalize a raw tool object (from WS event or history) into WebToolProgress. */
export function normalizeWebTool(raw: unknown): WebToolProgress | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  return {
    name: typeof r.name === 'string' ? r.name : '',
    label: typeof r.label === 'string' ? r.label : '',
    status: (typeof r.status === 'string' ? r.status : 'running') as WebToolProgress['status'],
    elapsedMs: typeof r.elapsed_ms === 'number' ? r.elapsed_ms : 0,
    summary: typeof r.summary === 'string' ? r.summary : '',
    detail: typeof r.detail === 'string' ? r.detail : '',
    args: typeof r.args === 'string' ? r.args : '',
    toolHints: typeof r.tool_hints === 'string' ? r.tool_hints : '',
    iteration: typeof r.iteration === 'number' ? r.iteration : undefined,
  }
}

function subAgentKey(node: WebSubAgentProgress): string {
  return `${node.role}:${node.instance ?? ''}`
}

// mergeSubAgentTrees — unified with TUI's cli_update_subagent.go mergeSubAgentTrees.
// Agents present in both trees are updated with new data (status, desc, children).
// Agents only in prev but NOT in next are dropped — the server stopped reporting
// them, meaning they completed. Keeping them causes zombie nodes that persist
// forever (the "explore card that never disappears" bug).
//
// Key rule: Role + ":" + Instance. Same-role different-instance agents are distinct.
function mergeSubAgentTrees(prev: WebSubAgentProgress[], next: WebSubAgentProgress[]): WebSubAgentProgress[] {
  if (prev.length === 0) return next
  if (next.length === 0) return [] // server stopped reporting all agents — they completed

  const newByKey = new Map(next.map((node) => [subAgentKey(node), node]))
  const result: WebSubAgentProgress[] = []

  // Start with all prev entries, updating those that have new data.
  // Entries NOT in new are skipped — the server stopped reporting them,
  // meaning they completed. No zombie preservation (unlike old web code).
  for (const p of prev) {
    const key = subAgentKey(p)
    const n = newByKey.get(key)
    if (n) {
      // Agent exists in both — merge: use new data but preserve previous
      // Desc when new is empty.
      const merged: WebSubAgentProgress = {
        ...n,
        desc: n.desc || p.desc,
        sessionKey: n.sessionKey || p.sessionKey,
        children: mergeSubAgentTrees(p.children ?? [], n.children ?? []),
      }
      // When parent is still active but its children list is empty in
      // the new update, preserve previous children as done.
      if ((n.children?.length ?? 0) === 0 && (p.children?.length ?? 0) > 0) {
        merged.children = markAllDone(p.children!)
      }
      result.push(merged)
      newByKey.delete(key)
    }
    // else: agent only in prev — server stopped reporting it → skip (completed)
  }

  // Add agents only in new
  for (const node of newByKey.values()) {
    result.push(node)
  }

  return result
}

function markAllDone(agents: WebSubAgentProgress[]): WebSubAgentProgress[] {
  return agents.map((a) => markDoneIfRunning(a))
}

function markDoneIfRunning(sa: WebSubAgentProgress): WebSubAgentProgress {
  const status = (sa.status === 'running' || sa.status === 'pending') ? 'done' : sa.status
  return {
    ...sa,
    status,
    children: (sa.children ?? []).map(markDoneIfRunning),
  }
}

/** Normalize an array of raw tool objects, filtering nulls. */
export function normalizeWebTools(raw: unknown[] | undefined): WebToolProgress[] {
  if (!raw || !Array.isArray(raw)) return []
  return raw.map(normalizeWebTool).filter(Boolean) as WebToolProgress[]
}

export function normalizeWebSubAgent(raw: unknown): WebSubAgentProgress | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const role = typeof r.role === 'string' ? r.role : ''
  if (!role) return null
  const children = Array.isArray(r.children)
    ? (r.children.map(normalizeWebSubAgent).filter(Boolean) as WebSubAgentProgress[])
    : []
  return {
    role,
    instance: typeof r.instance === 'string' ? r.instance : undefined,
    sessionKey: typeof r.session_key === 'string' ? r.session_key : undefined,
    status: typeof r.status === 'string' ? r.status : '',
    desc: typeof r.desc === 'string' ? r.desc : undefined,
    children,
  }
}

export function normalizeWebSubAgents(raw: unknown[] | undefined): WebSubAgentProgress[] {
  if (!Array.isArray(raw)) return []
  return raw.map(normalizeWebSubAgent).filter(Boolean) as WebSubAgentProgress[]
}

/**
 * Dedup tools by name+label.
 * generating-status tools are kept as-is (each call shows independently).
 * running/done/error tools with the same name+label are deduped (first wins).
 */
export function dedupTools(tools: WebToolProgress[]): WebToolProgress[] {
  const seen = new Set<string>()
  const result: WebToolProgress[] = []
  for (const tool of tools) {
    if (tool.status === 'generating') {
      result.push(tool)
      continue
    }
    const key = `${tool.name}\x00${tool.label}`
    if (!seen.has(key)) {
      seen.add(key)
      result.push(tool)
    }
  }
  return result
}

/**
 * Dedup messages by stable identity — no string matching.
 *
 * Strategy:
 * 1. Messages with turnID > 0: dedup by turnID:role (one message per turn per role).
 * 2. Messages with eventSeq: dedup by eventSeq (SSE sequence is globally unique).
 * 3. Messages with neither (history messages): never deduped — they have unique DB IDs.
 */
export function dedupMessages<T extends { turnID: number; role: string; content?: string; id?: string; eventSeq?: number }>(
  messages: T[],
): T[] {
  const turnSeen = new Map<string, number>()
  const seqSeen = new Set<number>()
  const result: T[] = []
  for (let i = 0; i < messages.length; i++) {
    // Dedup by turnID:role for tracked turns
    if (messages[i].turnID > 0) {
      const key = `${messages[i].turnID}:${messages[i].role}`
      const existing = turnSeen.get(key)
      if (existing !== undefined) {
        result[existing] = messages[i]
      } else {
        turnSeen.set(key, result.length)
        result.push(messages[i])
      }
      continue
    }
    // Dedup by eventSeq for live messages that have one
    const seqVal = messages[i].eventSeq
    if (seqVal != null) {
      const seq = seqVal
      if (seqSeen.has(seq)) {
        // Replace existing with the newer version (may have updated content/iterations)
        const existingIdx = result.findIndex((m) => m.eventSeq === seq)
        if (existingIdx >= 0) {
          result[existingIdx] = messages[i]
        }
        continue
      }
      seqSeen.add(seq)
    }
    // History messages (no turnID, no eventSeq) are never deduped — unique IDs.
    result.push(messages[i])
  }
  return result
}

// ── ProgressStore ──────────────────────────────────────────────────────────

export class ProgressStore {
  private current: ProgressSnapshot = { ...EMPTY_PROGRESS_SNAPSHOT }
  private snapshot: ProgressSnapshot = EMPTY_PROGRESS_SNAPSHOT
  private listeners = new Set<Listener>()
  private rafHandle: number | null = null
  private dirty = false
  private disposed = false
  /** Tracks the last seen TurnID for monotonicity assertions. 0 = untracked. */
  lastTurnID = 0
  /** Tracks the last seen iteration number within the current turn for continuity assertions.
   *  0 = uninitialized (no iteration seen yet). Iterations are 1-based. */
  lastIter = 0

  /** Subscribe to snapshot changes; returns an unsubscribe function. */
  subscribe = (listener: Listener): (() => void) => {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  /** Current snapshot. Stable between notifies (same reference). */
  getSnapshot = (): ProgressSnapshot => this.snapshot

  /** Apply a mutation under the hood; schedules a throttled notify. */
  mutate(mutator: Mutator): void {
    if (this.disposed) return
    mutator(this.current)
    this.dirty = true
    this.scheduleNotify()
  }

  /** Reset to idle (after a run completes or on errors). Synchronously flushes
   *  the snapshot so useSyncExternalStore immediately reads the empty state,
   *  preventing liveMessage and committed message from coexisting for a frame.
   *  Preserves todos — they are per-session state managed by TodoWrite tool,
   *  not per-turn streaming state. */
  reset(): void {
    if (this.disposed) return
    const todos = this.current.todos
    this.current = { ...EMPTY_PROGRESS_SNAPSHOT, todos }
    // Synchronously update snapshot + cancel pending RAF — avoids a one-frame
    // window where liveMessage is still non-null after reset.
    this.snapshot = { ...EMPTY_PROGRESS_SNAPSHOT, todos }
    this.dirty = false
    this.lastIter = 0
    // lastTurnID is NOT reset here — it tracks across turns for monotonicity.
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    // Notify listeners immediately (synchronous) so React re-render sees empty snapshot.
    this.listeners.forEach((l) => l())
  }

  /** Full reset including todos — used on session switch. */
  fullReset(): void {
    if (this.disposed) return
    this.current = { ...EMPTY_PROGRESS_SNAPSHOT }
    this.snapshot = { ...EMPTY_PROGRESS_SNAPSHOT }
    this.dirty = false
    this.lastTurnID = 0
    this.lastIter = 0
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    this.listeners.forEach((l) => l())
  }

  /** Reset only streaming fields, preserving iterationHistory, todos,
   *  AND in-flight stream text (streamContent/reasoningStreamContent/streaming).
   *
   *  streamContent/reasoningStreamContent are cumulative values that only grow
   *  within a turn. Clearing them mid-turn (e.g. on a synthetic session(busy)
   *  from SSE reconnect recovery) wipes the accumulated text, which causes the
   *  typewriter to reset to 0 and re-type the entire message ("来回 type").
   *  They are already cleared at genuine iteration boundaries
   *  (setStructuredTools) and on full reset(). */
  resetStreamingState(): void {
    if (this.disposed) return
    this.mutate((draft) => {
      draft.phase = ''
      draft.streamingTools = []
      draft.activeTools = []
      draft.completedTools = []
      draft.genuiContent = ''
      draft.lastReasoning = ''
      // Keep: iterationHistory, todos, subAgents, tokenUsage, iteration, lastIter,
      //       streamContent, reasoningStreamContent, content, streaming
    })
  }

  /** Freeze streaming — stop typewriter and animations, but keep ALL content
   *  visible as-is. Used on cancel: the user sees exactly what was on screen
   *  at the moment of cancel, with no re-render, no disappearance, no animation.
   *  The frozen state persists until the next turn's turn_started clears it.
   *
   *  Synchronously flushes the snapshot (like reset()) so session(idle) can
   *  immediately read phase='frozen' and skip the reset that would clear
   *  the frozen content. */
  freeze(): void {
    if (this.disposed) return
    this.mutate((draft) => {
      draft.streaming = false
      draft.phase = 'frozen'
      draft.streamingTools = []
      // Mark all in-progress tools as error (cancelled)
      const markError = (tools: WebToolProgress[]) => {
        for (const t of tools) {
          if (t.status === 'running' || t.status === 'generating' || t.status === 'pending') {
            t.status = 'error'
          }
        }
      }
      markError(draft.activeTools)
      markError(draft.completedTools)
      markError(draft.streamingTools)
      // Also mark in-progress tools inside iteration history
      for (const iter of draft.iterationHistory) {
        markError(iter.tools)
      }
    })
    // Synchronously update snapshot so getSnapshot() returns 'frozen' immediately
    this.snapshot = { ...this.current }
    this.dirty = false
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    this.listeners.forEach((l) => l())
  }

  /** Set streamed assistant text (cumulative value from stream_content events). */
  appendStreamContent(delta: string): void {
    if (!delta) return
    this.mutate((draft) => {
      draft.streamContent = delta  // cumulative value, use assignment not append
      draft.streaming = true
    })
  }

  /** Set streamed reasoning text (cumulative value from reasoning_stream_content events). */
  appendReasoningContent(delta: string): void {
    if (!delta) return
    this.mutate((draft) => {
      draft.reasoningStreamContent = delta  // cumulative value, use assignment not append
      draft.streaming = true
    })
  }

  /** Set streaming GenUI HTML content (from display_html tool arguments). */
  setGenUIContent(content: string): void {
    if (!content) return
    this.mutate((draft) => {
      draft.genuiContent = content
      draft.streaming = true
    })
  }

  /**
   * Apply stream-only fields (streaming_tools) without replacing the snapshot.
   * Called for stream_content events that carry tool-name detection (generating).
   */
  setStreamOnlyFields(opts: { streamingTools?: WebToolProgress[] }): void {
    this.mutate((draft) => {
      if (opts.streamingTools) {
        draft.streamingTools = opts.streamingTools
      }
    })
  }

  /** Stop streaming animations (typewriter) but KEEP all content/tools visible.
   *  Used on PhaseDone: the turn is over, so busy fallbacks (progressSnapshot.
   *  streaming) must return false and the typewriter must stop — but tools and
   *  iterations stay rendered until the text event commits them atomically. */
  stopStreaming(): void {
    if (this.disposed) return
    this.mutate((draft) => {
      draft.streaming = false
    })
  }

  /**
   * Apply a structured progress event with carry-forward + iteration snapshot.
   *
   * Stream-only fields (streamContent, reasoningStreamContent, streamingTools)
   * are preserved from current state — NOT overwritten by this method.
   * Structured fields (phase, iteration, activeTools, completedTools) are replaced.
   */
  setStructuredTools(opts: {
    eventSeq?: number
    phase?: string
    iteration?: number
    content?: string
    activeTools?: WebToolProgress[]
    completedTools?: WebToolProgress[]
    reasoning?: string
    iterationHistory?: WebIteration[]
    streamingTools?: WebToolProgress[]
    todos?: TodoItem[]
    subAgents?: WebSubAgentProgress[]
    tokenUsage?: TokenUsageInfo | null
    turnID?: number
  }): void {
    // ProgressEvent.Seq is the semantic log ID assigned before channel fan-out.
    // Replayed/duplicate events at or below the installed snapshot watermark
    // are no-ops — EXCEPT for iterationHistory. Recovery events (from
    // restoreActiveProgress) carry the same seq as the last event (they
    // come from lastProgressSnapshot), so the seq check would drop them
    // entirely — including their iterationHistory. We must still append
    // any new iterations before returning, otherwise lost iterations
    // (from dropped delta events) are permanently lost.
    //
    // Note: phase==='done' is handled by useProgressStream BEFORE calling
    // setStructuredTools (it only forwards eventSeq + todos, never phase).
    // So opts.phase === 'done' is never true here — no dead branch needed.
    if (opts.eventSeq !== undefined && opts.eventSeq <= this.current.eventSeq) {
      if (opts.todos !== undefined) {
        this.current.todos = opts.todos
      }
      if (opts.iterationHistory && opts.iterationHistory.length > 0) {
        this.mutate((draft) => {
          appendIterations(draft, opts.iterationHistory!)
          // Recovery snapshot (from restoreActiveProgress) is authoritative —
          // also update structured fields so the current iteration's tools,
          // phase, and content are restored. Without this, only iterationHistory
          // is appended but the current iteration's activeTools/completedTools
          // remain stale (from the last live event before disconnect), causing
          // the current iteration to show incomplete tool state.
          if (opts.phase !== undefined) {
            draft.phase = opts.phase
            draft.streaming = opts.phase !== 'done'
          }
          if (opts.iteration !== undefined) draft.iteration = opts.iteration
          if (opts.activeTools) draft.activeTools = dedupTools(opts.activeTools)
          if (opts.completedTools) {
            const currentIter = opts.iteration ?? draft.iteration
            const filtered = currentIter > 0
              ? opts.completedTools.filter((t) => t.iteration === undefined || t.iteration === currentIter)
              : opts.completedTools
            draft.completedTools = dedupTools(filtered)
          }
          if (opts.content !== undefined) draft.content = opts.content
          if (opts.reasoning) draft.lastReasoning = opts.reasoning
          if (opts.todos !== undefined) draft.todos = opts.todos
          if (opts.subAgents !== undefined) draft.subAgents = mergeSubAgentTrees(draft.subAgents, opts.subAgents)
          if (opts.tokenUsage !== undefined && opts.tokenUsage !== null) draft.tokenUsage = opts.tokenUsage
        })
      }
      return
    }

    this.mutate((draft) => {
      if (opts.eventSeq !== undefined) draft.eventSeq = opts.eventSeq
      if (opts.turnID !== undefined && opts.turnID > 0) draft.turnID = opts.turnID
      // ── semantic log watermark ──
      // IterationHistory from the backend is the only authoritative completed-
      // iteration log for every channel. The client must never synthesize a
      // second log entry while advancing the current snapshot: after reconnect,
      // the installed snapshot and replayed delta can overlap, and local
      // snapshotting would render the same tool group twice.
      if (opts.iteration !== undefined && opts.iteration > draft.lastIter) {
        const hadPreviousIteration = draft.lastIter >= 1
        draft.lastIter = opts.iteration
        // Clear stream/structured fields from the previous iteration so the
        // new iteration starts clean. The completed iteration itself arrives
        // through opts.iterationHistory with its backend iteration watermark.
        if (hadPreviousIteration) {
          draft.streamContent = ''
          draft.reasoningStreamContent = ''
          draft.genuiContent = ''
          draft.content = ''
          draft.streamingTools = []
          draft.activeTools = []
          draft.completedTools = []
          draft.subAgents = []
          draft.lastReasoning = ''
        }
      }

      // ── carry-forward: preserve stream-only fields within same iteration ──
      // streamContent, reasoningStreamContent are NOT overwritten here — they
      // are only modified by stream_content events. streamingTools is filtered
      // below to remove stale generating tools that have transitioned to active.

      // ── store structured content as fallback for streamContent ──
      // Server may send text via structured events (Content field) instead of
      // stream_content. Store it so LiveIteration can use it when streamContent
      // is empty — prevents text from disappearing mid-iteration.
      if (opts.content !== undefined) draft.content = opts.content

      // ── replace structured fields ──
      // Filter completedTools by current iteration to prevent cross-iteration
      // tool pollution. Server sends ALL completed tools across all iterations;
      // we only want the current iteration's tools (previous iterations are
      // already in iterationHistory).
      const currentIter = opts.iteration ?? draft.iteration
      if (opts.activeTools) draft.activeTools = dedupTools(opts.activeTools)
      if (opts.completedTools) {
        const filtered = currentIter > 0
          ? opts.completedTools.filter((t) => t.iteration === undefined || t.iteration === currentIter)
          : opts.completedTools
        draft.completedTools = dedupTools(filtered)
      }
      if (opts.iteration !== undefined) draft.iteration = opts.iteration

      // ── filter stale generating tools ──
      // A tool that was "generating" (from stream_content) may have transitioned
      // to "running"/"done" (in activeTools/completedTools). Filter it out of
      // streamingTools to prevent showing the same tool twice.
      // Mirrors TUI carryForwardProgressState (cli_update_progress.go:119-131).
      if (draft.streamingTools.length > 0 && (opts.activeTools || opts.completedTools)) {
        const activeNames = new Set<string>()
        for (const t of draft.activeTools) activeNames.add(t.name)
        for (const t of draft.completedTools) activeNames.add(t.name)
        draft.streamingTools = draft.streamingTools.filter(
          (t) => !activeNames.has(t.name),
        )
      }

      // ── phase + streaming ──
      if (opts.phase !== undefined) {
        draft.phase = opts.phase
        draft.streaming = opts.phase !== 'done'
      }

      // ── reasoning is a snapshot (non-incremental), replace lastReasoning ──
      if (opts.reasoning) {
        draft.lastReasoning = opts.reasoning
      }

      // ── streamingTools: update if provided ──
      if (opts.streamingTools) {
        draft.streamingTools = opts.streamingTools
      }

      // ── iterationHistory: Delta Push protocol — server sends only newly
      // completed iterations (0-1 entries). Must append with dedup by
      // iteration number, NOT replace. Replacing loses all prior iterations.
      if (opts.iterationHistory && opts.iterationHistory.length > 0) {
        const before = draft.iterationHistory.length
        appendIterations(draft, opts.iterationHistory)
        // If a new completed iteration was added, its tools are now in
        // iterationHistory (rendered via TurnBody). Clear completedTools
        // so LiveIteration doesn't render them again alongside the
        // iterationHistory entry — prevents tool doubling.
        if (draft.iterationHistory.length > before) {
          draft.completedTools = []
        }
      }

      // ── todos: always update when present (including empty arrays).
      //  undefined = event carries no todo data → carry-forward.
      //  [] = todo_write([]) explicitly cleared todos → update to empty.
      if (opts.todos !== undefined) {
        draft.todos = opts.todos
      }
      // subAgents: merge when non-empty, REPLACE when empty array (new iteration
      // cleared them). mergeSubAgentTrees would keep stale subAgents from a
      // previous iteration forever — the new iteration's SubAgent calls will
      // populate fresh nodes, but old ones must be cleared first.
      if (opts.subAgents !== undefined) {
        if (opts.subAgents.length === 0) {
          draft.subAgents = []
        } else {
          draft.subAgents = mergeSubAgentTrees(draft.subAgents, opts.subAgents)
        }
      }

      // ── tokenUsage: carry-forward when not present (mirrors TUI behavior).
      //  Only update when a non-null tokenUsage is provided; null means "no data"
      //  in this event, preserving the previous value.
      if (opts.tokenUsage !== undefined && opts.tokenUsage !== null) {
        draft.tokenUsage = opts.tokenUsage
      }
    })
  }

  /** Set iteration history directly (from history hydration). */
  setIterationHistory(history: WebIteration[]): void {
    this.mutate((draft) => {
      draft.iterationHistory = history
    })
  }

  /** Replace the whole progress (e.g. from history active_progress).
   *  iterationHistory is MERGED by iteration number (union) — not replaced —
   *  so a stale server snapshot can't clobber iterations already in the store
   *  from live SSE events or cache restoration.
   *  completedTools is filtered by current iteration (same as setStructured) —
   *  the server sends ALL completed tools across iterations, but only the
   *  current iteration's tools belong here (old iterations are in
   *  iterationHistory). Without this filter, LiveIteration's iteration-based
   *  filter removes old tools AND they're not in iterationHistory → disappear.
   *  lastIter and eventSeq are client-side tracking fields — NOT overwritten
   *  by server data. lastIter is computed from the merged iterationHistory
   *  (max iteration), eventSeq takes the max of current and incoming. */
  replace(next: Partial<ProgressSnapshot>): void {
    this.mutate((draft) => {
      // Filter completedTools by current iteration (mirrors setStructured)
      if (next.completedTools) {
        const currentIter = next.iteration ?? draft.iteration
        const filtered = currentIter > 0
          ? next.completedTools.filter((t) => t.iteration === undefined || t.iteration === currentIter)
          : next.completedTools
        draft.completedTools = dedupTools(filtered)
      }
      // Merge iterationHistory by iteration number (union)
      if (next.iterationHistory) {
        appendIterations(draft, next.iterationHistory)
        // On hydrate, completed_tools from active_progress may include tools
        // from completed iterations that are now in iterationHistory. Exclude
        // them by iteration number — they're rendered via TurnBody, not LiveIteration.
        const completedIterSet = new Set<number>()
        for (const iter of draft.iterationHistory) {
          completedIterSet.add(iter.iteration)
        }
        if (completedIterSet.size > 0) {
          draft.completedTools = draft.completedTools.filter(
            (t) => (t.iteration === undefined || t.iteration === null) || !completedIterSet.has(t.iteration),
          )
        }
        // Recompute lastIter from merged history so the delta push protocol
        // continues correctly (next SSE event knows which iterations exist).
        const maxIter = draft.iterationHistory.reduce((max, i) => Math.max(max, i.iteration), 0)
        if (maxIter > draft.lastIter) draft.lastIter = maxIter
      }
      // Assign remaining fields, but NEVER downgrade client-side tracking:
      // - eventSeq: take max (server seq may be older than what SSE already delivered)
      // - lastIter: already computed above from merged iterationHistory
      const { completedTools: _ct, iterationHistory: _ih, eventSeq: _es, lastIter: _li, ...rest } = next
      if (next.eventSeq !== undefined && next.eventSeq > draft.eventSeq) {
        draft.eventSeq = next.eventSeq
      }
      Object.assign(draft, rest)
    })
  }

  dispose(): void {
    this.disposed = true
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    this.listeners.clear()
  }

  /* ── internals ── */

  private scheduleNotify(): void {
    if (this.rafHandle !== null) return // already scheduled this frame
    this.rafHandle = requestAnimationFrame(() => {
      this.rafHandle = null
      this.flush()
    })
  }

  /** Build a fresh immutable snapshot (shallow-copied top-level) and notify. */
  private flush(): void {
    if (this.disposed || !this.dirty) return
    this.dirty = false
    this.snapshot = {
      eventSeq: this.current.eventSeq,
      phase: this.current.phase,
      iteration: this.current.iteration,
      streamContent: this.current.streamContent,
      content: this.current.content,
      reasoningStreamContent: this.current.reasoningStreamContent,
      streaming: this.current.streaming,
      activeTools: this.current.activeTools,
      completedTools: this.current.completedTools,
      iterationHistory: this.current.iterationHistory,
      streamingTools: this.current.streamingTools,
      genuiContent: this.current.genuiContent,
      lastIter: this.current.lastIter,
      lastReasoning: this.current.lastReasoning,
      todos: this.current.todos,
      // TODO-DBG: log todos in snapshot
      ...(this.current.todos.length > 0 ? { _todoDbg: this.current.todos.length } : {}),
      subAgents: this.current.subAgents,
      tokenUsage: this.current.tokenUsage,
      turnID: this.current.turnID,
    }
    this.listeners.forEach((l) => l())
  }
}

/** Create an isolated progress store. Caller owns its lifetime (dispose). */
export function createProgressStore(): ProgressStore {
  return new ProgressStore()
}
