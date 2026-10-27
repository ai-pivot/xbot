// Package common contains the backend-neutral storage contract and the shared
// value types both backends return.
//
// Dependency direction (enforced by the package layout):
//
//	storage/common  ←  storage/sqlite
//	storage/common  ←  storage/pg
//
// common must never import a backend package; otherwise the SQLite adapter in
// storage/sqlite would create an import cycle. Backend-specific code lives in
// the two sibling packages, exactly like the old sqlite/pg split.
package common

import (
	"encoding/json"
	"errors"
	"time"

	"xbot/llm"
	"xbot/protocol"
)

// ─── Append-only history records ───

type HistoryRecordType string

const (
	HistoryRecordMessage HistoryRecordType = "message"
	// HistoryRecordCommand = command-line input/output row (`!cmd` / slash):
	// display only, never part of the LLM context.
	HistoryRecordCommand     HistoryRecordType = "command"
	HistoryRecordCompress    HistoryRecordType = "compress"
	HistoryRecordPrune       HistoryRecordType = "prune"
	HistoryRecordContextEdit HistoryRecordType = "context_edit"
	HistoryRecordAskQuestion HistoryRecordType = "ask_question"
	HistoryRecordAskAnswer   HistoryRecordType = "ask_answer"
	HistoryRecordMask        HistoryRecordType = "mask"
)

type HistoryRecord struct {
	HistoryID       int64
	Type            HistoryRecordType
	TargetHistoryID int64
	Message         llm.ChatMessage
	Data            json.RawMessage
	CreatedAt       time.Time
	CompactedBy     int64
	Compression     *CompressionRange
}

type CompressionRange struct {
	StartHistoryID   int64
	EndHistoryID     int64
	SourceHistoryIDs []int64
}

type ContextSnapshot struct {
	Version        int               `json:"version,omitempty"`
	Messages       []llm.ChatMessage `json:"messages"`
	HistoryIDs     []int64           `json:"history_ids"`
	PendingAskUser *PendingAskUser   `json:"pending_ask_user,omitempty"`
}

type MessageMutation struct {
	TargetHistoryID  int64           `json:"target_history_id"`
	TargetOccurrence int             `json:"target_occurrence,omitempty"`
	Message          llm.ChatMessage `json:"message"`
}

type MessageMutations struct {
	Mutations []MessageMutation `json:"mutations"`
}

type MaskMutation struct {
	TargetHistoryID  int64  `json:"target_history_id"`
	TargetOccurrence int    `json:"target_occurrence,omitempty"`
	Content          string `json:"content"`
}

type MaskMutations struct {
	Mutations []MaskMutation `json:"mutations"`
}

type AskQuestionRecord struct {
	Metadata      map[string]string `json:"metadata"`
	ToolHistoryID int64             `json:"tool_history_id"`
}

type AskAnswerRecord struct {
	Answer        string `json:"answer"`
	ToolHistoryID int64  `json:"tool_history_id"`
}

