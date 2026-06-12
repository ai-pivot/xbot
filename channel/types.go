package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// Shared message types (used by Channel interface, AgentChannel, CLI, Web)
// ---------------------------------------------------------------------------

// InboundMsg represents a user message from CLI to server.
// This is the CLI-local equivalent of bus.InboundMessage, containing only
// the fields needed by the CLI channel.
type InboundMsg struct {
	Channel    string            `json:"channel"`
	ChatID     string            `json:"chat_id"`
	Content    string            `json:"content"`
	SenderID   string            `json:"sender_id"`
	SenderName string            `json:"sender_name"`
	ChatType   string            `json:"chat_type"`
	RequestID  string            `json:"request_id"`
	Media      []string          `json:"media,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// OutboundMsg represents a server response to CLI.
// This is the equivalent of bus.OutboundMessage for the Channel interface, containing only
// the fields needed by the CLI channel for display.
type OutboundMsg struct {
	Channel     string            `json:"channel"`
	ChatID      string            `json:"chat_id"`
	Content     string            `json:"content"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	WaitingUser bool              `json:"waiting_user"`
	IsPartial   bool              `json:"is_partial"`
	ToolsUsed   []string          `json:"tools_used,omitempty"`
	Media       []string          `json:"media,omitempty"`
	Error       error             `json:"-"`

	// Ctx carries the caller's context for cancellation propagation.
	// Used by AgentChannel.Send to respect caller cancellation (e.g. Ctrl+C).
	// Ignored by other Channel implementations. Not serialized.
	Ctx context.Context `json:"-"`
}

// ---------------------------------------------------------------------------
// Shared background task types (used by CLI, agent)
// ---------------------------------------------------------------------------

// BgTaskStatus represents the status of a background task.
type BgTaskStatus string

const (
	BgTaskRunning BgTaskStatus = "running"
	BgTaskDone    BgTaskStatus = "done"
	BgTaskError   BgTaskStatus = "error"
	BgTaskKilled  BgTaskStatus = "killed"
)

// BgTask represents a background task for CLI display.
// This is the CLI-local equivalent of tools.BackgroundTask, containing only
// the fields needed for task panel rendering.
type BgTask struct {
	ID         string       `json:"id"`
	Command    string       `json:"command"`
	Status     BgTaskStatus `json:"status"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Output     string       `json:"output"`
	ExitCode   int          `json:"exit_code"`
	Error      string       `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Shared metadata constants
// ---------------------------------------------------------------------------

const (
	// MetadataReplyPolicy controls how Agent should behave before final reply.
	MetadataReplyPolicy = "reply_policy"

	ReplyPolicyOptional = "optional"
)

// ---------------------------------------------------------------------------
// Shared token usage types (used by CLI, Web)
// ---------------------------------------------------------------------------

// UserTokenUsage represents a user's cumulative token usage.
// Mirror of sqlite.UserTokenUsage — used in CLIChannelConfig.UsageQuery callback
// so that cmd/xbot-cli does not need to import the sqlite package.
type UserTokenUsage struct {
	SenderID          string `json:"sender_id"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

// DailyTokenUsage represents token usage for a specific day+model.
// Mirror of sqlite.DailyTokenUsage — used in CLIChannelConfig.UsageQuery callback
// so that cmd/xbot-cli does not need to import the sqlite package.
type DailyTokenUsage struct {
	Date              string `json:"date"` // YYYY-MM-DD
	SenderID          string `json:"sender_id"`
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

// ---------------------------------------------------------------------------
// Shared panel types (used by CLI and agent)
// ---------------------------------------------------------------------------

// AgentPanelEntry represents an interactive agent in the Agent panel.
type AgentPanelEntry struct {
	Role         string
	Instance     string
	Running      bool
	Background   bool
	Task         string // one-shot subagent task (empty for interactive)
	Preview      string // latest progress/last reply summary for panel display
	ParentChatID string // parent session chatID (for session isolation filtering)
}

// SessionChatMessage represents a message in a chat session (used by CLI and Web).
type SessionChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SessionPanelEntry represents a session item in the Sessions panel.
type SessionPanelEntry struct {
	ID          string // chatID or "agent:role/instance"
	Type        string // "main" = main chatroom, "agent" = SubAgent session
	Channel     string // channel name (e.g. "cli", "web") for history loading
	Label       string // display label
	Role        string // agent role (for agent type)
	Instance    string // agent instance (for agent type)
	ParentID    string // parent chatID (for agent type)
	Running     bool   // true = currently active
	Active      bool   // true = currently selected (main session only)
	Busy        bool   // true = session is processing (agent thinking/tool_exec, etc.)
	MessageHint string // preview of last message
}

// ---------------------------------------------------------------------------
// Shared subscription types (used by CLI, Web, agent)
// ---------------------------------------------------------------------------

// Subscription represents a LLM subscription for display/selection.
type Subscription = protocol.Subscription

// PerModelConfig stores per-model token overrides within a subscription.
type PerModelConfig = protocol.PerModelConfig

// SubscriptionManager manages user LLM subscriptions.
type SubscriptionManager interface {
	List(senderID string) ([]Subscription, error)
	GetDefault(senderID string) (*Subscription, error)
	Add(sub *Subscription) error
	Remove(id string) error
	SetDefault(id, chatID string) error
	SetModel(id, model string) error
	Rename(id, name string) error
	Update(id string, sub *Subscription) error
	UpdatePerModelConfig(id, model string, pmc PerModelConfig) error
	// GetSessionSubscription queries the backend for the session→subscription mapping.
	// Returns empty strings if no mapping exists (server restart, first-time session, etc.).
	GetSessionSubscription(senderID, chatID string) (subscriptionID, model string, err error)
}

// LLMSubscriber switches the active LLM for a user (called when subscription changes).
type LLMSubscriber interface {
	SwitchSubscription(senderID string, sub *Subscription, chatID string) error
	SwitchModel(senderID, model, chatID string)
	GetDefaultModel() string
}

// ---------------------------------------------------------------------------
// Shared history types (used by CLI, Web, agent)
// ---------------------------------------------------------------------------

// HistoryIteration 历史迭代快照（用于会话恢复的 tool_summary 渲染）
type HistoryIteration = protocol.HistoryIteration

// HistoryMessage 历史消息（用于会话恢复）
type HistoryMessage = protocol.HistoryMessage

// IterSnapshot mirrors agent.IterationSnapshot for JSON unmarshaling Detail field.
type IterSnapshot struct {
	Iteration int            `json:"iteration"`
	Thinking  string         `json:"thinking,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	Tools     []IterToolSnap `json:"tools"`
}

type IterToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Summary   string `json:"summary,omitempty"`
}

