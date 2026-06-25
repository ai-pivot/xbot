// Package runner manages execution environments (runners) that provide tools to the agent.
//
// Architecture:
//
//	Agent (LLM loop) → ToolProvider → RunnerToolProvider → RunnerManager → Instance
//	                                                                   └── Transport (agent.Transport)
//
// A Runner is an execution backend — it declares what tools it can execute and
// handles tool execution. The local runner provides all standard tools
// (Shell, Read, Write, MCP, etc.) in-process. Remote runners connect via
// WebSocket and declare their own tool sets.
//
// Key design decision: Runner directly reuses agent.Transport (Call + Close).
// No new transport interface — local runner uses ChannelTransport with an
// RPCTable, remote runner uses a WebSocket transport. Same code path for both.
package runner

import (
	"time"

	"xbot/tools"
)

// Type classifies a runner by communication mode.
type Type string

const (
	Local  Type = "local"
	Remote Type = "remote"
)

// Status is the connection state of a runner.
type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusDisconnected Status = "disconnected"
	StatusError        Status = "error"
)

// Instance is a named execution environment that provides tools.
//
// Each runner has a unique ID, a display name, a type, and a connection status.
// Tools maps tool names to their implementations — for local runner these are real
// tools, for remote runner these are proxies that send RPC over Transport.
type Instance struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Type       Type                  `json:"type"`
	Status     Status                `json:"status"`
	Tools      map[string]tools.Tool `json:"-"` // tool implementations (real or proxy)
	CreatedAt  time.Time             `json:"created_at"`
	LastSeenAt time.Time             `json:"last_seen_at"`
}
