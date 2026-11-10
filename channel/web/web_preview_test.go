package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"xbot/bus"
)

const testPreviewOrigin = "https://preview-one.vercel.app"

func previewTestChannel(t *testing.T) *WebChannel {
	t.Helper()
	t.Setenv("XBOT_PREVIEW_ORIGINS", testPreviewOrigin)
	wc := NewWebChannel(WebChannelConfig{Store: newTestStorage(t, newTestDB(t))}, bus.NewMessageBus())
	t.Cleanup(wc.Stop)
	wc.sessions["parent"] = sessionInfo{userID: 1, username: "operator", expires: time.Now().Add(time.Hour)}
	return wc
}
func previewReq(h http.Handler, method, path, origin, token string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: "parent"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func mintPreview(t *testing.T, wc *WebChannel) string {
	t.Helper()
	w := previewReq(wc.newServeMux(), "POST", "/api/preview/authorize", "http://localhost", "", `{"origin":"`+testPreviewOrigin+`"}`)
	if w.Code != 200 {
		t.Fatalf("authorize: %d %s", w.Code, w.Body)
	}
	var res struct {
		OK   bool `json:"ok"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !res.OK || !strings.HasPrefix(res.Data.Token, "xbot_preview_") {
		t.Fatalf("bad token envelope: %s", w.Body)
	}
	return res.Data.Token
}
func TestPreviewConsentAndSession(t *testing.T) {
	wc := previewTestChannel(t)
	h := wc.newServeMux()
	expires := wc.sessions["parent"].expires
	w := previewReq(wc.securityHeadersMiddleware(h), "GET", "/preview/authorize?origin="+testPreviewOrigin, "", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "FULL user workspace") || !strings.Contains(w.Body.String(), testPreviewOrigin) || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("consent: %d %s", w.Code, w.Body)
	}
	js := previewReq(h, "GET", "/preview/authorize.js", "", "", "")
	if js.Code != 200 || !strings.Contains(js.Body.String(), "/api/preview/authorize") {
		t.Fatal("missing external consent script")
	}
	token := mintPreview(t, wc)
	w = previewReq(h, "POST", "/api/preview/session", testPreviewOrigin, token, "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"user_id":1`) || !strings.Contains(w.Body.String(), `"username":"operator"`) || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("session %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != testPreviewOrigin || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("unsafe CORS")
	}
	if !wc.sessions["parent"].expires.Equal(expires) {
		t.Fatal("parent renewed")
	}
}
func TestPreviewFailClosed(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	h := wc.newServeMux()
	for _, tc := range []struct {
		path, origin, token string
		status              int
	}{
		{"/api/preview/session", testPreviewOrigin, "invalid", 401},
		{"/api/preview/session", "https://preview-one.vercel.app.attacker.example", token, 403},
		{"/api/preview/session", "", token, 403},
		{"/api/auth/login", testPreviewOrigin, token, 403},
		{"/api/auth/logout", testPreviewOrigin, token, 403},
		{"/api/preview/authorize", testPreviewOrigin, token, 403},
		{"/api/runners/create", testPreviewOrigin, token, 403},
		{"/api/preview/session?preview_token=" + token, testPreviewOrigin, "", 403},
		{"/api/files/download?preview_token=" + token, testPreviewOrigin, "", 403},
	} {
		w := previewReq(h, "POST", tc.path, tc.origin, tc.token, "{}")
		if w.Code != tc.status {
			t.Errorf("%s %s => %d want %d: %s", tc.path, tc.origin, w.Code, tc.status, w.Body)
		}
	}
	for _, origin := range []string{"https://preview-one.vercel.app:443", "null", "https://preview-one.vercel.app/", "https://preview-one.vercel.app:444"} {
		if previewAllowed(origin) {
			t.Errorf("accepted %q", origin)
		}
	}
	// Invalid bearer cannot fall back to a valid cookie, and a preview token is not a cookie.
	r := httptest.NewRequest("POST", "http://localhost/api/settings", bytes.NewBufferString("{}"))
	r.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("preview token accepted as cookie")
	}
	t.Setenv("XBOT_PREVIEW_ORIGINS", "")
	w = previewReq(h, "POST", "/api/preview/session", testPreviewOrigin, token, "{}")
	if w.Code != 403 {
		t.Fatal("disabled token accepted")
	}
	called := false
	legacy := wc.previewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	previewReq(legacy, "POST", "/api/legacy", "https://existing.example", "normal-existing-token", "{}")
	if !called {
		t.Fatal("default-disabled broke existing traffic")
	}
}
func TestPreviewCORSAndRPC(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	h := wc.newServeMux()
	// Normal gate probe: public JSON envelope without a cookie or bearer.
	probe := httptest.NewRequest(http.MethodPost, "http://localhost/api/auth/config", strings.NewReader(`{}`))
	probe.Header.Set("Origin", testPreviewOrigin)
	probe.Header.Set("Content-Type", "application/json")
	probeResponse := httptest.NewRecorder()
	h.ServeHTTP(probeResponse, probe)
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  map[string]any  `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(probeResponse.Body.Bytes(), &envelope); err != nil || probeResponse.Code != 200 || !envelope.OK || envelope.Data == nil || string(envelope.Error) != "null" {
		t.Fatalf("gate probe envelope: %d %s (%v)", probeResponse.Code, probeResponse.Body, err)
	}
	if _, ok := envelope.Data["invite_only"].(bool); !ok {
		t.Fatal("probe data missing invite_only boolean")
	}
	if probeResponse.Header().Get("Access-Control-Allow-Origin") != testPreviewOrigin {
		t.Fatal("probe missing exact origin CORS")
	}
	dispatched := false
	guarded := wc.previewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { dispatched = true }))
	denied := probe.Clone(probe.Context())
	denied.Header.Set("Origin", "https://evil.example")
	rejected := httptest.NewRecorder()
	guarded.ServeHTTP(rejected, denied)
	if rejected.Code != 403 || dispatched {
		t.Fatalf("unapproved probe reached business handler: status=%d dispatched=%v", rejected.Code, dispatched)
	}
	r := httptest.NewRequest("OPTIONS", "http://localhost/api/rpc", nil)
	r.Header.Set("Origin", testPreviewOrigin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("preflight %d", w.Code)
	}
	called := false
	wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{}`), nil
	}
	for _, method := range []string{"create_web_user", "delete_web_user", "runner_create", "restart_server", "apply_update", "export_subscriptions", "get_channel_config", "plugin_config", "web_plugin_rpc"} {
		w = previewReq(h, "POST", "/api/rpc", testPreviewOrigin, token, `{"method":"`+method+`","params":{"method":"web_plugin_rpc","params":{"method":"export_subscriptions"}}}`)
		if w.Code != 403 || called {
			t.Fatalf("escalation %s %d", method, w.Code)
		}
	}
	w = previewReq(h, "POST", "/api/rpc", testPreviewOrigin, token, `{"method":"list_sessions"}`)
	if w.Code != 200 || !called {
		t.Fatal("workspace RPC blocked")
	}
}
func TestPreviewRevocationCancelsContext(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	wc.previewMu.Lock()
	g := wc.previewGrants[token]
	wc.previewMu.Unlock()
	started := make(chan struct{})
	finished := make(chan struct{})
	h := wc.previewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(finished) }))
	go previewReq(h, "GET", "/api/stream", testPreviewOrigin, token, "")
	<-started
	w := previewReq(wc.newServeMux(), "POST", "/api/preview/revoke", testPreviewOrigin, token, "{}")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stream not canceled")
	}
	if g.ctx.Err() == nil {
		t.Fatal("grant not canceled")
	}
	token = mintPreview(t, wc)
	wc.previewMu.Lock()
	g = wc.previewGrants[token]
	wc.previewMu.Unlock()
	previewReq(wc.newServeMux(), "POST", "/api/auth/logout", "http://localhost", "", "{}")
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent logout did not cancel")
	}
}
func TestPreviewExpiryAndNoRenewal(t *testing.T) {
	wc := previewTestChannel(t)
	wc.sessions["parent"] = sessionInfo{userID: 1, expires: time.Now().Add(150 * time.Millisecond)}
	token := mintPreview(t, wc)
	wc.previewMu.Lock()
	g := wc.previewGrants[token]
	wc.previewMu.Unlock()
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expiry not canceled")
	}
	if w := previewReq(wc.newServeMux(), "POST", "/api/preview/session", testPreviewOrigin, token, "{}"); w.Code != 401 {
		t.Fatal("expired grant accepted")
	}
}
func TestPreviewWebSocketClosesOnRevoke(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	srv := httptest.NewServer(wc.newServeMux())
	defer srv.Close()
	hdr := http.Header{"Origin": []string{testPreviewOrigin}}
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?preview_token="+token, hdr)
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("dial: %v %s", err, b)
		}
		t.Fatal(err)
	}
	defer conn.Close()
	wc.revokePreviewParent("parent")
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, _, err = conn.ReadMessage()
		if err != nil {
			break
		}
	}
	if e, ok := err.(interface{ Timeout() bool }); ok && e.Timeout() {
		t.Fatal("websocket remained open after revocation")
	}
}
func TestPreviewParentDeletionCancels(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	wc.previewMu.Lock()
	g := wc.previewGrants[token]
	wc.previewMu.Unlock()
	wc.sessionsMu.Lock()
	delete(wc.sessions, "parent")
	wc.sessionsMu.Unlock()
	select {
	case <-g.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("deleted parent stream alive")
	}
}

