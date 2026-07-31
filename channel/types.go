package channel

import "context"

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

	// TurnID identifies the agent turn that produced this reply. Set by
	// sendMessage from the active turn's TurnID. The frontend uses it to
	// associate the reply with the correct user message (by TurnID, not
	// arrival order). 0 = untracked (SubAgent, legacy).
	TurnID uint64 `json:"-"`

	// Ctx carries the caller's context for cancellation propagation.
	// Used by AgentChannel.Send to respect caller cancellation (e.g. Ctrl+C).
	// Ignored by other Channel implementations. Not serialized.
	Ctx context.Context `json:"-"`
}

// AskQItem is the JSON structure for questions metadata from the AskUser tool.
// Shared by CLI and Feishu channels for rendering interactive question UIs.
type AskQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// SessionChatMessage is a single message in a SubAgent conversation (for API responses).
// Used by both CLI (agent panel callback) and Web (API responses).
type SessionChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
