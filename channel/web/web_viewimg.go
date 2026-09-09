package web

// GET /api/files/viewimg/<uuid>.<ext> — serves images stored by the
// view_image tool from ~/.xbot/view_images/. Cookie-authenticated browser
// endpoint: the markdown references the view_image tool injects into the
// conversation use this RELATIVE URL, so the web frontend renders injected
// images with a plain <img src> (same-origin, session cookie) — no signed
// URL, no expiry. The LLM vision resolver reads the very same files from
// disk (serverapp.imageResolver) — one reference, two consumers.

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xbot/config"
	log "xbot/logger"
)

// viewimgIDPattern constrains the served filename: uuid + image extension.
// The file joins a filesystem path, so traversal ("..", separators) must be
// structurally impossible.
var viewimgIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}\.(png|jpe?g|gif|webp|bmp|tiff)$`)

func (wc *WebChannel) handleViewImage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/files/viewimg/")
	if id == "" || !viewimgIDPattern.MatchString(id) {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid viewimg id")
		return
	}
	// filepath.Join stays inside the view_images dir: id matches
	// ^[a-zA-Z0-9_-]+(\.[a-z]+)?$ (no separators, no ".."), so escaping is
	// structurally impossible. The resolved-prefix containment guard is
	// defense in depth on top of the charset check.
	dir := filepath.Join(config.XbotHome(), "view_images")
	path := filepath.Join(dir, id)
	if resolved, err := filepath.Abs(path); err != nil || !strings.HasPrefix(resolved, dir+string(os.PathSeparator)) {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid viewimg id")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		jsonErrorResponse(w, http.StatusNotFound, "view image not found")
		return
	}
	// Content type from magic bytes — never trust the extension alone.
	w.Header().Set("Content-Type", detectViewImageMIME(data))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(data); err != nil {
		log.WithError(err).WithField("id", id).Debug("viewimg write failed")
	}
}

// detectViewImageMIME sniffs the image Content-Type from magic bytes.
func detectViewImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "image/bmp"
	case len(data) >= 4 && (string(data[:4]) == "II*\x00" || string(data[:4]) == "MM\x00*"):
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}