func TestPreviewCrossOriginCannotExecuteWithoutGrant(t *testing.T) {
	wc := previewTestChannel(t)
	called := false
	h := wc.previewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	for _, origin := range []string{testPreviewOrigin, "https://evil.example", "null"} {
		w := previewReq(h, "POST", "/api/message", origin, "", `{}`)
		if called || w.Code < 400 {
			t.Fatalf("cross-origin cookie fallback executed for %s", origin)
		}
	}
	// Public ESM needs CORS; authenticated media still needs header fetch, not query.
	w := previewReq(h, "GET", "/plugins/demo/web/index.js", testPreviewOrigin, "", "")
	if !called || w.Header().Get("Access-Control-Allow-Origin") != testPreviewOrigin {
		t.Fatal("public module CORS failed")
	}
}
func TestPreviewAuthorizeRequiresCookieSameOriginJSON(t *testing.T) {
	wc := previewTestChannel(t)
	h := wc.newServeMux()
	for _, origin := range []string{"", testPreviewOrigin, "https://evil.example"} {
		w := previewReq(h, "POST", "/api/preview/authorize", origin, "", `{"origin":"`+testPreviewOrigin+`"}`)
		if w.Code < 400 {
			t.Fatalf("authorized without same-origin consent: %q", origin)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/api/preview/authorize", strings.NewReader(`{"origin":"`+testPreviewOrigin+`"}`))
	r.Header.Set("Origin", "http://localhost")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("authorized without cookie")
	}
	r.AddCookie(&http.Cookie{Name: webSessionCookieName, Value: "parent"})
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("simple POST authorized")
	}
}

