/**
 * Shared domain types (Spec 1 设计系统基础).
 *
 * Pure data types consumed across specs. Stateful interfaces (WSConnection,
 * TabManager, SessionStore) are defined in Spec 2; keep them out of here.
 */

export type Theme = 'dark' | 'light'
export type Locale = 'zh-CN' | 'en'
export type TabType = 'agent' | 'file' | 'terminal' | 'background'
export type SessionStatus = 'running' | 'waiting_input' | 'pending' | 'idle' | 'unread' | 'error'
export type SessionCategory = 'time' | 'status' | 'path'

/**
 * How Agent intermediate steps (tool calls / reasoning) are shown.
 * Spec 7 §3.4 — persisted to localStorage under COLLAPSE_LEVEL_STORAGE_KEY.
 */
export type CollapseLevel = 'all' | 'minimal' | 'none'

/** localStorage keys for cross-spec UI preferences. */
export const COLLAPSE_LEVEL_STORAGE_KEY = 'xbot-collapse-level'

export interface Tab {
  id: string
  type: TabType
  title: string
  icon?: string
  closable: boolean
  data?: TabData
}

export interface TabData {
  filePath?: string
  content?: string
  language?: string
  previewMode?: boolean
  /** Frontend terminal id (TerminalSession.id) for terminal tabs. */
  terminalId?: string
  /** SubAgent role (for agent tabs viewing a SubAgent conversation). */
  subAgentRole?: string
  /** SubAgent instance (for agent tabs viewing a SubAgent conversation). */
  subAgentInstance?: string
  /** Parent chatID for SubAgent tabs. */
  parentChatID?: string
  /** Parent channel for SubAgent tabs. */
  parentChannel?: string
  /** Full persisted agent tenant chatID for historical SubAgent tabs. */
  agentChatID?: string
  /** Background task id for background-output tabs. */
  taskID?: string
  /** Background task command for tab title/content. */
  command?: string
  /** Session channel for background task RPCs. */
  taskChannel?: string
  /** Session chatID for background task RPCs. */
  taskChatID?: string
}

export interface SessionInfo {
  chatID: string
  channel: string
  label: string
  /** Current persisted working directory returned by the session API. */
  workDir?: string
  lastActive: string
  preview: string
  status: SessionStatus
  isCurrent: boolean
  /** Session type: "main" for user↔agent, "agent" for SubAgent↔agent. */
  type?: 'main' | 'agent'
  /** TUI-compatible full interactive SubAgent key: <parent-channel>:<parent-chat-id>/<role>[:instance]. */
  fullKey?: string
  /** SubAgent role name (only for type === 'agent'). */
  role?: string
  /** SubAgent instance ID (only for type === 'agent'). */
  instance?: string
  /** Parent session chatID (only for type === 'agent'; links SubAgent to its parent). */
  parentChatID?: string
  /** Parent session channel (only for type === 'agent'). */
  parentChannel?: string
  /** Number of active terminals for this session (from terminal store). */
  terminalCount?: number
  /** True when the SubAgent is currently running. */
  running?: boolean
  /** True when the SubAgent row comes from a persisted agent tenant, not live interactive memory. */
  historical?: boolean
  /** Full persisted agent tenant chatID for historical SubAgent rows. */
  agentChatID?: string
  /** True for Web-only parent rows synthesized from SubAgent tenants. */
  synthetic?: boolean
  /** Backend-attached SubAgent children. Web renders this tree directly. */
  children?: SessionInfo[]
}

export interface SessionSelector {
  channel: string
  chatID: string
}

/** Authoritative context-usage snapshot returned by get_context_usage. */
export interface ContextUsage {
  available: boolean
  prompt_tokens: number
  completion_tokens: number
  max_context_tokens: number
  usage_percent: number | null
  model: string
  subscription_id: string
  subscription_name: string
}

/* ---------------------------------------------------------------------------
 * Realtime event envelopes shared by SSE and the Go transport protocol.
 * Added in Spec 2 (布局壳 + Dockview); these are pure data shapes shared by
 * the WS connection layer (useWSConnection) and consumers.
 * ------------------------------------------------------------------------- */

/** Server → Web SSE event types. */
export type WSMessageType =
  | 'text'
  | 'progress_structured'
  | 'stream_content'
  | 'rpc_response'
  | 'ask_user'
  | 'session'
  | 'user_echo'
  | 'inject_user'
  | 'card'
  | 'plugin_widgets'
  | 'runner_status'
  | 'sync_progress'
  | 'genui'
  | 'resync_required'
  | 'history_gap'
  | '__pong__'

/** Client operations mapped to REST endpoints by the connection adapter. */
export type WSClientMessageType =
  | 'message'
  | 'cancel'
  | 'rpc'
  | 'subscribe'
  | 'sync'
  | 'ask_user_response'
  | 'tui_control_resp'

