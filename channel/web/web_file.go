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
	"io/fs"
	"os"
	"sort"
	"time"
	"xbot/config"
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

	// 存储后端（2026-09-16 用户要求："默认 storage 应该是本地 static server，不要默认没有"）：
	//   · 未配置云 OSS（provider 为 nil）或显式 `oss.provider == "local"` ⇒ **本地 static**：
	//     写到 <xbotHome>/uploads/<key>，由同源 /api/files/download 读盘返回（免配置即可用）。
	//   · 配置了 qiniu/s3 ⇒ 走云（云端是 source of truth，本地另留一份 spill 供模型拿真实路径）。
	// 安全语义两侧一致：默认强制 attachment，只有 ?inline=1 才内联（云侧靠 attname 参数）。
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		wc.handleLocalUpload(w, r, header.Filename, ext, data, mimeType)
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
	// Only upload-issued or agent-published keys are addressable
	// (uploads/<uid>/<uuid><ext> or agent/<uuid>/<name>) —
	// blocks arbitrary object probing of the OSS bucket.
	if (!strings.HasPrefix(key, "uploads/") && !strings.HasPrefix(key, "agent/")) || strings.Contains(key, "..") {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid key")
		return
	}
	// 默认本地 static（见 handleFileUpload 的说明）：同源读盘返回字节，不再 302 到签名 URL。
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		wc.serveLocalFile(w, r, key)
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

	// Spill a local copy so the model can be handed a REAL path (see
	// webImageResolver.LocalPath). Without it a pasted screenshot exists only as
	// an opaque OSS key: the vision model sees the pixels but has no filename to
	// act on, so "ps this image" degenerates into a filesystem-wide search.
	// Best-effort: a failure here must not fail the upload.
	if home := config.XbotHome(); home != "" {
		uploadRoot := LocalUploadRoot(home)
		localPath := filepath.Join(uploadRoot, key)
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			log.WithError(err).WithField("path", localPath).Warn("Failed to create local upload dir")
		} else if err := os.WriteFile(localPath, data, 0o600); err != nil {
			log.WithError(err).WithField("path", localPath).Warn("Failed to spill local copy of upload")
		} else {
			pruneLocalUploads(uploadRoot, maxLocalUploads)
		}
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

// handleLocalUpload stores an upload on LOCAL disk — the DEFAULT storage backend
// when no cloud OSS is configured (user requirement 2026-09-16: uploads must work
// out of the box with a local static server instead of failing with
// "file storage not configured").
//
// Path: <xbotHome>/uploads/<key> (same root the image resolver derives — see
// LocalUploadRoot / webImageResolver.uploadDir — so uploads are always readable
// by both the HTTP layer and the multimodal resolver). Payload shape identical to
// the cloud path so the composer/attachments code is backend-agnostic.
func (wc *WebChannel) handleLocalUpload(w http.ResponseWriter, r *http.Request, filename, ext string, data []byte, mimeType string) {
	home := config.XbotHome()
	if home == "" {
		jsonErrorResponse(w, http.StatusInternalServerError, "xbot home not resolved")
		return
	}
	userID := "anonymous"
	if si := wc.validateSession(r); si != nil {
		userID = fmt.Sprintf("%d", si.userID)
	}
	key := fmt.Sprintf("uploads/%s/%s%s", userID, uuid.New().String(), ext)
	root := LocalUploadRoot(home)
	localPath := filepath.Join(root, key)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
		log.WithError(err).WithField("path", localPath).Error("Failed to create local upload dir")
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to store upload")
		return
	}
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		log.WithError(err).WithField("path", localPath).Error("Failed to write local upload")
		jsonErrorResponse(w, http.StatusInternalServerError, "failed to store upload")
		return
	}
	pruneLocalUploads(root, maxLocalUploads)
	log.WithFields(log.Fields{
		"key": key, "filename": filename, "size": len(data), "provider": "local",
	}).Info("File uploaded to local storage")
	writeJSON(w, http.StatusOK, map[string]any{
		"upload_key": key,
		"name":       filename,
		"size":       len(data),
		"mime":       mimeType,
	})
}

// serveLocalFile serves an upload straight from local disk (the local-storage
// counterpart of the cloud 302-to-signed-URL path). Same-origin + cookie auth,
// no external redirect.
//
// Security parity with the cloud path (which forces attachment via the qiniu
// attname parameter): the default is Content-Disposition: attachment; only
// ?inline=1 renders inline (used by the composer/history <img src>). nosniff is
// always set so a stored payload can never be sniffed into an active type.
func (wc *WebChannel) serveLocalFile(w http.ResponseWriter, r *http.Request, key string) {
	home := config.XbotHome()
	if home == "" {
		jsonErrorResponse(w, http.StatusInternalServerError, "xbot home not resolved")
		return
	}
	localPath := filepath.Join(LocalUploadRoot(home), key)
	data, err := os.ReadFile(localPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusNotFound, "file not found")
		return
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(localPath)))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}
	inline := r.URL.Query().Get("inline") == "1"
	if inline {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(localPath)))
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	if _, werr := w.Write(data); werr != nil {
		log.WithError(werr).WithField("key", key).Debug("Local file response write failed")
	}
}

// maxLocalUploads bounds the spill-to-disk directory. These copies are a CACHE
// (they exist so the model can be handed a real path) — OSS stays the source of
// truth — and without a cap the directory would grow without bound.
const maxLocalUploads = 500

// pruneLocalUploads keeps the newest `keep` files under root (recursively) and
// removes older ones. Best-effort: the upload has already succeeded, so nothing
// here is surfaced to the caller.
func pruneLocalUploads(root string, keep int) {
	type entry struct {
		path string
		mod  time.Time
	}
	var files []entry
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			files = append(files, entry{path: p, mod: info.ModTime()})
		}
		return nil
	})
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	removed := 0
	for _, f := range files[:len(files)-keep] {
		if err := os.Remove(f.path); err == nil {
			removed++
		}
	}
	// 观测：全静默时「根目录不可读 → 永远剪不掉 → 无界增长」将无从发现。
	log.WithFields(log.Fields{"root": root, "kept": keep, "removed": removed, "seen": len(files)}).
		Debug("Pruned local upload spills")
}

// LocalUploadRoot is the single definition of the local spill root
// (<xbotHome>/uploads). serverapp's image resolver must derive the same path —
// see webImageResolver.LocalPath — so the two sides cannot drift apart and
// silently stop mapping uploads to real files.
func LocalUploadRoot(xbotHome string) string {
	return filepath.Join(xbotHome, "uploads")
}
