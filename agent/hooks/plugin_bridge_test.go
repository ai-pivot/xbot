package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xbot/plugin"
)

// bridgeTestEvent is a flexible Event implementation for plugin_bridge tests.
type bridgeTestEvent struct {
	name      string
	toolName  string
	toolInput map[string]any
	payload   map[string]any
}

func (e *bridgeTestEvent) EventName() string         { return e.name }
func (e *bridgeTestEvent) ToolName() string          { return e.toolName }
func (e *bridgeTestEvent) ToolInput() map[string]any { return e.toolInput }
func (e *bridgeTestEvent) Payload() map[string]any {
	if e.payload != nil {
		return e.payload
	}
	return map[string]any{"hook_event_name": e.name}
}

// TestPluginBridgeCallback_PreToolUse verifies that a PreToolUse event is
// correctly converted to a plugin payload and dispatched.
func TestPluginBridgeCallback_PreToolUse(t *testing.T) {
	bridge := plugin.NewPluginHookBridge()

	var capturedPayload *plugin.HookPayload
	bridge.Register("test-plugin", plugin.HookPreToolUse, "Shell",
		func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
			capturedPayload = payload
			return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
		},
	)

	cb := PluginBridgeCallback(bridge)
	evt := &bridgeTestEvent{
		name:      "PreToolUse",
		toolName:  "Shell",
		toolInput: map[string]any{"command": "ls -la"},
		payload: map[string]any{
			"session_id": "sess-123",
			"channel":    "cli",
			"chat_id":    "chat-456",
			"sender_id":  "user-789",
		},
	}

	result, err := cb.Fn(context.Background(), evt)
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	// Verify result
	if result.Decision != "allow" {
		t.Errorf("expected decision 'allow', got %q", result.Decision)
	}

	// Verify payload conversion
	if capturedPayload == nil {
		t.Fatal("expected payload to be captured")
		return
	}
	if capturedPayload.ToolName != "Shell" {
		t.Errorf("expected ToolName 'Shell', got %q", capturedPayload.ToolName)
	}
	if capturedPayload.SessionID != "sess-123" {
		t.Errorf("expected SessionID 'sess-123', got %q", capturedPayload.SessionID)
	}
	if capturedPayload.Channel != "cli" {
		t.Errorf("expected Channel 'cli', got %q", capturedPayload.Channel)
	}
	if capturedPayload.ChatID != "chat-456" {
		t.Errorf("expected ChatID 'chat-456', got %q", capturedPayload.ChatID)
	}
	if capturedPayload.UserID != "user-789" {
		t.Errorf("expected UserID 'user-789', got %q", capturedPayload.UserID)
	}

	// Verify tool input was serialized to JSON
	var input map[string]any
	if err := json.Unmarshal([]byte(capturedPayload.ToolInput), &input); err != nil {
		t.Fatalf("failed to parse tool input JSON: %v", err)
	}
	if input["command"] != "ls -la" {
		t.Errorf("expected command 'ls -la', got %v", input["command"])
	}
}

// TestPluginBridgeCallback_PostToolUse verifies dispatch with no matching
// handlers returns defer decision.
func TestPluginBridgeCallback_PostToolUse(t *testing.T) {
	bridge := plugin.NewPluginHookBridge()
	// Register a handler for PreToolUse only — PostToolUse should have no match
	bridge.Register("test-plugin", plugin.HookPreToolUse, "",
		func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
			return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
		},
	)

	cb := PluginBridgeCallback(bridge)
	evt := &bridgeTestEvent{
		name:     "PostToolUse",
		toolName: "Read",
	}

	result, err := cb.Fn(context.Background(), evt)
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	// No handlers for PostToolUse → bridge returns Defer
	if result.Decision != "defer" {
		t.Errorf("expected decision 'defer' for unmatched event, got %q", result.Decision)
	}
}

// TestPluginBridgeCallback_DenyDecision verifies that a deny decision from a
// plugin hook is correctly propagated through the bridge callback.
func TestPluginBridgeCallback_DenyDecision(t *testing.T) {
	bridge := plugin.NewPluginHookBridge()
	bridge.Register("security-plugin", plugin.HookPreToolUse, "Shell",
		func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
			return &plugin.HookResult{
				Decision: plugin.DecisionDeny,
				Message:  "Shell commands are not allowed in production",
			}, nil
		},
	)

	cb := PluginBridgeCallback(bridge)
	evt := &bridgeTestEvent{
		name:      "PreToolUse",
		toolName:  "Shell",
		toolInput: map[string]any{"command": "rm -rf /"},
	}

	result, err := cb.Fn(context.Background(), evt)
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	if result.Decision != "deny" {
		t.Errorf("expected decision 'deny', got %q", result.Decision)
	}
	if !strings.Contains(result.Reason, "not allowed") {
		t.Errorf("expected reason to contain 'not allowed', got %q", result.Reason)
	}
}

// TestPluginBridgeCallback_NilToolInput verifies that events with nil tool
// input don't cause errors.
func TestPluginBridgeCallback_NilToolInput(t *testing.T) {
	bridge := plugin.NewPluginHookBridge()

	var capturedPayload *plugin.HookPayload
	bridge.Register("test-plugin", plugin.HookSessionStart, "",
		func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
			capturedPayload = payload
			return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
		},
	)

	cb := PluginBridgeCallback(bridge)
	evt := &bridgeTestEvent{
		name:     "SessionStart",
		toolName: "",
	}

	result, err := cb.Fn(context.Background(), evt)
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	if result.Decision != "allow" {
		t.Errorf("expected decision 'allow', got %q", result.Decision)
	}
	if capturedPayload.ToolInput != "" {
		t.Errorf("expected empty ToolInput, got %q", capturedPayload.ToolInput)
	}
}

// TestPluginBridgeCallback_NilPayload verifies that events with nil payload
// map don't cause panics.
func TestPluginBridgeCallback_NilPayload(t *testing.T) {
	bridge := plugin.NewPluginHookBridge()
	bridge.Register("test-plugin", plugin.HookPreToolUse, "",
		func(ctx context.Context, payload *plugin.HookPayload) (*plugin.HookResult, error) {
			return &plugin.HookResult{Decision: plugin.DecisionAllow}, nil
		},
	)

	cb := PluginBridgeCallback(bridge)
	// Use a PreToolUseEvent (real event) with minimal setup
	evt := &PreToolUseEvent{
		ToolName_:  "Read",
		ToolInput_: map[string]any{"path": "/tmp/test"},
	}

	result, err := cb.Fn(context.Background(), evt)
	if err != nil {
		t.Fatalf("callback returned error: %v", err)
	}

	if result.Decision != "allow" {
		t.Errorf("expected decision 'allow', got %q", result.Decision)
	}
}
