package web

// Preview grants deliberately delegate the logged-in operator's workspace, not
// a read-only sandbox. They are separate from cookies and CLI/admin tokens.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type previewGrant struct {
	parent, origin string
	session        sessionInfo
	expires        time.Time
	ctx            context.Context
	cancel         context.CancelFunc
}
type previewContextKey struct{}

func previewOrigin(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" {
		return ""
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return ""
	}
	if strings.ContainsAny(s, "*\\\r\n\t ") || s != u.Scheme+"://"+u.Host {
		return ""
	}
	return s
}
func previewAllowed(origin string) bool {
	if previewOrigin(origin) == "" {
		return false
	}
	for _, s := range strings.Split(os.Getenv("XBOT_PREVIEW_ORIGINS"), ",") {
		if strings.TrimSpace(s) == origin {
			return true
		}
	}
	return false
}
func (wc *WebChannel) previewSameOrigin(r *http.Request) bool {
	// PublicURL is operator-configured, unlike untrusted forwarded headers.
	// It is authoritative when TLS terminates at a reverse proxy.
	if publicURL := strings.TrimSpace(wc.config.PublicURL); publicURL != "" {
		u, err := url.Parse(publicURL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
		return r.Host == u.Host && r.Header.Get("Origin") == u.Scheme+"://"+u.Host
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return r.Header.Get("Origin") == scheme+"://"+r.Host
}
func previewFromRequest(r *http.Request) *previewGrant {
	g, _ := r.Context().Value(previewContextKey{}).(*previewGrant)
	return g
}
func (wc *WebChannel) previewValid(g *previewGrant) bool {
	if g == nil || !previewAllowed(g.origin) || !time.Now().Before(g.expires) || g.ctx.Err() != nil {
		return false
	}
	wc.sessionsMu.RLock()
	si, ok := wc.sessions[g.parent]
	wc.sessionsMu.RUnlock()
	return ok && si.userID == g.session.userID && time.Now().Before(si.expires)
}
func (wc *WebChannel) revokePreviewParent(parent string) {
	wc.previewMu.Lock()
	defer wc.previewMu.Unlock()
	for token, g := range wc.previewGrants {
		if g.parent == parent {
			g.cancel()
			delete(wc.previewGrants, token)
		}
	}
}
func previewBlocked(path string) bool {
	return strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/admin") || strings.HasPrefix(path, "/api/runners/") || path == "/api/preview/authorize" || path == "/preview/authorize"
}
func (wc *WebChannel) previewMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		cross := origin != "" && !wc.previewSameOrigin(r)
		auth := r.Header.Get("Authorization")
		query := r.URL.Query().Get("preview_token")
		// Existing CLI tokens are not preview credentials. Cross-site browser traffic
		// is rejected before dispatch, including simple POSTs that need no preflight.
		if strings.TrimSpace(os.Getenv("XBOT_PREVIEW_ORIGINS")) == "" && !strings.Contains(auth, "xbot_preview_") && query == "" {
			next.ServeHTTP(w, r)
			return
		}
		if cross || strings.HasPrefix(auth, "Bearer xbot_preview_") || query != "" {
			if !previewAllowed(origin) {
				jsonErrorResponse(w, 403, "preview origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Type")
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if r.Method == http.MethodOptions {
				method := r.Header.Get("Access-Control-Request-Method")
				if method != "POST" && method != "GET" && method != "DELETE" {
					jsonErrorResponse(w, 403, "method not allowed")
					return
				}
				for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
					h = strings.ToLower(strings.TrimSpace(h))
					if h != "" && h != "authorization" && h != "content-type" && h != "accept" {
						jsonErrorResponse(w, 403, "header not allowed")
						return
					}
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
				w.WriteHeader(204)
				return
			}
			// Public plugin modules need CORS, but never receive a credential.
			if strings.HasPrefix(r.URL.Path, "/plugins/") && r.Method == "GET" && auth == "" && query == "" {
				next.ServeHTTP(w, r)
				return
			}
			// Public connection probe only: no credential acceptance or cookie auth.
			if r.URL.Path == "/api/auth/config" && r.Method == http.MethodPost && auth == "" && query == "" {
				next.ServeHTTP(w, r)
				return
			}
			if previewBlocked(r.URL.Path) {
				jsonErrorResponse(w, 403, "preview cannot manage authentication or administrator credentials")
				return
			}
			token := ""
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}
			if query != "" {
				if auth != "" || r.Method != "GET" || (r.URL.Path != "/api/sse" && r.URL.Path != "/ws" && r.URL.Path != "/ws/terminal") {
					jsonErrorResponse(w, 403, "query credential not allowed")
					return
				}
				token = query
			}
			wc.previewMu.Lock()
			g := wc.previewGrants[token]
			wc.previewMu.Unlock()
			if !wc.previewValid(g) || g.origin != origin {
				jsonErrorResponse(w, 401, "invalid or expired preview grant")
				return
			}
			if r.URL.Query().Get("client_type") == "cli" || r.URL.Query().Get("token") != "" {
				jsonErrorResponse(w, 403, "preview is not a CLI credential")
				return
			}
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			stop := context.AfterFunc(g.ctx, cancel)
			defer stop()
			ctx = context.WithValue(ctx, previewContextKey{}, g)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (wc *WebChannel) handlePreviewSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonErrorResponse(w, 405, "method not allowed")
		return
	}
	g := previewFromRequest(r)
	if g == nil {
		jsonErrorResponse(w, 401, "preview credential required")
		return
	}
	writeJSON(w, 200, map[string]any{"user_id": g.session.userID, "username": g.session.username, "origin": g.origin, "expires_at": g.expires.UTC().Format(time.RFC3339)})
}
func (wc *WebChannel) handlePreviewRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonErrorResponse(w, 405, "method not allowed")
		return
	}
	g := previewFromRequest(r)
	if g == nil {
		jsonErrorResponse(w, 401, "preview credential required")
		return
	}
	wc.previewMu.Lock()
	for token, grant := range wc.previewGrants {
		if grant == g {
			g.cancel()
			delete(wc.previewGrants, token)
		}
	}
	wc.previewMu.Unlock()
	writeJSON(w, 200, map[string]any{})
}

const previewConsentHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Authorize preview</title></head><body><h1>Authorize preview workspace access</h1><p>Account: {{.Username}}</p><p>Preview origin: <strong>{{.Origin}}</strong></p><p>Warning: this preview's code receives FULL user workspace capabilities, including files, commands, terminal and settings. This is not read-only. Trust its code before approving. HTTP localhost transport is unencrypted. Grant expires in 15 minutes or when the parent session ends.</p><button id="approve">I trust this exact origin — generate token</button><p>Copy the token below into the preview gate. Do not share it elsewhere.</p><pre id="result"></pre><script src="/preview/authorize.js"></script></body></html>`
const previewConsentJS = `document.getElementById('approve').onclick=async function(){this.disabled=true;try{const r=await fetch('/api/preview/authorize',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({origin:new URLSearchParams(location.search).get('origin')})});document.getElementById('result').textContent=JSON.stringify(await r.json(),null,2)}catch(e){document.getElementById('result').textContent=String(e)}finally{this.disabled=false}};`

func (wc *WebChannel) handlePreviewConsent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Method != "GET" {
		jsonErrorResponse(w, 405, "method not allowed")
		return
	}
	// No session renewal simply by visiting the consent page.
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil {
		jsonErrorResponse(w, 401, "log in to the local backend first")
		return
	}
	wc.sessionsMu.RLock()
	si, ok := wc.sessions[cookie.Value]
	wc.sessionsMu.RUnlock()
	if !ok || !time.Now().Before(si.expires) {
		jsonErrorResponse(w, 401, "log in to the local backend first")
		return
	}
	if r.URL.Path == "/preview/authorize.js" {
		if strings.TrimSpace(os.Getenv("XBOT_PREVIEW_ORIGINS")) == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		fmt.Fprint(w, previewConsentJS)
		return
	}
	origin := r.URL.Query().Get("origin")
	if !previewAllowed(origin) {
		jsonErrorResponse(w, 403, "preview origin not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = template.Must(template.New("consent").Parse(previewConsentHTML)).Execute(w, map[string]string{"Username": si.username, "Origin": origin})
}
func (wc *WebChannel) handlePreviewAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != "POST" {
		jsonErrorResponse(w, 405, "method not allowed")
		return
	}
	if previewFromRequest(r) != nil || !wc.previewSameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		jsonErrorResponse(w, 403, "same-origin cookie consent required")
		return
	}
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil {
		jsonErrorResponse(w, 401, "login required")
		return
	}
	wc.sessionsMu.RLock()
	si, ok := wc.sessions[cookie.Value]
	wc.sessionsMu.RUnlock()
	if !ok || !time.Now().Before(si.expires) {
		jsonErrorResponse(w, 401, "login required")
		return
	}
	var req struct {
		Origin string `json:"origin"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || !previewAllowed(req.Origin) {
		jsonErrorResponse(w, 403, "preview origin not allowed")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		jsonErrorResponse(w, 500, "token generation failed")
		return
	}
	token := "xbot_preview_" + hex.EncodeToString(raw)
	expires := time.Now().Add(15 * time.Minute)
	if si.expires.Before(expires) {
		expires = si.expires
	}
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	g := &previewGrant{parent: cookie.Value, origin: req.Origin, session: si, expires: expires, ctx: ctx, cancel: cancel}
	wc.previewMu.Lock()
	if wc.previewGrants == nil {
		wc.previewGrants = make(map[string]*previewGrant)
	}
	for t, old := range wc.previewGrants {
		if old.ctx.Err() != nil {
			delete(wc.previewGrants, t)
		}
	}
	if len(wc.previewGrants) >= 128 {
		wc.previewMu.Unlock()
		cancel()
		jsonErrorResponse(w, 429, "too many preview grants")
		return
	}
	wc.previewGrants[token] = g
	wc.previewMu.Unlock()
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !wc.previewValid(g) {
					cancel()
					return
				}
			case <-wc.stopCh:
				cancel()
				return
			}
		}
	}()
	// Remove expired grants; do not retain bearer secrets beyond their lifetime.
	context.AfterFunc(ctx, func() { wc.previewMu.Lock(); delete(wc.previewGrants, token); wc.previewMu.Unlock() })
	writeJSON(w, 200, map[string]any{"token": token, "user_id": si.userID, "origin": g.origin, "expires_at": expires.UTC().Format(time.RFC3339)})
}

// The preview is an operator workspace, but must not mint persistent credentials
// or invoke administrative lifecycle controls through the generic RPC tunnel.
func previewRPCBlocked(method string) bool {
	// These handlers return raw persistent credentials without a token/admin name.
	// Masked subscription/storage reads remain available. This is a direct RPC
	// guard, not a sandbox: the consented grant still has full workspace access.
	switch method {
	case "export_subscriptions", "get_channel_config", "plugin_config":
		return true
	}
	return strings.HasPrefix(method, "runner_") || strings.Contains(method, "web_user") || strings.Contains(method, "admin") || strings.Contains(method, "token") || strings.HasPrefix(method, "auth_") || method == "apply_update" || method == "restart_server"
}
