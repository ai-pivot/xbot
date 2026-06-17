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
}

// SubAgentInfo represents a sub-agent's structured progress status.
type SubAgentInfo struct {
	Role     string         `json:"role"`
	Instance string         `json:"instance,omitempty"`
	Status   string         `json:"status"`
	Desc     string         `json:"desc,omitempty"`
	Children []SubAgentInfo `json:"children,omitempty"`
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
	Thinking               string            `json:"thinking,omitempty"`
	SubAgents              []SubAgentInfo    `json:"sub_agents,omitempty"`
	Todos                  []TodoItem        `json:"todos,omitempty"`
	TokenUsage             *TokenUsage       `json:"token_usage,omitempty"`
	Questions              []AskUserQuestion `json:"questions,omitempty"`
	RequestID              string            `json:"request_id,omitempty"`
	StreamContent          string            `json:"stream_content,omitempty"`
	ReasoningStreamContent string            `json:"reasoning_stream_content,omitempty"`
	// StreamingTools carries tool names detected during LLM streaming,
	// before arguments finish generating. Each entry has Status="generating".
	// This is a stream-only field (like StreamContent) — it must NOT enter
	// snapshotIterationChange or any structured snapshot path.
	StreamingTools   []ToolProgress  `json:"streaming_tools,omitempty"`
	IterationHistory []ProgressEvent `json:"iteration_history,omitempty"`
	HistoryCompacted bool            `json:"history_compacted,omitempty"`
	CWD              string          `json:"cwd,omitempty"`
}

func (ProgressEvent) EventType() string { return "progress" }
func (ProgressEvent) EventVersion() int { return 1 }

// HistoryIteration represents a completed iteration in history.
type HistoryIteration struct {
	Iteration   int            `json:"iteration"`
	Thinking    string         `json:"thinking,omitempty"`
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
	PerModelConfigs map[string]PerModelConfig `json:"per_model_configs,omitempty"`
	Active          bool                      `json:"active"`
}

// PerModelConfig stores per-model token overrides within a subscription.
type PerModelConfig struct {
	MaxOutputTokens int `json:"max_output_tokens,omitempty"` // 0 = use subscription default
	MaxContext      int `json:"max_context,omitempty"`       // 0 = use subscription default
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
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	Action   string `json:"action"`
	Label    string `json:"label,omitempty"`
	Role     string `json:"role,omitempty"`
	Instance string `json:"instance,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
}

func (SessionEvent) EventType() string { return "session" }
func (SessionEvent) EventVersion() int { return 1 }
