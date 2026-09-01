package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	log "xbot/logger"

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
func CreateWebUser(db *sql.DB, username string) (string, string, error) {
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
	result, err := db.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?)",
		username, string(hash),
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return "", "", fmt.Errorf("username %q already exists", username)
		}
		return "", "", fmt.Errorf("failed to create user: %w", err)
	}

	id, _ := result.LastInsertId()
	log.WithFields(log.Fields{
		"user_id":  id,
		"username": username,
	}).Info("Web user created by admin")

	return username, password, nil
}

// WebUserInfo represents a web user record for listing.
type WebUserInfo struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

// ListWebUsers returns all web users sorted by ID.
func ListWebUsers(db *sql.DB) ([]WebUserInfo, error) {
	rows, err := db.Query("SELECT id, username, created_at FROM web_users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []WebUserInfo
	for rows.Next() {
		var u WebUserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			log.WithError(err).Warn("web_users row scan failed")
			continue
		}
		users = append(users, u)
	}
	if users == nil {
		users = []WebUserInfo{}
	}
	return users, nil
}

// DeleteWebUser deletes a web user by username. Returns error if user not found.
func DeleteWebUser(db *sql.DB, username string) error {
	result, err := db.Exec("DELETE FROM web_users WHERE username = ?", username)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	log.WithField("username", username).Info("Web user deleted by admin")
	return nil
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
func webUsersEmpty(db *sql.DB) (bool, error) {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM web_users").Scan(&n); err != nil {
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
		empty, err := webUsersEmpty(wc.db)
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

	// Insert user. The bootstrap path (invite-only + empty table) runs the
	// emptiness re-check and the INSERT in ONE transaction — two concurrent
	// first registrations race for the same empty-table window; the loser
	// gets a clean invite-only rejection instead of a second account.
	var id int64
	if bootstrap {
		tx, txErr := wc.db.Begin()
		if txErr != nil {
			writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
			return
		}
		defer tx.Rollback()
		var n int
		if qErr := tx.QueryRow("SELECT COUNT(*) FROM web_users").Scan(&n); qErr != nil || n > 0 {
			// Lost the race (another first-account landed first) — normal
			// invite-only rules apply now.
			writeJSON(w, http.StatusForbidden, authResponse{OK: false, Message: "registration is invite-only, please contact admin"})
			return
		}
		result, insErr := tx.Exec("INSERT INTO web_users (username, password) VALUES (?, ?)", req.Username, string(hash))
		if insErr != nil {
			if strings.Contains(insErr.Error(), "UNIQUE constraint failed") {
				writeJSON(w, http.StatusConflict, authResponse{OK: false, Message: "username already exists"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
			return
		}
		id, _ = result.LastInsertId()
		if cErr := tx.Commit(); cErr != nil {
			writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
			return
		}
	} else {
		// Open registration — the UNIQUE constraint catches duplicates.
		result, err := wc.db.Exec(
			"INSERT INTO web_users (username, password) VALUES (?, ?)",
			req.Username, string(hash),
		)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				writeJSON(w, http.StatusConflict, authResponse{OK: false, Message: "username already exists"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, authResponse{OK: false, Message: "internal error"})
			return
		}
		id, _ = result.LastInsertId()
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
	var id int
	var hash string
	err := wc.db.QueryRow(
		"SELECT id, password FROM web_users WHERE username = ?",
		strings.TrimSpace(req.Username),
	).Scan(&id, &hash)
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
		userID:   id,
		username: strings.TrimSpace(req.Username),
		expires:  time.Now().Add(webSessionMaxAge),
	}
	wc.sessionsMu.Unlock()

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   wc.isSecureCookie(),
		MaxAge:   int(webSessionMaxAge.Seconds()),
	})

	writeJSON(w, http.StatusOK, authResponse{OK: true, UserID: id})
}

// handleLogout handles POST /api/auth/logout
func (wc *WebChannel) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Clear cookie
	http.SetCookie(w, &http.Cookie{
		Name:     webSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   wc.isSecureCookie(),
		MaxAge:   -1,
	})

	// Remove session
	if cookie, err := r.Cookie(webSessionCookieName); err == nil {
		wc.sessionsMu.Lock()
		delete(wc.sessions, cookie.Value)
		wc.sessionsMu.Unlock()
	}

	writeJSON(w, http.StatusOK, authResponse{OK: true})
}

// validateSession checks the session cookie and returns session info.
// Sessions are automatically renewed when more than half of their lifetime has passed.
func (wc *WebChannel) validateSession(r *http.Request) *sessionInfo {
	cookie, err := r.Cookie(webSessionCookieName)
	if err != nil {
		return nil
	}

	wc.sessionsMu.RLock()
	si, ok := wc.sessions[cookie.Value]
	wc.sessionsMu.RUnlock()

	if !ok || time.Now().After(si.expires) {
		return nil
	}

	// Auto-renew session if more than half of its lifetime has passed
	remaining := time.Until(si.expires)
	if remaining < webSessionMaxAge/2 {
		wc.sessionsMu.Lock()
		if _, exists := wc.sessions[cookie.Value]; exists {
			si.expires = time.Now().Add(webSessionMaxAge)
			wc.sessions[cookie.Value] = si
		}
		wc.sessionsMu.Unlock()
	}

	return &si
}

// authMiddleware wraps a handler with session validation
func (wc *WebChannel) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Uploads enforce their own size policy in their handlers:
		//   - /api/files/upload: handleFileUpload 自带 10MB 限制
		//   - /api/plugin-files/upload: handlePluginFileUpload 无上限（用户明确
		//     要求壁纸上传不限大小——ParseMultipartForm(32MB) 只是内存缓冲，
		//     超出自动落盘临时文件）。这里若再包 1MB MaxBytesReader 会在
		//     handler 之前拒绝请求体（"http: request body too large"）。
		if r.Method != http.MethodGet &&
			r.URL.Path != "/api/files/upload" &&
			r.URL.Path != "/api/plugin-files/upload" {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		}
		si := wc.validateSession(r)
		if si == nil {
			jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
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
		if empty, err := webUsersEmpty(wc.db); err == nil {
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
