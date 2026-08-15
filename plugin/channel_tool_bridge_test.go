package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// mockExecutor captures execute_tool calls and returns a canned result.
type mockExecutor struct {
	result json.RawMessage
	called int
}

func (m *mockExecutor) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	m.called++
	if method != "execute_tool" {
		return nil, &rpcErr{msg: "unexpected method: " + method}
	}
	if m.result == nil {
		return json.Marshal(map[string]string{"content": "ok"})
	}
	return m.result, nil
}

type rpcErr struct{ msg string }

func (e *rpcErr) Error() string { return e.msg }

// TestChannelToolBridge_UIDecl verifies the bridge exposes the tool's UI
// declaration via tools.UIDeclProvider — the metadata-driven lookup that
// replaces hardcoded tool-name checks in engine_wire.
func TestChannelToolBridge_UIDecl(t *testing.T) {
	decl := ChannelToolDecl{
		Name:        "render_chart",
		Description: "Render a chart",
		Parameters:  []llm.ToolParam{{Name: "tsx", Type: "string", Required: true}},
		Channels:    []string{"web", "feishu"},
		UI: &tools.UIDecl{
			Mode:  "genui",
			Param: "tsx",
			Libs:  []string{"echarts"},
		},
	}
	exec := &mockExecutor{}
	bridge := NewChannelToolBridge(decl, exec)

	// UIDeclProvider
	p, ok := any(bridge).(tools.UIDeclProvider)
	if !ok {
		t.Fatal("ChannelToolBridge must implement tools.UIDeclProvider")
	}
	ui := p.UIDecl()
	if ui == nil {
		t.Fatal("expected non-nil UIDecl")
	}
	if ui.Mode != "genui" || ui.Param != "tsx" {
		t.Fatalf("UIDecl = %+v, want mode=genui param=tsx", ui)
	}
	if len(ui.Libs) != 1 || ui.Libs[0] != "echarts" {
		t.Fatalf("Libs = %v, want [echarts]", ui.Libs)
	}
	if len(bridge.Channels()) != 2 {
		t.Fatalf("Channels() = %v, want [web feishu]", bridge.Channels())
	}
}

// TestChannelToolBridge_NoUI verifies a tool without UI declaration returns nil.
func TestChannelToolBridge_NoUI(t *testing.T) {
	decl := ChannelToolDecl{Name: "plain_tool", Description: "no ui"}
	bridge := NewChannelToolBridge(decl, &mockExecutor{})
	p, _ := any(bridge).(tools.UIDeclProvider)
	if p.UIDecl() != nil {
		t.Fatalf("expected nil UIDecl for plain tool, got %+v", p.UIDecl())
	}
}

// TestChannelToolBridge_UICodeExecute verifies that when the channel process
// returns ui_code in execute_tool, the bridge (1) pushes the UI via ctx.SendFunc
// with genui metadata, and (2) stores the full code in ToolResult.Detail so the
// committed message can rebuild the UI from history.
func TestChannelToolBridge_UICodeExecute(t *testing.T) {
	decl := ChannelToolDecl{Name: "display_html", Description: "genui"}
	exec := &mockExecutor{}
	exec.result = json.RawMessage(`{"content":"rendered","is_error":false,"ui_code":"export default function App(){return <div/>}"}`)
	bridge := NewChannelToolBridge(decl, exec)

	var sentChannel, sentChatID, sentContent string
	var sentMeta map[string]string
	sendFunc := func(channel, chatID, content string, metadata ...map[string]string) error {
		sentChannel = channel
		sentChatID = chatID
		sentContent = content
		if len(metadata) > 0 {
			sentMeta = metadata[0]
		}
		return nil
	}

	ctx := &tools.ToolContext{
		Channel:  "web",
		ChatID:   "chat-1",
		SendFunc: sendFunc,
	}
	result, err := bridge.Execute(ctx, `{"code":"export default function App(){return <div/>}"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Summary != "rendered" {
		t.Fatalf("Summary = %q, want rendered", result.Summary)
	}
	if result.IsError {
		t.Fatal("IsError = true, want false")
	}
	// SendFunc must be called with the UI code + genui metadata.
	if sentChannel != "web" || sentChatID != "chat-1" {
		t.Fatalf("SendFunc called with channel=%q chatID=%q, want web/chat-1", sentChannel, sentChatID)
	}
	if sentContent == "" {
		t.Fatal("SendFunc content must carry ui_code")
	}
	if sentMeta["genui"] != "true" {
		t.Fatalf("SendFunc metadata = %v, want genui=true", sentMeta)
	}
	// Detail must carry the full UI code for history rebuild.
	if result.Detail == "" || result.Detail == result.Summary {
		t.Fatalf("Detail = %q, want to include ui_code", result.Detail)
	}
}

// TestChannelToolBridge_UICode_NoSendFunc verifies ui_code handling is a no-op
// when SendFunc is nil (e.g. non-web channels) — no panic, Detail still stored.
func TestChannelToolBridge_UICode_NoSendFunc(t *testing.T) {
	decl := ChannelToolDecl{Name: "display_html", Description: "genui"}
	exec := &mockExecutor{}
	exec.result = json.RawMessage(`{"content":"ok","ui_code":"export default function App(){}"}`)
	bridge := NewChannelToolBridge(decl, exec)

	result, err := bridge.Execute(&tools.ToolContext{Channel: "feishu", ChatID: "c"}, `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Detail == "" {
		t.Fatal("Detail must still carry ui_code when SendFunc is nil")
	}
	_ = context.Background() // keep import
}
