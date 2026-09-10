package protocol

// SharedArtifact is the wire contract for the generic shared-artifact
// capability: a plugin may publish an immutable snapshot of its own content and
// hand out a link that grants read access without any session.
//
// The host is content-agnostic by design — ContentType is named by the
// producing plugin (e.g. "xbot.genui/tsx") and Payload is opaque to the host;
// only that plugin's own share renderer can interpret it. Token is the
// credential: a high-entropy random string that resolves to this one artifact.
type SharedArtifact struct {
	Token string `json:"token"`
	// PluginID is the producing plugin — the public share page loads exactly
	// that plugin to obtain the renderer for this artifact.
	PluginID    string `json:"plugin_id"`
	ContentType string `json:"content_type"`
	Payload     string `json:"payload"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	CreatedBy   int64  `json:"created_by"`
	// ExpiresAt is RFC3339, or "" for no expiry.
	ExpiresAt string `json:"expires_at,omitempty"`
	// RevokedAt is RFC3339 once revoked, otherwise "".
	RevokedAt string `json:"revoked_at,omitempty"`
}

// ShareCreateRequest is the request body of POST /api/share/create.
type ShareCreateRequest struct {
	// PluginID is filled by the plugin runtime (ctx.share knows it from
	// ctx.meta) — plugins never pass it themselves.
	PluginID string `json:"plugin_id"`
	// ContentType must be a value the calling plugin owns; the host only
	// validates that it is non-empty and bounded in length.
	ContentType string `json:"content_type"`
	// Payload is the opaque snapshot handed back to the plugin's renderer.
	Payload string `json:"payload"`
	// Title is display metadata for share listings.
	Title string `json:"title,omitempty"`
	// ExpiresInDays <= 0 means the link does not expire.
	ExpiresInDays int `json:"expires_in_days,omitempty"`
}
