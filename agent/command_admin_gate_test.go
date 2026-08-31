package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/channel"
	"xbot/protocol"
)

// testAdminCmd is a minimal Command used to verify the AdminOnly gate plumbing.
type testAdminCmd struct{}

func (testAdminCmd) Name() string              { return "/test-admin" }
func (testAdminCmd) Aliases() []string         { return nil }
func (testAdminCmd) Match(content string) bool { return content == "/test-admin" }
func (testAdminCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return nil, nil
}
func (testAdminCmd) Concurrent() bool { return false }
func (testAdminCmd) CommandInfo() protocol.CommandInfo {
	return protocol.CommandInfo{Name: "test-admin", Usage: "/test-admin", AdminOnly: true}
}

// testPlainCmd is a plain (non-admin) command. NOTE: it must NOT embed
// testAdminCmd — embedded method promotion would inherit the AdminOnly
// CommandInfo and make every command admin-gated.
type testPlainCmd struct{}

func (testPlainCmd) Name() string              { return "/test-plain" }
func (testPlainCmd) Aliases() []string         { return nil }
func (testPlainCmd) Match(content string) bool { return content == "/test-plain" }
func (testPlainCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
	return nil, nil
}
func (testPlainCmd) Concurrent() bool { return false }
func (testPlainCmd) CommandInfo() protocol.CommandInfo {
	return protocol.CommandInfo{Name: "test-plain", Usage: "/test-plain"}
}

// TestCommandRequiresAdmin verifies the AdminOnly metadata plumbing: a command
// registered with AdminOnly=true is recognized as admin-gated; every other
// command is not.
func TestCommandRequiresAdmin(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&testAdminCmd{}, protocol.CommandInfo{Usage: "/test-admin", AdminOnly: true})
	r.Register(&testPlainCmd{}, protocol.CommandInfo{Usage: "/test-plain"})

	if cmd := r.Match("/test-admin"); cmd == nil || !commandRequiresAdmin(cmd) {
		t.Fatalf("commandRequiresAdmin(/test-admin) = false, want true (registered with AdminOnly)")
	}
	if cmd := r.Match("/test-plain"); cmd == nil || commandRequiresAdmin(cmd) {
		t.Fatalf("commandRequiresAdmin(/test-plain) = true, want false (no AdminOnly)")
	}
}

// TestBuiltinManagementCommandsAdminOnly guards the builtin registration:
// after the multi-user removal the management commands (subscriptions,
// models, settings, plugins, usage) mutate the operator's GLOBAL config and
// MUST be marked AdminOnly so non-admin channel users (feishu group members
// not in agent.admins) are rejected at dispatch.
func TestBuiltinManagementCommandsAdminOnly(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	adminOnly := map[string]bool{
		"/set-llm":   true,
		"/unset-llm": true,
		"/llm":       true,
		"/llms":      true,
		"/models":    true,
		"/set-model": true,
		"/settings":  true,
		"/usage":     true,
	}
	for usage := range adminOnly {
		var found bool
		for _, info := range r.CommandList() {
			// Usage carries the full argument string (e.g. "/set-llm <name>
			// provider=..."), so match by prefix.
			if strings.HasPrefix(info.Usage+" ", usage+" ") || info.Usage == usage {
				found = true
				if !info.AdminOnly {
					t.Errorf("builtin command %s is not marked AdminOnly (mutates global operator config)", usage)
				}
				break
			}
		}
		if !found {
			t.Errorf("builtin command %s not registered in the list — check the exact Usage string", usage)
		}
	}
}

// TestIsAdminSender verifies the admin decision used by the command gate:
// cli/web channels are trusted (local operator / password login), every
// other channel requires the senderID to be listed in agent.admins.
func TestIsAdminSender(t *testing.T) {
	a := &Agent{admins: []string{"ou_allowlisted"}}

	if !a.isAdminSender("cli", "cli_user") {
		t.Error("cli sender must be admin (local operator)")
	}
	if !a.isAdminSender("web", "web-7") {
		t.Error("web sender must be admin (password login)")
	}
	if !a.isAdminSender("feishu", "ou_allowlisted") {
		t.Error("feishu sender in the allowlist must be admin")
	}
	if a.isAdminSender("feishu", "ou_random_group_member") {
		t.Error("feishu sender NOT in the allowlist must not be admin")
	}
	if a.isAdminSender("qq", "123456") {
		t.Error("qq sender not in the allowlist must not be admin")
	}
}
