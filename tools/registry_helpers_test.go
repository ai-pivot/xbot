package tools

import "testing"

// TestDefaultRegistry_UnregisterBlacklist verifies the GLOBAL tool blacklist
// mechanism: a blacklisted tool name is removed from the registry so it is
// neither visible (List/AsDefinitions) nor executable (Get).
func TestDefaultRegistry_UnregisterBlacklist(t *testing.T) {
	r := DefaultRegistry("flat")

	// Sanity: Shell is registered by default.
	if _, ok := r.Get("Shell"); !ok {
		t.Fatal("expected Shell tool to be registered by default")
	}

	r.Unregister("Shell")

	if _, ok := r.Get("Shell"); ok {
		t.Fatal("Shell must be gone after Unregister")
	}
	for _, tool := range r.List() {
		if tool.Name() == "Shell" {
			t.Fatal("Shell must not appear in List after Unregister")
		}
	}
}
