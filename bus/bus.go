package bus

import "time"

const (
	// MetadataReplyPolicy controls how Agent should behave before final reply.
	// Supported values:
	// - "auto" (default): normal flow, send ack/progress
	// - "optional": no ack/progress; agent may decide to not reply
	MetadataReplyPolicy = "reply_policy"

	ReplyPolicyAuto     = "auto"
	ReplyPolicyOptional = "optional"
)

// InboundReplyPolicy returns normalized reply policy for inbound metadata.
func InboundReplyPolicy(metadata map[string]string) string {
	if metadata == nil {
		return ReplyPolicyAuto
	}
	policy := metadata[MetadataReplyPolicy]
	if policy == "" {
		return ReplyPolicyAuto
	}
	return policy
}

// ShouldPreReplyNotify indicates whether ack/progress UI should be sent before final reply.
func ShouldPreReplyNotify(metadata map[string]string) bool {
	return InboundReplyPolicy(metadata) != ReplyPolicyOptional
}

// InboundMessage is a unified inbound message.
// Source can be an IM channel (feishu/qq/cli) or another Agent (internal agent call).
type InboundMessage struct {
	// === Routing (unified addressing, Phase 1) ===
	From Address // Sender address: im://feishu/ou_xxx or agent://main
	To   Address // Target address: im://feishu/oc_xxx or agent://main/code-reviewer

	// === Routing (legacy fields, kept during migration, dual-written at channel layer) ===
	Channel    string // Channel name: "feishu", "cli", "agent", etc.
	SenderID   string // Sender identifier
	SenderName string // Sender name (parsed by channel)
	ChatID     string // Session/group identifier
	ChatType   string // Session type: "p2p" / "group" / "agent"

	// === Content ===
	Content string   // Message text
	Media   []string // Media file paths

	// === Metadata ===
	Metadata  map[string]string // Channel/caller-specific metadata
	Time      time.Time
	IsCron    bool   // Whether triggered by a cron scheduled task
	RequestID string // Request trace ID (UUID without hyphens), generated when channel receives a message

	// Event-triggered metadata (generalization beyond IsCron)
	EventSource  string // event origin: "webhook", "cron", "" (user message)
	EventTrigger string // trigger ID that fired this message (optional)

	// === Inter-agent communication (only set when Channel="agent") ===
	ParentAgentID string   // Parent Agent ID (e.g. "main")
	SystemPrompt  string   // Override system prompt
	AllowedTools  []string // Tool whitelist (empty = all tools available except SubAgent)
	RoleName      string   // SubAgent role name (e.g. "code-reviewer")
	// Capabilities declares the capabilities granted to a SubAgent.
	// Uses map[string]bool to avoid bus→tools circular dependency.
	// Known keys: "memory", "send_message", "spawn_agent"
	Capabilities map[string]bool
}

// IsFromAgent checks if the message is from another Agent (not an IM channel).
func (m *InboundMessage) IsFromAgent() bool {
	return m.Channel == SchemeAgent || m.From.IsAgent()
}

// OriginChannel returns the original IM channel name.
// For inter-agent calls, inherited from Metadata["origin_channel"]; otherwise returns current Channel.
func (m *InboundMessage) OriginChannel() string {
	if m.IsFromAgent() {
		if ch, ok := m.Metadata["origin_channel"]; ok {
			return ch
		}
	}
	return m.Channel
}

// OriginChatID returns the original IM session ID.
func (m *InboundMessage) OriginChatID() string {
	if m.IsFromAgent() {
		if id, ok := m.Metadata["origin_chat_id"]; ok {
			return id
		}
	}
	return m.ChatID
}

// OriginSenderID returns the original IM sender ID.
func (m *InboundMessage) OriginSenderID() string {
	if m.IsFromAgent() {
		if id, ok := m.Metadata["origin_sender"]; ok {
			return id
		}
	}
	return m.SenderID
}

// OutboundMessage is a unified outbound message.
// Target can be an IM channel or calling Agent.
type OutboundMessage struct {
	// === Routing (unified addressing, Phase 1) ===
	From Address // Sender address: agent://main
	To   Address // Target address: im://feishu/oc_xxx or agent://main（返回给父 Agent）

	// === 路由（旧字段，迁移期间保留） ===
	Channel string // Target channel
	ChatID  string // Target session

	// === Content ===
	Content string   // Message text
	Media   []string // Attachment file paths

	// === Metadata ===
	Metadata map[string]string // Additional metadata

	// === Streaming output ===
	IsPartial bool // Whether this is a partial streaming message (true=append, false=complete)

	// === Inter-agent communication ===
	ToolsUsed   []string // List of tools used (carried on SubAgent return)
	WaitingUser bool     // Whether waiting for user response
	Error       error    // Execution error (carried on SubAgent return)
}

// MessageBus is an async message bus that decouples channels and the Agent
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage
}

// NewMessageBus creates a new message bus
func NewMessageBus() *MessageBus {
	return &MessageBus{
		Inbound:  make(chan InboundMessage, 64),
		Outbound: make(chan OutboundMessage, 64),
	}
}
