package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// POST /api/account/link-code — generate a one-time link code
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleLinkCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	// Resolve current user
	userID, _, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	code, err := wc.callbacks.IdentityResolver.GenerateLinkCode(userID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":         code,
		"expires_in":   300,
		"instructions": "Run /link-account " + code + " in CLI, or use POST /api/account/link with this code from another channel.",
	})
}

// ---------------------------------------------------------------------------
// POST /api/account/link — link current identity to a target user via code
// Body: {"code": "AB3X9K", "confirm": false}
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	var req struct {
		Code    string `json:"code"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Code == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "code is required")
		return
	}
	// Validate link code WITHOUT consuming (preview-safe).
	// Code is only consumed on actual link/merge execution.
	targetUserID, err := wc.callbacks.IdentityResolver.ValidateLinkCode(req.Code)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	// Resolve current user
	currentUserID, _, _ := wc.callbacks.IdentityResolver.Resolve("web", senderID)
	if currentUserID == targetUserID {
		// Consume code (it was only validated, not consumed yet)
		wc.callbacks.IdentityResolver.ConsumeLinkCode(req.Code)
		writeJSON(w, http.StatusOK, map[string]any{"action": "noop", "message": "already linked to this user"})
		return
	}
	// Check if this is a simple link or a merge
	_, err = wc.callbacks.IdentityResolver.LinkIdentity(targetUserID, "web", senderID)
	if err == nil {
		// Simple link succeeded — consume the code now
		wc.callbacks.IdentityResolver.ConsumeLinkCode(req.Code)
		writeJSON(w, http.StatusOK, map[string]any{
			"action":  "linked",
			"user_id": targetUserID,
			"message": "identity linked successfully",
		})
		return
	}
	// Merge required — current identity belongs to a different user
	if !req.Confirm {
		preview, err := wc.callbacks.IdentityResolver.PreviewMerge(currentUserID, targetUserID)
		if err != nil {
			jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"action":  "merge_required",
			"preview": preview,
			"message": "This identity is linked to another user. Re-send with confirm=true to merge.",
		})
		return
	}
	// Execute merge: currentUser → targetUser
	// Consume code first (single-use), then merge
	wc.callbacks.IdentityResolver.ConsumeLinkCode(req.Code)
	if err := wc.callbacks.IdentityResolver.MergeUsers(currentUserID, targetUserID); err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"action":  "merged",
		"user_id": targetUserID,
		"message": "accounts merged successfully",
	})
}

// ---------------------------------------------------------------------------
// GET /api/account/identities — list current user's linked identities
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	userID, _, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	identities, err := wc.callbacks.IdentityResolver.ListIdentities(userID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":    userID,
		"identities": identities,
	})
}

// ---------------------------------------------------------------------------
// DELETE /api/account/identities/{id} — unlink a channel identity
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleUnlinkIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	// Extract identity ID from path
	pathParts := splitPath(r.URL.Path)
	if len(pathParts) < 1 {
		jsonErrorResponse(w, http.StatusBadRequest, "identity ID required")
		return
	}
	identityID, err := strconv.ParseInt(pathParts[len(pathParts)-1], 10, 64)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid identity ID")
		return
	}
	userID, _, err := wc.callbacks.IdentityResolver.Resolve("web", senderID)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := wc.callbacks.IdentityResolver.UnlinkIdentity(userID, identityID); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// GET /api/admin/users — list all users (admin only)
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !wc.isAdmin(r.Context(), senderID) {
		jsonErrorResponse(w, http.StatusForbidden, "admin only")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	users, err := wc.callbacks.IdentityResolver.ListAllUsers()
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// ---------------------------------------------------------------------------
// POST /api/admin/users/{id}/role — set user role (admin only)
// Body: {"role": "admin"}
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleAdminSetRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !wc.isAdmin(r.Context(), senderID) {
		jsonErrorResponse(w, http.StatusForbidden, "admin only")
		return
	}
	if wc.callbacks.IdentityResolver == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "identity resolver not available")
		return
	}
	// Extract user ID from path
	pathParts := splitPath(r.URL.Path)
	if len(pathParts) < 2 {
		jsonErrorResponse(w, http.StatusBadRequest, "user ID required")
		return
	}
	// Path: /api/admin/users/{id}/role → parts after "users" = [id, role]
	userIDIdx := len(pathParts) - 2 // second-to-last is the ID
	userID, err := strconv.ParseInt(pathParts[userIDIdx], 10, 64)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid user ID")
		return
	}
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Role != "admin" && req.Role != "user" {
		jsonErrorResponse(w, http.StatusBadRequest, "role must be 'admin' or 'user'")
		return
	}
	if err := wc.callbacks.IdentityResolver.SetRole(userID, req.Role); err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user_id": userID, "role": req.Role})
}

// splitPath splits a URL path into segments, skipping empty parts.
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
