package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xbot/config"
	"xbot/version"
)

// ─── deriveReleaseTag ───────────────────────────────────────────────────────

func TestDeriveReleaseTag(t *testing.T) {
	cases := []struct {
		name           string
		flag, env, ver string
		channel        string
		want           string
		wantErr        bool
	}{
		{"flag wins", "v9.9.9", "v1.0.0", "v1.2.3", "stable", "v9.9.9", false},
		{"env wins over version", "", "v8.8.8", "v1.2.3", "stable", "v8.8.8", false},
		{"nightly channel → fixed tag", "", "", "nightly-20260907-abc1234", "nightly", "nightly", false},
		{"nightly version fallback (no channel)", "", "", "nightly-20260907-abc1234", "", "nightly", false},
		{"stable tag == version", "", "", "v1.2.3", "stable", "v1.2.3", false},
		{"beta tag == version", "", "", "v1.2.3-beta.1", "beta", "v1.2.3-beta.1", false},
		{"tagged version without channel", "", "", "v0.0.23", "", "v0.0.23", false},
		{"dev build errors", "", "", "dev", "", "", true},
		{"empty version errors", "", "", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deriveReleaseTag(tc.flag, tc.env, tc.ver, tc.channel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("deriveReleaseTag(%q,%q,%q,%q) = %q, want error", tc.flag, tc.env, tc.ver, tc.channel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("deriveReleaseTag(%q,%q,%q,%q) = %q, want %q", tc.flag, tc.env, tc.ver, tc.channel, got, tc.want)
			}
		})
	}
}

// ─── parseChecksums ────────────────────────────────────────────────────────

func TestParseChecksums(t *testing.T) {
	in := "abc123  xbot-web-dist.tar.gz\ndef456  xbot-plugins-linux-amd64.tar.gz\n\nnot a checksum line\n"
	got := parseChecksums(in)
	if got["xbot-web-dist.tar.gz"] != "abc123" {
		t.Errorf("web dist checksum = %q, want abc123", got["xbot-web-dist.tar.gz"])
	}
	if got["xbot-plugins-linux-amd64.tar.gz"] != "def456" {
		t.Errorf("plugins checksum = %q, want def456", got["xbot-plugins-linux-amd64.tar.gz"])
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %d (%v)", len(got), got)
	}
}

// ─── ghMirrorURL ───────────────────────────────────────────────────────────

func TestGhMirrorURL(t *testing.T) {
	raw := "https://github.com/ai-pivot/xbot/releases/download/v1/xbot-web-dist.tar.gz"
	if got := ghMirrorURL("", raw); got != raw {
		t.Errorf("no mirror: %q != %q", got, raw)
	}
	if got := ghMirrorURL("ghfast.top", raw); got != "https://ghfast.top/"+raw {
		t.Errorf("mirror: %q", got)
	}
	// API URLs never proxied
	api := "https://api.github.com/repos/x/releases"
	if got := ghMirrorURL("ghfast.top", api); got != api {
		t.Errorf("api url must not be proxied: %q", got)
	}
}

// ─── extractTarGz / installFromTarGz ───────────────────────────────────────

// buildTarGz builds an in-memory tar.gz with the given entries
// (name → {content, mode}) for extraction tests.
func buildTarGz(t *testing.T, entries map[string]tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, e := range entries {
		if e.isDir {
			if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: int64(len(e.content)), Mode: int64(e.mode)}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type tarEntry struct {
	content string
	mode    os.FileMode
	isDir   bool
}

func TestExtractTarGz_PreservesExecModeAndLayout(t *testing.T) {
	dest := t.TempDir()
	data := buildTarGz(t, map[string]tarEntry{
		"xbot.genui/":                         {isDir: true},
		"xbot.genui/plugin.json":              {content: `{"id":"xbot.genui"}`, mode: 0o644},
		"xbot.genui/bin/genui-plugin":         {content: "binary", mode: 0o755},
		"xbot.git-fancy/bin/git-fancy-plugin": {content: "binary2", mode: 0o755},
		".xbot-version":                       {content: "v1.0.0", mode: 0o644},
	})
	if err := extractTarGz(data, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "xbot.genui", "plugin.json")); err != nil || string(b) != `{"id":"xbot.genui"}` {
		t.Fatalf("plugin.json content wrong: %q err=%v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "xbot.genui", "bin", "genui-plugin")); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" {
		// Unix permission bits round-trip through the tar header. Windows has
		// no exec bit (mode comes back as 0666) — exec semantics there are
		// resolved by plugin/runtime.go's .exe sibling logic instead.
		if fi.Mode().Perm() != 0o755 {
			t.Errorf("exec bit lost: mode=%v, want 0755", fi.Mode().Perm())
		}
	}
}

func TestExtractTarGz_RejectsTraversal(t *testing.T) {
	dest := t.TempDir()
	for _, evil := range []string{"../escape.txt", "/abs/escape.txt"} {
		data := buildTarGz(t, map[string]tarEntry{evil: {content: "x", mode: 0o644}})
		if err := extractTarGz(data, dest); err == nil {
			t.Errorf("entry %q must be rejected", evil)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "escape.txt")); err == nil {
		t.Error("traversal file escaped the dest dir")
	}
	// The absolute-path rejection is tar-semantic (raw hdr.Name "/...")
	// and must hold on Windows too, where filepath.IsAbs("/abs/x") is false
	// (no drive letter) — covered by the "/abs/escape.txt" case above.
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join(dest, "abs", "escape.txt")); err == nil {
			t.Error("absolute tar entry was joined under dest — must be rejected instead")
		}
	}
}

func TestInstallFromTarGz_SwapAndStamp(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dist")
	stamp := filepath.Join(parent, ".stamp")
	// Pre-existing old install with stale content.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "old.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := buildTarGz(t, map[string]tarEntry{"index.html": {content: "new", mode: 0o644}})
	if err := installFromTarGz(dest, data, stamp, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	// Old content replaced, new content present, stamp written.
	if _, err := os.Stat(filepath.Join(dest, "old.txt")); !os.IsNotExist(err) {
		t.Error("old dist content must be removed after swap")
	}
	if b, err := os.ReadFile(filepath.Join(dest, "index.html")); err != nil || string(b) != "new" {
		t.Fatalf("new content missing: %q err=%v", b, err)
	}
	if b, err := os.ReadFile(stamp); err != nil || strings.TrimSpace(string(b)) != "v1.0.0" {
		t.Fatalf("stamp = %q err=%v, want v1.0.0", b, err)
	}
	// No leftover staging dirs.
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".dist.new-") || strings.HasSuffix(e.Name(), ".old") {
			t.Errorf("leftover staging/old dir: %s", e.Name())
		}
	}
}

