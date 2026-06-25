package runner

import (
	"time"

	"xbot/tools"
)

// NewLocal creates a local runner that directly holds tool implementations.
//
// The agent executes tools via: provider.GetTool(name) → tool.Execute(ctx, args).
// For local runner this is a direct function call with zero serialization overhead.
// For remote runner the tool would be a proxy that sends RPC over Transport.
func NewLocal(toolList []tools.Tool) *Instance {
	toolMap := make(map[string]tools.Tool, len(toolList))
	for _, t := range toolList {
		toolMap[t.Name()] = t
	}

	return &Instance{
		ID:        "local",
		Name:      "Local Runner",
		Type:      Local,
		Status:    StatusConnected,
		Tools:     toolMap,
		CreatedAt: time.Now(),
	}
}
