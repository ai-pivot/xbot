package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverPlugins_SkipsSearchPathContainer pins the fix for a warning that
// fired on every single startup:
//
// DefaultPluginDirs returns both <home>/plugins and <home>/plugins/builtin.
// `builtin` is a CONTAINER of built-in plugins — it has no plugin.json of its
// own — so the outer loop tried LoadManifest("…/plugins/builtin"), failed, and
// logged "Skipping invalid plugin: … no such file or directory". Its real
// contents were then found correctly by the next search path.
//
// A directory that is itself a search path must be left to its own iteration.
func TestDiscoverPlugins_SkipsSearchPathContainer(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	plugins := filepath.Join(home, "plugins")
	builtin := filepath.Join(plugins, "builtin")

	mk := func(dir, id string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestManifest(t, dir, &PluginManifest{
			ID: id, Name: id, Version: "1.0.0", Description: "test", Runtime: RuntimeNative,
		})
	}
	mk(filepath.Join(plugins, "user.one"), "user.one")
	mk(filepath.Join(builtin, "xbot.genui"), "xbot.genui")
	// builtin 自身故意不建 plugin.json —— 这就是生产环境的形态。

	got := DiscoverPlugins([]string{plugins, builtin})

	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids["user.one"] {
		t.Error("user-installed plugin was not discovered")
	}
	if !ids["xbot.genui"] {
		t.Error("built-in plugin inside the container was not discovered")
	}
	if ids["builtin"] {
		t.Error("the container directory was loaded as a plugin")
	}
}

// TestDiscoverPlugins_SkipsUnderscoreAndDot guards the pre-existing backup
// convention: the container check must not make these scannable again.
func TestDiscoverPlugins_SkipsUnderscoreAndDot(t *testing.T) {
	t.Parallel()
	plugins := filepath.Join(t.TempDir(), "plugins")
	for _, name := range []string{"_backup", ".old", "real"} {
		dir := filepath.Join(plugins, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestManifest(t, dir, &PluginManifest{
			ID: name, Name: name, Version: "1.0.0", Description: "d", Runtime: RuntimeNative,
		})
	}
	got := DiscoverPlugins([]string{plugins})
	if len(got) != 1 || got[0].ID != "real" {
		ids := make([]string, 0, len(got))
		for _, m := range got {
			ids = append(ids, m.ID)
		}
		t.Fatalf("expected only 'real' to be discovered, got %v", ids)
	}
}
