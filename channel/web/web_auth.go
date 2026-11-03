package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/storage"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// usernameRegex validates usernames: alphanumeric, underscore, hyphen, dot.
var usernameRegex = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// strongPasswordChars defines the character sets for password generation.
var strongPasswordChars = []string{
	"abcdefghijklmnopqrstuvwxyz",
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	"0123456789",
	"!@#$%^&*-_=+?",
}

// GenerateStrongPassword generates a cryptographically secure random password.
func GenerateStrongPassword(length int) (string, error) {
	if length < 12 {
		length = 16
	}
	allChars := strings.Join(strongPasswordChars, "")

	var password strings.Builder
	// Ensure at least one character from each set
	for _, set := range strongPasswordChars {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return "", err
		}
		password.WriteByte(set[n.Int64()])
	}
	// Fill remaining length
	remaining := length - len(strongPasswordChars)
	for i := 0; i < remaining; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(allChars))))
		if err != nil {
			return "", err
		}
		password.WriteByte(allChars[n.Int64()])
	}

	// Shuffle using Fisher-Yates
	result := []byte(password.String())
	for i := len(result) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return "", err
		}
		result[i], result[j.Int64()] = result[j.Int64()], result[i]
	}
	return string(result), nil
}

// CreateWebUser creates a new web user with auto-generated strong password.
// Returns (username, plaintextPassword, error).
//
// Password generation + bcrypt hashing stay here (business policy); only the
// storage operation goes through the backend-agnostic WebAuthStore.
func CreateWebUser(store storage.WebAuthStore, username string) (string, string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 64 {
		return "", "", fmt.Errorf("invalid username (must be 1-64 chars)")
	}
	// Only allow alphanumeric, underscore, hyphen, dot
	if !usernameRegex.MatchString(username) {
		return "", "", fmt.Errorf("username can only contain letters, digits, underscores, hyphens, and dots")
	}

	// Generate strong password
	password, err := GenerateStrongPassword(16)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate password: %w", err)
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", "", fmt.Errorf("failed to hash password: %w", err)
	}

	// Insert user
	id, err := store.CreateWebUser(username, string(hash))
	if err != nil {
		if isUniqueViolation(err) {
			return "", "", fmt.Errorf("username %q already exists", username)
		}
		return "", "", fmt.Errorf("failed to create user: %w", err)
	}

	log.WithFields(log.Fields{
		"user_id":  id,
		"username": username,
	}).Info("Web user created by admin")

	return username, password, nil
}

// ListWebUsers returns all web users sorted by ID.
func ListWebUsers(store storage.WebAuthStore) ([]storage.WebUserInfo, error) {
	return store.ListWebUsers()
}

// DeleteWebUser deletes a web user by username. Returns error if user not found.
func DeleteWebUser(store storage.WebAuthStore, username string) error {
	if err := store.DeleteWebUser(username); err != nil {
		return err
	}
	log.WithField("username", username).Info("Web user deleted by admin")
	return nil
}

// isUniqueViolation reports whether err is a UNIQUE conflict from either
// backend. SQLite says "UNIQUE constraint failed: web_users.username"; PG says
// "duplicate key value violates unique constraint ...".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value")
}

// ---------------------------------------------------------------------------
// Auth handlers
// ---------------------------------------------------------------------------

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	UserID  int    `json:"user_id,omitempty"`
}

// webUsersEmpty reports whether the web_users table has no rows (first-user
// bootstrap check). Errors are treated as "not empty" (fail-closed — a
// bootstrap path that misfires on DB errors would hand out registrations).
func webUsersEmpty(store storage.WebAuthStore) (bool, error) {
	n, err := store.CountWebUsers()
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// handleRegister handles POST /api/auth/register
func (wc *WebChannel) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Invite-only mode: reject self-registration — UNLESS the web_users table
	// is EMPTY (first-user bootstrap). The very first account is the
	// operator's way into a fresh invite-only deployment; after it exists,
	// registration closes again. The AdminToken (config web.admin_token)
	// remains the escape hatch for a locked-out deployment.
	bootstrap := false
	if wc.config.InviteOnly {
		empty, err := webUsersEmpty(wc.webAuth())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
			return
		}
		if !empty {
			writeJSON(w, http.StatusForbidden, authResponse{OK: false, Message: "registration is invite-only, please contact admin"})
			return
		}
		bootstrap = true
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)

	if req.Username == "" || len(req.Username) > 64 || req.Password == "" || len(req.Password) > 128 {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid username or password"})
		return
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
		return
	}

	// Insert user. The bootstrap path (invite-only + empty table) re-checks
	// emptiness right before the insert; the UNIQUE constraint is the backstop
	// for whatever slips through the (tiny) check→insert race window.
	var id int64
	if bootstrap {
		if n, err := wc.webAuth().CountWebUsers(); err != nil || n > 0 {
			// Lost the race (another first-account landed first) — normal
			// invite-only rules apply now.
			writeJSON(w, http.StatusForbidden, authResponse{OK: false, Message: "registration is invite-only, please contact admin"})
			return
		}
	}
	id, err = wc.webAuth().CreateWebUser(req.Username, string(hash))
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict, authResponse{OK: false, Message: "username already exists"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, authResponse{OK: true, UserID: int(id)})
}