func TestInstallFromTarGz_EmptyTarballRejected(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dist")
	empty := buildTarGz(t, map[string]tarEntry{})
	if err := installFromTarGz(dest, empty, filepath.Join(parent, ".stamp"), "v1"); err == nil {
		t.Error("empty tarball must be rejected (corrupt guard)")
	}
}

// ─── scanChannelActivation / fixChannelActivationConfig ────────────────────

func writePluginManifest(t *testing.T, dir, id, channel string, enabledDefault string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"id":      id,
		"runtime": "stdio",
	}
	if channel != "" {
		manifest["contributes"] = map[string]any{
			"channelProvider": map[string]any{
				"name": channel,
				"config_schema": []map[string]any{
					{"key": "enabled", "default_value": enabledDefault},
					{"key": "other", "default_value": "x"},
				},
			},
		}
	}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanChannelActivation(t *testing.T) {
	home := t.TempDir()
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.git-fancy"), "xbot.git-fancy", "", "") // no channel provider
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.ambience"), "xbot.ambience", "", "")
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.custom"), "xbot.custom", "custom", "false") // explicit default false
	// Stray file (not a dir) must be skipped.
	if err := os.WriteFile(filepath.Join(home, "plugins", "stray.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := scanChannelActivation(home)
	if len(got) != 1 {
		t.Fatalf("scanChannelActivation = %v, want only genui (custom has default false, git-fancy/ambience have no provider)", got)
	}
	if got["genui"] != "xbot.genui" {
		t.Errorf("genui → %q, want xbot.genui", got["genui"])
	}
}

func TestFixChannelActivationConfig_SetIfMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.custom"), "xbot.custom", "custom", "true")

	// Pre-existing config: genui explicitly disabled by the user, custom absent.
	if err := os.WriteFile(config.ConfigFilePath(), []byte(`{
		"channels": {"genui": {"enabled": "false"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (custom channel added)")
	}
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		t.Fatal("config unparseable after fixup")
	}
	// set_if_missing: the user's explicit false is preserved.
	if v := cfg.Channels["genui"]["enabled"]; v != "false" {
		t.Errorf("genui enabled = %q, want \"false\" (user's explicit value must be preserved)", v)
	}
	// custom (missing) got enabled=true.
	if v := cfg.Channels["custom"]["enabled"]; v != "true" {
		t.Errorf("custom enabled = %q, want \"true\"", v)
	}

	// Idempotent: second run changes nothing.
	changed, err = fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second run must be a no-op (set_if_missing)")
	}
}

func TestFixChannelActivationConfig_CreatesConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	// No config.json at all.

	changed, err := fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true on fresh config")
	}
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil || cfg.Channels["genui"]["enabled"] != "true" {
		t.Fatalf("fresh config missing channels.genui.enabled=true: %+v", cfg)
	}
}

// ─── componentInstalled (plugins marker in subdirs) ─────────────────────────

func TestComponentInstalled_PluginsMarkerInSubdirs(t *testing.T) {
	home := t.TempDir()
	// Empty builtin dir → not installed.
	if componentInstalled(home, "plugins") {
		t.Error("empty plugins/builtin must not count as installed")
	}
	// A plugin dir WITH plugin.json → installed.
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	if !componentInstalled(home, "plugins") {
		t.Error("plugins/builtin/<id>/plugin.json must count as installed")
	}
	// Web marker: index.html at dist root.
	if componentInstalled(home, "web") {
		t.Error("no web/dist → not installed")
	}
	if err := os.MkdirAll(filepath.Join(home, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "web", "dist", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !componentInstalled(home, "web") {
		t.Error("web/dist/index.html → installed")
	}
}

// ─── resolvePluginBinary is tested in plugin/runtime_entry_test.go ─────────

func TestSetupOptions_Parse(t *testing.T) {
	// version.Version is "dev" in tests; deriveReleaseTag must refuse
	// downloads for dev builds (guidance error), which is covered above.
	// This test guards the smoke path: errSetupIncomplete is a distinct
	// sentinel so main can exit 3 on warnings.
	if !strings.Contains(errSetupIncomplete.Error(), "warnings") {
		t.Error("errSetupIncomplete sentinel unexpectedly changed")
	}
	_ = version.Version // keep import
}
