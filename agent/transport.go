package agent

import (
	"context"
	"encoding/json"

	"xbot/bus"
	"xbot/channel"
)

// Transport is the execution layer. Every Backend method goes through Transport.
//
// Local mode uses localTransport (in-process handler dispatch that directly
// operates on *Agent). Remote mode uses RemoteTransport (WebSocket RPC to xbot server).
//
// The key insight: Backend is a pure typed RPC client. Transport decides whether
// the call executes locally or remotely. Backend never branches on mode.
type Transport interface {
	// === Lifecycle ===
	Start(ctx context.Context) error
	Stop()
	Close() error
	Run(ctx context.Context) error // blocks until done (local: agent.Run, remote: <-ctx.Done())

	// === RPC ===
	// Call sends a request and returns the response.
	// method is an RPC method name (e.g. "get_settings").
	Call(method string, payload json.RawMessage) (json.RawMessage, error)

	// === Communication ===
	SendMessage(msg Message) error
	Subscribe(chatID string) error

	// === Server-push events ===
	OnOutbound(cb func(bus.OutboundMessage))
	OnProgress(cb func(*channel.CLIProgressPayload))
	OnInjectUserMessage(cb func(chatID, content string))
	OnReconnect(cb func())
	OnConnStateChange(cb func(state string))
	OnPluginWidgets(cb func(zones map[string]string, chatID string))
	OnTUIControlRequest(cb func(action string, params map[string]string) (map[string]string, error))

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