// formatToolLabel generates a short human-readable label from a tool name and its JSON arguments.
// Used when restoring progress from intermediate assistant messages (no Detail snapshot),
// e.g. after server restart. Produces labels like "Shell(tail -100 file.log)" or "Read(path)".
func formatToolLabel(name, argsJSON string) string {
	const maxLen = 60
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return name
	}

	get := func(key string) string {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	switch name {
	case "Shell":
		cmd := get("command")
		if cmd != "" {
			if len(cmd) > maxLen-len(name)-2 {
				cmd = cmd[:maxLen-len(name)-5] + "..."
			}
			return name + "(" + cmd + ")"
		}
	case "Read":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Grep":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Glob":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Write", "FileCreate":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Edit", "FileReplace":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "WebSearch":
		q := get("query")
		if q != "" {
			return name + "(" + q + ")"
		}
	case "SubAgent":
		r := get("role")
		t := get("task")
		if r != "" {
			if t != "" && len(t) > 30 {
				t = t[:27] + "..."
			}
			if t != "" {
				return name + "(" + r + ": " + t + ")"
			}
			return name + "(" + r + ")"
		}
	default:
		// Generic: show first string parameter
		for _, v := range args {
			if s, ok := v.(string); ok && s != "" {
				if len(s) > maxLen-len(name)-2 {
					s = s[:maxLen-len(name)-5] + "..."
				}
				return name + "(" + s + ")"
			}
		}
	}
	return name
}

