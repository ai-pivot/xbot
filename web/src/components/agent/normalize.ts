/**
 * Normalizers turning raw backend shapes (history rows, WS progress payloads,
 * iteration-history JSON) into the clean Agent domain types (Spec 3/4).
 *
 * Shared by useChatMessages (history hydration) and useProgressStream (live
 * events) so the two paths never diverge on how a tool/iteration is parsed.
 */
import {
  normalizeWebSubAgents,
  normalizeWebTool,
  normalizeWebTools,
} from '@/components/agent/progressStore'
import type { HistProgress } from '@/components/agent/api'
import type { WebIteration, WebToolProgress, ProgressSnapshot, TodoItem } from '@/types/shared'
import { EMPTY_PROGRESS_SNAPSHOT } from '@/types/shared'
import type { IterationSnapshot, IterationTool, ToolProgress } from '@/types/agent'

// ── WebIteration normalizers (Spec 3 shared types) ─────────────────────────

/** Coerce a raw iteration-history entry into WebIteration.
 *  Reads `tools` (from `detail` JSON) and falls back to `completed_tools`
 *  (the slim histIterSnapshot shape from GET /api/history active_progress). */
export function normalizeWebIteration(raw: unknown): WebIteration | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const rawTools = Array.isArray(r.tools) ? r.tools : Array.isArray(r.completed_tools) ? r.completed_tools : []
  const tools = rawTools.map(normalizeWebTool).filter(Boolean) as WebToolProgress[]
  // Per-iteration LLM metrics. Two sources:
  //  - stream_stats (live progress event): { ttft_ms, tokens_per_sec, total_ms }
  //  - tokens (DB persisted iteration record): per-iteration completion tokens
  const stats = (r.stream_stats ?? r.stats) as Record<string, unknown> | undefined
  const ttftMs = stats != null && typeof stats.ttft_ms === 'number' ? stats.ttft_ms as number : undefined
  const tokensPerSec = stats != null && typeof stats.tokens_per_sec === 'number' ? stats.tokens_per_sec as number : undefined
  const tokens = typeof r.tokens === 'number' ? r.tokens as number : undefined
  // Tool wall-time = sum of the iteration's tool elapsedMs.
  const toolMs = tools.reduce((sum, t) => sum + (t.elapsedMs || 0), 0)
  return {
    iteration: typeof r.iteration === 'number' ? r.iteration : 0,
    // 文本输出以 "content" 为准（后端 HistoryIteration.Content = 迭代文本输出，
    // 最终回复 = 最终 iter 的 content）。旧快照可能用 "thinking" —— 兼容 fallback。
    content: typeof r.content === 'string' && r.content !== ''
      ? r.content
      : typeof r.thinking === 'string' ? r.thinking : '',
    reasoning: typeof r.reasoning === 'string' ? r.reasoning : '',
    tools,
    toolCount: tools.length,
    tokens,
    ttftMs,
    tokensPerSec,
    toolMs,
    // 该迭代 spawn 的 SubAgent 树（后台 SubAgent 的进度归属原迭代）。
    subAgents: normalizeWebSubAgents(Array.isArray(r.sub_agents) ? r.sub_agents : undefined),
  }
}

/** Parse a `detail`/`progress_history` JSON string into WebIteration[]. */
export function parseWebIterations(json: string | undefined | null): WebIteration[] {
  if (!json) return []
  try {
    const parsed = JSON.parse(json)
    if (!Array.isArray(parsed)) return []
    return parsed.map(normalizeWebIteration).filter(Boolean) as WebIteration[]
  } catch {
    return []
  }
}

// ── Legacy normalizers (kept for backward compat with components using IterationSnapshot) ──

/** Coerce a raw iteration-history entry (from `detail` JSON) into IterationSnapshot.
 *  @deprecated use normalizeWebIteration instead */
export function normalizeIteration(raw: unknown): IterationSnapshot | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const rawTools = Array.isArray(r.tools) ? r.tools : Array.isArray(r.completed_tools) ? r.completed_tools : []
  return {
    iteration: typeof r.iteration === 'number' ? r.iteration : 0,
    content: typeof r.content === 'string' && r.content !== ''
      ? r.content
      : typeof r.thinking === 'string' ? r.thinking : undefined,
    reasoning: typeof r.reasoning === 'string' ? r.reasoning : undefined,
    elapsedMs: typeof r.elapsed_wall === 'number' ? r.elapsed_wall : undefined,
    tools: rawTools.map(normalizeIterationTool).filter(Boolean) as IterationTool[],
  }
}

