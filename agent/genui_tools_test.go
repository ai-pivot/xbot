package agent

import (
	"encoding/json"
	"testing"

	"xbot/llm"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/tools"
)

// TestHandleChannelTools_MultiChannel verifies that a channel_tools declaration
// with explicit `channels` registers the tool to EACH listed channel, and that
// tools without `channels` fall back to the plugin's own channel name.
func TestHandleChannelTools_MultiChannel(t *testing.T) {
	reg := tools.NewRegistry()
	pio := newMockProcessIO()
	tp := NewChannelPluginTransportWithIO("genui", pio, nil, make(chan protocol.WSMessage, 4))
	tp.registry = reg

	raw := json.RawMessage(`{"tools":[
		{"name":"display_html","description":"genui","parameters":[],
		 "channels":["web","feishu"],
		 "ui":{"mode":"genui","param":"code","libs":["echarts"]}},
		{"name":"own_tool","description":"plugin own","parameters":[]}
	]}`)
	tp.handleChannelTools(raw)

	// display_html must be visible in BOTH web and feishu sessions.
	for _, ch := range []string{"web", "feishu"} {
		tool, ok := reg.GetForSession("display_html", 0, ch+":chat-1")
		if !ok {
			t.Fatalf("display_html not registered for channel %q", ch)
		}
		// UIDecl must survive registration (metadata-driven lookup).
		p, ok := tool.(tools.UIDeclProvider)
		if !ok {
			t.Fatalf("registered tool for %q must implement UIDeclProvider", ch)
		}
		if ui := p.UIDecl(); ui == nil || ui.Mode != "genui" || ui.Param != "code" {
			t.Fatalf("UIDecl = %+v, want genui/code", ui)
		}
	}

	// own_tool: no channels → plugin's own channel only.
	if _, ok := reg.GetForSession("own_tool", 0, "genui:chat-1"); !ok {
		t.Fatal("own_tool must be registered to plugin's own channel 'genui'")
	}
	if _, ok := reg.GetForSession("own_tool", 0, "web:chat-1"); ok {
		t.Fatal("own_tool must NOT be registered to 'web' (no channels declared)")
	}
}

// TestHandleChannelTools_EmptyTools clears previous registrations (hot-update).
func TestHandleChannelTools_HotUpdate(t *testing.T) {
	reg := tools.NewRegistry()
	pio := newMockProcessIO()
	tp := NewChannelPluginTransportWithIO("genui", pio, nil, make(chan protocol.WSMessage, 4))
	tp.registry = reg

	// First declaration: register display_html.
	tp.handleChannelTools(json.RawMessage(`{"tools":[
		{"name":"display_html","channels":["web"]}
	]}`))
	if _, ok := reg.GetForSession("display_html", 0, "web:chat-1"); !ok {
		t.Fatal("display_html should be registered after first declaration")
	}

	// Hot-update: replace the set (only other_tool now).
	tp.handleChannelTools(json.RawMessage(`{"tools":[
		{"name":"other_tool","channels":["web"]}
	]}`))
	if _, ok := reg.GetForSession("display_html", 0, "web:chat-1"); ok {
		t.Fatal("display_html must be unregistered on hot-update")
	}
	if _, ok := reg.GetForSession("other_tool", 0, "web:chat-1"); !ok {
		t.Fatal("other_tool should be registered after hot-update")
	}
}

// TestExtractPartialParam verifies generic field extraction from partial JSON,
// including escaped values and incomplete (streaming) input.
func TestExtractPartialParam(t *testing.T) {
	cases := []struct {
		name  string
		args  string
		param string
		want  string
	}{
		{"complete code", `{"code":"<div>hi</div>"}`, "code", "<div>hi</div>"},
		{"partial code", `{"code":"<div cla`, "code", "<div cla"},
		{"custom param", `{"tsx":"export default","other":1}`, "tsx", "export default"},
		{"absent field", `{"name":"x"}`, "code", ""},
		{"escaped quotes", `{"code":"say \"hi\""}`, "code", `say "hi"`},
		// JSON `\\n` decodes to literal backslash+n (NOT newline). Raw string in
		// the test keeps the double backslash as-is — the extractor unescapes it
		// to a single backslash+n.
		{"backslash escapes", `{"code":"a\\nb"}`, "code", `a\nb`},
		{"spaces around colon", `{"code" : "x"}`, "code", "x"},
		{"single quotes", `{"code":'<div>'}`, "code", "<div>"},
		{"not a string", `{"code":123}`, "code", ""},
	}
	for _, c := range cases {
		got := extractPartialParam(c.args, c.param)
		if got != c.want {
			t.Errorf("extractPartialParam(%q, %q) = %q, want %q", c.args, c.param, got, c.want)
		}
	}
}

// TestToolUIDecl_MetadataDriven verifies a.toolUIDecl resolves the UI declaration
// from the registry for a channel-registered tool, and returns nil for unknown
// or non-UI tools — the metadata-driven replacement for hardcoded tool names.
func TestToolUIDecl_MetadataDriven(t *testing.T) {
	reg := tools.NewRegistry()
	a := &Agent{tools: reg}

	// Register a UI-capable tool via channel bridge (as a channel plugin would).
	bridge := plugin.NewChannelToolBridge(plugin.ChannelToolDecl{
		Name: "render_chart",
		UI:   &tools.UIDecl{Mode: "genui", Param: "tsx", Libs: []string{"echarts"}},
	}, &fakeExecutor{})
	reg.RegisterForChannel("web", bridge)

	// Resolve via session context (web:chat-1).
	ui := a.toolUIDecl("web:chat-1", 0, "render_chart")
	if ui == nil || ui.Mode != "genui" || ui.Param != "tsx" {
		t.Fatalf("toolUIDecl = %+v, want genui/tsx", ui)
	}

	// Unknown tool → nil.
	if ui := a.toolUIDecl("web:chat-1", 0, "nope"); ui != nil {
		t.Fatalf("toolUIDecl(unknown) = %+v, want nil", ui)
	}

	// Global tool without UIDecl → nil.
	reg.Register(&plainTool{name: "plain"})
	if ui := a.toolUIDecl("web:chat-1", 0, "plain"); ui != nil {
		t.Fatalf("toolUIDecl(plain) = %+v, want nil", ui)
	}
}

// fakeExecutor satisfies plugin.ChannelToolExecutor.
type fakeExecutor struct{}

func (f *fakeExecutor) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	return json.Marshal(map[string]string{"content": "ok"})
}

// plainTool is a minimal tools.Tool without UIDecl.
type plainTool struct{ name string }

func (p *plainTool) Name() string        { return p.name }
func (p *plainTool) Description() string { return "plain tool" }
func (p *plainTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{{Name: "input", Type: "string"}}
}
func (p *plainTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	return &tools.ToolResult{Summary: "ok"}, nil
}
