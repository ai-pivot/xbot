// xbot Web Channel — upload reference URL resolution.
//
// ONE implementation resolves an upload key into the reference URL carried by
// <file> tags (the model-facing attachment reference consumed by the
// DownloadFile tool). Business code must never assemble these URLs itself —
// see docs/agent/channel.md.

package web

import (
	"net/url"
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
//   - Local static storage (the default): the provider cannot sign anything, so
//     the reference is built from the OPERATOR-DECLARED public URL
//     (WebChannelConfig.PublicURL) + the same-origin /api/files/download path.
//   - When no public URL is declared — or a cloud signer fails — the reference
//     degrades to the stable RELATIVE /api/files/download?key=… URL (never
//     expires; served same-origin; resolved locally by the server-side
//     resolver). ⛔ It must NEVER fall back to this server's bind address: an
//     undeclared bind is a loopback/wildcard/internal address no consumer can
//     use, and this string is persisted and shown to users (P0 user report
//     2026-10-12: every attachment link read
//     "http://127.0.0.1:16000/api/files/download?key=…" — "根本下载不了", and the
//     same dead URL got copied into the reports agents produced).
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

// serverBaseURL returns this server's operator-declared base URL
// (scheme://host[:port]) used to build absolute references for locally-stored
// files, or "" when none is declared.
//
//  1. WebChannelConfig.PublicURL — the operator-declared external address of
//     this deployment (NAT / reverse proxy / port mapping). Only absolute
//     http(s) URLs are usable as an HTTP base; other schemes (ws:// / wss:// —
//     the same field also feeds runner connect commands) are skipped rather
//     than guessed into http(s).
//  2. "" — undeterminable. Callers MUST degrade to the relative reference
//     (uploadDownloadRef does).
//
// ⛔ The address the HTTP server happens to be bound to is deliberately NOT a
// source. A wildcard bind maps to loopback and a direct bind is typically a
// private address: both are unreachable for the browser that renders the
// reference (and for any other machine), while the download endpoint requires a
// session cookie — so an undeclared bind address is never a usable download
// link, yet it PERSISTS (message content, agent-written reports). Emitting it
// is the P0 defect this function must not reintroduce (2026-10-12).
func (wc *WebChannel) serverBaseURL() string {
	if pu := strings.TrimSpace(wc.config.PublicURL); pu != "" {
		if u, err := url.Parse(pu); err == nil &&
			(u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return ""
}
