// xbot Web Channel — upload reference URL resolution.
//
// ONE implementation resolves an upload key into the reference URL carried by
// <file> tags (the model-facing attachment reference consumed by the
// DownloadFile tool). Business code must never assemble these URLs itself —
// see docs/agent/channel.md.

package web

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	log "xbot/logger"
)

// uploadDownloadRef resolves the reference URL for an upload key — the SINGLE
// implementation behind every model-facing attachment reference.
//
// Storage semantics:
//
//   - Cloud OSS (qiniu / s3): the provider's signed absolute URL is the whole
//     point — the object lives in the cloud, DownloadFile fetches it directly.
//   - Local static storage (the default): the provider cannot sign anything,
//     so the reference is built from THIS server's own base URL
//     (serverBaseURL) + the same-origin /api/files/download path.
//   - When no absolute base can be determined — or a cloud signer fails — the
//     reference degrades to the stable RELATIVE /api/files/download?key=… URL
//     (never expires; served same-origin).
//
// It NEVER fails and NEVER returns error text: the internal "no signed URL"
// failure must not leak into user-visible content (P0 user report 2026-09-20:
// local storage appended "（获取下载链接失败）" to the message even though the
// upload itself had succeeded).
func (wc *WebChannel) uploadDownloadRef(key string) string {
	// The relative form is the canonical fallback — the same-origin endpoint
	// that serves uploads for local storage and 302s to the signed URL for
	// cloud storage.
	relative := "/api/files/download?key=" + url.QueryEscape(key)

	if wc.ossProvider != nil && wc.ossProvider.Name() != "local" {
		if signed, err := wc.ossProvider.GetDownloadURL(key); err == nil && signed != "" {
			return signed
		} else if err != nil {
			log.WithError(err).WithField("key", key).
				Warn("Failed to sign download URL; degrading to the same-origin reference")
		}
	}

	if base := wc.serverBaseURL(); base != "" {
		return base + relative
	}
	return relative
}

// serverBaseURL returns this server's own base URL (scheme://host[:port]) used
// to build absolute references for locally-stored files. Resolution order:
//
//  1. WebChannelConfig.PublicURL — the operator-declared external address of
//     this deployment (NAT / reverse proxy / port mapping). Only absolute
//     http(s) URLs are usable as an HTTP base; other schemes (ws:// / wss:// —
//     the same field also feeds runner connect commands) are skipped rather
//     than guessed into http(s).
//  2. The address the HTTP server is actually listening on (set by Start;
//     reflects the real port even when the configured Port was 0). Wildcard
//     binds (0.0.0.0 / ::) are not dialable hosts — they map to loopback.
//  3. The configured Host/Port (when no listener exists yet), same mapping.
//  4. "" — undeterminable. Callers MUST degrade to the relative reference
//     (uploadDownloadRef does) rather than fail.
//
// The app is assumed to be served at the deployment root (every other absolute
// URL in the app — static assets, /ws, /api/… — bakes in that assumption too).
func (wc *WebChannel) serverBaseURL() string {
	if pu := strings.TrimSpace(wc.config.PublicURL); pu != "" {
		if u, err := url.Parse(pu); err == nil &&
			(u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	if wc.listener != nil {
		if host, port, err := net.SplitHostPort(wc.listener.Addr().String()); err == nil && port != "" {
			return "http://" + net.JoinHostPort(dialableHost(host), port)
		}
	}
	if wc.config.Port > 0 {
		return "http://" + net.JoinHostPort(dialableHost(wc.config.Host), strconv.Itoa(wc.config.Port))
	}
	return ""
}

// dialableHost maps a bind host to one that is valid inside a URL and reachable
// from this machine: wildcard / unspecified binds ("" / 0.0.0.0 / ::) are not
// dialable addresses — loopback is.
func dialableHost(host string) string {
	switch strings.TrimSpace(host) {
	case "", "0.0.0.0", "::", "[::]":
		return "127.0.0.1"
	}
	return host
}
