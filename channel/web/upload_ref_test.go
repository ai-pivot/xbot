package web

// uploadModelRef — the SINGLE implementation behind the model-facing attachment
// reference carried by <file> tags.
//
// Contract (P0 user report 2026-11-27): the reference must be something the
// consumer can ACT on.
//   - Local storage (default) → the absolute path on this machine. DownloadFile
//     copies it, Read/Shell open it. The old same-origin relative
//     "/api/files/download?key=…" URL had no host/port and needs a session
//     cookie: the model copied it and failed twice (view_image "unsupported url
//     scheme", DownloadFile `unsupported protocol scheme ""`).
//   - Cloud OSS → the provider's signed absolute URL.
//
// It never fails and never emits internal error text (P0 2026-09-20).

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"xbot/protocol"
)

// fileRefRe extracts the <file …> tag's name/url/size attributes.
var fileRefRe = regexp.MustCompile(`<file name="([^"]*)" url="([^"]*)" size="(\d+)" />`)

// fileRefURL returns the url attribute of the single <file …> tag in out.
func fileRefURL(t *testing.T, out string) string {
	t.Helper()
	m := fileRefRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no <file name=… url=… size=…> tag found in output: %q", out)
	}
	return m[2]
}

func expandOne(wc *WebChannel, key, name string, size int64) string {
	return wc.expandUploadKeys(protocol.WSClientMessage{
		Content:    "x",
		UploadKeys: []string{key},
		FileNames:  []string{name},
		FileSizes:  []int64{size},
	})
}

// Local storage ⇒ the file's absolute path, nothing host-derived.
func TestExpandUploadKeys_LocalStorageYieldsLocalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	wc := &WebChannel{ossProvider: NewLocalProvider(t.TempDir())}

	out := expandOne(wc, "uploads/3/15.gz", "15.gz", 7)
	ref := fileRefURL(t, out)

	if want := home + "/uploads/uploads/3/15.gz"; ref != want {
		t.Fatalf("local storage must reference the local file: want %q, got %q", want, ref)
	}
	if strings.Contains(out, "/api/files/download") {
		t.Fatalf("a relative web URL is not consumable by the model: %q", out)
	}
	if !strings.Contains(out, `name="15.gz"`) || !strings.Contains(out, `size="7"`) {
		t.Fatalf("display name/size must be preserved: %q", out)
	}
}

// No deploy-time address (configured Host/Port, PublicURL in any scheme) may end
// up in the reference — local storage produces a path, full stop.
func TestExpandUploadKeys_LocalStorageNeverLeaksServerAddress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)

	wcs := map[string]*WebChannel{
		"public_url": {ossProvider: NewLocalProvider(t.TempDir()),
			config: WebChannelConfig{PublicURL: "https://xbot.example.com/"}},
		"ws_public_url": {ossProvider: NewLocalProvider(t.TempDir()),
			config: WebChannelConfig{PublicURL: "ws://xbot.example.com:8080"}},
		"configured_bind": {ossProvider: NewLocalProvider(t.TempDir()),
			config: WebChannelConfig{Host: "0.0.0.0", Port: 8082}},
	}
	for name, wc := range wcs {
		ref := fileRefURL(t, expandOne(wc, "uploads/3/a.gz", "a.gz", 1))
		if want := home + "/uploads/uploads/3/a.gz"; ref != want {
			t.Fatalf("%s: want local path %q, got %q", name, want, ref)
		}
		for _, banned := range []string{"127.0.0.1", "0.0.0.0", "10.0.0.", "[::1]", "xbot.example.com"} {
			if strings.Contains(ref, banned) {
				t.Fatalf("%s: reference leaked a server address (%s): %q", name, banned, ref)
			}
		}
	}
}

// Cloud storage ⇒ the provider's signed absolute URL (fetchable by any consumer).
func TestExpandUploadKeys_CloudStorageYieldsSignedURL(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	wc := &WebChannel{ossProvider: signedOSS{}}
	ref := fileRefURL(t, expandOne(wc, "uploads/3/15.gz", "15.gz", 7))
	if ref != "https://cdn.example.com/signed/15.gz" {
		t.Fatalf("cloud reference must be the signed absolute URL, got %q", ref)
	}
}

// A broken signer must not produce error text (the builder never fails) and must
// fall back to the local spilled copy — a path, not a dead URL.
func TestExpandUploadKeys_CloudSignerFailureYieldsLocalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	wc := &WebChannel{ossProvider: failingOSS{}}

	out := expandOne(wc, "uploads/3/b.gz", "b.gz", 2)
	if strings.Contains(out, "获取下载链接失败") {
		t.Fatalf("internal failure text leaked: %q", out)
	}
	if want := home + "/uploads/uploads/3/b.gz"; fileRefURL(t, out) != want {
		t.Fatalf("want local path %q, got %q", want, out)
	}
}

// Images keep the markdown reference: the browser renders it inline and the
// vision resolver turns it into pixels + a local-path caption. Only the
// model-facing <file> tag changed.
func TestExpandUploadKeys_ImageKeepsMarkdownReference(t *testing.T) {
	t.Setenv("XBOT_HOME", t.TempDir())
	wc := &WebChannel{ossProvider: NewLocalProvider(t.TempDir())}

	out := wc.expandUploadKeys(protocol.WSClientMessage{
		Content:    "analyze this",
		UploadKeys: []string{"uploads/1/abc-123.png"},
		FileNames:  []string{"chart.png"},
		FileSizes:  []int64{12345},
	})
	if want := "![chart.png](/api/files/download?key=uploads%2F1%2Fabc-123.png&inline=1)"; !strings.Contains(out, want) {
		t.Fatalf("image content must keep the inline markdown reference %q, got:\n%q", want, out)
	}
}

// signedOSS is a cloud provider with a working signer.
type signedOSS struct{ expandTestOSS }

func (signedOSS) Name() string { return "qiniu" }

func (signedOSS) GetDownloadURL(string) (string, error) {
	return "https://cdn.example.com/signed/15.gz", nil
}

// failingOSS mirrors a cloud provider whose signer is broken.
type failingOSS struct{ expandTestOSS }

func (failingOSS) GetDownloadURL(string) (string, error) {
	return "", errors.New("signer down")
}
