// xbot Web Channel - File upload handlers

package web

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	log "xbot/logger"

	"github.com/google/uuid"
)

const (
	maxFileSize = 10 << 20 // 10MB
)

// handleFileUpload handles POST /api/files/upload
func (wc *WebChannel) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+1024)

	if err := r.ParseMultipartForm(maxFileSize); err != nil {
		jsonErrorResponse(w, http.StatusRequestEntityTooLarge, "file too large (max 10MB)")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to read file")
		return
	}

	if int64(len(data)) > maxFileSize {
		jsonErrorResponse(w, http.StatusRequestEntityTooLarge, "file too large (max 10MB)")
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	// Non-blocking observability (CR security note, PR #345): uploads are
	// type-unrestricted BY DESIGN (user requirement 2026-09-05 — never
	// reintroduce a whitelist/blacklist). Flag executable/web content types
	// in the log stream for downstream security auditing — a log line only,
	// NEVER a gate. Download-side mitigation: OSS URLs force
	// Content-Disposition: attachment (oss.go GetDownloadURL attname).
	if isExecutableLikeUpload(ext, mimeType) {
		log.WithFields(log.Fields{
			"filename":  header.Filename,
			"mime_type": mimeType,
			"size":      len(data),
		}).Info("Accepted executable-like upload (unrestricted by design; forced attachment download on serve)")
	}

	// Web uploads MUST go to cloud OSS - local storage is never allowed for security
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		log.Error("Web file upload rejected: no cloud OSS provider configured (local storage is forbidden for web uploads)")
		jsonErrorResponse(w, http.StatusServiceUnavailable, "file storage not configured")
		return
	}

	wc.handleCloudUpload(w, r, header.Filename, ext, data, mimeType)
}

// isExecutableLikeUpload reports whether the uploaded file is executable- or
// web-content-like — used ONLY for a non-blocking observability log (audit
// trail), never as a gate. Uploads stay type-unrestricted by design.
func isExecutableLikeUpload(ext, mimeType string) bool {
	switch mimeType {
	case "text/html", "application/xhtml+xml", "application/x-httpd-php",
		"application/javascript", "text/javascript", "application/x-sh",
		"application/x-msdownload", "application/x-dosexec", "application/x-sharedlib":
		return true
	}
	switch ext {
	case ".exe", ".msi", ".bat", ".cmd", ".com", ".scr", ".ps1",
		".sh", ".bash", ".zsh", ".fish", ".ksh",
		".php", ".jsp", ".asp", ".aspx",
		".html", ".htm", ".xhtml", ".svg", ".xml",
		".so", ".dylib", ".dll", ".app", ".deb", ".rpm", ".apk", ".jar":
		return true
	}
	return false
}

// handleFileDownload handles GET /api/files/download?key=<upload_key>&inline=1
// — resolves the OSS signed URL and 302-redirects. Two modes:
//   - default: attachment download (GetDownloadURL — qiniu attname forces
//     Content-Disposition: attachment, the CR security mitigation)
//   - ?inline=1: inline rendering (GetViewURL — no attname) for composer
//     <img> src (pasted images render inline in the tiptap editor).
//
// Same-origin + cookie auth: the URL is embedded in composer markdown
// ([name](/api/files/download?key=...)) — works in editor rendering AND in
// rendered chat history; the agent receives the semantic payload separately
// via the attachments upload_key array.
func (wc *WebChannel) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "key is required")
		return
	}
	// Only upload-issued keys are addressable (uploads/<uid>/<uuid><ext>) —
	// blocks arbitrary object probing of the OSS bucket.
	if !strings.HasPrefix(key, "uploads/") || strings.Contains(key, "..") {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid key")
		return
	}
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		jsonErrorResponse(w, http.StatusServiceUnavailable, "file storage not configured")
		return
	}
	var (
		target string
		err    error
	)
	if r.URL.Query().Get("inline") == "1" {
		target, err = wc.ossProvider.GetViewURL(key)
	} else {
		target, err = wc.ossProvider.GetDownloadURL(key)
	}
	if err != nil {
		log.WithError(err).WithField("key", key).Warn("File download URL resolve failed")
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to resolve download URL")
		return
	}
	log.WithFields(log.Fields{
		"key":    key,
		"inline": r.URL.Query().Get("inline") == "1",
	}).Debug("File download redirect")
	http.Redirect(w, r, target, http.StatusFound)
}

// handleCloudUpload uploads a file to cloud OSS (e.g., Qiniu) and returns the upload key.
func (wc *WebChannel) handleCloudUpload(w http.ResponseWriter, r *http.Request, filename, ext string, data []byte, mimeType string) {
	userID := "anonymous"
	if si := wc.validateSession(r); si != nil {
		userID = fmt.Sprintf("%d", si.userID)
	}

	key := fmt.Sprintf("uploads/%s/%s%s", userID, uuid.New().String(), ext)

	if err := wc.ossProvider.Upload(key, data); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"key":      key,
			"filename": filename,
		}).Error("Failed to upload file to cloud OSS")
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to upload to cloud storage")
		return
	}

	log.WithFields(log.Fields{
		"key":      key,
		"filename": filename,
		"size":     len(data),
		"provider": wc.ossProvider.Name(),
	}).Info("File uploaded to cloud OSS")

	writeJSON(w, http.StatusOK, map[string]any{
		"upload_key": key,
		"name":       filename,
		"size":       len(data),
		"mime":       mimeType,
	})
}