// handleLogin handles POST /api/auth/login
func (wc *WebChannel) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, authResponse{OK: false, Message: "invalid request body"})
		return
	}

	// Look up user
	id, hash, err := wc.webAuth().GetWebUserForLogin(strings.TrimSpace(req.Username))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, authResponse{OK: false, Message: "invalid credentials"})
		return
	}

	// Create session
	token := strings.ReplaceAll(uuid.New().String(), "-", "")
	wc.sessionsMu.Lock()
	wc.sessions[token] = sessionInfo{
		userID:   int(id),
		username: strings.TrimSpace(req.Username),
		expires:  time.Now().Add(webSessionMaxAge),
	}
	wc.sessionsMu.Unlock()

	// Set cookie under this instance's scoped name (see webSessionCookieNameFor).
	wc.issueSessionCookie(w, wc.sessionCookieName(), token)

	writeJSON(w, http.StatusOK, authResponse{OK: true, UserID: int(id)})
}

// handleLogout handles POST /api/auth/logout
//
// Logout clears BOTH names (this instance's scoped name and the legacy unscoped
// one) and drops every token presented under either name: a legacy cookie left
// behind by an old build would otherwise resurrect the session on the next
// request through the migration read path.
func (wc *WebChannel) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	scoped := wc.sessionCookieName()
	names := []string{scoped}
	if legacy := legacySessionCookieName(scoped); legacy != "" {
		names = append(names, legacy)
	}

	// Clear every name first (response side).
	for _, name := range names {
		wc.clearSessionCookie(w, name)
	}

	// Remove the sessions those cookies point at (deduped: a browser cannot
	// hold two cookies with the same name, but it can hold old + new).
	tokens := make(map[string]struct{}, len(names))
	for _, name := range names {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			tokens[c.Value] = struct{}{}
		}
	}
	if len(tokens) > 0 {
		wc.sessionsMu.Lock()
		for token := range tokens {
			delete(wc.sessions, token)
		}
		wc.sessionsMu.Unlock()
	}

	writeJSON(w, http.StatusOK, authResponse{OK: true})
}

// ---------------------------------------------------------------------------
// Session cookie naming (per-instance name; RFC 6265 scopes cookies by host)
// ---------------------------------------------------------------------------

// legacySessionCookieName returns the legacy unscoped name to fall back to when
// reading/clearing, or "" when the scoped name already IS the legacy name (no
// identity known). Only the exact legacy constant is ever returned — a
// "xbot_session_" prefix match would re-introduce cross-instance reads, i.e.
// the very bug this file is fixing.
func legacySessionCookieName(scoped string) string {
	if scoped == webSessionCookieName {
		return ""
	}
	return webSessionCookieName
}

// cookieScope resolves the session cookie name for this instance together with
// the provenance of the identity used to build it (`instance_id` | `env` |
// `home_basename` | `port` | "" when nothing could be resolved).
//
// Identity first (server.instance_id → XBOT_INSTANCE_ID → basename(XBOT_HOME)):
// identity is stable across ports and across a reverse proxy that hides the
// real port, which is exactly where a port-derived name collapses. The listen
// port is only the last resort (ephemeral :0 deploys, tests that build a
// channel without identity); "" means the legacy shared name.
func (wc *WebChannel) cookieScope() (name, source string) {
	id := sanitizeCookieScopeID(wc.config.InstanceID)
	if id != "" {
		source = wc.config.InstanceIDSource
		if source == "" {
			source = instanceIDSourceConfig
		}
		return webSessionCookieNameFor(id), source
	}
	if id, source = ResolveInstanceIDWithSource(""); id != "" {
		return webSessionCookieNameFor(id), source
	}
	if port := wc.listenPort(); port > 0 {
		wc.cookieScopeWarnOnce.Do(func() {
			log.WithField("port", port).
				Warn("web: no server.instance_id and no XBOT_HOME, session cookie falls back to a port-derived name; instances behind one reverse proxy will still share it")
		})
		return webSessionCookieNameFor(strconv.Itoa(port)), instanceIDSourcePort
	}
	return "", ""
}

