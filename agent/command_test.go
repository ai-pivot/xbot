package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/bus"
)

func TestCommandRegistry_Match(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	tests := []struct {
		input   string
		wantCmd string // expected Name(), or "" for no match
	}{
		// Exact matches
		{"/new", "/new"},
		{"/version", "/version"},
		{"/help", "/help"},
		{"/llm", "/llm"},
		{"/llms", "/llms"},

		// Case insensitive
		{"/NEW", "/new"},
		{"/Version", "/version"},
		{"/HELP", "/help"},
		{"/LLM", "/llm"},

		// With leading/trailing whitespace
		{"  /new  ", "/new"},
		{" /version ", "/version"},

		// Prefix commands
		{"/prompt", "/prompt"},
		{"/prompt show me the system prompt", "/prompt"},
		{"/set-llm", "/set-llm"},
		{"/set-llm provider=openai", "/set-llm"},
		{"/unset-llm", "/unset-llm"},
		{"/set-model", "/set-model"},
		{"/set-model gpt-4", "/set-model"},
		{"/models", "/models"},

		// Bang commands
		{"!ls", "!"},
		{"!echo hello world", "!"},
		{"! ls -la", "!"},

		// Not commands
		{"hello", ""},
		{"", ""},
		{"new", ""},
		{"version", ""},
		{"!", ""},           // bare ! is not a bang command
		{"!! echo hi", "!"}, // !! is valid (command is "! echo hi")
	}

	for _, tt := range tests {
		cmd := r.Match(tt.input)
		if tt.wantCmd == "" {
			if cmd != nil {
				t.Errorf("Match(%q) = %q, want nil", tt.input, cmd.Name())
			}
		} else {
			if cmd == nil {
				t.Errorf("Match(%q) = nil, want %q", tt.input, tt.wantCmd)
			} else if cmd.Name() != tt.wantCmd {
				t.Errorf("Match(%q) = %q, want %q", tt.input, cmd.Name(), tt.wantCmd)
			}
		}
	}
}

func TestCommandRegistry_IsCommand(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	if !r.IsCommand("/new") {
		t.Error("IsCommand(/new) = false, want true")
	}
	if !r.IsCommand("!ls") {
		t.Error("IsCommand(!ls) = false, want true")
	}
	if r.IsCommand("hello world") {
		t.Error("IsCommand(hello world) = true, want false")
	}
}

func TestCommandRegistry_Commands(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	cmds := r.Commands()
	if len(cmds) != 24 {
		t.Errorf("Commands() returned %d commands, want 24", len(cmds))
	}

	// Verify all expected commands are registered
	names := make(map[string]bool)
	for _, cmd := range cmds {
		names[cmd.Name()] = true
	}
	expected := []string{"/new", "/version", "/help", "/prompt", "/set-llm", "/unset-llm", "/llm", "/llms", "/models", "/set-model", "/compress", "/context", "!", "/publish", "/unpublish", "/browse", "/install", "/uninstall", "/my", "/settings", "/menu"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("Command %q not found in registry", name)
		}
	}
}

func TestCommandRegistry_HelpTextUsesRegisteredCommands(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)
	r.RegisterCommand(&pluginCmdAdapter{name: "/deploy", description: "部署当前项目"})

	help := r.HelpText()
	for _, want := range []string{
		"/new — 开始新对话",
		"/set-model <订阅名> <模型名> — 切换当前会话模型",
		"/plugin reload-all — 重新加载所有插件",
		"/deploy — 部署当前项目",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("HelpText() missing %q\n%s", want, help)
		}
	}
}

func TestHelpCommandRendersRegistryHelp(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)
	r.RegisterCommand(&pluginCmdAdapter{name: "/deploy", description: "部署当前项目"})

	cmd := r.Match("/help")
	if cmd == nil {
		t.Fatal("Match(/help) = nil")
	}
	out, err := cmd.Execute(context.Background(), &Agent{commands: r}, bus.InboundMessage{Channel: "cli", ChatID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !strings.Contains(out.Content, "/deploy — 部署当前项目") {
		t.Fatalf("/help output did not include registered plugin command: %#v", out)
	}
}

func TestCommandConcurrency(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	// Commands that mutate session state must NOT be concurrent
	nonConcurrent := map[string]bool{
		"/new":          true,
		"/compress":     true,
		"/set-llm":      true,
		"/unset-llm":    true,
		"/set-model":    true,
		"/context mode": true,
		"/publish":      true,
		"/unpublish":    true,
		"/install":      true,
		"/uninstall":    true,
	}

	// Commands that are stateless/read-only should be concurrent
	concurrent := map[string]bool{
		"/version":  true,
		"/help":     true,
		"/llm":      true,
		"/llms":     true,
		"/models":   true,
		"/prompt":   true,
		"/context":  true,
		"!":         true,
		"/browse":   true,
		"/my":       true,
		"/settings": true,
		"/menu":     true,
	}

	for _, cmd := range r.Commands() {
		name := cmd.Name()
		if nonConcurrent[name] {
			if cmd.Concurrent() {
				t.Errorf("Command %q: Concurrent() = true, want false (mutates session state)", name)
			}
		}
		if concurrent[name] {
			if !cmd.Concurrent() {
				t.Errorf("Command %q: Concurrent() = false, want true (stateless)", name)
			}
		}
	}
}