func TestPreviewPublicConnectionProbe(t *testing.T) {
	wc := previewTestChannel(t)
	h := wc.newServeMux()
	w := previewReq(h, "POST", "/api/auth/config", testPreviewOrigin, "", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("probe %d %s", w.Code, w.Body)
	}
	w = previewReq(h, "POST", "/api/auth/config", "https://evil.example", "", `{}`)
	if w.Code != 403 {
		t.Fatal("unapproved probe origin")
	}
	for _, path := range []string{"/api/auth/login", "/api/auth/register", "/api/auth/logout"} {
		w = previewReq(h, "POST", path, testPreviewOrigin, "", `{}`)
		if w.Code != 403 {
			t.Fatalf("public probe bypass: %s", path)
		}
	}
}

func TestPreviewResourceHeaderUsesParentIdentity(t *testing.T) {
	wc := previewTestChannel(t)
	token := mintPreview(t, wc)
	var sender string
	var user int
	h := wc.previewMiddleware(wc.authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		sender = senderIDFromContext(r.Context())
		user = userIDFromContext(r.Context())
		w.WriteHeader(200)
	}))
	w := previewReq(h, "GET", "/api/fs/raw?path=example.png", testPreviewOrigin, token, "")
	if w.Code != 200 || sender != "web-1" || user != 1 {
		t.Fatalf("resource identity: %d %s %d", w.Code, sender, user)
	}
	wc.previewMu.Lock()
	g := wc.previewGrants[token]
	wc.previewMu.Unlock()
	if time.Until(g.expires) > 15*time.Minute || len(strings.TrimPrefix(token, "xbot_preview_")) != 64 {
		t.Fatal("grant lifetime/entropy contract")
	}
}
func TestPreviewWebSocketRPCBlocksCredentialMint(t *testing.T) {
	for _, method := range []string{"export_subscriptions", "web_plugin_rpc"} {
		t.Run(method, func(t *testing.T) {
			wc := previewTestChannel(t)
			token := mintPreview(t, wc)
			called := make(chan struct{}, 1)
			wc.callbacks.RPCHandler = func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error) {
				called <- struct{}{}
				return json.RawMessage(`{}`), nil
			}
			srv := httptest.NewServer(wc.newServeMux())
			defer srv.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws?preview_token="+token, http.Header{"Origin": []string{testPreviewOrigin}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err = conn.WriteJSON(map[string]any{"type": "rpc", "id": "escalate", "method": method, "params": map[string]any{"method": "web_plugin_rpc", "params": map[string]any{"method": "export_subscriptions"}}}); err != nil {
				t.Fatal(err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			for {
				_, raw, e := conn.ReadMessage()
				if e != nil {
					t.Fatal(e)
				}
				if strings.Contains(string(raw), "preview cannot manage administrator credentials") {
					break
				}
			}
			select {
			case <-called:
				t.Fatal("credential RPC executed")
			default:
			}
		})
	}
}

