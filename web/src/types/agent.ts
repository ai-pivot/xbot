/**
 * Agent rendering domain types (Spec 4).
 *
 * Pure data types for the Agent workspace: chat messages, iteration history,
 * tool/reasoning snapshots, ask-user interactions, and the collapse-level
 * preference. These mirror the Go shapes consumed over the HTTP history API
 * and the WS progress stream (see protocol/events.go, agent/engine.go,
 * channel/web/web_api.go). Keeping them in one module avoids circular imports
 * between the hooks and components.
 *
 * Spec 3 migration: `LiveProgress` is now an alias for `ProgressSnapshot`
 * (defined in `shared.ts`). `ChatMessage` is re-exported from `shared.ts`.
 * The stream-only field `reasoningContent` has been renamed to
 * `reasoningStreamContent` to match the spec.
 *
 * Conventions:
 *  - `id` is a string for messages (DB row ids are coerced to string for stable
 *    React keys across reload + live append).
 *  - Optional backend fields are typed optional/nullable and normalized at the
 *    hook boundary so components can assume a clean shape.
 */

// Re-export shared types from Spec 3 (shared.ts) so existing import paths
// (`@/types/agent`) continue to work during the migration.
export {
  type ProgressSnapshot,
  type WebToolProgress,
  type WebIteration,
  type ChatMessage,
  type ChatMessageRole,
  type ToolStatus,
  EMPTY_PROGRESS_SNAPSHOT,
} from './shared'

// Local import for type aliasing below.
import type { ProgressSnapshot } from './shared'

/** Collapse preference persisted at localStorage key `xbot-collapse-level`. */
export type CollapseLevel = 'all' | 'minimal' | 'none'

export const COLLAPSE_LEVEL_STORAGE_KEY = 'xbot-collapse-level'
export const DEFAULT_COLLAPSE_LEVEL: CollapseLevel = 'all'
export const COLLAPSE_LEVELS: CollapseLevel[] = ['all', 'minimal', 'none']

/** Merge-tools toggle persisted at localStorage key `xbot-merge-tools`.
 *  Orthogonal to CollapseLevel — controls whether consecutive tool calls
 *  are merged into a compact row. Default: true. */
export const MERGE_TOOLS_STORAGE_KEY = 'xbot-merge-tools'
export const DEFAULT_MERGE_TOOLS = true

/** A single tool snapshot inside an iteration (agent/engine.go IterationToolSnapshot). */
export interface IterationTool {
  name: string
  label?: string
  /** 'done' | 'error' (history is always completed). */
  status: string
  elapsedMs?: number
  summary?: string
}

/** One iteration snapshot from the `detail` JSON of an assistant message. */
export interface IterationSnapshot {
  iteration: number
  thinking?: string
  reasoning?: string
  /** Wall-clock duration of this iteration (ms), from `elapsed_wall` in the JSON. */
  elapsedMs?: number
  tools: IterationTool[]
}

/** A live tool being executed (protocol/events.go ToolProgress).
 *  Kept for backward compatibility with components that accept both
 *  `IterationTool` (history) and `ToolProgress` (live) tool shapes. */
export interface ToolProgress {
  name?: string
  label?: string
  /** 'pending' | 'running' | 'done' | 'error' | 'generating'. */
  status?: string
  elapsedMs?: number
  iteration?: number
  summary?: string
  detail?: string
  args?: string
}

/** A user-facing question from the agent (protocol/events.go AskUserQuestion). */
export interface AskUserQuestion {
  question: string
  options?: string[]
}

/** An active ask-user interaction awaiting a response. */
export interface AskUserPrompt {
  requestId: string
  questions: AskUserQuestion[]
}

/** Chat message role (backward-compat alias). */
export type MessageRole = 'user' | 'assistant'

/**
 * LiveProgress is now an alias for ProgressSnapshot (Spec 3).
 * Components reading `reasoningContent` should use `reasoningStreamContent`.
 */
export type LiveProgress = ProgressSnapshot

/** Empty snapshot — the idle state (Spec 3 alias). */
export const EMPTY_LIVE_PROGRESS: LiveProgress = {
  phase: '',
  iteration: 0,
  streamContent: '',
  reasoningStreamContent: '',
  streaming: false,
  activeTools: [],
  completedTools: [],
  iterationHistory: [],
  streamingTools: [],
  lastIter: -1,
  lastReasoning: '',
  todos: [],
  subAgents: [],
}

/** Status badge kind for a tool, derived from its status string. */
export type ToolStatusKind = 'pending' | 'running' | 'done' | 'error'

export function toolStatusKind(status: string | undefined): ToolStatusKind {
  switch (status) {
    case 'done':
      return 'done'
    case 'error':
      return 'error'
    case 'running':
      return 'running'
    case 'pending':
      return 'pending'
    default:
      return 'pending'
  }
}
