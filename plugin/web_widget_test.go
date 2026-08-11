package plugin

import (
	"strings"
	"testing"
)

// stubWidget returns fixed spans regardless of render.
type stubWidget struct {
	spans []WidgetSpan
}

func (s stubWidget) Render(_ int) []WidgetSpan { return s.spans }

func TestRenderSessionWebWidgets_Structured(t *testing.T) {
	wr := NewWidgetRegistry()
	wr.Register("plugA", "w1", "statusBarLeft", stubWidget{
		spans: []WidgetSpan{{Text: "git:main", Style: StyleAccent}, {Text: " ⬆3", Style: StyleSuccess}},
	}, 10)
	wr.Register("plugA", "w2", "infoBar", stubWidget{
		spans: []WidgetSpan{{Text: "CI ok", Style: StyleSuccess}},
	}, 5)

	zones := RenderSessionWebWidgets(wr, nil, "/home/test")

	// statusBarLeft should carry structured spans, not ANSI.
	spans := zones["statusBarLeft"]
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d: %+v", len(spans), spans)
	}
	if spans[0].Text != "git:main" || spans[0].Style != string(StyleAccent) {
		t.Errorf("span[0] = %+v, want git:main/accent", spans[0])
	}
	if spans[1].Text != " ⬆3" || spans[1].Style != string(StyleSuccess) {
		t.Errorf("span[1] = %+v, want ' ⬆3'/success", spans[1])
	}
	if len(zones["infoBar"]) != 1 {
		t.Fatalf("expected 1 infoBar span, got %d", len(zones["infoBar"]))
	}
	// No ANSI escapes should ever reach the web channel.
	for z, zs := range zones {
		for _, sp := range zs {
			if strings.Contains(sp.Text, "\x1b") {
				t.Errorf("zone %q span contains ANSI escape: %q", z, sp.Text)
			}
		}
	}
}

func TestRenderSessionWebWidgets_RawSkipped(t *testing.T) {
	wr := NewWidgetRegistry()
	// StyleRaw is a terminal-escape pass-through — must be skipped for web.
	wr.Register("plugA", "w1", "footer", stubWidget{
		spans: []WidgetSpan{{Text: "\x1b[31mred\x1b[0m", Style: StyleRaw}},
	}, 10)

	zones := RenderSessionWebWidgets(wr, nil, "/home/test")
	if len(zones["footer"]) != 0 {
		t.Errorf("expected raw span to be skipped for web, got %+v", zones["footer"])
	}
}

func TestRenderSessionWebWidgets_GetCWDPassed(t *testing.T) {
	wr := NewWidgetRegistry()
	var gotCWD string
	wr.Register("plugA", "w1", "toolHint", workDirStubWidget{
		fn: func(width int, workDir string) []WidgetSpan {
			gotCWD = workDir
			return []WidgetSpan{{Text: "cwd:" + workDir}}
		},
	}, 10)

	zones := RenderSessionWebWidgets(wr, func(_ string) string { return "/custom/cwd" }, "/home/test")
	_ = zones
	if gotCWD != "/custom/cwd" {
		t.Errorf("expected WorkDirRenderer to receive '/custom/cwd', got %q", gotCWD)
	}
}

type workDirStubWidget struct {
	fn func(width int, workDir string) []WidgetSpan
}

func (w workDirStubWidget) Render(_ int) []WidgetSpan { return nil }
func (w workDirStubWidget) RenderForWorkDir(width int, workDir string) []WidgetSpan {
	return w.fn(width, workDir)
}

func TestWebUIRegistry_HotUpdate(t *testing.T) {
	reg := NewWebUIRegistry()
	reg.SetChannel("chanA", []WebUIComponent{
		{WidgetID: "ci", Slot: "right_sidebar", Component: &WebComponent{Type: "sparkline", Props: map[string]any{"data": []int{1, 2, 3}}}},
		{WidgetID: "st", Slot: "status_bar_left"},
	})
	if reg.Count() != 2 {
		t.Fatalf("expected 2 components, got %d", reg.Count())
	}
	if owner, ok := reg.Owner("ci"); !ok || owner != "chanA" {
		t.Errorf("Owner(ci) = %q, %v; want chanA, true", owner, ok)
	}

	// Invalid slot is rejected.
	reg.SetChannel("chanA", []WebUIComponent{{WidgetID: "bad", Slot: "not_a_slot"}})
	if reg.Count() != 0 {
		t.Errorf("expected invalid slot to be rejected, got %d components", reg.Count())
	}

	// Hot-update replaces the channel's previous set.
	reg.SetChannel("chanA", []WebUIComponent{{WidgetID: "new", Slot: "panel"}})
	if reg.Count() != 1 {
		t.Fatalf("expected 1 component after hot-update, got %d", reg.Count())
	}
	if _, ok := reg.Owner("ci"); ok {
		t.Errorf("old widget should be removed after hot-update")
	}

	// Channel ownership isolation: chanB components don't clash with chanA.
	reg.SetChannel("chanB", []WebUIComponent{{WidgetID: "b1", Slot: "panel"}})
	if reg.Count() != 2 {
		t.Errorf("expected 2 components after chanB add, got %d", reg.Count())
	}
	reg.RemoveChannel("chanB")
	if reg.Count() != 1 {
		t.Errorf("expected 1 component after chanB remove, got %d", reg.Count())
	}
}

func TestPluginManager_WebActionHandlerForWidget(t *testing.T) {
	pm := newTestPM(t)
	ctx := &pluginContextImpl{pluginID: "com.test.example"}
	called := ""
	err := ctx.RegisterWebActionHandler("widget1", func(action, data string) (string, error) {
		called = action + ":" + data
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("RegisterWebActionHandler: %v", err)
	}

	pm.mu.Lock()
	pm.entries["com.test.example"] = &PluginEntry{
		Manifest: &PluginManifest{ID: "com.test.example"},
		Context:  ctx,
	}
	pm.mu.Unlock()

	h, ok := pm.WebActionHandlerForWidget("widget1")
	if !ok {
		t.Fatal("expected web action handler to be found")
	}
	res, err := h("click", `{"x":1}`)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res != "ok" || called != "click:{\"x\":1}" {
		t.Errorf("handler result = %q, called = %q", res, called)
	}

	if _, ok := pm.WebActionHandlerForWidget("unknown-widget"); ok {
		t.Error("expected no handler for unknown widget")
	}
}

func TestPluginContext_RegisterWebActionHandler_Nil(t *testing.T) {
	ctx := &pluginContextImpl{pluginID: "test"}
	if err := ctx.RegisterWebActionHandler("w", nil); err == nil {
		t.Error("expected error for nil handler")
	}
}