// Ordinary integration bearer headers are not preview credentials, including
// when the preview allowlist is enabled. Only the reserved prefix opts in.
func TestPreviewEnabledPreservesSameOriginIntegrationBearer(t *testing.T) {
	wc := previewTestChannel(t)
	for _, origin := range []string{"", "http://localhost"} {
		called := false
		h := wc.previewMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Header.Get("Authorization") != "Bearer existing-cli-admin-token" {
				t.Error("integration bearer altered")
			}
			if previewFromRequest(r) != nil {
				t.Error("integration became preview identity")
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		w := previewReq(h, "POST", "/api/rpc", origin, "existing-cli-admin-token", `{}`)
		if !called || w.Code != http.StatusNoContent {
			t.Fatalf("enabled preview intercepted ordinary bearer, origin=%q status=%d", origin, w.Code)
		}
	}
}

func TestPreviewRPCSecretPolicy(t *testing.T) {
	for _, method := range []string{"export_subscriptions", "get_channel_config", "plugin_config", "web_plugin_rpc", "create_web_user", "runner_create", "restart_server", "apply_update"} {
		t.Run(method, func(t *testing.T) {
			if !previewRPCBlocked(method) {
				t.Fatalf("secret/admin method %s allowed", method)
			}
		})
	}
	for _, method := range []string{"list_subscriptions", "get_default_subscription", "get_session_subscription", "get_storage_config", "list_models", "get_settings", "list_sessions"} {
		t.Run(method, func(t *testing.T) {
			if previewRPCBlocked(method) {
				t.Fatalf("masked/workspace method %s blocked", method)
			}
		})
	}
}

// XBOT_PREVIEW_ORIGINS 支持单标签通配，免得每个 Vercel 部署（主机名带 hash）都要改环境变量重启后端。
// 通配只能是 HTTPS、恰好一个 `*`、位于最左标签内、两侧都有字面量，且只匹配 [a-z0-9-]（不跨点）。
func TestPreviewOriginPatternAllowlist(t *testing.T) {
	const pattern = "https://xbot-preview-*-mattos-projects-6bf6c006.vercel.app"
	t.Setenv("XBOT_PREVIEW_ORIGINS", "http://localhost:5173, "+pattern)
	for origin, want := range map[string]bool{
		"https://xbot-preview-ouws4udwi-mattos-projects-6bf6c006.vercel.app":                 true,
		"https://xbot-preview-git-fix-markdown-a-46d8f7-mattos-projects-6bf6c006.vercel.app": true,
		"http://localhost:5173": true,
		"http://xbot-preview-ouws4udwi-mattos-projects-6bf6c006.vercel.app":       false, // scheme
		"https://xbot-preview--mattos-projects-6bf6c006.vercel.app":               false, // empty wildcard
		"https://xbot-preview-a.b-mattos-projects-6bf6c006.vercel.app":            false, // crosses a dot
		"https://evil.xbot-preview-x-mattos-projects-6bf6c006.vercel.app":         false, // extra label
		"https://xbot-preview-x-mattos-projects-6bf6c006.vercel.app.evil.example": false, // suffix
		"https://xbot-preview-x-mattos-projects-6bf6c006.vercel.app:8443":         false, // port
		"https://XBOT-PREVIEW-X-mattos-projects-6bf6c006.vercel.app":              false, // not canonical
		"https://xbot-preview-x_y-mattos-projects-6bf6c006.vercel.app":            false, // charset
		"https://other-preview-x-mattos-projects-6bf6c006.vercel.app":             false,
		"https://xbot-preview-x-other.vercel.app":                                 false,
	} {
		if got := previewAllowed(origin); got != want {
			t.Errorf("previewAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

// 过宽或畸形的通配条目整条作废（fail closed），绝不退化成放行。
func TestPreviewOriginPatternRejectsBroadEntries(t *testing.T) {
	const origin = "https://xbot-preview-abc-team.vercel.app"
	for _, entry := range []string{
		"*",
		"https://*",
		"https://*.vercel.app",                    // whole label wildcard: any Vercel user
		"https://*-team.vercel.app",               // no literal prefix
		"https://xbot-preview-*",                  // no literal suffix / public suffix
		"https://xbot-preview-*.vercel.app",       // no literal suffix inside the label
		"https://xbot-preview-*-team.app",         // only one label after the wildcard label
		"https://xbot-preview-**-team.vercel.app", // two wildcards
		"https://xbot-preview-*-*.vercel.app",
		"https://xbot.preview-*-team.vercel.app/", // path
		"http://xbot-preview-*-team.vercel.app",   // HTTP patterns never allowed
		"https://xbot-preview-*-team.vercel.app:443",
		"https://xbot-preview-*-team.*.app",
	} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("XBOT_PREVIEW_ORIGINS", entry)
			if previewAllowed(origin) {
				t.Fatalf("entry %q must not allow %q", entry, origin)
			}
		})
	}
}

// 端到端：通配条目下同源 consent 能为匹配的部署签发 grant，grant 绑定的是**具体** origin，
// 换一个同样匹配通配的 origin 也不能复用。
func TestPreviewOriginPatternGrantBindsExactOrigin(t *testing.T) {
	wc := previewTestChannel(t)
	t.Setenv("XBOT_PREVIEW_ORIGINS", "https://preview-*-team.vercel.app")
	h := wc.newServeMux()
	const first, second = "https://preview-aaa111-team.vercel.app", "https://preview-bbb222-team.vercel.app"
	w := previewReq(h, "POST", "/api/preview/authorize", "http://localhost", "", `{"origin":"`+first+`"}`)
	if w.Code != 200 {
		t.Fatalf("authorize matching origin: %d %s", w.Code, w.Body)
	}
	var res struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || res.Data.Token == "" {
		t.Fatalf("bad envelope: %s", w.Body)
	}
	if w := previewReq(h, "POST", "/api/preview/session", first, res.Data.Token, `{}`); w.Code != 200 {
		t.Fatalf("session from bound origin: %d %s", w.Code, w.Body)
	}
	if w := previewReq(h, "POST", "/api/preview/session", second, res.Data.Token, `{}`); w.Code == 200 {
		t.Fatalf("grant reused from another matching origin: %d %s", w.Code, w.Body)
	}
	if w := previewReq(h, "POST", "/api/preview/authorize", "http://localhost", "", `{"origin":"https://preview-x-other.vercel.app"}`); w.Code != 403 {
		t.Fatalf("non-matching origin authorized: %d %s", w.Code, w.Body)
	}
}
