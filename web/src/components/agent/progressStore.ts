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
 * 3. **iteration snapshot** — when iteration changes (N→N+1), the previous
 *    iteration's reasoning/thinking/tools are snapshotted into iterationHistory.
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
  }
}

function subAgentKey(node: WebSubAgentProgress): string {
  return `${node.role}:${node.instance ?? ''}`
}

function mergeSubAgentTrees(prev: WebSubAgentProgress[], next: WebSubAgentProgress[]): WebSubAgentProgress[] {
  if (next.length === 0) return prev
  const prevByKey = new Map(prev.map((node) => [subAgentKey(node), node]))
  const nextKeys = new Set(next.map(subAgentKey))
  const merged: WebSubAgentProgress[] = []

  // Preserve prev nodes that are no longer in next but are done/error — they
  // should remain visible (with their final status) rather than disappearing.
  for (const old of prev) {
    if (!nextKeys.has(subAgentKey(old)) && (old.status === 'done' || old.status === 'error')) {
      merged.push(old)
    }
  }

  for (const node of next) {
    // Done/error nodes are kept — they show the final state of the SubAgent.
    // The rendering layer (SubAgentProgressTree) applies gray/red styling.
    const old = prevByKey.get(subAgentKey(node))
    merged.push({
      ...node,
      sessionKey: node.sessionKey || old?.sessionKey,
      desc: node.desc || old?.desc,
      children: mergeSubAgentTrees(old?.children ?? [], node.children ?? []),
    })
  }
  return merged
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
 * Dedup messages by (turnID, role): only the last occurrence is kept.
 * For turnID=0 messages, only dedup live-append messages (id starts with 'asst-')
 * by (role, content) — prevents duplicate committed messages from multiple
 * onAssistantComplete calls. History messages (DB id) are never deduped.
 */
export function dedupMessages<T extends { turnID: number; role: string; content?: string; id?: string }>(
  messages: T[],
): T[] {
  const seen = new Map<string, number>()
  const result: T[] = []
  for (let i = 0; i < messages.length; i++) {
    // Dedup by turnID:role for tracked turns
    if (messages[i].turnID > 0) {
      const key = `${messages[i].turnID}:${messages[i].role}`
      const existing = seen.get(key)
      if (existing !== undefined) {
        result[existing] = messages[i]
      } else {
        seen.set(key, result.length)
        result.push(messages[i])
      }
      continue
    }
    // For turnID=0 assistant messages, only dedup live-append messages (id starts with 'asst-').
    // History messages (DB numeric id) are never deduped — they have unique ids.
    const content = messages[i].content ?? ''
    const id = messages[i].id ?? ''
    if (content && messages[i].role === 'assistant' && id.startsWith('asst-')) {
      const contentKey = `${messages[i].role}:${content}`
      const existingIdx = seen.get(contentKey)
      if (existingIdx !== undefined) {
        result[existingIdx] = messages[i]
        continue
      }
      seen.set(contentKey, result.length)
    }
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
  private finalizingTimer: ReturnType<typeof setTimeout> | null = null

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
   */
  reset(): void {
    if (this.disposed) return
    // Clear any pending finalizing timeout — reset means we're done waiting.
    if (this.finalizingTimer) {
      clearTimeout(this.finalizingTimer)
      this.finalizingTimer = null
    }
    this.current = { ...EMPTY_PROGRESS_SNAPSHOT }
    // Synchronously update snapshot + cancel pending RAF — avoids a one-frame
    // window where liveMessage is still non-null after reset.
    this.snapshot = { ...EMPTY_PROGRESS_SNAPSHOT }
    this.dirty = false
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    // Notify listeners immediately (synchronous) so React re-render sees empty snapshot.
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

  /**
   * Apply a structured progress event with carry-forward + iteration snapshot.
   *
   * Stream-only fields (streamContent, reasoningStreamContent, streamingTools)
   * are preserved from current state — NOT overwritten by this method.
   * Structured fields (phase, iteration, activeTools, completedTools) are replaced.
   */
  setStructuredTools(opts: {
    phase?: string
    iteration?: number
    activeTools?: WebToolProgress[]
    completedTools?: WebToolProgress[]
    reasoning?: string
    iterationHistory?: WebIteration[]
    streamingTools?: WebToolProgress[]
    todos?: TodoItem[]
    subAgents?: WebSubAgentProgress[]
    tokenUsage?: TokenUsageInfo | null
  }): void {
    // ── PhaseDone → finalizing transition ──
    // When phase='done' arrives, enter the 'finalizing' state instead of
    // immediately resetting. This keeps the progress snapshot visible (tools
    // marked done, no pulse animation) while waiting for the final `text`
    // event to arrive. A 3s timeout guards against missing text events.
    if (opts.phase === 'done') {
      this.mutate((draft) => {
        // Mark all active tools as completed
        if (draft.activeTools.length > 0) {
          draft.completedTools = [...draft.completedTools, ...draft.activeTools]
          draft.activeTools = []
        }
        draft.phase = 'finalizing'
        draft.streaming = false
      })
      this.startFinalizingTimeout()
      return
    }

    // Cancel any pending finalizing timeout — a new structured event means
    // the agent is active again (e.g. a new turn started after PhaseDone).
    if (this.finalizingTimer) {
      clearTimeout(this.finalizingTimer)
      this.finalizingTimer = null
    }

    this.mutate((draft) => {
      // ── iteration snapshot ──
      // When iteration advances (N→N+1), snapshot the previous iteration.
      // lastIter starts at -1; first advance sets it without snapshotting.
      if (opts.iteration !== undefined && opts.iteration > draft.lastIter) {
        const hadPreviousIteration = draft.lastIter >= 0
        if (hadPreviousIteration) {
          const snap: WebIteration = {
            iteration: draft.lastIter,
            thinking: draft.streamContent,
            reasoning: draft.lastReasoning || draft.reasoningStreamContent,
            tools: dedupTools(draft.completedTools),
            toolCount: draft.completedTools.length,
          }
          draft.iterationHistory = [...draft.iterationHistory, snap]
        }
        draft.lastIter = opts.iteration
        // Clear stream/structured fields from the previous iteration so the
        // new iteration starts clean. Only clear when there was an actual
        // previous iteration (lastIter was >= 0 before the update).
        // Mirrors TUI: iteration switch = sameIter=false → no carry-forward.
        if (hadPreviousIteration) {
          draft.streamContent = ''
          draft.reasoningStreamContent = ''
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

      // ── replace structured fields ──
      if (opts.activeTools) draft.activeTools = dedupTools(opts.activeTools)
      if (opts.completedTools) draft.completedTools = dedupTools(opts.completedTools)
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
        const existing = new Set(draft.iterationHistory.map((i) => i.iteration))
        const appended = [...draft.iterationHistory]
        for (const iter of opts.iterationHistory) {
          if (!existing.has(iter.iteration)) {
            appended.push(iter)
            existing.add(iter.iteration)
          }
        }
        draft.iterationHistory = appended
      }

      // ── todos: carry-forward when not present (mirrors TUI cli_update_progress).
      //  An empty/undefined todos means the event carries no todo data, not that
      //  todos were deleted. Only update when a non-empty array is provided.
      if (opts.todos && opts.todos.length > 0) {
        draft.todos = opts.todos
      }
      if (opts.subAgents !== undefined) {
        draft.subAgents = mergeSubAgentTrees(draft.subAgents, opts.subAgents)
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

  /** Replace the whole progress (e.g. from history active_progress). */
  replace(next: Partial<ProgressSnapshot>): void {
    this.mutate((draft) => {
      Object.assign(draft, next)
    })
  }

  dispose(): void {
    this.disposed = true
    if (this.finalizingTimer) {
      clearTimeout(this.finalizingTimer)
      this.finalizingTimer = null
    }
    if (this.rafHandle !== null) {
      cancelAnimationFrame(this.rafHandle)
      this.rafHandle = null
    }
    this.listeners.clear()
  }

  /** Start a 1.5s timeout to auto-reset if the `text` event never arrives. */
  private startFinalizingTimeout(): void {
    if (this.finalizingTimer) clearTimeout(this.finalizingTimer)
    this.finalizingTimer = setTimeout(() => {
      this.finalizingTimer = null
      this.reset()
    }, 1500)
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
      phase: this.current.phase,
      iteration: this.current.iteration,
      streamContent: this.current.streamContent,
      reasoningStreamContent: this.current.reasoningStreamContent,
      streaming: this.current.streaming,
      activeTools: this.current.activeTools,
      completedTools: this.current.completedTools,
      iterationHistory: this.current.iterationHistory,
      streamingTools: this.current.streamingTools,
      lastIter: this.current.lastIter,
      lastReasoning: this.current.lastReasoning,
      todos: this.current.todos,
      subAgents: this.current.subAgents,
      tokenUsage: this.current.tokenUsage,
    }
    this.listeners.forEach((l) => l())
  }
}

/** Create an isolated progress store. Caller owns its lifetime (dispose). */
export function createProgressStore(): ProgressStore {
  return new ProgressStore()
}
