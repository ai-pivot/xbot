package web

import (
	"net/http"
)

// ---------------------------------------------------------------------------
// GET /api/channels/list — discover every channel that has sessions in the DB
// ---------------------------------------------------------------------------
// Multi-user removal: the account-identity management handlers (link codes,
// identities, admin user roles) were removed with the canonical user system.
// This endpoint is the surviving piece: the frontend ActivityBar uses the
// discovered channel list to show channel icons (including plugin channels
// like github/gitlab) without hardcoding.

func (wc *WebChannel) handleChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// Also return all channels that have sessions in the DB — this
	// dynamically discovers plugin channels (github, gitlab, etc.) without
	// hardcoding. Frontend uses this to show channel icons in ActivityBar.
	var channels []string
	if wc.db != nil {
		rows, err := wc.db.Query(`SELECT DISTINCT channel FROM tenants WHERE chat_id != '_shared' ORDER BY channel`)
		if err == nil {
			for rows.Next() {
				var ch string
				if rows.Scan(&ch) == nil {
					channels = append(channels, ch)
				}
			}
			rows.Close()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": channels,
	})
}
