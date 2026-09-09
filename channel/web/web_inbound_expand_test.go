package web

// expandUploadKeys / appendUploadRef — the canonical image reference format.
//
// Images must expand to ONE markdown reference with a stable RELATIVE URL
// (`/api/files/download?key=…&inline=1`): the relative URL never expires
// (302 signs on serve) so persisted history renders forever, and the LLM
// vision resolver (serverapp imageResolver) resolves the same reference.
// The legacy format (absolute OSS signed URL + duplicate <image> tag) is
// GONE — signed URLs rot in history and doubled references polluted prompts.

import (
	"strings"
	"testing"

	"xbot/protocol"
)

// fixedOSSProvider for expandUploadKeys tests (GetDownloadURL returns a
// deterministic absolute URL; GetViewURL unused by the ref builder).
type expandTestOSS struct{}

func (expandTestOSS) Upload(key string, data []byte) error { return nil }
func (expandTestOSS) GetDownloadURL(key string) (string, error) {
	return "https://oss.example.com/signed/" + key + "?token=abc", nil
}
func (expandTestOSS) GetViewURL(key string) (string, error) {
	return "https://oss.example.com/inline/" + key, nil
}
func (expandTestOSS) Name() string   { return "test" }
func (expandTestOSS) Domain() string { return "" }

func newExpandUploadKeysChannel(t *testing.T) *WebChannel {
	t.Helper()
	wc := &WebChannel{ossProvider: expandTestOSS{}}
	return wc
}

func TestExpandUploadKeys_ImageSingleMarkdownReference(t *testing.T) {
	wc := newExpandUploadKeysChannel(t)
	msg := protocol.WSClientMessage{
		Content:    "analyze this",
		UploadKeys: []string{"uploads/1/abc-123.png"},
		FileNames:  []string{"chart.png"},
		FileSizes:  []int64{12345},
	}
	out := wc.expandUploadKeys(msg)

	// ONE markdown reference with the stable relative URL.
	want := "analyze this\n\n![chart.png](/api/files/download?key=uploads%2F1%2Fabc-123.png&inline=1)"
	if out != want {
		t.Fatalf("image expansion mismatch:\n got: %q\nwant: %q", out, want)
	}
	// NO legacy <image> tag.
	if strings.Contains(out, "<image") {
		t.Fatalf("legacy <image> tag must not appear: %q", out)
	}
	// NO absolute signed URL (rots in persisted history).
	if strings.Contains(out, "https://oss.example.com") {
		t.Fatalf("absolute signed URL must not leak into image references: %q", out)
	}
	// NO duplicate reference (the old format appended tag + markdown).
	if got := strings.Count(out, "![chart.png]("); got != 1 {
		t.Fatalf("exactly one image reference expected, got %d: %q", got, out)
	}
}

func TestExpandUploadKeys_NonImageAttachmentKeepsSignedURL(t *testing.T) {
	wc := newExpandUploadKeysChannel(t)
	msg := protocol.WSClientMessage{
		Content:    "see attachment",
		UploadKeys: []string{"uploads/1/report.pdf"},
		FileNames:  []string{"report.pdf"},
		FileSizes:  []int64{2048},
	}
	out := wc.expandUploadKeys(msg)
	want := "see attachment\n\n<file name=\"report.pdf\" url=\"https://oss.example.com/signed/uploads/1/report.pdf?token=abc\" size=\"2048\" />"
	if out != want {
		t.Fatalf("attachment expansion mismatch:\n got: %q\nwant: %q", out, want)
	}
	// The signed URL IS the point here — DownloadFile fetches it directly.
	if !strings.Contains(out, "https://oss.example.com/signed/") {
		t.Fatalf("attachment must carry the absolute signed URL for DownloadFile: %q", out)
	}
}

func TestExpandUploadKeys_MixedUploads(t *testing.T) {
	wc := newExpandUploadKeysChannel(t)
	msg := protocol.WSClientMessage{
		Content:    "both",
		UploadKeys: []string{"uploads/1/img.png", "uploads/1/doc.pdf"},
		FileNames:  []string{"img.png", "doc.pdf"},
		FileSizes:  []int64{100, 200},
	}
	out := wc.expandUploadKeys(msg)
	if !strings.Contains(out, "![img.png](/api/files/download?key=uploads%2F1%2Fimg.png&inline=1)") {
		t.Fatalf("image reference missing/wrong: %q", out)
	}
	if !strings.Contains(out, "<file name=\"doc.pdf\"") {
		t.Fatalf("file reference missing: %q", out)
	}
	// Image key must be URL-escaped (forward slashes → %2F) so the query
	// string round-trips through URL parsing.
	if !strings.Contains(out, "key=uploads%2F1%2Fimg.png") {
		t.Fatalf("image key must be query-escaped: %q", out)
	}
}

func TestExpandUploadKeys_NoKeysPassthrough(t *testing.T) {
	wc := newExpandUploadKeysChannel(t)
	msg := protocol.WSClientMessage{Content: "plain message"}
	if out := wc.expandUploadKeys(msg); out != "plain message" {
		t.Fatalf("no-upload message must pass through unchanged: %q", out)
	}
	// Missing provider → passthrough (uploads without OSS are rejected at
	// upload time; nothing to expand).
	wc2 := &WebChannel{}
	msg2 := protocol.WSClientMessage{Content: "x", UploadKeys: []string{"uploads/1/a.png"}, FileNames: []string{"a.png"}}
	if out := wc2.expandUploadKeys(msg2); out != "x" {
		t.Fatalf("no provider → passthrough, got %q", out)
	}
}

func TestAppendUploadRef_FilenameSanitized(t *testing.T) {
	wc := newExpandUploadKeysChannel(t)
	// displayNames come from filepath.Base already, but a bare key fallback
	// must not break the markdown (no quotes/parens injection via key).
	out := wc.appendUploadRef("", "uploads/weird key.png", "name with (parens).png", 1)
	if !strings.Contains(out, "![name with (parens).png](") {
		t.Fatalf("display name must be used verbatim in the alt: %q", out)
	}
}
