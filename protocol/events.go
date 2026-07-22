package protocol

import "time"

// ToolCallSnapshot is a per-tool-call snapshot in progress events.
type ToolCallSnapshot struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Args    string `json:"args"`
	Status  string `json:"status"`
	Elapsed int64  `json:"elapsed"`
}

// TodoItem represents a TODO item for CLI display.
type TodoItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// ToolProgress represents a single tool's execution progress.
type ToolProgress struct {
	Name      string    `json:"name,omitempty"`
	Label     string    `json:"label,omitempty"`
	Status    string    `json:"status,omitempty"`
	Elapsed   int64     `json:"elapsed_ms,omitempty"`
	Iteration int       `json:"iteration,omitempty"`
	Summary   string    `json:"summary,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	Args      string    `json:"args,omitempty"`
	ToolHints string    `json:"tool_hints,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	// GenChars is the accumulated argument character count for generating tools
	// (Status="generating"). Populated from streaming tool call deltas — shows
	// real-time progress of argument generation (e.g. "42 chars").
	GenChars int `json:"gen_chars,omitempty"`
}

// SubAgentInfo represents a sub-agent's structured progress status.
type SubAgentInfo struct {
	Role       string         `json:"role"`
	Instance   string         `json:"instance,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	Status     string         `json:"status"`
	Desc       string         `json:"desc,omitempty"`
	Children   []SubAgentInfo `json:"children,omitempty"`
}

// TokenUsage represents a token usage snapshot.
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CacheHitTokens   int64 `json:"cache_hit_tokens,omitempty"`
	MaxOutputTokens  int64 `json:"max_output_tokens,omitempty"`
}

// AskUserQuestion represents a single question in the AskUser flow.
type AskUserQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// ProgressEvent is the comprehensive structured progress payload.
// It serves both as a per-iteration event and as the full progress snapshot.
// This is the single source of truth for all progress data across local CLI,
// remote CLI (WebSocket), and Web channel — no separate WsProgressPayload needed.
type ProgressEvent struct {
	// Basic iteration info
	Iteration   int                `json:"iteration"`
	Content     string             `json:"content,omitempty"`
	Reasoning   string             `json:"reasoning,omitempty"`
	ToolCalls   []ToolCallSnapshot `json:"tool_calls,omitempty"`
	ElapsedWall int64              `json:"elapsed_wall"`

	// Extended fields
	ChatID                 string            `json:"chat_id,omitempty"`
	Seq                    uint64            `json:"seq,omitempty"`
	Phase                  string            `json:"phase,omitempty"`
	ActiveTools            []ToolProgress    `json:"active_tools,omitempty"`
	CompletedTools         []ToolProgress    `json:"completed_tools,omitempty"`
	SubAgents              []SubAgentInfo    `json:"sub_agents,omitempty"`
	Todos                  []TodoItem        `json:"todos"`
	TokenUsage             *TokenUsage       `json:"token_usage,omitempty"`
	Questions              []AskUserQuestion `json:"questions,omitempty"`
	RequestID              string            `json:"request_id,omitempty"`
	StreamContent          string            `json:"stream_content,omitempty"`
	ReasoningStreamContent string            `json:"reasoning_stream_content,omitempty"`
	// GenUIContent carries streaming HTML from display_html tool arguments.
	// Stream-only field like StreamContent — must NOT enter structured snapshots.
	GenUIContent string `json:"genui_content,omitempty"`
	// StreamingTools carries tool names detected during LLM streaming,
	// before arguments finish generating. Each entry has Status="generating".
	// This is a stream-only field (like StreamContent) — it must NOT enter
	// snapshotIterationChange or any structured snapshot path.
	StreamingTools []ToolProgress `json:"streaming_tools,omitempty"`
	// StreamTokens carries incremental completion token count during LLM streaming.
	// Populated from Anthropic's message_delta usage events (OpenAI/DeepSeek only
	// provide usage at stream end). When >0, TUI displays token count instead of
	// char count for streaming progress.
	StreamTokens     int64           `json:"stream_tokens,omitempty"`
	IterationHistory []ProgressEvent `json:"iteration_history,omitempty"`
	HistoryCompacted bool            `json:"history_compacted,omitempty"`
	CWD              string          `json:"cwd,omitempty"`
}

func (ProgressEvent) EventType() string { return "progress" }
func (ProgressEvent) EventVersion() int { return 1 }

// HistoryIteration represents a completed iteration in history.
type HistoryIteration struct {
	Iteration   int            `json:"iteration"`
	Content     string         `json:"content,omitempty"`
	Reasoning   string         `json:"reasoning,omitempty"`
	Tools       []ToolProgress `json:"tools,omitempty"`
	ElapsedWall int64          `json:"elapsed_wall"`
}

// HistoryMessage represents a message in session history.
type HistoryMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content"`
	Timestamp  time.Time          `json:"timestamp"`
	Iterations []HistoryIteration `json:"iterations,omitempty"`
}

