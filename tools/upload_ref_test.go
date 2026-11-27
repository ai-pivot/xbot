package tools

// Local upload references must be CONSUMABLE by the tools the model is told to
// use. The Web channel writes an upload reference into message content; with the
// default LOCAL storage backend that reference is a path on this machine.
//
// P0 user report 2026-11-27 (real session, turn 5384): the injected reference was
// the same-origin RELATIVE URL "/api/files/download?key=uploads%2F4%2F…&inline=1".
// The model copied it and failed twice:
//
//	view_image:   Error: unsupported url scheme (want http/https)
//	DownloadFile: Error: download request failed: Get "/api/files/download?…":
//	              unsupported protocol scheme ""
//
// A reference the model cannot act on is a defect regardless of who emitted it,
// so both tools must resolve the upload scheme AND plain local paths.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes encodes a real (1×1) PNG so view_image's magic-byte sniff passes.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// seedLocalUpload writes an upload into the LOCAL storage layout
// (<xbotHome>/uploads/<key>) and returns its absolute path + reference.
func seedLocalUpload(t *testing.T, key string, data []byte) (path, ref string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	path = filepath.Join(home, "uploads", key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	q := strings.ReplaceAll(key, "/", "%2F")
	return path, "/api/files/download?key=" + q + "&inline=1"
}

func TestLocalUploadRefPath_ResolvesAndRejectsTraversal(t *testing.T) {
	path, ref := seedLocalUpload(t, "uploads/4/a.png", []byte("x"))

	got, ok := LocalUploadRefPath(ref)
	if !ok || got != path {
		t.Fatalf("LocalUploadRefPath(%q) = (%q,%v), want (%q,true)", ref, got, ok, path)
	}

	// share_file publishes under the agent/ namespace of the SAME upload root —
	// the model is handed that reference too, so it must resolve just as well.
	agentPath, agentRef := seedLocalUpload(t, "agent/9f1c/chart.png", []byte("x"))
	if got, ok := LocalUploadRefPath(agentRef); !ok || got != agentPath {
		t.Fatalf("LocalUploadRefPath(%q) = (%q,%v), want (%q,true)", agentRef, got, ok, agentPath)
	}

	for _, bad := range []string{
		"/api/files/download?key=uploads%2F..%2F..%2Fetc%2Fpasswd",
		"/api/files/download?key=../etc/passwd",
		"/api/files/download?key=",
		"https://example.com/other?key=uploads%2F4%2Fa.png",
		"http://example.com/a.png",
		"/tmp/local.png",
	} {
		if p, ok := LocalUploadRefPath(bad); ok {
			t.Fatalf("LocalUploadRefPath(%q) must not resolve, got %q", bad, p)
		}
	}
}

// The exact call the model made in the incident must now succeed.
func TestViewImage_AcceptsWebUploadRef(t *testing.T) {
	_, ref := seedLocalUpload(t, "uploads/4/shown.png", pngBytes(t))
	tool := NewViewImageTool()
	args, _ := json.Marshal(map[string]string{"url": ref})
	res, err := tool.Execute(&ToolContext{Ctx: t.Context()}, string(args))
	if err != nil {
		t.Fatalf("view_image must resolve the upload reference, got error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("view_image must succeed, got %+v", res)
	}
}

func TestDownloadFile_AcceptsWebUploadRef(t *testing.T) {
	_, ref := seedLocalUpload(t, "uploads/4/lora4.jpg", []byte("JPEGDATA"))
	work := t.TempDir()
	out := filepath.Join(work, "copied.jpg")
	args, _ := json.Marshal(map[string]string{"url": ref, "output_path": "copied.jpg"})

	res, err := NewDownloadFileTool("", "").Execute(&ToolContext{Ctx: t.Context(), WorkingDir: work}, string(args))
	if err != nil {
		t.Fatalf("DownloadFile must resolve the upload reference, got error: %v", err)
	}
	if res == nil || res.IsError {
		t.Fatalf("DownloadFile must succeed, got %+v", res)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "JPEGDATA" {
		t.Fatalf("copied content = %q (err %v), want JPEGDATA", got, err)
	}
}

// A plain local path is just as valid a reference as a URL.
func TestDownloadFile_AcceptsLocalPath(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("LOCALDATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	out := filepath.Join(work, "out.bin")
	args, _ := json.Marshal(map[string]string{"url": src, "output_path": "out.bin"})

	if _, err := NewDownloadFileTool("", "").Execute(&ToolContext{Ctx: t.Context(), WorkingDir: work}, string(args)); err != nil {
		t.Fatalf("DownloadFile must accept a local path, got error: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "LOCALDATA" {
		t.Fatalf("copied content = %q, want LOCALDATA", got)
	}
}

// Content that lands in a TOOL message (user interjection / synthetic pair) is
// never multimodal-parsed, so the raw markdown reference reaches the model.
// Localizing turns it into the local path the model can actually use.
func TestLocalizeUploadImageRefs(t *testing.T) {
	path, ref := seedLocalUpload(t, "uploads/4/inject.png", pngBytes(t))
	in := "把这个也部署过去![shot.jpg](" + ref + ")，step 50 ckpt"
	got := LocalizeUploadImageRefs(in)
	if strings.Contains(got, "/api/files/download") {
		t.Fatalf("model-visible text must not keep the unconsumable web reference: %q", got)
	}
	if !strings.Contains(got, path) {
		t.Fatalf("localized text must carry the local path %q, got %q", path, got)
	}
	if !strings.Contains(got, "step 50 ckpt") {
		t.Fatalf("surrounding text must be preserved: %q", got)
	}
	// Non-upload images (real http URLs) stay untouched.
	if keep := "![x](https://example.com/a.png)"; LocalizeUploadImageRefs(keep) != keep {
		t.Fatalf("http image references must not be rewritten")
	}
}
