package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Exercise the real terminal handler and both websocket pumps without launching
// a platform-dependent shell. Resize is an acknowledgement from the read pump.
type previewTerminalBackend struct {
	PtyBackend
	resized chan struct{}
}

func (b *previewTerminalBackend) Resize(string, uint16, uint16) error {
	b.resized <- struct{}{}
	return nil
}
func (b *previewTerminalBackend) Close(string) error { return nil }

func TestPreviewTerminalWebSocketRevoke(t *testing.T) {
	for _, preview := range []bool{true, false} {
		name := "cookie"
		if preview {
			name = "preview"
		}
		t.Run(name, func(t *testing.T) {
			wc := previewTestChannel(t)
			token := mintPreview(t, wc)
			backend := &previewTerminalBackend{resized: make(chan struct{}, 1)}
			sess := &ptySession{tid: "test-terminal", backend: backend, clients: make(map[*ptyWSClient]struct{}), history: []byte("ready")}
			wc.ptyMgr.mu.Lock()
			wc.ptyMgr.sessions[sess.tid] = sess
			wc.ptyMgr.mu.Unlock()
			returned := make(chan struct{})
			mux := wc.newServeMux()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mux.ServeHTTP(w, r)
				if r.URL.Path == "/ws/terminal" {
					close(returned)
				}
			}))
			defer srv.Close()
			path := "/ws/terminal?tid=" + sess.tid
			hdr := http.Header{}
			if preview {
				path += "&preview_token=" + token
				hdr.Set("Origin", testPreviewOrigin)
			} else {
				hdr.Set("Cookie", webSessionCookieName+"=parent")
				hdr.Set("Origin", srv.URL)
			}
			conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+path, hdr)
			if err != nil {
				t.Fatalf("terminal dial: %v (response %v)", err, resp)
			}
			defer conn.Close()
			select {
			case <-returned:
			case <-time.After(time.Second):
				t.Fatal("terminal handler did not return")
			}
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var frame struct {
				Type string `json:"type"`
			}
			if err := conn.ReadJSON(&frame); err != nil || frame.Type != "stdout" {
				t.Fatalf("history: %+v, %v", frame, err)
			}
			w := previewReq(mux, "POST", "/api/preview/revoke", testPreviewOrigin, token, "{}")
			if w.Code != http.StatusOK {
				t.Fatalf("revoke: %d %s", w.Code, w.Body)
			}
			if preview {
				for {
					if _, _, err = conn.ReadMessage(); err != nil {
						break
					}
				}
				if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
					t.Fatal("terminal websocket remained open after revoke")
				}
			} else {
				// Cookie sockets must survive both handler return and unrelated revocation.
				if err := conn.WriteJSON(map[string]any{"type": "resize", "cols": 80, "rows": 24}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-backend.resized:
				case <-time.After(time.Second):
					t.Fatal("ordinary terminal read pump stopped")
				}
				sess.broadcast([]byte("still open"))
				if err := conn.ReadJSON(&frame); err != nil || frame.Type != "stdout" {
					t.Fatalf("ordinary terminal write pump stopped: %v", err)
				}
			}
		})
	}
}

func TestPreviewConfiguredPublicURLBehindHTTP(t *testing.T) {
	wc := previewTestChannel(t)
	wc.config.PublicURL = "https://bot.example/xbot/"
	h := wc.newServeMux()
	for _, tc := range []struct {
		name, host, origin string
		status             int
	}{
		{"configured HTTPS", "bot.example", "https://bot.example", 200},
		{"wrong scheme", "bot.example", "http://bot.example", 403},
		{"wrong origin host", "bot.example", "https://evil.example", 403},
		{"wrong request host", "evil.example", "https://bot.example", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/terminal/list", "/api/preview/authorize"} {
				body := `{}`
				if path == "/api/preview/authorize" {
					body = `{"origin":"` + testPreviewOrigin + `"}`
				}
				r := httptest.NewRequest("POST", "http://"+tc.host+path, strings.NewReader(body))
				r.Header.Set("Origin", tc.origin)
				r.Header.Set("Content-Type", "application/json")
				// These attacker-controlled headers must never affect the decision.
				r.Header.Set("X-Forwarded-Proto", "https")
				r.Header.Set("X-Forwarded-Host", "bot.example")
				r.Header.Set("Forwarded", "proto=https;host=bot.example")
				r.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: "parent"})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("%s: %d want %d: %s", path, w.Code, tc.status, w.Body)
				}
				if tc.status == 200 && path == "/api/preview/authorize" {
					var result struct {
						Data struct {
							Token string `json:"token"`
						} `json:"data"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !strings.HasPrefix(result.Data.Token, "xbot_preview_") {
						t.Fatalf("mint: %s", w.Body)
					}
				}
			}
		})
	}
}

func TestPreviewSameOriginIgnoresForwardedHeaders(t *testing.T) {
	wc := previewTestChannel(t)
	r := httptest.NewRequest("POST", "http://bot.example/api/preview/authorize", nil)
	r.Header.Set("Origin", "https://bot.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "bot.example")
	r.Header.Set("Forwarded", "proto=https;host=bot.example")
	if wc.previewSameOrigin(r) {
		t.Fatal("trusted forwarded headers without configured PublicURL")
	}
	r.Header.Set("Origin", "http://bot.example")
	if !wc.previewSameOrigin(r) {
		t.Fatal("ordinary HTTP same-origin rejected")
	}
}