// Subscription represents an LLM subscription for display/selection.
type Subscription struct {
	ID              string                    `json:"id"`
	Name            string                    `json:"name"`
	Provider        string                    `json:"provider"`
	BaseURL         string                    `json:"base_url"`
	APIKey          string                    `json:"api_key"`
	Model           string                    `json:"model"`
	MaxOutputTokens int                       `json:"max_output_tokens,omitempty"`
	MaxContext      int                       `json:"max_context,omitempty"` // subscription-level max context (0 = use default)
	ThinkingMode    string                    `json:"thinking_mode,omitempty"`
	APIType         string                    `json:"api_type,omitempty"` // "chat_completions" (default) | "responses"
	PerModelConfigs map[string]PerModelConfig `json:"per_model_configs,omitempty"`
	Active          bool                      `json:"active"`
	// Enabled is the subscription-level enabled flag (v40). A disabled subscription
	// stops contributing models to the picker; credentials are preserved. Populated
	// from user_llm_subscriptions.enabled by listSubscriptions/mergeSubscriptionModels.
	Enabled bool `json:"enabled,omitempty"`
	// IsSystem marks the shared system subscription (v44): reconciled from
	// config/env at boot, read-only, and the lowest-priority default/fallback.
	// The UI uses this to render a lock badge and disable edit/disable/delete.
	IsSystem bool `json:"is_system,omitempty"`
}

// PerModelConfig stores per-model token overrides within a subscription.
type PerModelConfig struct {
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"` // 0 = use subscription default
	MaxContext      int    `json:"max_context,omitempty"`       // 0 = use subscription default
	APIType         string `json:"api_type,omitempty"`          // "" = use subscription default
	// Enabled is a read-side projection of subscription_models.enabled, populated by
	// mergeSubscriptionModels so the UI can show/toggle per-model enabled state. It is
	// NOT authoritative on writes — enabled is managed by the set_model_enabled RPC.
	Enabled bool `json:"enabled,omitempty"`
}

// ContextUsage is the authoritative context snapshot for one session.
// UsagePercent is nil until the model API has returned an exact prompt token
// count. It is intentionally not capped at 100.
type ContextUsage struct {
	Available        bool     `json:"available"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	MaxContextTokens int      `json:"max_context_tokens"`
	UsagePercent     *float64 `json:"usage_percent"`
	Model            string   `json:"model"`
	SubscriptionID   string   `json:"subscription_id"`
	SubscriptionName string   `json:"subscription_name"`
}

// ModelEntry is a selectable model paired with the subscription that provides it.
// Used by the model picker (ListAllModelEntries) so the UI can show "订阅名 · 模型名"
// and disambiguate models served by different subscriptions. System-default models
// (not owned by any user subscription) carry empty SubID/SubName.
//
// The list is DB-driven: it unions sub.CachedModels (fetched) + sub.Model +
// subscription_models rows (manually added / param overrides). Status reflects
// per-(SubID,Model) availability:
//   - "normal": present in CachedModels (or it's sub.Model) and enabled → fetched & usable
//   - "offline": has a subscription_models record but NOT fetched, and enabled →
//     manually added; still selectable (anything not disabled is selectable)
//   - "disabled": subscription_models.enabled=0 → rendered greyed, not selectable;
//     press ctrl+e to re-enable
type ModelEntry struct {
	SubID   string `json:"sub_id,omitempty"`
	SubName string `json:"sub_name,omitempty"`
	Model   string `json:"model"`
	Status  string `json:"status"` // normal | offline | disabled
}

type OutboundEvent struct {
	Channel   string            `json:"channel"`
	ChatID    string            `json:"chat_id"`
	Content   string            `json:"content"`
	IsPartial bool              `json:"is_partial"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func (OutboundEvent) EventType() string { return "outbound" }
func (OutboundEvent) EventVersion() int { return 1 }

type InjectUserEvent struct {
	ChatID  string `json:"chat_id"`
	Content string `json:"content"`
}

func (InjectUserEvent) EventType() string { return "inject_user" }
func (InjectUserEvent) EventVersion() int { return 1 }

type ConnStateEvent struct {
	State string `json:"state"`
}

func (ConnStateEvent) EventType() string { return "conn_state" }
func (ConnStateEvent) EventVersion() int { return 1 }

type ReconnectEvent struct{}

func (ReconnectEvent) EventType() string { return "reconnect" }
func (ReconnectEvent) EventVersion() int { return 1 }

type PluginWidgetEvent struct {
	ChatID string            `json:"chat_id"`
	Zones  map[string]string `json:"zones"`
}

func (PluginWidgetEvent) EventType() string { return "plugin_widget" }
func (PluginWidgetEvent) EventVersion() int { return 1 }

type TUIControlEvent struct {
	Action  string                                    `json:"action"`
	Params  map[string]string                         `json:"params"`
	Respond func(result map[string]string, err error) `json:"-"`
}

func (TUIControlEvent) EventType() string { return "tui_control" }
func (TUIControlEvent) EventVersion() int { return 1 }

type AskUserEvent struct {
	Channel   string `json:"channel"`
	ChatID    string `json:"chat_id"`
	Questions string `json:"questions"`
	RequestID string `json:"request_id,omitempty"`
}

func (AskUserEvent) EventType() string { return "ask_user" }
func (AskUserEvent) EventVersion() int { return 1 }

// SessionEvent represents a session state change pushed from server to client.
// Covers busy/idle transitions, session lifecycle (create/delete/rename),
// and SubAgent lifecycle (started/stopped).
// Action values: "busy", "idle", "created", "deleted", "renamed",
// "subagent_started", "subagent_stopped".
type SessionEvent struct {
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	Action     string `json:"action"`
	Label      string `json:"label,omitempty"`
	Role       string `json:"role,omitempty"`
	Instance   string `json:"instance,omitempty"`
	SessionKey string `json:"session_key,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
}

func (SessionEvent) EventType() string { return "session" }
func (SessionEvent) EventVersion() int { return 1 }