// ConvertMessagesToHistory converts raw DB messages into HistoryMessages for CLI display.
// It handles three scenarios:
//  1. Normal completed turn: assistant with Detail → one tool_summary + assistant
//  2. Cancelled/interrupted turn: intermediate assistant(ToolCalls) without Detail → pending tool_summary
//  3. Mixed: some turns completed, last one cancelled
func ConvertMessagesToHistory(msgs []llm.ChatMessage) []HistoryMessage {
	var history []HistoryMessage
	var pendingIters []HistoryIteration
	var curIterTools []protocol.ToolProgress
	var curIterIdx int
	var curIterThinking string
	var curIterReasoning string

	finishCurIter := func() {
		if len(curIterTools) > 0 || curIterThinking != "" || curIterReasoning != "" {
			pendingIters = append(pendingIters, HistoryIteration{
				Iteration: curIterIdx,
				Thinking:  curIterThinking,
				Reasoning: curIterReasoning,
				Tools:     curIterTools,
			})
		}
		curIterTools = nil
		curIterThinking = ""
		curIterReasoning = ""
	}

	// lastAssistantTS tracks the timestamp of the last processed assistant
	// message, used to assign a unique Timestamp to flushPending()-generated
	// tool_summary messages. Without this, multiple interrupted turns produce
	// tool_summary messages with zero timestamps, causing dedup to drop all
	// but the first.
	var lastAssistantTS time.Time
	// syntheticIdx provides monotonically-increasing nanosecond offsets to
	// guarantee unique timestamps for consecutive flushPending() calls when
	// no real assistant timestamp is available (e.g. all turns interrupted).
	var syntheticIdx int

	flushPending := func() {
		finishCurIter()
		if len(pendingIters) > 0 {
			ts := lastAssistantTS
			if ts.IsZero() {
				// No assistant message in this turn — assign a synthetic
				// timestamp so each assistant message gets a unique dedup key.
				ts = time.Date(2024, 1, 1, 0, 0, 0, syntheticIdx, time.UTC)
				syntheticIdx++
			}
			history = append(history, HistoryMessage{
				Role:       "assistant",
				Content:    "",
				Timestamp:  ts,
				Iterations: pendingIters,
			})
			pendingIters = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			lastAssistantTS = m.Timestamp
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()
				pendingIters = nil

				var snaps []IterSnapshot
				if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
					iters := make([]HistoryIteration, 0, len(snaps))
					for _, snap := range snaps {
						toolList := make([]protocol.ToolProgress, len(snap.Tools))
						for i, t := range snap.Tools {
							label := t.Label
							if label == "" {
								label = t.Name
							}
							toolList[i] = protocol.ToolProgress{
								Name:      t.Name,
								Label:     label,
								Status:    t.Status,
								Elapsed:   t.ElapsedMS,
								Iteration: snap.Iteration,
								Summary:   t.Summary,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Thinking:  snap.Thinking,
							Reasoning: snap.Reasoning,
							Tools:     toolList,
						})
					}
					if len(iters) > 0 {
						// [interrupted] messages carry cancelled-turn iteration history
						// with full elapsed data. Use empty Content so the UI shows
						// only the progress block, not the "[interrupted]" marker text.
						isInterrupted := strings.HasPrefix(m.Content, "[interrupted]")
						if m.Content != "" && !isInterrupted {
							history = append(history, HistoryMessage{
								Role:       "assistant",
								Content:    m.Content,
								Timestamp:  m.Timestamp,
								Iterations: iters,
							})
						} else {
							// Detail has iterations but no displayable content
							// (intermediate assistant, cancelled turn, or [interrupted] marker).
							history = append(history, HistoryMessage{
								Role:       "assistant",
								Content:    "",
								Timestamp:  m.Timestamp,
								Iterations: iters,
							})
						}
					} else if m.Content != "" && !strings.HasPrefix(m.Content, "[interrupted]") {
						history = append(history, HistoryMessage{
							Role:      "assistant",
							Content:   m.Content,
							Timestamp: m.Timestamp,
						})
					}
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls from incremental persistence.
				// Accumulate into pending — don't flush yet.
				finishCurIter()
				curIterIdx++
				curIterThinking = m.Content
				curIterReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					curIterTools = append(curIterTools, protocol.ToolProgress{
						Name:      tc.Name,
						Label:     formatToolLabel(tc.Name, tc.Arguments),
						Status:    "done",
						Elapsed:   0,
						Iteration: curIterIdx,
					})
				}
			} else if m.Content != "" {
				flushPending()
				// Merge with previous assistant message that had iterations but no content.
				// Backend stores iterations in a separate DisplayOnly assistant message
				// (Detail set, content empty), followed by the real assistant reply (content set).
				// We need to combine them into one HistoryMessage for unified rendering.
				if len(history) > 0 && history[len(history)-1].Role == "assistant" &&
					history[len(history)-1].Content == "" && len(history[len(history)-1].Iterations) > 0 {
					history[len(history)-1].Content = m.Content
					history[len(history)-1].Timestamp = m.Timestamp
				} else {
					history = append(history, HistoryMessage{
						Role:      m.Role,
						Content:   m.Content,
						Timestamp: m.Timestamp,
					})
				}
			}
		default:
			flushPending()
			// Reset lastAssistantTS after flushing: the next tool_summary
			// belongs to a new turn (this default case is typically "user"),
			// so it should use its own synthetic timestamp if that turn
			// is also interrupted (no assistant reply).
			lastAssistantTS = time.Time{}
			if m.Content != "" {
				history = append(history, HistoryMessage{
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		}
	}
	flushPending()
	return history
}