type PendingAskUser struct {
	HistoryID     int64             `json:"history_id"`
	ToolHistoryID int64             `json:"tool_history_id"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type ReplayResult struct {
	Messages       []llm.ChatMessage
	PendingAskUser *PendingAskUser
}

// ─── Iteration history / usage ───

// IterationRecord is one structured iteration-history row (v54+).
type IterationRecord struct {
	MessageID    int64  `json:"message_id"`
	TurnID       uint64 `json:"turn_id"`
	Iteration    int    `json:"iteration"`
	Content      string `json:"content"`
	Reasoning    string `json:"reasoning"`
	Tools        string `json:"tools"` // JSON array of tool snapshots
	Tokens       int64  `json:"tokens"`
	TTFTMs       int64  `json:"ttft_ms"`
	TokensPerSec int64  `json:"tokens_per_sec"`
	TotalMs      int64  `json:"total_ms"`
	TPOTMs       int64  `json:"tpot_ms"`
	InputTokens  int64  `json:"input_tokens"`
	CachedTokens int64  `json:"cached_tokens"`
	Model        string `json:"model"`
	// SubscriptionID is the owning subscription of the model (v62).
	SubscriptionID string `json:"subscription_id"`
	// CreatedAt is the DB insertion time, parsed to time.Time. It locates a
	// mid-turn compression relative to iteration boundaries.
	CreatedAt time.Time `json:"created_at,omitempty"`
}

type UsageModelRow struct {
	SubscriptionID string  `json:"subscription_id"`
	Model          string  `json:"model"`
	Iterations     int64   `json:"iterations"`
	Turns          int64   `json:"turns"`
	InputTokens    int64   `json:"input_tokens"`
	OutputTokens   int64   `json:"output_tokens"`
	CachedTokens   int64   `json:"cached_tokens"`
	AvgTTFTMs      float64 `json:"avg_ttft_ms"`
	AvgTPOTMs      float64 `json:"avg_tpot_ms"`
}

type UsageIterationRow struct {
	TurnID         uint64 `json:"turn_id"`
	Iteration      int    `json:"iteration"`
	InputTokens    int64  `json:"input_tokens"`
	OutputTokens   int64  `json:"output_tokens"`
	CachedTokens   int64  `json:"cached_tokens"`
	TTFTMs         int64  `json:"ttft_ms"`
	TPOTMs         int64  `json:"tpot_ms"`
	TokensPerSec   int64  `json:"tokens_per_sec"`
	TotalMs        int64  `json:"total_ms"`
	Model          string `json:"model"`
	SubscriptionID string `json:"subscription_id"`
	CreatedAt      string `json:"created_at"`
}

// TenantUsageStats aggregates a session's usage and performance.
type TenantUsageStats struct {
	IterationCount   int64   `json:"iteration_count"`
	TurnCount        int64   `json:"turn_count"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	LLMTotalMs       int64   `json:"llm_total_ms"`
	AvgTTFTMs        float64 `json:"avg_ttft_ms"`
	AvgTPOTMs        float64 `json:"avg_tpot_ms"`
	AvgTokensPerSec  float64 `json:"avg_tokens_per_sec"`
	FirstIterationAt string  `json:"first_iteration_at"`
	LastIterationAt  string  `json:"last_iteration_at"`

	LastPromptTokens     int64 `json:"last_prompt_tokens"`
	LastCompletionTokens int64 `json:"last_completion_tokens"`

	CurrentModel      string `json:"current_model"`
	SessionCreatedAt  string `json:"session_created_at"`
	SessionLastActive string `json:"session_last_active"`

	ByModel          []UsageModelRow     `json:"by_model"`
	RecentIterations []UsageIterationRow `json:"recent_iterations"`
}

// UsageBucket is one fixed-width time bucket of a session's usage.
type UsageBucket struct {
	BucketStart  int64 `json:"bucket_start"`
	InputTokens  int64 `json:"input_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	Calls        int64 `json:"calls"`
	TTFTMsSum    int64 `json:"ttft_ms_sum"`
	TotalMsSum   int64 `json:"total_ms_sum"`
}

// ─── Cron ───

type CronJob struct {
	ID           string     `json:"id"`
	Message      string     `json:"message"`
	Channel      string     `json:"channel"`
	ChatID       string     `json:"chat_id"`
	SenderID     string     `json:"sender_id,omitempty"`
	CronExpr     string     `json:"cron_expr,omitempty"`
	EverySeconds int        `json:"every_seconds,omitempty"`
	DelaySeconds int        `json:"delay_seconds,omitempty"`
	At           string     `json:"at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	NextRun      time.Time  `json:"next_run"`
	LastTrigger  *time.Time `json:"last_trigger,omitempty"`
	OneShot      bool       `json:"one_shot"`
}

// ─── LLM subscriptions ───

type PerModelConfig = protocol.PerModelConfig

