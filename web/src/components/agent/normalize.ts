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
  return {
    iteration: typeof r.iteration === 'number' ? r.iteration : 0,
    thinking: typeof r.thinking === 'string' ? r.thinking : '',
    reasoning: typeof r.reasoning === 'string' ? r.reasoning : '',
    tools,
    toolCount: tools.length,
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
    thinking: typeof r.thinking === 'string' ? r.thinking : undefined,
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
    return { ...EMPTY_PROGRESS_SNAPSHOT }
  }
  const active = normalizeWebTools(p.active_tools)
  const completed = normalizeWebTools(p.completed_tools)
  const iterHistory = (p.iteration_history ?? [])
    .map(normalizeWebIteration)
    .filter(Boolean) as WebIteration[]
  return {
    phase: p.phase,
    iteration: typeof p.iteration === 'number' ? p.iteration : 0,
    streamContent: p.stream_content ?? '',
    reasoningStreamContent: '',
    streaming: true,
    activeTools: active,
    completedTools: completed,
    iterationHistory: iterHistory,
    streamingTools: [],
    lastIter: typeof p.iteration === 'number' ? p.iteration : 0,
    lastReasoning: '',
    todos: (p.todos ?? []) as TodoItem[],
    subAgents: normalizeWebSubAgents(p.sub_agents),
  }
}
