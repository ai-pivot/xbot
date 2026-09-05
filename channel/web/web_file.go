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

	// Web uploads MUST go to cloud OSS - local storage is never allowed for security
	if wc.ossProvider == nil || wc.ossProvider.Name() == "local" {
		log.Error("Web file upload rejected: no cloud OSS provider configured (local storage is forbidden for web uploads)")
		jsonErrorResponse(w, http.StatusServiceUnavailable, "file storage not configured")
		return
	}

	wc.handleCloudUpload(w, r, header.Filename, ext, data, mimeType)
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
