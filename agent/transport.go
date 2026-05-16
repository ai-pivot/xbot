package agent

import (
	"encoding/json"
)

// Transport is the pure transmission layer.
// It knows nothing about Agent, Bus, or channels — it only transports
// (method, json) → (json, error).
//
// Implementations:
//   - ChannelTransport: in-process direct dispatch (local mode)
//   - RemoteTransport: WebSocket RPC (remote mode)
//   - Future: gRPC, HTTP/2, etc.
//
// Adding a new transport only requires implementing Call + Close.
type Transport interface {
	// Call sends an RPC request and returns the response.
	// method is an RPC method name (e.g. "get_settings").
	Call(method string, payload json.RawMessage) (json.RawMessage, error)

	// Close releases transport resources.
	Close() error
}
