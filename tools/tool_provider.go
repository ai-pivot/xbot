package tools

// ToolProvider is a source of tools for the agent.
//
// The agent merges tools from all registered providers (runner, channel, plugin, agent core)
// and routes tool execution to the provider that declared the tool.
//
// Priority determines lookup order: lower numbers are checked first.
// Agent core tools (priority 1) override runner tools (priority 2), etc.
type ToolProvider interface {
	// Name returns a human-readable identifier for logging/debugging.
	Name() string

	// ListTools returns all tools this provider makes available for the given session.
	ListTools(sessionKey string, tenantID int64) []Tool

	// GetTool looks up a tool by name. Returns nil, false if not found.
	GetTool(sessionKey string, tenantID int64, name string) (Tool, bool)

	// Priority returns the lookup priority (lower = checked first).
	Priority() int
}
