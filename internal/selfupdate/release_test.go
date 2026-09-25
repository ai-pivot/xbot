package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── DeriveReleaseTag ──────────────────────────────────────────────────────

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
			got, err := DeriveReleaseTag(tc.flag, tc.env, tc.ver, tc.channel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("DeriveReleaseTag(%q,%q,%q,%q) = %q, want error", tc.flag, tc.env, tc.ver, tc.channel, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("DeriveReleaseTag(%q,%q,%q,%q) = %q, want %q", tc.flag, tc.env, tc.ver, tc.channel, got, tc.want)
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

// ─── extractTarGz / InstallTarGz ───────────────────────────────────────────

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

func TestInstallTarGz_SwapAndStamp(t *testing.T) {
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
	if err := InstallTarGz(dest, data, stamp, "v1.0.0"); err != nil {
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

func TestInstallTarGz_EmptyTarballRejected(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dist")
	empty := buildTarGz(t, map[string]tarEntry{})
	if err := InstallTarGz(dest, empty, filepath.Join(parent, ".stamp"), "v1"); err == nil {
		t.Error("empty tarball must be rejected (corrupt guard)")
	}
}

// ─── ComponentInstalled (plugins marker in subdirs) ────────────────────────

func TestComponentInstalled_PluginsMarkerInSubdirs(t *testing.T) {
	home := t.TempDir()
	// Empty builtin dir → not installed.
	if ComponentInstalled(home, "plugins") {
		t.Error("empty plugins/builtin must not count as installed")
	}
	// A plugin dir WITH plugin.json → installed.
	writeTestPluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"))
	if !ComponentInstalled(home, "plugins") {
		t.Error("plugins/builtin/<id>/plugin.json must count as installed")
	}
	// Web marker: index.html at dist root.
	if ComponentInstalled(home, "web") {
		t.Error("no web/dist → not installed")
	}
	if err := os.MkdirAll(filepath.Join(home, "web", "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "web", "dist", "index.html"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ComponentInstalled(home, "web") {
		t.Error("web/dist/index.html → installed")
	}
}

func writeTestPluginManifest(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id":"xbot.genui"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ─── BinaryAssetName ───────────────────────────────────────────────────────

func TestBinaryAssetName(t *testing.T) {
	name := BinaryAssetName()
	wantPrefix := "xbot-cli-" + runtime.GOOS + "-" + runtime.GOARCH
	if !strings.HasPrefix(name, wantPrefix) {
		t.Errorf("BinaryAssetName() = %q, want prefix %q", name, wantPrefix)
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(name, ".exe") {
		t.Errorf("windows asset must end with .exe: %q", name)
	}
	if runtime.GOOS != "windows" && strings.HasSuffix(name, ".exe") {
		t.Errorf("non-windows asset must not end with .exe: %q", name)
	}
}
