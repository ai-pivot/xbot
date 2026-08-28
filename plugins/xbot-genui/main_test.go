package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestDeclareTools verifies the channel_tools declaration carries the display_html
// tool registered to the web channel WITH UI metadata (the metadata-driven
// contract that engine_wire and the frontend rely on — no hardcoded tool names).
func TestDeclareTools(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	declareTools(enc)

	var msg struct {
		Type  string `json:"type"`
		Tools []struct {
			Name     string   `json:"name"`
			Channels []string `json:"channels"`
			UI       struct {
				Mode  string   `json:"mode"`
				Param string   `json:"param"`
				Libs  []string `json:"libs"`
			} `json:"ui"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(buf.Bytes(), &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Type != "channel_tools" {
		t.Fatalf("type = %q, want channel_tools", msg.Type)
	}
	if len(msg.Tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(msg.Tools))
	}
	tool := msg.Tools[0]
	if tool.Name != "display_html" {
		t.Fatalf("name = %q, want display_html", tool.Name)
	}
	// Must be registered to the web channel.
	foundWeb := false
	for _, c := range tool.Channels {
		if c == "web" {
			foundWeb = true
		}
	}
	if !foundWeb {
		t.Fatalf("channels = %v, want to include web", tool.Channels)
	}
	// UI metadata must be present (mode=genui, param=code).
	if tool.UI.Mode != "genui" || tool.UI.Param != "code" {
		t.Fatalf("ui = %+v, want mode=genui param=code", tool.UI)
	}
}

// TestHandleExecuteTool_ValidCode verifies a valid TSX returns ui_code.
func TestHandleExecuteTool_ValidCode(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	code := `export default function App(){ return <div className="p-4"><XBOT_UI.Stat label="Clicks" value={3}/></div> }`
	req := rpcRequest{ID: "r1", Method: "execute_tool", Params: mustParams(t, map[string]any{
		"name": "display_html", "input": mustJSON(t, map[string]any{"code": code}),
	})}
	handleExecuteTool(enc, req)

	var resp struct {
		ID     string `json:"id"`
		Result struct {
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
			UICode  string `json:"ui_code"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ID != "r1" {
		t.Fatalf("id = %q, want r1", resp.ID)
	}
	if resp.Result.IsError {
		t.Fatalf("is_error = true, want false (content=%q)", resp.Result.Content)
	}
	if resp.Result.UICode == "" {
		t.Fatal("ui_code must be returned for valid code")
	}
	if !strings.Contains(resp.Result.UICode, "XBOT_UI.Stat") {
		t.Fatalf("ui_code must carry the full code, got %q", resp.Result.UICode)
	}
}

// TestHandleExecuteTool_InvalidCode verifies validation failures are errors.
//
// NOTE: syntax/balance validation deliberately lives in the FRONTEND (sucrase,
// the same compiler the renderer uses). A Go-side hand-written bracket counter
// was removed after it false-rejected perfectly valid code containing regex
// literals (2026-08-28: every large GenUI draft failed with depth=2). Unbalanced
// code now passes through and the frontend shows the compile error where it
// renders — the tool result itself succeeds.
func TestHandleExecuteTool_InvalidCode(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"no app", "export default function Foo(){return <div/>}"},
		// Multi-line (realistic LLM output): `return null` at line start.
		{"null render", "export default function App() {\n  return null\n}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			req := rpcRequest{ID: "r2", Method: "execute_tool", Params: mustParams(t, map[string]any{
				"name": "display_html", "input": mustJSON(t, map[string]any{"code": c.code}),
			})}
			handleExecuteTool(enc, req)
			var resp struct {
				Result struct {
					IsError bool   `json:"is_error"`
					Content string `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !resp.Result.IsError {
				t.Fatalf("expected error for %q, got content=%q", c.name, resp.Result.Content)
			}
		})
	}
}

// TestHandleExecuteTool_SyntaxLeftToFrontend verifies the plugin does NOT
// reject syntactically-questionable code (e.g. unbalanced brackets, regex
// literals) — pass-through with ui_code is the contract; the frontend sucrase
// compile is the authoritative gate.
func TestHandleExecuteTool_SyntaxLeftToFrontend(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"unbalanced passes through", "export default function App(){return <div>"},
		{"regex literal with slashes", "export default function App(){const u='https://a.b';return <div>{u.replace(/^https?:\\/\\//, '')}</div>}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			req := rpcRequest{ID: "r4", Method: "execute_tool", Params: mustParams(t, map[string]any{
				"name": "display_html", "input": mustJSON(t, map[string]any{"code": c.code}),
			})}
			handleExecuteTool(enc, req)
			var resp struct {
				Result struct {
					IsError bool   `json:"is_error"`
					Content string `json:"content"`
					UICode  string `json:"ui_code"`
				} `json:"result"`
			}
			if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.Result.IsError {
				t.Fatalf("expected pass-through for %q, got error: %s", c.name, resp.Result.Content)
			}
			if resp.Result.UICode == "" {
				t.Fatalf("expected ui_code for %q", c.name)
			}
		})
	}
}

// TestHandleExecuteTool_UnknownTool verifies unknown tools are rejected.
func TestHandleExecuteTool_UnknownTool(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	req := rpcRequest{ID: "r3", Method: "execute_tool", Params: mustParams(t, map[string]any{
		"name": "nope", "input": "{}",
	})}
	handleExecuteTool(enc, req)
	var resp struct {
		Result struct {
			IsError bool   `json:"is_error"`
			Content string `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Result.IsError || !strings.Contains(resp.Result.Content, "unknown tool") {
		t.Fatalf("expected unknown-tool error, got %+v", resp.Result)
	}
}

// TestHandleActivate verifies the activate response declares the channel provider.
func TestHandleActivate(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	handleActivate(enc)
	var resp struct {
		Result          string `json:"result"`
		ChannelProvider struct {
			Name string `json:"name"`
		} `json:"channel_provider"`
	}
	if err := json.Unmarshal(buf.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Result != "ok" {
		t.Fatalf("result = %q, want ok", resp.Result)
	}
	if resp.ChannelProvider.Name != "genui" {
		t.Fatalf("channel_provider.name = %q, want genui", resp.ChannelProvider.Name)
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func mustParams(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return b
}
