package llm

// Image captions must only advertise references the consumer can act on.
//
// P0 user report 2026-11-27: the caption carried ref="/api/files/download?key=…"
// — a same-origin relative URL with no host/port (and a session-cookie
// requirement). The model copied it and failed: view_image "unsupported url
// scheme (want http/https)", DownloadFile `unsupported protocol scheme ""`.
// Once the resolver supplies the LOCAL PATH the path is all the model needs.

import (
	"context"
	"strings"
	"testing"
)

// pathResolver is an ImageResolver that also implements localPathProvider.
type pathResolver struct {
	dataURLs map[string]string
	locals   map[string]string
}

func (r *pathResolver) ResolveImage(_ context.Context, ref string) (string, error) {
	if u, ok := r.dataURLs[ref]; ok {
		return u, nil
	}
	return "data:image/png;base64,RESOLVED", nil
}

func (r *pathResolver) LocalPath(ref string) (string, bool) {
	p, ok := r.locals[ref]
	return p, ok
}

func captionText(parts []imageContentPart) string {
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func TestImageCaption_DropsUnconsumableRelativeRef(t *testing.T) {
	const ref = "/api/files/download?key=uploads%2F4%2Fa.png&inline=1"
	const local = "/home/u/.xbot/uploads/uploads/4/a.png"
	r := &pathResolver{locals: map[string]string{ref: local}}
	mc := &MultimodalConfig{ImageResolver: r, VisionEnabled: true}

	got := captionText(parseMultimodalContent(context.TODO(), "look ![shot]("+ref+")", mc))
	if strings.Contains(got, "/api/files/download") {
		t.Fatalf("caption must not advertise a reference the model cannot use: %q", got)
	}
	if !strings.Contains(got, local) {
		t.Fatalf("caption must carry the local path %q, got %q", local, got)
	}
}

func TestImageCaption_KeepsFetchableHTTPRef(t *testing.T) {
	const ref = "https://cdn.example.com/a.png"
	const local = "/tmp/a.png"
	r := &pathResolver{locals: map[string]string{ref: local}}
	mc := &MultimodalConfig{ImageResolver: r, VisionEnabled: true}

	got := captionText(parseMultimodalContent(context.TODO(), "look ![shot]("+ref+")", mc))
	if !strings.Contains(got, ref) {
		t.Fatalf("an absolute http(s) reference IS usable and must be kept, got %q", got)
	}
}

func TestImagePlaceholder_DropsUnconsumableRelativeRef(t *testing.T) {
	const ref = "/api/files/download?key=uploads%2F4%2Fa.png&inline=1"
	const local = "/home/u/.xbot/uploads/uploads/4/a.png"
	r := &pathResolver{locals: map[string]string{ref: local}}
	// Vision OFF: the degrade path must be just as usable.
	mc := &MultimodalConfig{ImageResolver: r}

	got := captionText(parseMultimodalContent(context.TODO(), "look ![shot]("+ref+")", mc))
	if strings.Contains(got, "/api/files/download") {
		t.Fatalf("placeholder must not advertise an unusable reference: %q", got)
	}
	if !strings.Contains(got, local) {
		t.Fatalf("placeholder must carry the local path %q, got %q", local, got)
	}
}
