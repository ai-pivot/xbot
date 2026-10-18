package web

// uploadDownloadRef / serverBaseURL — the SINGLE implementation that resolves
// an upload key into the reference URL carried by <file> tags (the model-facing
// attachment reference consumed by the DownloadFile tool).
//
// P0 regression (2026-09-20 user report): with the DEFAULT local static storage
// the OSS provider cannot sign anything, so the old code appended its internal
// failure text ("（获取下载链接失败）") straight INTO the user-visible message
// content — while the file itself had uploaded fine. Local storage must build
// the reference from the server's own base URL (or degrade to the stable
// relative /api/files/download?key=… URL); it must NEVER fail.

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"xbot/protocol"
)

// fileRefRe extracts the <file …> tag's name/url/size attributes.
var fileRefRe = regexp.MustCompile(`<file name="([^"]*)" url="([^"]*)" size="(\d+)" />`)

// assertParsableUploadRef asserts the content carries a reference the model can
// actually act on: an absolute http(s) URL or the same-origin relative
// /api/files/download path, with the key URL-round-tripping. Returns the URL.
func assertParsableUploadRef(t *testing.T, out, key string) string {
	t.Helper()
	m := fileRefRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no <file name=… url=… size=…> tag found in output: %q", out)
	}
	ref := m[2]
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") &&
		!strings.HasPrefix(ref, "/api/files/download?key=") {
		t.Fatalf("reference %q must be absolute (http/https) or the relative /api/files/download path", ref)
	}
	if !strings.HasSuffix(ref, "/api/files/download?key="+queryEscapePath(key)) {
		t.Fatalf("reference %q must resolve to /api/files/download?key=<escaped>, want suffix %q",
			ref, "/api/files/download?key="+queryEscapePath(key))
	}
	return ref
}

// queryEscapePath is the escaping the reference builder uses for keys.
func queryEscapePath(key string) string {
	return url.QueryEscape(key)
}

// fakeListener pins a deterministic bind address without opening a real socket.
type fakeListener struct{ addr net.Addr }

func (f fakeListener) Accept() (net.Conn, error) { return nil, nil }
func (f fakeListener) Close() error              { return nil }
func (f fakeListener) Addr() net.Addr            { return f.addr }

// failingOSS mirrors a cloud provider whose signer is broken.
type failingOSS struct{ expandTestOSS }

func (failingOSS) GetDownloadURL(string) (string, error) {
	return "", errors.New("signer down")
}

// ── P0 repro ───────────────────────────────────────────────────────────────

// Local static storage (no signing capability) must produce a usable
// reference — the internal failure text must never reach user-visible content.
func TestExpandUploadKeys_LocalProviderNeverLeaksFailure(t *testing.T) {
	wc := &WebChannel{ossProvider: NewLocalProvider(t.TempDir())}
	msg := protocol.WSClientMessage{
		Content:    "看下这个",
		UploadKeys: []string{"uploads/3/9f6c-15.gz"},
		FileNames:  []string{"15.gz"},
		FileSizes:  []int64{15321},
	}
	out := wc.expandUploadKeys(msg)

	if strings.Contains(out, "获取下载链接失败") {
		t.Fatalf("internal failure text leaked into user-visible content: %q", out)
	}
	ref := assertParsableUploadRef(t, out, "uploads/3/9f6c-15.gz")
	// No PublicURL, no listener, Port 0 → the stable RELATIVE reference
	// (never expires, served same-origin).
	if ref != "/api/files/download?key=uploads%2F3%2F9f6c-15.gz" {
		t.Fatalf("want the stable relative reference, got %q", ref)
	}
	if !strings.Contains(out, `name="15.gz"`) || !strings.Contains(out, `size="15321"`) {
		t.Fatalf("display name/size must be preserved: %q", out)
	}
}

// Local storage + configured public URL ⇒ an ABSOLUTE reference built from the
// server's own base URL (DownloadFile needs a directly fetchable URL).
func TestExpandUploadKeys_LocalProviderAbsoluteRefFromPublicURL(t *testing.T) {
	wc := &WebChannel{
		ossProvider: NewLocalProvider(t.TempDir()),
		config:      WebChannelConfig{PublicURL: "https://xbot.example.com/"},
	}
	msg := protocol.WSClientMessage{
		Content:    "see attachment",
		UploadKeys: []string{"uploads/3/15.gz"},
		FileNames:  []string{"15.gz"},
		FileSizes:  []int64{7},
	}
	out := wc.expandUploadKeys(msg)

	if strings.Contains(out, "获取下载链接失败") {
		t.Fatalf("internal failure text leaked: %q", out)
	}
	assertParsableUploadRef(t, out, "uploads/3/15.gz")
	if want := `url="https://xbot.example.com/api/files/download?key=uploads%2F3%2F15.gz"`; !strings.Contains(out, want) {
		t.Fatalf("want absolute reference %s, got:\n%q", want, out)
	}
}

