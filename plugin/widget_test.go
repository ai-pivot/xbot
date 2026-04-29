package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateUIContributions(t *testing.T) {
	tests := []struct {
		name        string
		ui          []UISlotContribution
		permissions []string
		wantErr     bool
		errContains string
	}{
		{
			name:        "empty contributions",
			ui:          nil,
			permissions: []string{PermUIContribute},
			wantErr:     false,
		},
		{
			name:        "valid single widget",
			ui:          []UISlotContribution{{ID: "test-widget", Slot: "statusBarRight", Priority: 10}},
			permissions: []string{PermUIContribute},
			wantErr:     false,
		},
		{
			name:        "valid with wildcard permission",
			ui:          []UISlotContribution{{ID: "w", Slot: "titleBarLeft"}},
			permissions: []string{"*"},
			wantErr:     false,
		},
		{
			name:        "missing ui.contribute permission",
			ui:          []UISlotContribution{{ID: "w", Slot: "statusBarLeft"}},
			permissions: []string{PermHooksSubscribe},
			wantErr:     true,
			errContains: "ui.contribute",
		},
		{
			name: "missing ID",
			ui: []UISlotContribution{
				{Slot: "infoBar"},
			},
			permissions: []string{PermUIContribute},
			wantErr:     true,
			errContains: "id is required",
		},
		{
			name: "invalid slot",
			ui: []UISlotContribution{
				{ID: "w", Slot: "invalidSlot"},
			},
			permissions: []string{PermUIContribute},
			wantErr:     true,
			errContains: "unknown slot",
		},
		{
			name: "duplicate ID",
			ui: []UISlotContribution{
				{ID: "same", Slot: "titleBarLeft"},
				{ID: "same", Slot: "titleBarRight"},
			},
			permissions: []string{PermUIContribute},
			wantErr:     true,
			errContains: "duplicate",
		},
		{
			name:        "exceeds max widgets",
			ui:          makeUIContributions(11),
			permissions: []string{PermUIContribute},
			wantErr:     true,
			errContains: "maximum 10",
		},
		{
			name:        "exactly 10 widgets ok",
			ui:          makeUIContributions(10),
			permissions: []string{PermUIContribute},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUIContributions(tt.ui, tt.permissions)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
				} else if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing %q, got %v", tt.errContains, err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func makeUIContributions(n int) []UISlotContribution {
	slots := []string{"titleBarLeft", "titleBarRight", "statusBarLeft", "statusBarRight", "infoBar", "footer"}
	result := make([]UISlotContribution, n)
	for i := 0; i < n; i++ {
		result[i] = UISlotContribution{
			ID:       string(rune('a'+byte(i%26))) + string(rune('0'+byte(i/26))),
			Slot:     slots[i%len(slots)],
			Priority: i * 10,
		}
	}
	return result
}

// TestWidgetRegistry exercises the basic widget lifecycle.
func TestWidgetRegistry_Basic(t *testing.T) {
	r := NewWidgetRegistry()

	// Mock widget provider
	type mockWidget struct {
		text string
	}
	w := &mockWidget{text: "hello"}
	_ = w // used below

	// Register
	err := r.Register("test-plugin", "widget1", "statusBarRight", &staticWidget{"hello"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count() != 1 {
		t.Errorf("expected 1 widget, got %d", r.Count())
	}

	// Duplicate register should fail
	err = r.Register("test-plugin", "widget1", "statusBarRight", &staticWidget{"dup"}, 10)
	if err == nil {
		t.Error("expected duplicate registration error")
	}

	// Invalid slot should fail
	err = r.Register("test-plugin", "widget2", "invalid", &staticWidget{"x"}, 10)
	if err == nil {
		t.Error("expected invalid slot error")
	}

	// Render with no render function → plain text
	err = r.RefreshWidget("test-plugin", "widget1", 40, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := r.RenderZone("statusBarRight")
	if out != "hello" {
		t.Errorf("expected 'hello', got %q", out)
	}

	// Unregister
	r.Unregister("test-plugin", "widget1")
	if r.Count() != 0 {
		t.Errorf("expected 0 widgets, got %d", r.Count())
	}
	out = r.RenderZone("statusBarRight")
	if out != "" {
		t.Errorf("expected empty after unregister, got %q", out)
	}
}

// TestWidgetRegistry_RenderFunc tests render function application.
func TestWidgetRegistry_RenderFunc(t *testing.T) {
	r := NewWidgetRegistry()
	r.Register("test", "w1", "titleBarLeft", &staticWidget{"test"}, 10)

	// Set a custom render function that wraps text in brackets
	r.SetDefaultRenderFn(func(spans []WidgetSpan, width int) string {
		var s string
		for _, sp := range spans {
			s += "[" + sp.Text + "]"
		}
		return s
	})

	err := r.RefreshWidget("test", "w1", 40, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := r.RenderZone("titleBarLeft")
	if out != "[test]" {
		t.Errorf("expected '[test]', got %q", out)
	}
}

// TestWidgetRegistry_MultiZone tests that widgets are grouped by zone correctly.
func TestWidgetRegistry_MultiZone(t *testing.T) {
	r := NewWidgetRegistry()
	r.Register("p", "a", "statusBarLeft", &staticWidget{"L"}, 10)
	r.Register("p", "b", "statusBarRight", &staticWidget{"R"}, 10)
	r.Register("p", "c", "statusBarLeft", &staticWidget{"L2"}, 20)

	r.RefreshAllWidgets(40, nil)

	// statusBarLeft should have both L (priority 10) and L2 (priority 20)
	left := r.RenderZone("statusBarLeft")
	if left != "L  L2" { // joined with "  " separator
		t.Errorf("expected 'L  L2', got %q", left)
	}

	right := r.RenderZone("statusBarRight")
	if right != "R" {
		t.Errorf("expected 'R', got %q", right)
	}

	// Unknown zone should be empty
	if out := r.RenderZone("infoBar"); out != "" {
		t.Errorf("expected empty for infoBar, got %q", out)
	}
}

// TestWidgetRegistry_UnregisterAll tests mass cleanup.
func TestWidgetRegistry_UnregisterAll(t *testing.T) {
	r := NewWidgetRegistry()
	r.Register("p1", "a", "statusBarLeft", &staticWidget{"x"}, 10)
	r.Register("p1", "b", "statusBarRight", &staticWidget{"y"}, 10)
	r.Register("p2", "c", "infoBar", &staticWidget{"z"}, 10)
	if r.Count() != 3 {
		t.Errorf("expected 3 widgets, got %d", r.Count())
	}
	r.UnregisterAll("p1")
	if r.Count() != 1 {
		t.Errorf("expected 1 widget after unregistering p1, got %d", r.Count())
	}
}

// TestWidgetRegistry_WidgetInfo tests the info listing.
func TestWidgetRegistry_WidgetInfo(t *testing.T) {
	r := NewWidgetRegistry()
	r.Register("p1", "w1", "statusBarRight", &staticWidget{"x"}, 5)
	r.Register("p1", "w2", "titleBarLeft", &staticWidget{"y"}, 10)

	infos := r.WidgetInfo()
	if len(infos) != 2 {
		t.Fatalf("expected 2 infos, got %d", len(infos))
	}
	// Sorted by zone then priority
	if infos[0].Zone != "statusBarRight" || infos[1].Zone != "titleBarLeft" {
		t.Errorf("unexpected order: %+v, %+v", infos[0], infos[1])
	}
}

// staticWidget is a simple widget that always returns the same text.
type staticWidget struct {
	text string
}

func (w *staticWidget) Render(width int) []WidgetSpan {
	return []WidgetSpan{{Text: w.text, Style: StyleNormal}}
}

// TestScriptPluginE2E tests the full lifecycle: discover → activate → widget → render.
func TestScriptPluginE2E(t *testing.T) {
	tmpHome, err := os.MkdirTemp("", "xbot-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpHome) })

	pluginsDir := filepath.Join(tmpHome, "plugins")
	pluginDir := filepath.Join(pluginsDir, "git-info")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write plugin.json with script runtime + UI contribution
	writeTestManifest(t, pluginDir, &PluginManifest{
		ID:      "git-info",
		Name:    "git-info",
		Version: "1.0.0",
		Runtime: RuntimeScript,
		Entry:   "echo ok",
		Permissions: []string{
			PermUIContribute,
		},
		Contributes: &PluginContributes{
			UI: []UISlotContribution{
				{ID: "git-branch", Slot: "infoBar", Priority: 10, RefreshInterval: "1h"},
			},
		},
	})

	// Create PluginManager
	pm := NewPluginManager(tmpHome)
	pm.SetRuntimeFactory(NewCompositeRuntimeFactory())

	// Discover
	discovered, err := pm.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	if discovered != 1 {
		t.Fatalf("expected 1 discovered plugin, got %d", discovered)
	}

	// Activate
	if err := pm.ActivateAll(context.Background()); err != nil {
		t.Fatalf("ActivateAll failed: %v", err)
	}
	if pm.ActiveCount() != 1 {
		t.Fatalf("expected 1 active plugin, got %d", pm.ActiveCount())
	}

	// Wait for async script execution to complete
	time.Sleep(100 * time.Millisecond)

	// Check WidgetRegistry
	wr := pm.WidgetRegistry()
	if wr.Count() != 1 {
		t.Fatalf("expected 1 widget, got %d", wr.Count())
	}

	// Refresh widget and verify rendered content
	wr.RefreshAllWidgets(40, nil)
	content := wr.RenderZone("infoBar")
	t.Logf("infoBar widget content: %q", content)
	if content == "" {
		t.Error("infoBar widget content is empty after activation")
	}
}

// TestScriptPluginDiscoveryOnly tests that manifest with script runtime is loadable.
func TestScriptPluginDiscoveryOnly(t *testing.T) {
	tmpHome := t.TempDir()
	pluginsDir := filepath.Join(tmpHome, "plugins")
	pluginDir := filepath.Join(pluginsDir, "test-script")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTestManifest(t, pluginDir, &PluginManifest{
		ID:      "test-script",
		Name:    "test-script",
		Version: "1.0.0",
		Runtime: RuntimeScript,
		Entry:   "echo hi",
	})

	// LoadManifest should accept script runtime now
	loaded, err := LoadManifest(pluginDir)
	if err != nil {
		t.Fatalf("LoadManifest with RuntimeScript failed: %v", err)
	}
	if loaded.Runtime != RuntimeScript {
		t.Errorf("expected runtime=script, got %q", loaded.Runtime)
	}
}
