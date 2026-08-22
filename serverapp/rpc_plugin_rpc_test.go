package serverapp

import (
	"context"
	"testing"

	"xbot/plugin"
)

// fakePluginManager exposes only what resolvePluginRPCMethod needs.
type fakePluginManagerForRPC struct {
	entries []*plugin.PluginEntry
}

func (f *fakePluginManagerForRPC) ListPlugins() []*plugin.PluginEntry { return f.entries }
func (f *fakePluginManagerForRPC) IsPluginActive(id string) bool {
	for _, e := range f.entries {
		if e.Manifest.ID == id && e.State == plugin.StateActive {
			return true
		}
	}
	return false
}

func activeEntry(id string) *plugin.PluginEntry {
	return &plugin.PluginEntry{Manifest: &plugin.PluginManifest{ID: id}, State: plugin.StateActive}
}

func TestResolvePluginRPCMethod(t *testing.T) {
	pm := &fakePluginManagerForRPC{
		entries: []*plugin.PluginEntry{
			activeEntry("xbot.git-fancy"),
			activeEntry("xbot.iteration-stats"),
		},
	}
	// pluginID contains dots: must NOT split into "xbot".
	pid, method, err := resolvePluginRPCMethod(pm, "xbot.git-fancy.status")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != "xbot.git-fancy" {
		t.Fatalf("pluginID = %q, want %q (must not be split at the first dot)", pid, "xbot.git-fancy")
	}
	if method != "status" {
		t.Fatalf("method = %q, want %q", method, "status")
	}
}

func TestResolvePluginRPCMethodSubDots(t *testing.T) {
	pm := &fakePluginManagerForRPC{entries: []*plugin.PluginEntry{activeEntry("xbot.git-fancy")}}
	// sub-method itself may contain dots — the remainder after the pluginID prefix is kept whole.
	pid, method, err := resolvePluginRPCMethod(pm, "xbot.git-fancy.log.extra")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid != "xbot.git-fancy" || method != "log.extra" {
		t.Fatalf("got %q + %q, want xbot.git-fancy + log.extra", pid, method)
	}
}

func TestResolvePluginRPCMethodInactive(t *testing.T) {
	pm := &fakePluginManagerForRPC{
		entries: []*plugin.PluginEntry{
			{Manifest: &plugin.PluginManifest{ID: "xbot.git-fancy"}, State: plugin.StateInactive},
		},
	}
	if _, _, err := resolvePluginRPCMethod(pm, "xbot.git-fancy.status"); err == nil {
		t.Fatal("expected error for inactive plugin")
	}
}

func TestResolvePluginRPCMethodUnknown(t *testing.T) {
	pm := &fakePluginManagerForRPC{entries: []*plugin.PluginEntry{activeEntry("xbot.git-fancy")}}
	if _, _, err := resolvePluginRPCMethod(pm, "nonexistent.method"); err == nil {
		t.Fatal("expected error for unknown plugin")
	}
}

var _ = context.Background