// ⛔ Regression (2026-10-12 user P0「文件和图片那里写的链接全是错的，根本下载不了」):
// without an operator-declared PublicURL an attachment reference must NEVER be
// built from this server's bind address. The old behaviour baked
// "http://127.0.0.1:<port>/api/files/download?key=…" into the persisted,
// user-visible message content — a loopback URL (or a private address) that no
// browser and no other machine can reach, while the endpoint needs a session
// cookie. Wildcard binds, explicit loopback and private hosts must all degrade
// to the same-origin relative reference instead.
func TestExpandUploadKeys_UndeclaredBindNeverLeaksIntoRef(t *testing.T) {
	for _, bind := range []string{
		"127.0.0.1:39217",
		"0.0.0.0:39217",
		"[::]:39217",
		"10.0.0.67:30100",
		"[::1]:39217",
	} {
		wc := &WebChannel{
			ossProvider: NewLocalProvider(t.TempDir()),
			listener:    fakeListener{addr: tcpAddr(t, bind)},
		}
		out := wc.expandUploadKeys(protocol.WSClientMessage{
			Content:    "x",
			UploadKeys: []string{"uploads/3/a.gz"},
			FileNames:  []string{"a.gz"},
			FileSizes:  []int64{1},
		})
		ref := assertParsableUploadRef(t, out, "uploads/3/a.gz")
		if want := "/api/files/download?key=uploads%2F3%2Fa.gz"; ref != want {
			t.Fatalf("bind %s: want the same-origin relative reference %q, got %q", bind, want, ref)
		}
		for _, banned := range []string{"127.0.0.1", "0.0.0.0", "10.0.0.67", "[::1]"} {
			if strings.Contains(ref, banned) {
				t.Fatalf("bind %s: reference leaked the bind address (%s): %q", bind, banned, ref)
			}
		}
	}
}

// A failing cloud signer must degrade to a usable reference too — never to
// error text (the reference builder cannot fail by construction).
func TestExpandUploadKeys_CloudSignerFailureDegradesWithoutErrorText(t *testing.T) {
	wc := &WebChannel{ossProvider: failingOSS{}}
	out := wc.expandUploadKeys(protocol.WSClientMessage{
		Content:    "x",
		UploadKeys: []string{"uploads/3/b.gz"},
		FileNames:  []string{"b.gz"},
		FileSizes:  []int64{2},
	})
	if strings.Contains(out, "获取下载链接失败") {
		t.Fatalf("internal failure text leaked: %q", out)
	}
	assertParsableUploadRef(t, out, "uploads/3/b.gz")
}

// The configured Host/Port is a BIND target, not a public address — before
// Start() it must not leak into the reference either.
func TestExpandUploadKeys_ConfiguredBindHostAndPortNeverLeakIntoRef(t *testing.T) {
	wc := &WebChannel{
		ossProvider: NewLocalProvider(t.TempDir()),
		config:      WebChannelConfig{Host: "0.0.0.0", Port: 8082},
	}
	out := wc.expandUploadKeys(protocol.WSClientMessage{
		Content:    "x",
		UploadKeys: []string{"uploads/3/d.gz"},
		FileNames:  []string{"d.gz"},
		FileSizes:  []int64{4},
	})
	ref := assertParsableUploadRef(t, out, "uploads/3/d.gz")
	if want := "/api/files/download?key=uploads%2F3%2Fd.gz"; ref != want {
		t.Fatalf("want the same-origin relative reference %q, got %q", want, ref)
	}
	if strings.Contains(ref, "127.0.0.1:8082") {
		t.Fatalf("reference leaked the configured bind target: %q", ref)
	}
}

// The configured PublicURL also feeds runner connect commands and may carry a
// ws:// scheme — that is NOT an HTTP base, so it must be skipped rather than
// guessed into http(s). With no other declared base the reference stays
// relative (the listener/bind address is never a substitute).
func TestExpandUploadKeys_NonHTTPSPublicURLSkipped(t *testing.T) {
	wc := &WebChannel{
		ossProvider: NewLocalProvider(t.TempDir()),
		config:      WebChannelConfig{PublicURL: "ws://xbot.example.com:8080"},
		listener:    fakeListener{addr: tcpAddr(t, "127.0.0.1:39218")},
	}
	out := wc.expandUploadKeys(protocol.WSClientMessage{
		Content:    "x",
		UploadKeys: []string{"uploads/3/c.gz"},
		FileNames:  []string{"c.gz"},
		FileSizes:  []int64{3},
	})
	ref := assertParsableUploadRef(t, out, "uploads/3/c.gz")
	if want := "/api/files/download?key=uploads%2F3%2Fc.gz"; ref != want {
		t.Fatalf("ws:// PublicURL must be skipped (and the bind address never used); want %q, got %q", want, ref)
	}
}

func tcpAddr(t *testing.T, bind string) net.Addr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", bind)
	if err != nil {
		t.Fatalf("resolve %q: %v", bind, err)
	}
	return addr
}