/** Generic server → client envelope. Fields are optional because different
 *  message types populate different subsets. */
export interface WSMessage {
  type: WSMessageType | string
  id?: string
  seq?: number
  content?: string
  original_content?: string
  ts?: number
  progress?: ProgressEvent | null
  progress_history?: string
  channel?: string
  chat_id?: string
  sender_id?: string
  sender_name?: string
  chat_type?: string
  session_reset?: boolean
  metadata?: Record<string, string>
  result?: unknown
  error?: string
  session?: SessionEvent | null
}

/** Client → server envelope. */
export interface WSClientMessage {
  type: WSClientMessageType
  content?: string
  file_ids?: string[]
  file_names?: string[]
  file_sizes?: number[]
  upload_keys?: string[]
  file_mimes?: string[]
  channel?: string
  chat_id?: string
  sender_id?: string
  sender_name?: string
  chat_type?: string
  id?: string
  method?: string
  params?: unknown
  /** ask_user_response payload: answers keyed by question index. */
  answers?: Record<string, string>
  /** ask_user_response: true to cancel the prompt. */
  cancelled?: boolean
  /** sync: last event seq the client has processed (from history API last_seq).
   *  Omitted or 0 = full replay (backward compatible). */
  last_seq?: number
}

/** Progress event (mirrors Go protocol/events.go ProgressEvent). */
export interface ProgressEvent {
  iteration?: number
  content?: string
  reasoning?: string
  tool_calls?: unknown[]
  elapsed_wall?: number
  chat_id?: string
  seq?: number
  phase?: string
  thinking?: string
  stream_content?: string
  /** Streaming HTML from display_html tool arguments (stream-only, like stream_content). */
  genui_content?: string
  cwd?: string
  // Extended fields present in the backend payload (events.go ActiveTools /
  // CompletedTools / IterationHistory / ReasoningStreamContent / Questions /
  // RequestID). Typed as unknown[] / unknown so consumers normalize them.
  active_tools?: unknown[]
  completed_tools?: unknown[]
  iteration_history?: unknown[]
  reasoning_stream_content?: string
  token_usage?: {
    prompt_tokens?: number
    completion_tokens?: number
    total_tokens?: number
    cache_hit_tokens?: number
    max_output_tokens?: number
  }
  questions?: unknown[]
  request_id?: string
  /** Tools detected during LLM streaming (status="generating"), before
   *  arguments finish generating. Sent via stream_content events. */
  streaming_tools?: unknown[]
  stream_tokens?: number
  /** Tool hints from plugins (PostToolUse hook). */
  tool_hints?: string
  /** TODO list from TodoWrite tool (mirrors Go protocol.ProgressEvent.Todos). */
  todos?: TodoItem[]
  /** Structured SubAgent progress tree (mirrors Go protocol.SubAgentInfo). */
  sub_agents?: unknown[]
  [key: string]: unknown
}

/** Session event (mirrors Go protocol/events.go SessionEvent). */
export interface SessionEvent {
  channel?: string
  chat_id?: string
  session_key?: string
  action?: string
  target_history_id?: number
  label?: string
  role?: string
  instance?: string
  parent_id?: string
}

/* ---------------------------------------------------------------------------
 * Streaming data model (Spec 3 — 流式数据模型与 Store 重写).
 *
 * These types are the shared contract for Spec 4 (Agent workspace) and
 * Spec 5 (history / persistence). ProgressStore owns a ProgressSnapshot;
 * useProgressStream derives a live ChatMessage from it; useChatMessages
 * owns the committed ChatMessage[] list.
 * ------------------------------------------------------------------------- */

/** Tool call progress status. */
export type ToolStatus = 'pending' | 'generating' | 'running' | 'done' | 'error'

/** TODO item — mirrors Go protocol.TodoItem (json: id, text, done). */
export interface TodoItem {
  id: number
  text: string
  done: boolean
}

/** SubAgent progress node — mirrors Go protocol.SubAgentInfo. */
export interface WebSubAgentProgress {
  role: string
  instance?: string
  sessionKey?: string
  status: string
  desc?: string
  children?: WebSubAgentProgress[]
}

/** Tool call progress — normalized from WS progress events or history. */
export interface WebToolProgress {
  name: string
  label: string
  status: ToolStatus
  elapsedMs: number
  summary: string
  detail: string
  args: string
  toolHints: string
  /** Iteration number this tool belongs to (from server). Used to filter
   * cross-iteration tool pollution — completedTools should only contain
   * tools from the CURRENT iteration, not all iterations. */
  iteration?: number
}

