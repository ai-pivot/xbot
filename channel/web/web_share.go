package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/protocol"
)

// Shared artifacts — the generic host capability behind plugin-provided
// shareable content.
//
// Design constraints (these ARE the feature):
//   - The host is content-agnostic. content_type is named by the producing
//     plugin and payload is opaque; only that plugin's own share renderer can
//     interpret it. No core code branches on genui — or any other content.
//   - The token IS the credential: a 256-bit random string granting read access
//     to exactly one artifact with no session. GET /api/share/{token} is
//     therefore unauthenticated BY DESIGN.
//   - Unknown / revoked / expired tokens are indistinguishable (404) so the
//     endpoint never confirms whether a token ever existed.
const (
	shareMaxPayload     = 1 << 20 // 1 MiB — a snapshot, not a file store
	shareMaxTitle       = 200
	shareMaxContentType = 128
)

// handleShareCreate creates a share link. Requires a session: creating a public
// link is an explicit, authenticated act.
func (wc *WebChannel) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.ShareCreate == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "sharing is not available")
		return
	}
	var req protocol.ShareCreateRequest
	if err := decodeJSONBody(r, &req, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.PluginID = strings.TrimSpace(req.PluginID)
	req.ContentType = strings.TrimSpace(req.ContentType)
	if req.PluginID == "" || len(req.PluginID) > shareMaxContentType {
		jsonErrorResponse(w, http.StatusBadRequest, "plugin_id is required")
		return
	}
	if req.ContentType == "" || len(req.ContentType) > shareMaxContentType {
		jsonErrorResponse(w, http.StatusBadRequest, "content_type is required")
		return
	}
	if req.Payload == "" || len(req.Payload) > shareMaxPayload {
		jsonErrorResponse(w, http.StatusBadRequest, "payload is required")
		return
	}
	if len(req.Title) > shareMaxTitle {
		jsonErrorResponse(w, http.StatusBadRequest, "title is too long")
		return
	}

	expiresAt := ""
	if req.ExpiresInDays > 0 {
		expiresAt = time.Now().UTC().AddDate(0, 0, req.ExpiresInDays).Format(time.RFC3339)
	}

	artifact, err := wc.callbacks.ShareCreate(userIDFromContext(r.Context()), req.PluginID, req.ContentType, req.Payload, req.Title, expiresAt)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to create share")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"token":        artifact.Token,
		"plugin_id":    artifact.PluginID,
		"content_type": artifact.ContentType,
		"title":        artifact.Title,
		"created_at":   artifact.CreatedAt,
		"expires_at":   artifact.ExpiresAt,
		"path":         "/s/" + artifact.Token,
	})
}

// handleShareList returns the caller's own shares (newest first).
func (wc *WebChannel) handleShareList(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.ShareList == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "sharing is not available")
		return
	}
	items, err := wc.callbacks.ShareList(userIDFromContext(r.Context()))
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to list shares")
		return
	}
	if items == nil {
		items = []protocol.SharedArtifact{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": items})
}

// handleShareRevoke revokes one of the caller's own shares.
func (wc *WebChannel) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	if wc.callbacks.ShareRevoke == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "sharing is not available")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSONBody(r, &req, false); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Token == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := wc.callbacks.ShareRevoke(userIDFromContext(r.Context()), req.Token); err != nil {
		// Scoped to the creator: someone else's token is indistinguishable from
		// a nonexistent one.
		jsonErrorResponse(w, http.StatusNotFound, "share not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleShareGet serves one shared artifact BY TOKEN, WITHOUT a session — this
// is the whole point of the feature (hand the link to a friend). The token is
// the credential, so this handler must never be wrapped in authMiddleware.
func (wc *WebChannel) handleShareGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Never let a shared artifact be cached by intermediaries: revocation must
	// take effect immediately.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")

	if wc.callbacks.ShareGet == nil {
		jsonErrorResponse(w, http.StatusNotImplemented, "sharing is not available")
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/share/")
	if token == "" || strings.Contains(token, "/") {
		jsonErrorResponse(w, http.StatusNotFound, "not found")
		return
	}
	artifact, err := wc.callbacks.ShareGet(token)
	if err != nil || artifact == nil {
		jsonErrorResponse(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":        artifact.Token,
		"plugin_id":    artifact.PluginID,
		"content_type": artifact.ContentType,
		// module_url lets the public page load exactly the plugin that produced
		// this artifact, so that plugin's own share renderer can draw it. It is
		// resolved from the serve dirs (not a plugin-manager session) so a shared
		// link keeps working even when the plugin subsystem is disabled.
		"module_url": wc.pluginModuleURL(artifact.PluginID),
		"payload":    artifact.Payload,
		"title":      artifact.Title,
		"created_at": artifact.CreatedAt,
	})
}

// pluginModuleURL resolves a plugin's frontend ESM entry from its plugin.json in
// the serve directories. The web layer already owns those directories (it serves
// /plugins/<id>/web/*), so resolving the entry here avoids a plugin-manager
// dependency — and the host still knows nothing about the artifact's contents.
func (wc *WebChannel) pluginModuleURL(pluginID string) string {
	if pluginID == "" || len(wc.pluginDirs) == 0 {
		return ""
	}
	for _, dir := range wc.pluginDirs {
		raw, err := os.ReadFile(filepath.Join(dir, pluginID, "plugin.json"))
		if err != nil {
			continue
		}
		var m struct {
			Web *struct {
				Entry string `json:"entry"`
			} `json:"web"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Web == nil || m.Web.Entry == "" {
			continue
		}
		return "/plugins/" + pluginID + "/web/" + strings.TrimPrefix(m.Web.Entry, "/")
	}
	return ""
}
