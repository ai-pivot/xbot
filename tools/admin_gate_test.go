package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigTool_ManagementActionsRequireAdmin verifies the admin gate on the
// config tool's GLOBAL management actions (subscription/runner/reload_*):
// after the multi-user removal these mutate the operator's shared config —
// non-admin channel users (feishu group members not in agent.admins) must
// be rejected. `model` is intentionally NOT in this list: switching the
// current session's model is a user-level action (see the switch test below),
// only catalogue edits stay gated.
func TestConfigTool_ManagementActionsRequireAdmin(t *testing.T) {
	tool := &ConfigTool{}
	nonAdmin := &ToolContext{OriginUserIsAdmin: false}
	admin := &ToolContext{OriginUserIsAdmin: true}

	for _, action := range []string{"subscription", "runner", "reload_plugins", "reload_hooks"} {
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

// TestConfigTool_ModelSwitchAllowedForNonAdmin —— 用户 2026-11-17 要求：
// 非 admin 用户要能用自然语言让 agent 切换**本会话**模型，所以 model
// sub=switch 不再是 admin 专属；而改写全局模型目录（add/remove/refresh/
// enable/disable/set_context/set_output）仍然是 admin 专属。
func TestConfigTool_ModelSwitchAllowedForNonAdmin(t *testing.T) {
	tool := &ConfigTool{}
	called := false
	nonAdmin := &ToolContext{
		OriginUserIsAdmin: false,
		SelectModelFn: func(subID, model string) error {
			called = true
			if subID != "sub_x" || model != "gemini-4-argon" {
				t.Errorf("SelectModelFn got (%q,%q)", subID, model)
			}
			return nil
		},
	}

	// switch：必须放行并真的执行
	input, _ := json.Marshal(map[string]any{"action": "model", "sub": "switch", "sub_id": "sub_x", "model": "gemini-4-argon"})
	res, err := tool.Execute(nonAdmin, string(input))
	if err != nil {
		t.Fatalf("non-admin model switch rejected: %v", err)
	}
	if !called {
		t.Fatal("SelectModelFn was not invoked for non-admin switch")
	}
	if res == nil || (!strings.Contains(res.Summary, "gemini-4-argon") && !strings.Contains(res.Detail, "gemini-4-argon")) {
		t.Fatalf("unexpected result: %+v", res)
	}

	// 全局目录写入：仍然要 admin
	for _, sub := range []string{"add", "remove", "refresh", "enable", "disable", "set_context", "set_output"} {
		input, _ := json.Marshal(map[string]any{"action": "model", "sub": sub, "sub_id": "sub_x", "model": "m", "value": "1"})
		_, err := tool.Execute(nonAdmin, string(input))
		if err == nil || !strings.Contains(err.Error(), "requires operator (admin) rights") {
			t.Errorf("non-admin model %q: err=%v, want admin rejection", sub, err)
		}
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