/** Iteration snapshot — one completed iteration's reasoning + tools. */
export interface WebIteration {
  iteration: number
  thinking: string
  reasoning: string
  tools: WebToolProgress[]
  toolCount: number
  /** Wall-clock duration (ms), optional — not always available from snapshots. */
  elapsedMs?: number
}

/**
 * ProgressStore snapshot — the complete live state of an in-flight agent turn.
 *
 * Stream-only fields (`streamContent`, `reasoningStreamContent`, `streamingTools`)
 * are accumulated by stream_content events and preserved (carry-forward) when
 * structured events arrive. Structured fields (`phase`, `iteration`, `activeTools`,
 * `completedTools`) are replaced by progress_structured events.
 */
export interface ProgressSnapshot {
  /** Monotonic semantic progress-log ID from protocol.ProgressEvent.Seq. */
  eventSeq: number
  phase: string
  iteration: number
  streamContent: string
  reasoningStreamContent: string
  /** Structured content from progress_structured events — fallback for
   * text output when streamContent is empty (server may send text via
   * structured events instead of stream_content). */
  content: string
  streaming: boolean
  activeTools: WebToolProgress[]
  completedTools: WebToolProgress[]
  iterationHistory: WebIteration[]
  streamingTools: WebToolProgress[]
  /** Streaming HTML from display_html tool (stream-only, like streamContent). */
  genuiContent: string
  lastIter: number
  lastReasoning: string
  /** TODO list (from TodoWrite tool, carried forward when not present in events). */
  todos: TodoItem[]
  /** Structured SubAgent progress tree, carried forward while active. */
  subAgents: WebSubAgentProgress[]
  /** Token usage from the last LLM API response (mirrors protocol.TokenUsage). */
  tokenUsage: TokenUsageInfo | null
}

/** Token usage info (mirrors protocol.TokenUsage). */
export interface TokenUsageInfo {
  promptTokens: number
  completionTokens: number
  totalTokens: number
}

/** Empty snapshot — the idle state. */
export const EMPTY_PROGRESS_SNAPSHOT: ProgressSnapshot = {
  eventSeq: 0,
  phase: '',
  iteration: 0,
  streamContent: '',
  reasoningStreamContent: '',
  content: '',
  streaming: false,
  activeTools: [],
  completedTools: [],
  iterationHistory: [],
  streamingTools: [],
  genuiContent: '',
  lastIter: -1,
  lastReasoning: '',
  todos: [],
  subAgents: [],
  tokenUsage: null,
}

/** Chat message role. */
export type ChatMessageRole = 'user' | 'assistant' | 'system' | 'tool'

/**
 * Committed chat message — the shape all rendering components consume.
 * `assistant` messages carry `iterations` (parsed from history `detail` JSON).
 * Live streaming messages use `isPartial: true` and `turnID: 0`.
 */
export interface ChatMessage {
  id: string
  historyID?: number
  recordType?: string
  compactedBy?: number
  compression?: {
    startHistoryID?: number
    endHistoryID?: number
    sourceHistoryIDs?: number[]
  }
  role: ChatMessageRole
  content: string
  reasoningContent?: string
  toolCallID?: string
  toolName?: string
  toolArguments?: string
  toolCalls?: { id: string; name: string; arguments: string }[]
  iterations: WebIteration[]
  timestamp: string
  isPartial: boolean
  turnID: number
  displayOnly?: boolean
  /** True when loaded from persisted backend history, not an optimistic echo. */
  persisted?: boolean
  /** SSE sequence for live committed rows, used to reconcile them with history. */
  eventSeq?: number
  /** Stable logical-send ID used to correlate optimistic rows with echoes. */
  requestID?: string
}

/* ---------------------------------------------------------------------------
 * LLM Subscription & Model Management types (Spec D — LLM 配置设计).
 *
 * These mirror the Go protocol.Subscription / protocol.PerModelConfig /
 * protocol.ModelEntry structs (serverapp/rpc_table.go + protocol/events.go).
 * JSON field names match the backend serialization exactly.
 * ------------------------------------------------------------------------- */

/** Per-model override config (mirrors protocol.PerModelConfig). */
export interface PerModelConfig {
  max_output_tokens: number
  max_context: number
  api_type: string
  enabled: boolean
}

/** LLM subscription (mirrors protocol.Subscription JSON serialization). */
export interface Subscription {
  id: string
  name: string
  provider: string
  base_url: string
  api_key: string
  model: string
  max_output_tokens: number
  max_context: number
  api_type: string
  thinking_mode: string
  per_model_configs: Record<string, PerModelConfig>
  active: boolean
  enabled: boolean
  is_system: boolean
}

/** Selectable model paired with its subscription (mirrors protocol.ModelEntry). */
export interface ModelEntry {
  sub_id: string
  sub_name: string
  model: string
  status: 'normal' | 'offline' | 'disabled'
}
