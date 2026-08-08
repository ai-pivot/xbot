package tools

// memoryToolFactories maps provider name → tool factory function.
// Each provider's tools register via init() using RegisterMemoryTools.
var memoryToolFactories = map[string]func() []Tool{}

// RegisterMemoryTools registers a tool factory for a memory provider name.
// Called by each provider's tools package in init().
func RegisterMemoryTools(providerName string, factory func() []Tool) {
	memoryToolFactories[providerName] = factory
}

// GetMemoryTools returns the tool instances for a memory provider name.
// Returns nil if no factory is registered (e.g., "none" provider).
func GetMemoryTools(providerName string) []Tool {
	if f, ok := memoryToolFactories[providerName]; ok {
		return f()
	}
	return nil
}
