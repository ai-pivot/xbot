package plugin

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

// TestPluginConfigStore_SubscribeNotifies verifies that Subscribe callbacks fire
// after Update/Save, and that Update no longer deadlocks by taking an RLock
// while it still holds the write lock (the Go sync.RWMutex writer-reentry bug
// that previously hung the plugin test suite).
func TestPluginConfigStore_SubscribeNotifies(t *testing.T) {
	cs := NewPluginConfigStore(t.TempDir())
	id := "test.plugin"

	called := make(chan map[string]any, 1)
	unsub := cs.Subscribe(id, func(cfg map[string]any) { called <- cfg })
	defer unsub()

	// Update must notify AFTER releasing the write lock — a deadlock here
	// would hang the test and hit the timeout.
	if err := cs.Update(id, "a", "1"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	select {
	case cfg := <-called:
		if cfg["a"] != "1" {
			t.Fatalf("expected a=1 after Update, got %v", cfg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Update did not notify subscriber (deadlock?)")
	}

	if err := cs.Save(id, map[string]any{"b": 2}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	select {
	case cfg := <-called:
		// Save notifies with the raw map passed in, so b is int(2) — unlike
		// Update which round-trips through JSON (float64). Accept both.
		v, ok := cfg["b"]
		if !ok {
			t.Fatalf("expected b after Save, got %v", cfg)
		}
		switch n := v.(type) {
		case int:
			if n != 2 {
				t.Fatalf("expected b=2, got %v", n)
			}
		case float64:
			if n != 2 {
				t.Fatalf("expected b=2, got %v", n)
			}
		default:
			t.Fatalf("unexpected type %T for b", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Save did not notify subscriber (deadlock?)")
	}
}

// TestPluginConfigStore_UnsubscribeStopsNotify verifies Unsubscribe stops future
// notifications.
func TestPluginConfigStore_UnsubscribeStopsNotify(t *testing.T) {
	cs := NewPluginConfigStore(t.TempDir())
	id := "test.plugin"
	called := make(chan struct{}, 1)
	unsub := cs.Subscribe(id, func(map[string]any) { called <- struct{}{} })

	if err := cs.Update(id, "a", "1"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("expected first notification")
	}

	unsub()
	if err := cs.Update(id, "a", "2"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	select {
	case <-called:
		t.Fatal("subscriber still notified after Unsubscribe")
	case <-time.After(200 * time.Millisecond):
		// expected: no notification after unsubscribe
	}
}

// TestConfigSchema_FromWebContribs verifies that frontend 'setting' contributions
// in web.contributes are converted to a ConfigurationContribution, and that
// non-setting contributions (e.g. views) are skipped.
func TestConfigSchema_FromWebContribs(t *testing.T) {
	m := &PluginManifest{
		ID: "xbot.test",
		Web: &WebPluginDecl{
			Contributes: json.RawMessage(`[
				{"kind":"setting","key":"mode","type":"select","label":"Mode","default":"auto","options":[{"label":"Auto","value":"auto"}]},
				{"kind":"view","id":"xbot.test.v"}
			]`),
		},
	}
	schema := ConfigSchema(m)
	if schema == nil {
		t.Fatal("expected schema from web contributions")
	}
	prop, ok := schema.Properties["mode"]
	if !ok {
		t.Fatal("expected 'mode' property")
	}
	if prop.Type != "select" {
		t.Fatalf("expected type select, got %s", prop.Type)
	}
	if prop.Label != "Mode" {
		t.Fatalf("expected label Mode, got %s", prop.Label)
	}
	if len(prop.Options) != 1 {
		t.Fatalf("expected 1 option, got %d", len(prop.Options))
	}
	if _, exists := schema.Properties["xbot.test.v"]; exists {
		t.Fatal("view contribution must not become a config property")
	}
}

// TestConfigSchema_FromGitFancyManifest loads the real xbot-git-fancy
// plugin.json (which declares contributes.configuration) and verifies
// ConfigSchema extracts its properties — confirming the settings panel has a
// data source, i.e. an empty panel is because no plugin declared config, not a
// parsing bug.
func TestConfigSchema_FromGitFancyManifest(t *testing.T) {
	// Go test cwd is the plugin package dir (/xbot/plugin); repo root is ".." (i.e. /xbot).
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	m, err := LoadManifest(filepath.Join(root, "plugins", "xbot-git-fancy"))
	if err != nil {
		t.Fatalf("LoadManifest(git-fancy) failed: %v", err)
	}
	schema := ConfigSchema(m)
	if schema == nil {
		t.Fatal("expected schema from git-fancy manifest")
	}
	prop, ok := schema.Properties["defaultLogLimit"]
	if !ok {
		t.Fatal("expected defaultLogLimit property")
	}
	if prop.Type != "number" {
		t.Fatalf("expected type number, got %s", prop.Type)
	}
	if prop.Default != float64(10) {
		t.Fatalf("expected default 10, got %v", prop.Default)
	}
	if prop.Minimum == nil || *prop.Minimum != 1 {
		t.Fatalf("expected minimum 1, got %v", prop.Minimum)
	}
	if _, ok := schema.Properties["showDiffStats"]; !ok {
		t.Fatal("expected showDiffStats property")
	}
	if prop2 := schema.Properties["showDiffStats"]; prop2.Type != "boolean" {
		t.Fatalf("expected boolean, got %s", prop2.Type)
	}
}

func TestConfigSchema_PrefersContributesConfiguration(t *testing.T) {
	m := &PluginManifest{
		ID: "xbot.test",
		Contributes: &PluginContributes{
			Configuration: &ConfigurationContribution{
				Title: "My Settings",
				Properties: map[string]ConfigProperty{
					"level": {Type: "number", Default: 5, Description: "Level"},
				},
			},
		},
		Web: &WebPluginDecl{
			Contributes: json.RawMessage(`[{"kind":"setting","key":"mode"}]`),
		},
	}
	schema := ConfigSchema(m)
	if schema == nil {
		t.Fatal("expected schema")
	}
	if schema.Title != "My Settings" {
		t.Fatalf("expected title pass-through, got %s", schema.Title)
	}
	if _, ok := schema.Properties["level"]; !ok {
		t.Fatal("expected 'level' from manifest configuration")
	}
	if _, ok := schema.Properties["mode"]; ok {
		t.Fatal("'mode' from web must be ignored when manifest configuration present")
	}
}
