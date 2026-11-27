package tools

// share_file returns ONE reference that serves two consumers: the browser (it is
// embedded in the reply as markdown) and the model (the tool RESULT). The former
// needs the same-origin URL; the latter needs something it can act on — a
// same-origin "/api/files/download?key=…" URL is unfetchable for every tool
// (P0 user report 2026-11-27).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubSharer returns a fixed web reference without touching any storage.
type stubSharer struct{ url string }

func (s stubSharer) ShareFile(string, string) (string, error) { return s.url, nil }

func TestShareFile_SummaryNamesLocalPathResultKeepsEmbedURL(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)

	key := "agent/9f1c/chart.png"
	local := filepath.Join(home, "uploads", key)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "/api/files/download?key=agent%2F9f1c%2Fchart.png&inline=1"

	src := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(src, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": src})

	res, err := NewShareFileTool(stubSharer{url: url}).Execute(&ToolContext{WorkingDir: filepath.Dir(src)}, string(args))
	if err != nil {
		t.Fatalf("share_file: %v", err)
	}

	// The RESULT the model reads must carry the usable local path...
	if !strings.Contains(res.Summary, local) {
		t.Fatalf("summary must name the local file %q, got %q", local, res.Summary)
	}
	// ...while the embed markdown keeps the browser-facing reference.
	if !strings.Contains(res.Tips, "](/api/files/download?key=agent%2F9f1c%2Fchart.png&inline=1)") {
		t.Fatalf("tips must keep the browser-facing embed markdown, got %q", res.Tips)
	}
}
