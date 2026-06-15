package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"time"

	ch "xbot/channel"
	log "xbot/logger"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/image/draw"
)

const (
	// maxImageBytes is the maximum allowed image size (5MB raw, ~6.7MB base64).
	maxImageBytes = 5 << 20
	// maxImageDim is the maximum dimension (width or height) after resize.
	maxImageDim = 2048
	// jpegQuality is the quality used during compression.
	jpegQuality = 85
	// maxPasteKeep is the number of paste images to keep on disk.
	maxPasteKeep = 20
)

// ReadClipboardImage reads an image from the system clipboard.
// Returns the raw image bytes and mime type, or an error if the clipboard
// doesn't contain an image or the clipboard is unavailable.
func ReadClipboardImage() ([]byte, string, error) {
	// clipboard.Init may panic on platforms without a display server.
	// We catch panics to provide a graceful error message.
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Warn("Clipboard panic during read")
		}
	}()

	// Lazy import via init guard — clipboard package Init is called once.
	if err := clipboardInit(); err != nil {
		return nil, "", fmt.Errorf("clipboard unavailable: %w", err)
	}

	data := clipboardReadImage()
	if len(data) == 0 {
		return nil, "", fmt.Errorf("no image in clipboard")
	}

	mimeType := detectMimeType(data)
	return data, mimeType, nil
}

// CompressImage compresses an image if it exceeds maxBytes.
// If the image is already within the limit, it's returned as-is.
// Returns the (possibly compressed) data, the mime type, and an error.
// The image is resized to at most maxImageDim×maxImageDim and re-encoded as JPEG.
func CompressImage(data []byte, mimeType string, maxBytes int) ([]byte, string, error) {
	if len(data) <= maxBytes {
		return data, mimeType, nil
	}

	// Decode the image
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode image (%s): %w", format, err)
	}

	bounds := img.Bounds()
	if bounds.Dx() > maxImageDim || bounds.Dy() > maxImageDim {
		img = resizeImage(img, maxImageDim)
	}

	// Re-encode as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, "", fmt.Errorf("failed to encode JPEG: %w", err)
	}

	if buf.Len() > maxBytes {
		return nil, "", fmt.Errorf("image too large after compression: %d bytes (max %d)", buf.Len(), maxBytes)
	}

	return buf.Bytes(), "image/jpeg", nil
}

// resizeImage scales an image down so that neither dimension exceeds maxDim.
// Uses high-quality CatmullRom interpolation.
func resizeImage(src image.Image, maxDim int) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w <= maxDim && h <= maxDim {
		return src
	}

	scale := float64(maxDim) / float64(max(w, h))
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)

	// Ensure at least 1px
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}

// SavePasteImage saves image data to ~/.xbot/paste/paste_{timestamp}.{ext}.
// Returns the file path.
func SavePasteImage(data []byte, ext string) (string, error) {
	xbotHome, err := getXbotHome()
	if err != nil {
		return "", err
	}

	pasteDir := filepath.Join(xbotHome, "paste")
	if err := os.MkdirAll(pasteDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create paste dir: %w", err)
	}

	filename := fmt.Sprintf("paste_%s.%s", time.Now().Format("20060102_150405"), ext)
	path := filepath.Join(pasteDir, filename)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("failed to write paste image: %w", err)
	}

	// Clean up old paste images
	go cleanupOldPasteImages(pasteDir, maxPasteKeep)

	return path, nil
}

// cleanupOldPasteImages keeps only the most recent `maxKeep` files in the directory.
func cleanupOldPasteImages(dir string, maxKeep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= maxKeep {
		return
	}

	// Sort by modification time (oldest first)
	type fileInfo struct {
		name    string
		modTime time.Time
	}
	var files []fileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: e.Name(), modTime: info.ModTime()})
	}

	if len(files) <= maxKeep {
		return
	}

	// Simple sort by modTime
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			if files[i].modTime.After(files[j].modTime) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}

	// Delete oldest files
	toDelete := len(files) - maxKeep
	for i := 0; i < toDelete; i++ {
		path := filepath.Join(dir, files[i].name)
		if err := os.Remove(path); err != nil {
			log.WithError(err).WithField("file", files[i].name).Warn("Failed to delete old paste image")
		}
	}
}

// detectMimeType tries to detect the MIME type from image data magic bytes.
func detectMimeType(data []byte) string {
	if len(data) >= 8 && bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return "image/png"
	}
	if len(data) >= 3 && bytes.HasPrefix(data, []byte("\xFF\xD8\xFF")) {
		return "image/jpeg"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("RIFF")) && bytes.HasPrefix(data[8:], []byte("WEBP")) {
		return "image/webp"
	}
	if len(data) >= 12 && bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")) {
		return "image/gif"
	}
	// Default to PNG (clipboard images are typically PNG screenshots)
	return "image/png"
}

// extFromMime returns the file extension for a given MIME type.
func extFromMime(mime string) string {
	switch mime {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}

// EncodeToMediaContent converts raw image data to a MediaContent struct.
func EncodeToMediaContent(data []byte, mimeType, filename string) ch.MediaContent {
	return ch.MediaContent{
		MIMEType: mimeType,
		Base64:   base64.StdEncoding.EncodeToString(data),
		Filename: filename,
	}
}

// handlePasteCommand handles the /paste slash command.
// Reads an image from the clipboard, compresses if needed, saves to disk,
// and sends it as an inline media message.
func (m *cliModel) handlePasteCommand() tea.Cmd {
	data, mimeType, err := ReadClipboardImage()
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("📎 %s", err.Error()), feedbackWarning)
		return nil
	}

	// Compress if needed
	data, mimeType, err = CompressImage(data, mimeType, maxImageBytes)
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("📎 %s", err.Error()), feedbackWarning)
		return nil
	}

	// Save to disk for reference
	ext := extFromMime(mimeType)
	path, err := SavePasteImage(data, ext)
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("📎 保存图片失败: %s", err.Error()), feedbackWarning)
		return nil
	}

	// Build display text (shown in terminal, not sent to LLM)
	filename := filepath.Base(path)
	sizeKB := len(data) / 1024
	displayText := fmt.Sprintf("📎 已粘贴图片 (%s, %dKB)", filename, sizeKB)

	// Build MediaContent
	mediaContent := EncodeToMediaContent(data, mimeType, filename)

	// Send the image message
	return m.sendImageMessage(mediaContent, displayText)
}