type LLMSubscription struct {
	ID              string
	SenderID        string
	Name            string
	Provider        string
	BaseURL         string
	APIKey          string
	Model           string
	MaxContext      int
	MaxOutputTokens int
	ThinkingMode    string
	APIType         string
	IsDefault       bool
	Enabled         bool
	CachedModels    []string
	PerModelConfigs map[string]PerModelConfig
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// GetPerModelMaxTokens returns the per-model max_output_tokens override, or 0.
func (sub *LLMSubscription) GetPerModelMaxTokens(model string) int {
	if sub.PerModelConfigs == nil || model == "" {
		return 0
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.MaxOutputTokens
	}
	return 0
}

// GetPerModelMaxContext returns the per-model max_context override, or 0.
func (sub *LLMSubscription) GetPerModelMaxContext(model string) int {
	if sub.PerModelConfigs == nil || model == "" {
		return 0
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.MaxContext
	}
	return 0
}

// GetPerModelAPIType returns the per-model API type override, or "".
func (sub *LLMSubscription) GetPerModelAPIType(model string) string {
	if sub.PerModelConfigs == nil || model == "" {
		return ""
	}
	if cfg, ok := sub.PerModelConfigs[model]; ok {
		return cfg.APIType
	}
	return ""
}

type SubscriptionModel struct {
	ID              string
	SubscriptionID  string
	Model           string
	MaxContext      int
	MaxOutputTokens int
	ThinkingMode    string
	APIType         string
	Enabled         bool
	Vision          bool
	VisionDetail    string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type UserDefaultModel struct {
	SenderID       string
	SubscriptionID string
	Model          string
	UpdatedAt      time.Time
}

// ─── Tenants / chats ───

type UserChatWithPreview struct {
	ChatID     string    `json:"chat_id"`
	Label      string    `json:"label"`
	LastActive time.Time `json:"last_active"`
	Preview    string    `json:"preview"`
	IsCurrent  bool      `json:"is_current"`
	CreatedAt  time.Time `json:"created_at"`
	SortOrder  int       `json:"sort_order"`
}

type TenantInfo struct {
	ID             int64
	Channel        string
	ChatID         string
	Label          string `json:"label,omitempty"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	Model          string `json:"model,omitempty"`
	CreatedAt      time.Time
	LastActiveAt   time.Time
	// Preview is the last user/assistant message preview maintained by the
	// write paths (tenants.preview).
	Preview string `json:"preview,omitempty"`
	// SortOrder is the user-defined sidebar order from user_chats (0 = unset).
	SortOrder int `json:"sort_order,omitempty"`
}

type TenantDBInfo struct {
	DBPath   string
	Migrated bool
}

type PendingResume struct {
	Channel   string
	ChatID    string
	SenderID  string
	CreatedAt string
}

// ─── Shared artifacts ───

type SharedArtifact struct {
	Token       string `json:"token"`
	PluginID    string `json:"plugin_id"`
	ContentType string `json:"content_type"`
	Payload     string `json:"payload"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   int64  `json:"created_by"`
	ExpiresAt   string `json:"expires_at"`
	RevokedAt   string `json:"revoked_at"`
}

// ─── Token usage ───

type UserTokenUsage struct {
	SenderID          string `json:"sender_id"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

type DailyTokenUsage struct {
	Date              string `json:"date"`
	SenderID          string `json:"sender_id"`
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

// ─── Interactive SubAgent registry ───

type InteractiveAgentRecord struct {
	AgentKey      string
	ParentChannel string
	ParentChatID  string
	RoleName      string
	Instance      string
	GroupID       string
	Task          string
	Background    bool
	Status        string
	TurnID        uint64
	ModelName     string
	SubID         string
	OriginUserID  string
	WorkspaceRoot string
	ParentAgentID string
	LastError     string
	LastReply     string
	LastReplyAt   string
	CreatedAt     string
	UpdatedAt     string
}

const (
	InteractiveAgentIdle        = "idle"
	InteractiveAgentRunning     = "running"
	InteractiveAgentInterrupted = "interrupted"
	InteractiveAgentUnloaded    = "unloaded"
)

var ErrInteractiveAgentNotFound = errors.New("interactive agent not found")

type AgentActivity struct {
	AgentKey    string `json:"agent_key"`
	RoleName    string `json:"role_name"`
	Instance    string `json:"instance"`
	Status      string `json:"status"`
	UpdatedAt   string `json:"updated_at"`
	LastReplyAt string `json:"last_reply_at"`
}