// listenPort is the port this channel actually listens on: the configured one
// when fixed, otherwise the one the listener bound (available after Start —
// every login/request arrives after that, since Start binds before Serve).
func (wc *WebChannel) listenPort() int {
	if wc.config.Port > 0 {
		return wc.config.Port
	}
	if ln := wc.listener; ln != nil && ln.Addr() != nil {
		if addr, ok := ln.Addr().(*net.TCPAddr); ok && addr.Port > 0 {
			return addr.Port
		}
		if _, port, err := net.SplitHostPort(ln.Addr().String()); err == nil {
			if p, err := strconv.Atoi(port); err == nil && p > 0 {
				return p
			}
		}
	}
	return 0
}

// sessionCookieName is the name this instance issues, reads first, and clears.
func (wc *WebChannel) sessionCookieName() string {
	name, _ := wc.cookieScope()
	if name != "" {
		return name
	}
	// Never fail a login over this — fall back to the shared legacy name and
	// say it once, loudly enough to be findable in the log.
	wc.cookieLegacyWarnOnce.Do(func() {
		log.WithField("cookie", webSessionCookieName).
			Warn("web: instance identity unknown, session cookie uses the shared legacy name; instances on this host will evict each other's sessions")
	})
	return webSessionCookieName
}

// sessionCookieScopeLogf is the sink for the startup scope line. It is a
// variable so a test can capture the exact line without reconfiguring the
// global logger.
var sessionCookieScopeLogf = log.Infof

// logSessionCookieScope prints the ONE line an operator can grep on both
// instances to prove they are not sharing a cookie name:
//
//	session cookie scope: name=xbot_session_smith source=home_basename
//
// Silent when the identity is unknown (that case already WARNs on first use).
func (wc *WebChannel) logSessionCookieScope() {
	name, source := wc.cookieScope()
	if name == "" {
		return
	}
	sessionCookieScopeLogf("session cookie scope: name=%s source=%s", name, source)
}

// sessionCookie builds the session cookie. Login, legacy migration and logout
// all go through it so the attributes stay byte-identical — a migration cookie
// whose Path/Secure/SameSite differed would not replace the legacy one.
func (wc *WebChannel) sessionCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   wc.isSecureCookie(),
		MaxAge:   maxAge,
	}
}

// issueSessionCookie sets name=token with the full session lifetime.
func (wc *WebChannel) issueSessionCookie(w http.ResponseWriter, name, token string) {
	http.SetCookie(w, wc.sessionCookie(name, token, int(webSessionMaxAge.Seconds())))
}

// clearSessionCookie expires name (logout).
func (wc *WebChannel) clearSessionCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, wc.sessionCookie(name, "", -1))
}

// sessionCookieHit is the outcome of locating a session token on a request.
type sessionCookieHit struct {
	Token string
	// Legacy is true when the token came from the legacy unscoped cookie name,
	// so the response must re-issue the scoped name (one-time migration).
	Legacy bool
}

// findSessionCookie locates the session token: this instance's scoped name
// first, then the legacy unscoped name for the one-time migration read.
// Cookies belonging to OTHER instances are never consulted (exact name match).
func (wc *WebChannel) findSessionCookie(r *http.Request) sessionCookieHit {
	scoped := wc.sessionCookieName()
	if c, err := r.Cookie(scoped); err == nil && c.Value != "" {
		return sessionCookieHit{Token: c.Value}
	}
	if legacy := legacySessionCookieName(scoped); legacy != "" {
		if c, err := r.Cookie(legacy); err == nil && c.Value != "" {
			return sessionCookieHit{Token: c.Value, Legacy: true}
		}
	}
	return sessionCookieHit{}
}

// validateSession checks the session cookie and returns session info.
// Sessions are automatically renewed when more than half of their lifetime has passed.
func (wc *WebChannel) validateSession(r *http.Request) *sessionInfo {
	si, _ := wc.authenticateSession(r)
	return si
}

// authenticateSession is validateSession plus the cookie hit, so the caller can
// silently migrate a legacy-named cookie onto the scoped name.
func (wc *WebChannel) authenticateSession(r *http.Request) (*sessionInfo, sessionCookieHit) {
	hit := wc.findSessionCookie(r)
	if hit.Token == "" {
		return nil, sessionCookieHit{}
	}

	wc.sessionsMu.RLock()
	si, ok := wc.sessions[hit.Token]
	wc.sessionsMu.RUnlock()

	if !ok || time.Now().After(si.expires) {
		return nil, hit
	}

	// Auto-renew session if more than half of its lifetime has passed
	remaining := time.Until(si.expires)
	if remaining < webSessionMaxAge/2 {
		wc.sessionsMu.Lock()
		if _, exists := wc.sessions[hit.Token]; exists {
			si.expires = time.Now().Add(webSessionMaxAge)
			wc.sessions[hit.Token] = si
		}
		wc.sessionsMu.Unlock()
	}

	return &si, hit
}

