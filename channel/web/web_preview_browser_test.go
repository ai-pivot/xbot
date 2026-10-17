package web

// Opt-in transport fixture, not a second application server. It has a temporary
// database, no agent/bus consumer, no filesystem API and no real PTY backend.
import (
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"xbot/bus"
	"xbot/protocol"
)

// All methods are harmless, including stdin and create: never inherit a local
// backend or launch a shell. A large buffered channel is unnecessary here.
type browserFixtureTerminal struct{}

func (*browserFixtureTerminal) Create(string, uint16, uint16) (string, error) {
	return "fixture-terminal", nil
}
func (*browserFixtureTerminal) WriteStdin(string, []byte) error             { return nil }
func (*browserFixtureTerminal) Resize(string, uint16, uint16) error         { return nil }
func (*browserFixtureTerminal) Close(string) error                          { return nil }
func (*browserFixtureTerminal) SetHandlers(string, func([]byte), func(int)) {}

func TestPreviewBrowserFixture(t *testing.T) {
	if os.Getenv("XBOT_PREVIEW_BROWSER_FIXTURE") != "1" {
		t.Skip("set XBOT_PREVIEW_BROWSER_FIXTURE=1 and XBOT_PREVIEW_BROWSER_ORIGIN to opt in")
	}
	origin := os.Getenv("XBOT_PREVIEW_BROWSER_ORIGIN")
	if origin == "" {
		origin = os.Getenv("XBOT_PREVIEW_ORIGINS")
	}
	u, err := url.Parse(origin)
	if err != nil || previewOrigin(origin) != origin || origin == "" || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".vercel.app") || u.Port() != "" {
		t.Fatal("fixture requires one exact HTTPS *.vercel.app origin in XBOT_PREVIEW_BROWSER_ORIGIN (or XBOT_PREVIEW_ORIGINS)")
	}
	duration := 10 * time.Minute
	if raw := os.Getenv("XBOT_PREVIEW_BROWSER_DURATION"); raw != "" {
		duration, err = time.ParseDuration(raw)
		if err != nil || duration <= 0 || duration > time.Hour {
			t.Fatal("XBOT_PREVIEW_BROWSER_DURATION must be positive and <= 1h (for example 10m)")
		}
	}
	t.Setenv("XBOT_PREVIEW_ORIGINS", origin)
	db := newTestDB(t)
	result, err := db.Exec("INSERT INTO web_users (username, password) VALUES (?, ?)", "preview-browser-fixture", "disabled-fixture-password-not-a-bcrypt-hash")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	wc := NewWebChannel(WebChannelConfig{DB: db}, bus.NewMessageBus())
	t.Cleanup(wc.Stop)
	const parent = "isolated-browser-fixture-parent"
	wc.sessions[parent] = sessionInfo{userID: int(id), username: "preview-browser-fixture", expires: time.Now().Add(duration + time.Minute)}
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		switch method {
		case "fixture_echo", "fixture_status":
			return json.Marshal(map[string]any{"fixture": true, "method": method, "params": params, "identity": identity})
		default:
			return nil, fmt.Errorf("isolated fixture: RPC method not enabled")
		}
	}
	const tid = "fixture-terminal"
	sess := &ptySession{tid: tid, backend: &browserFixtureTerminal{}, clients: make(map[*ptyWSClient]struct{}), history: []byte("isolated fixture terminal ready\r\n")}
	wc.ptyMgr.mu.Lock()
	wc.ptyMgr.sessions[tid] = sess
	wc.ptyMgr.mu.Unlock()
	mux := wc.newServeMux()
	// This wrapper only exists inside this opted-in test. The top-level local
	// bootstrap establishes a synthetic cookie; grant minting is NOT bypassed.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "127.0.0.1:18092" && r.Host != "localhost:18092" {
			http.Error(w, "fixture host required", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/fixture" {
			if r.Method != http.MethodGet || (r.Header.Get("Origin") != "" && !wc.previewSameOrigin(r)) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "open fixture directly in a local top-level tab", http.StatusForbidden)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: webSessionCookieName, Value: parent, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, "<!doctype html><title>Isolated preview fixture</title><h1>Isolated preview fixture</h1><p>No real user, model, files or shell.</p><a href=\"%s\">Authorize exact preview origin</a><p>SSE chat_id=web-%d; terminal tid=fixture-terminal; RPC fixture_echo or fixture_status.</p>", html.EscapeString("/preview/authorize?origin="+url.QueryEscape(origin)), id)
			return
		}
		// Explicitly deny all non-transport APIs: the regular mux includes shell,
		// file and settings operations which must never be exposed by this fixture.
		switch r.URL.Path {
		case "/preview/authorize", "/preview/authorize.js", "/api/preview/authorize", "/api/preview/session", "/api/preview/revoke", "/api/auth/config", "/api/rpc", "/api/sse", "/ws", "/ws/terminal":
			mux.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:18092")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	// Stop connections before waiting for httptest shutdown, including SSE/WS.
	defer func() { wc.Stop(); server.CloseClientConnections(); server.Close() }()
	fmt.Printf("READY http://127.0.0.1:18092/fixture origin=%s duration=%s chat_id=web-%d terminal=fixture-terminal\n", origin, duration, id)
	// A bounded opt-in foreground fixture; the caller owns browser interaction.
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return
		case <-ticker.C:
			// Synthetic application event proves real SSE frames/flush as well
			// as handshake; broadcast never invokes a model or bus consumer.
			payload, _ := json.Marshal(map[string]any{"fixture": true, "tick": true})
			wc.hub.broadcastSessionState("web", fmt.Sprintf("web-%d", id), protocol.WSMessage{Type: "fixture_tick", Result: payload})
			sess.broadcast([]byte("fixture tick\r\n"))
		}
	}
}
