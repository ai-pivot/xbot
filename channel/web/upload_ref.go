// xbot Web Channel — upload reference resolution.
//
// ONE implementation resolves an upload key into the reference carried by
// <file> tags (the model-facing attachment reference consumed by the
// DownloadFile tool). Business code must never assemble these references itself
// — see docs/agent/channel.md.

package web

import (
	"path/filepath"

	"xbot/config"
	log "xbot/logger"
)

// uploadModelRef resolves the reference carried by <file> tags — the SINGLE
// implementation behind every model-facing attachment reference.
//
// The contract is "the model must be able to ACT on it":
//
//   - Cloud OSS (qiniu / s3): the provider's signed absolute URL — fetchable by
//     any consumer, which is the whole point of signing.
//   - Local storage (the default): the file is on THIS machine, so the reference
//     is its absolute path. Tools consume paths directly (DownloadFile copies it,
//     Read/Shell open it). A same-origin relative
//     "/api/files/download?key=…" URL is NOT consumable — it has no host/port and
//     the endpoint needs a session cookie — so it must never be emitted
//     (P0 user report 2026-11-27: the model copied that URL out of the message and
//     failed twice — view_image "unsupported url scheme", DownloadFile
//     `unsupported protocol scheme ""`).
//
// It NEVER fails and NEVER returns error text: the internal "no signed URL"
// failure must not leak into user-visible content (P0 user report 2026-09-20:
// local storage appended "（获取下载链接失败）" to the message even though the
// upload itself had succeeded).
func (wc *WebChannel) uploadModelRef(key string) string {
	if wc.ossProvider != nil && wc.ossProvider.Name() != "local" {
		if signed, err := wc.ossProvider.GetDownloadURL(key); err == nil && signed != "" {
			return signed
		} else if err != nil {
			// The object still exists — and handleCloudUpload spills a local copy
			// precisely so the model has a real file to work with. A path beats a
			// URL we could not sign.
			log.WithError(err).WithField("key", key).
				Warn("Failed to sign download URL; using the local upload path")
		}
	}
	// Local layout: <xbotHome>/uploads/<key> — the exact file the upload wrote.
	return filepath.Join(LocalUploadRoot(config.XbotHome()), key)
}

// ⛔ Regression guard (2026-10-12, restated by the 2026-11-27 P0): a reference
// handed to the model/consumer must NEVER be built from this server's bind
// address (wildcard → loopback, direct bind → private address; the endpoint also
// requires a session cookie). The current contract removes the whole hazard for
// local storage: no URL is produced at all — uploadModelRef returns the file's
// absolute path, so there is nothing to reach and nothing to guess. For cloud
// storage the reference is the provider-signed absolute URL.