// authMiddleware wraps a handler with session validation
func (wc *WebChannel) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Uploads enforce their own limits in their handlers:
		// /api/files/upload (10MB) and /api/plugin-files/upload (no limit —
		// plugin/wallpaper upload; handler reads multipart to disk, no
		// body cap). Both must bypass the 1MB middleware limit — wallpaper
		// and plugin files routinely exceed 1MB.
		if r.Method != http.MethodGet && r.URL.Path != "/api/files/upload" && r.URL.Path != "/api/plugin-files/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		}
		si, hit := wc.authenticateSession(r)
		if si == nil {
			jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// One-time migration: the browser is still carrying the legacy unscoped
		// cookie but the session is valid ⇒ re-issue it under the scoped name
		// with the same token, so the existing login survives and every later
		// request uses the scoped cookie.
		if hit.Legacy {
			wc.issueSessionCookie(w, wc.sessionCookieName(), hit.Token)
		}
		senderID := "web-" + strconv.Itoa(si.userID)
		ctx := contextWithSenderID(r.Context(), senderID)
		ctx = contextWithUserID(ctx, si.userID)
		ctx = context.WithValue(ctx, webSessionKey, *si)
		next(w, r.WithContext(ctx))
	}
}

// isSecureCookie returns true if the web channel is served over HTTPS.
func (wc *WebChannel) isSecureCookie() bool {
	return wc.config.PublicURL != "" && strings.HasPrefix(wc.config.PublicURL, "https://")
}

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

type contextKey string

const (
	senderIDKey        contextKey = "sender_id"
	userIDKey          contextKey = "user_id"
	webSessionKey      contextKey = "web_session"
	canonicalUserIDKey contextKey = "canonical_user_id"
	canonicalRoleKey   contextKey = "canonical_role"
)

func contextWithSenderID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, senderIDKey, id)
}

func senderIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(senderIDKey).(string); ok {
		return id
	}
	return ""
}

func contextWithUserID(ctx context.Context, userID int) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

func userIDFromContext(ctx context.Context) int {
	if id, ok := ctx.Value(userIDKey).(int); ok {
		return id
	}
	return 0
}

func webSessionFromContext(ctx context.Context) (sessionInfo, bool) {
	si, ok := ctx.Value(webSessionKey).(sessionInfo)
	return si, ok
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// handleAuthConfig handles POST /api/auth/config.
// Returns public auth configuration (e.g., invite-only mode).
func (wc *WebChannel) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Bootstrap: invite-only AND no web account exists yet — the frontend
	// shows the "create the operator account" wizard instead of the login
	// form (the first registration is allowed through even in invite-only
	// mode; after the account exists, registration closes again).
	bootstrap := false
	if wc.config.InviteOnly {
		if empty, err := webUsersEmpty(wc.webAuth()); err == nil {
			bootstrap = empty
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"invite_only": wc.config.InviteOnly,
		"bootstrap":   bootstrap,
	})
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiResponse struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data"`
	Error *apiError `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	payload := normalizeAPIValue(v)
	if status >= http.StatusBadRequest {
		message := http.StatusText(status)
		if fields, ok := payload.(map[string]any); ok {
			if value, ok := fields["error"].(string); ok && value != "" {
				message = value
			} else if value, ok := fields["message"].(string); ok && value != "" {
				message = value
			}
		}
		writeAPIResponse(w, status, apiResponse{
			OK:    false,
			Data:  nil,
			Error: &apiError{Code: apiErrorCode(status), Message: message},
		})
		return
	}

	if fields, ok := payload.(map[string]any); ok {
		delete(fields, "ok")
		delete(fields, "error")
	}
	writeAPIResponse(w, status, apiResponse{OK: true, Data: payload, Error: nil})
}

func writeAPIResponse(w http.ResponseWriter, status int, response apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func normalizeAPIValue(v any) any {
	if v == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return map[string]any{}
	}
	return normalized
}

func apiErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusNotImplemented:
		return "not_implemented"
	case http.StatusServiceUnavailable:
		return "service_unavailable"
	default:
		return "internal_error"
	}
}

// jsonErrorResponse writes a JSON-formatted error response (for consistent API errors).
func jsonErrorResponse(w http.ResponseWriter, status int, message string) {
	writeAPIResponse(w, status, apiResponse{
		OK:    false,
		Data:  nil,
		Error: &apiError{Code: apiErrorCode(status), Message: message},
	})
}