export function normalizeIterationTool(raw: unknown): IterationTool | null {
  if (!raw || typeof raw !== 'object') return null
  const t = raw as Record<string, unknown>
  return {
    name: typeof t.name === 'string' ? t.name : '',
    label: typeof t.label === 'string' ? t.label : undefined,
    status: typeof t.status === 'string' ? t.status : 'done',
    elapsedMs: typeof t.elapsed_ms === 'number' ? t.elapsed_ms : undefined,
    summary: typeof t.summary === 'string' ? t.summary : undefined,
  }
}

/** Coerce a raw tool_calls/active_tools entry (from a progress event) into ToolProgress.
 *  @deprecated use normalizeWebTool from progressStore instead */
export function normalizeTool(raw: unknown): ToolProgress | null {
  if (!raw || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  return {
    name: typeof r.name === 'string' ? r.name : undefined,
    label: typeof r.label === 'string' ? r.label : undefined,
    status: typeof r.status === 'string' ? r.status : undefined,
    elapsedMs: typeof r.elapsed_ms === 'number' ? r.elapsed_ms : undefined,
    iteration: typeof r.iteration === 'number' ? r.iteration : undefined,
    summary: typeof r.summary === 'string' ? r.summary : undefined,
    detail: typeof r.detail === 'string' ? r.detail : undefined,
    args: typeof r.args === 'string' ? r.args : undefined,
  }
}

/** Parse a `detail`/`progress_history` JSON string into IterationSnapshot[].
 *  @deprecated use parseWebIterations instead */
export function parseIterations(json: string | undefined | null): IterationSnapshot[] {
  if (!json) return []
  try {
    const parsed = JSON.parse(json)
    if (!Array.isArray(parsed)) return []
    return parsed.map(normalizeIteration).filter(Boolean) as IterationSnapshot[]
  } catch {
    return []
  }
}

// ── History hydration (Spec 3 §2.4) ─────────────────────────────────────────

/**
 * Normalize a history `active_progress` snapshot into a ProgressSnapshot
 * suitable for store.replace(). A busy session (phase != done) resumed after
 * a page refresh can hydrate the ProgressStore so the progress panel resumes
 * instead of showing an empty stream.
 */
export function historyProgressToLive(p: HistProgress | null): ProgressSnapshot {
  if (!p || !p.phase || p.phase === 'done') {
    // Even for done/idle sessions, restore todos so they survive session switch.
    const todos = (p?.todos ?? []) as TodoItem[]
    return { ...EMPTY_PROGRESS_SNAPSHOT, todos }
  }
  const active = normalizeWebTools(p.active_tools)
  let completed = normalizeWebTools(p.completed_tools)
  const iterHistory = (p.iteration_history ?? [])
    .map(normalizeWebIteration)
    .filter(Boolean) as WebIteration[]

  // ── Iteration boundary guard ──
  // When GetActiveProgress is called after snapshotCompletedIteration but
  // before prepareForIteration, the snapshot carries the PREVIOUS iteration's
  // Content and CompletedTools (not yet cleared). If activeTools is empty but
  // completedTools is non-empty, the snapshot is at this boundary.
  //
  // Without this guard, LiveIteration renders the stale Content and
  // CompletedTools as if they belong to the current (in-flight) iteration,
  // duplicating them alongside the iterationHistory entry.
  //
  // Fix: clear content and completedTools from the live snapshot. Do NOT add
  // a synthetic entry to iterationHistory — it would be appended at the END
  // of the array (which is the BEGINNING if the store is empty), causing the
  // content to appear BEFORE earlier iterations' tools. The delta protocol
  // will deliver the correct data in order when the next iteration starts.
  let content = p.content ?? ''
  if (active.length === 0 && completed.length > 0) {
    content = ''
    completed = []
  }

  return {
    eventSeq: typeof p.seq === 'number' ? p.seq : 0,
    phase: p.phase,
    iteration: typeof p.iteration === 'number' ? p.iteration : 0,
    streamContent: p.stream_content ?? '',
    content,
    reasoningStreamContent: p.reasoning_stream_content ?? '',
    streaming: true,
    activeTools: active,
    completedTools: completed,
    iterationHistory: iterHistory,
    streamingTools: [],
    genuiContent: '',
    lastIter: 0, // 0 = uninitialized; iterations are 1-based
    lastReasoning: p.reasoning ?? '',
    todos: (p.todos ?? []) as TodoItem[],
    subAgents: normalizeWebSubAgents(p.sub_agents),
    tokenUsage: null,
    turnID: typeof p.turn_id === 'number' && p.turn_id > 0 ? p.turn_id : 0,
  }
}
