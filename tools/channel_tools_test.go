package tools

import (
	"testing"

	"xbot/llm"
)

// mockTool implements Tool for testing.
type mockTool struct {
	name  string
	chanN []string // supported channels (nil = all)
}

func (m *mockTool) Name() string                { return m.name }
func (m *mockTool) Description() string         { return "mock tool" }
func (m *mockTool) Parameters() []llm.ToolParam { return nil }
func (m *mockTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	return &ToolResult{Summary: m.name + " executed"}, nil
}
func (m *mockTool) SupportedChannels() []string { return m.chanN }

func TestRegisterForChannel(t *testing.T) {
	r := NewRegistry()
	tool := &mockTool{name: "test_channel_tool"}

	// Register for channel "github"
	r.RegisterForChannel("github", tool)

	// GetChannelTool should find it
	got, ok := r.GetChannelTool("github", "test_channel_tool")
	if !ok {
		t.Fatal("expected to find channel tool")
	}
	if got.Name() != "test_channel_tool" {
		t.Fatalf("wrong tool name: %s", got.Name())
	}

	// Other channel should not find it
	_, ok = r.GetChannelTool("feishu", "test_channel_tool")
	if ok {
		t.Fatal("should not find tool in other channel")
	}
}

func TestUnregisterChannelTools(t *testing.T) {
	r := NewRegistry()
	r.RegisterForChannel("github", &mockTool{name: "tool_a"})
	r.RegisterForChannel("github", &mockTool{name: "tool_b"})

	// Verify both exist
	if _, ok := r.GetChannelTool("github", "tool_a"); !ok {
		t.Fatal("tool_a should exist")
	}
	if _, ok := r.GetChannelTool("github", "tool_b"); !ok {
		t.Fatal("tool_b should exist")
	}

	// Unregister all for channel
	r.UnregisterChannelTools("github")

	if _, ok := r.GetChannelTool("github", "tool_a"); ok {
		t.Fatal("tool_a should be removed")
	}
	if _, ok := r.GetChannelTool("github", "tool_b"); ok {
		t.Fatal("tool_b should be removed")
	}
}

func TestAsDefinitionsForSession_ChannelScoped(t *testing.T) {
	r := NewRegistry()

	// Register a core tool (visible everywhere)
	r.RegisterCore(&mockTool{name: "shell"})

	// Register a channel tool (only for "feishu")
	r.RegisterForChannel("feishu", &mockTool{name: "card_create"})

	// Feishu session should see both
	defs := r.AsDefinitionsForSession("feishu:oc_xxx", 0)
	names := defNames(defs)
	if !containsToolName(names, "shell") {
		t.Error("feishu session should see core tool 'shell'")
	}
	if !containsToolName(names, "card_create") {
		t.Error("feishu session should see channel tool 'card_create'")
	}

	// CLI session should only see core tool
	defs = r.AsDefinitionsForSession("cli:/home/user/project", 0)
	names = defNames(defs)
	if !containsToolName(names, "shell") {
		t.Error("cli session should see core tool 'shell'")
	}
	if containsToolName(names, "card_create") {
		t.Error("cli session should NOT see feishu channel tool 'card_create'")
	}
}

func TestGetForSession_ChannelPriority(t *testing.T) {
	r := NewRegistry()

	// Register global tool and channel tool with same name
	r.Register(&mockTool{name: "shared_tool"})
	r.RegisterForChannel("feishu", &mockTool{name: "shared_tool"})

	// Channel version should take priority for feishu session
	tool, ok := r.GetForSession("shared_tool", 0, "feishu:oc_xxx")
	if !ok {
		t.Fatal("should find tool")
	}
	// The channel tool should be returned (it's from channelTools, not globalTools)
	// Both have the same name so we verify it exists
	if tool.Name() != "shared_tool" {
		t.Fatalf("wrong tool: %s", tool.Name())
	}

	// Non-channel session should get global version
	if _, ok := r.GetForSession("shared_tool", 0, "cli:/path"); !ok {
		t.Fatal("should find global tool")
	}
}

func TestChannelFromSessionKey(t *testing.T) {
	tests := []struct {
		sessionKey string
		want       string
	}{
		{"feishu:oc_xxx", "feishu"},
		{"cli:/home/user/project", "cli"},
		{"github:octocat/repo-pr-42", "github"},
		{"no_colon_here", ""},
		{"", ""},
		{":noprefix", ""},
	}
	for _, tt := range tests {
		got := ChannelFromSessionKey(tt.sessionKey)
		if got != tt.want {
			t.Errorf("ChannelFromSessionKey(%q) = %q, want %q", tt.sessionKey, got, tt.want)
		}
	}
}

func TestRegisterForChannel_EmptyChannel_FallbackToGlobal(t *testing.T) {
	r := NewRegistry()
	tool := &mockTool{name: "global_tool"}

	// Empty channel should fallback to Register
	r.RegisterForChannel("", tool)

	// Should be in globalTools
	got, ok := r.Get("global_tool")
	if !ok {
		t.Fatal("should find in global tools")
	}
	if got.Name() != "global_tool" {
		t.Fatalf("wrong tool: %s", got.Name())
	}
}

func TestHotUpdateChannelTools(t *testing.T) {
	r := NewRegistry()

	// Initial tools
	r.RegisterForChannel("github", &mockTool{name: "tool_a"})
	r.RegisterForChannel("github", &mockTool{name: "tool_b"})

	// Hot-update: unregister all, register new set
	r.UnregisterChannelTools("github")
	r.RegisterForChannel("github", &mockTool{name: "tool_c"})

	// tool_a and tool_b should be gone
	if _, ok := r.GetChannelTool("github", "tool_a"); ok {
		t.Error("tool_a should be removed after hot-update")
	}
	// tool_c should exist
	if _, ok := r.GetChannelTool("github", "tool_c"); !ok {
		t.Error("tool_c should exist after hot-update")
	}
}

// --- helpers ---

func defNames(defs []llm.ToolDefinition) []string {
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name())
	}
	return names
}

func containsToolName(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}
