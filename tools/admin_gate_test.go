package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigTool_ManagementActionsRequireAdmin verifies the admin gate on the
// config tool's management actions (model/subscription/runner/reload_*):
// after the multi-user removal these mutate the operator's GLOBAL config —
// non-admin channel users (feishu group members not in agent.admins) must
// be rejected.
func TestConfigTool_ManagementActionsRequireAdmin(t *testing.T) {
	tool := &ConfigTool{}
	nonAdmin := &ToolContext{OriginUserIsAdmin: false}
	admin := &ToolContext{OriginUserIsAdmin: true}

	for _, action := range []string{"model", "subscription", "runner", "reload_plugins", "reload_hooks"} {
		input, _ := json.Marshal(map[string]any{"action": action})
		_, err := tool.Execute(nonAdmin, string(input))
		if err == nil || !strings.Contains(err.Error(), "requires operator (admin) rights") {
			t.Errorf("config action %q for non-admin: err=%v, want management-action rejection", action, err)
		}
	}

	// Admin passes the gate (may fail later on missing callbacks — that is
	// fine, the gate must not be what rejects them).
	input, _ := json.Marshal(map[string]any{"action": "model", "sub": "list"})
	_, err := tool.Execute(admin, string(input))
	if err != nil && strings.Contains(err.Error(), "requires operator (admin) rights") {
		t.Errorf("config action model for admin: rejected by the admin gate: %v", err)
	}
}

// TestManageTools_ManagementActionsRequireAdmin verifies the admin gate on
// ManageTools (add_mcp/remove_mcp/reload): MCP server management mutates the
// operator's global MCP config. list_mcp stays available to everyone.
func TestManageTools_ManagementActionsRequireAdmin(t *testing.T) {
	tool := NewManageTools(t.TempDir(), t.TempDir()+"/mcp.json")
	nonAdmin := &ToolContext{OriginUserIsAdmin: false}

	for _, action := range []string{"add_mcp", "remove_mcp", "reload"} {
		input, _ := json.Marshal(manageToolsArgs{Action: action})
		_, err := tool.Execute(nonAdmin, string(input))
		if err == nil || !strings.Contains(err.Error(), "requires operator (admin) rights") {
			t.Errorf("manage_tools action %q for non-admin: err=%v, want management-action rejection", action, err)
		}
	}

	// list_mcp is read-only — never gated.
	input, _ := json.Marshal(manageToolsArgs{Action: "list_mcp"})
	if _, err := tool.Execute(nonAdmin, string(input)); err != nil {
		t.Errorf("list_mcp for non-admin should pass the gate, got: %v", err)
	}

	// Admin passes the gate (add_mcp proceeds to the real handler).
	admin := &ToolContext{OriginUserIsAdmin: true, Channel: "cli", MCPConfigPath: t.TempDir() + "/user-mcp.json", GlobalMCPConfigPath: t.TempDir() + "/global-mcp.json"}
	input, _ = json.Marshal(manageToolsArgs{
		Action:       "add_mcp",
		Name:         "gate-test",
		MCPConfig:    `{"url":"http://example.com/mcp"}`,
		Instructions: "test",
	})
	res, err := tool.Execute(admin, string(input))
	if err != nil {
		t.Fatalf("add_mcp for admin should pass the gate, got: %v", err)
	}
	if res == nil {
		t.Fatal("add_mcp for admin: nil result")
	}
}
