package tools

// view_image tool tests — path whitelist, magic-byte validation, view_images
// persistence, ImageInjection reference format (relative /api/files/viewimg/
// URL), and rejection paths (traversal, non-image, both-params).

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// viewImageIDPattern mirrors the web endpoint's viewimgIDPattern (the file the
// endpoint serves must match this charset) — duplicated for the tools test to
// assert the tool's generated ids stay compatible with the served URL.
var viewImageIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}\.(png|jpe?g|gif|webp|bmp|tiff)$`)

func makeTinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func viewImageToolCtx(workspace string) *ToolContext {
	return &ToolContext{
		Ctx:           nil,
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		Channel:       "cli",
		ChatID:        "test",
	}
}

func TestViewImage_LocalPath_StoresAndReturnsInjection(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	pngData := makeTinyPNG(t)
	src := filepath.Join(ws, "chart.png")
	if err := os.WriteFile(src, pngData, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewViewImageTool()
	res, err := tool.Execute(viewImageToolCtx(ws), `{"path": "chart.png"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Summary)
	}
	if len(res.Images) != 1 {
		t.Fatalf("expected 1 ImageInjection, got %d", len(res.Images))
	}
	inj := res.Images[0]
	// Reference format: relative /api/files/viewimg/<uuid>.png (browser +
	// LLM resolver both consume this exact form).
	if !strings.HasPrefix(inj.Ref, "/api/files/viewimg/") || !strings.HasSuffix(inj.Ref, ".png") {
		t.Fatalf("injection ref must be /api/files/viewimg/<uuid>.png, got %q", inj.Ref)
	}
	if inj.Label != "chart.png" {
		t.Fatalf("label = %q, want chart.png", inj.Label)
	}
	// The image id must be filesystem-safe (mirrors the web endpoint's
	// viewimgIDPattern charset: uuid + image ext).
	id := strings.TrimPrefix(inj.Ref, "/api/files/viewimg/")
	if !viewImageIDPattern.MatchString(id) {
		t.Fatalf("id %q does not match the endpoint pattern", id)
	}

	// The stored file must exist under ~/.xbot/view_images (config.XbotHome is
	// the test's home — the test process HOME may be anywhere, so verify via
	// the path the ref implies: viewImagesDir + id).
	stored := filepath.Join(viewImagesDirForTest(t), id)
	data, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("stored image missing: %v", err)
	}
	if !bytes.Equal(data, pngData) {
		t.Fatalf("stored image differs from source (%d vs %d bytes)", len(data), len(pngData))
	}

	// Dimensions in the summary (4×4 test image).
	if !strings.Contains(res.Summary, "4×4") {
		t.Fatalf("summary should carry dimensions: %q", res.Summary)
	}
}

func TestViewImage_PathWhitelist_RejectsOutside(t *testing.T) {
	ws := t.TempDir()
	// Write a PNG OUTSIDE the workspace (e.g. /tmp/evil.png).
	outside := filepath.Join(t.TempDir(), "evil.png")
	if err := os.WriteFile(outside, makeTinyPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewViewImageTool()
	// json.Marshal avoids hand-escaping Windows paths (C:\... breaks JSON).
	outsideArgs, _ := json.Marshal(map[string]string{"path": outside})
	_, err := tool.Execute(viewImageToolCtx(ws), string(outsideArgs))
	if err == nil {
		t.Fatal("absolute path outside workspace must be rejected")
	}
	if !strings.Contains(err.Error(), "outside the readable roots") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Traversal from inside the workspace must also be rejected.
	if _, err := tool.Execute(viewImageToolCtx(ws), `{"path": "../../etc/passwd"}`); err == nil {
		t.Fatal("traversal path must be rejected")
	}
}

func TestViewImage_NonImageRejected(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("hello, definitely not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewViewImageTool()
	res, err := tool.Execute(viewImageToolCtx(ws), `{"path": "notes.txt"}`)
	if err != nil {
		t.Fatalf("non-image is a TOOL error result, not a transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("non-image must produce IsError result: %+v", res)
	}
	if !strings.Contains(res.Summary, "not a recognized image") {
		t.Fatalf("unexpected summary: %q", res.Summary)
	}
}

func TestViewImage_ParamValidation(t *testing.T) {
	ws := t.TempDir()
	tool := NewViewImageTool()
	if _, err := tool.Execute(viewImageToolCtx(ws), `{}`); err == nil {
		t.Fatal("no params must error")
	}
	if _, err := tool.Execute(viewImageToolCtx(ws), `{"path":"a.png","url":"http://x/a.png"}`); err == nil {
		t.Fatal("path+url together must error")
	}
	if _, err := tool.Execute(viewImageToolCtx(ws), `{"url":"ftp://x/a.png"}`); err == nil {
		t.Fatal("non-http url scheme must error")
	}
}

func TestViewImage_AbsoluteInsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	png := makeTinyPNG(t)
	sub := filepath.Join(ws, "out")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "shot.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewViewImageTool()
	insideArgs, _ := json.Marshal(map[string]string{"path": filepath.Join(sub, "shot.png")})
	res, err := tool.Execute(viewImageToolCtx(ws), string(insideArgs))
	if err != nil {
		t.Fatalf("absolute path inside workspace must be allowed: %v", err)
	}
	if res.IsError || len(res.Images) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// viewImagesDirForTest resolves the real view_images dir (the tool uses
// config.XbotHome() — in tests this is the process HOME-based path).
func viewImagesDirForTest(t *testing.T) string {
	t.Helper()
	dir, err := viewImagesDir()
	if err != nil {
		t.Fatalf("viewImagesDir: %v", err)
	}
	return dir
}
