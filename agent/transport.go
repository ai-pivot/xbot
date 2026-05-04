package agent

import (
	"context"
	"encoding/json"

	"xbot/bus"
	"xbot/channel"
)

// Transport is the pure communication layer. It handles how data is sent and
// received, without any knowledge of business semantics.
//
// Implementations:
//   - RemoteTransport: WebSocket-based, for remote CLI connecting to xbot server.
//   - Future: gRPCTransport, MCPTransport, etc.
//
// Local mode does NOT use Transport — Backend directly accesses Agent.
type Transport interface {
	// === Lifecycle ===
	Start(ctx context.Context) error
	Stop()
	Close() error

	// === Communication ===

	// Call sends a request and waits for a response.
	// method is an RPC method name (e.g. "get_settings").
	// payload and response are JSON-encoded.
	Call(method string, payload json.RawMessage) (json.RawMessage, error)

	// SendMessage sends a user message to the agent (fire-and-forget).
	SendMessage(msg Message) error

	// Subscribe registers this connection to receive events for chatID.
	Subscribe(chatID string) error

	// === Server-push events ===

	OnOutbound(cb func(bus.OutboundMessage))
	OnProgress(cb func(*channel.CLIProgressPayload))
	OnInjectUserMessage(cb func(content string))
	OnReconnect(cb func())
	OnConnStateChange(cb func(state string))
	OnPluginWidgets(cb func(zones map[string]string, chatID string))

	// === State ===

	ConnState() string
	IsRemote() bool
	ServerURL() string
}

// Message is a transport-level message sent to the agent.
type Message struct {
	Content    string
	Channel    string
	ChatID     string
	SenderID   string
	SenderName string
	ChatType   string
	Cancel     bool
}
