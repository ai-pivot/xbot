package agent

import (
	"context"
	"encoding/json"

	"xbot/protocol"
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
	SendMessage(msg protocol.InboundMessage) error
	// BindChat registers a chat session for event routing (WS channel subscription).
	BindChat(chatID string) error

	// === Event subscription (new protocol-based API) ===
	// Subscribe registers a handler for protocol events matching the given pattern.
	// Returns a cancel function to unsubscribe.
	Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func())

	// === TUI Control (request-response, cannot be expressed as fire-and-forget event) ===
	// SetTUIControlHandler registers the handler for server-initiated TUI control requests.
	// The handler receives (action, params) and returns (result, error) via WebSocket RPC.
	SetTUIControlHandler(cb func(action string, params map[string]string) (map[string]string, error))

	// === State ===
	ConnState() string
	IsRemote() bool
	ServerURL() string
}
