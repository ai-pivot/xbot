package feishu

// Feishu inbound image ingestion (multimodal vision, P2).
//
// Feishu image messages carry an opaque image_key (server-side resource). The
// model can't see the image from the key alone — download the bytes via the
// lark MessageResource API, store them in the shared view_images directory,
// and rewrite the message content to the SAME canonical markdown reference
// the web upload path and the view_image tool produce:
//
//	![图片](/api/files/viewimg/<uuid>.<ext>)
//
// One reference format, three consumers:
//   - LLM vision resolver (serverapp.imageResolver) → base64 content parts
//   - web frontend history rendering (cookie-auth <img>, /api/files/viewimg)
//   - vision-off degrade → text placeholder (parseMultimodalContent)
//
// Download failures degrade to the legacy <image image_key=...> tag — the
// message still flows (the model sees the tag; the user can be told to re-send).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"xbot/config"
	log "xbot/logger"
)

// feishuImageMaxBytes caps a single downloaded message image.
const feishuImageMaxBytes = 20 << 20

// downloadAndStoreFeishuImage downloads a message image by (messageID,
// imageKey) and stores it under <xbotHome>/view_images/. Returns the canonical
// markdown reference ("/api/files/viewimg/<uuid>.<ext>") or "" on failure
// (caller falls back to the legacy image_key tag).
func (f *FeishuChannel) downloadAndStoreFeishuImage(messageID, imageKey string) string {
	if f.client == nil || messageID == "" || imageKey == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()
	resp, err := f.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"message_id": messageID, "image_key": imageKey}).
			Warn("feishu vision: download message image failed")
		return ""
	}
	if !resp.Success() {
		log.WithFields(log.Fields{"message_id": messageID, "image_key": imageKey, "code": resp.Code, "msg": resp.Msg}).
			Warn("feishu vision: message image download API error")
		return ""
	}
	if resp.File == nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.File, feishuImageMaxBytes+1))
	if err != nil {
		log.WithError(err).WithField("image_key", imageKey).Warn("feishu vision: read image body failed")
		return ""
	}
	if len(data) == 0 || len(data) > feishuImageMaxBytes {
		log.WithFields(log.Fields{"image_key": imageKey, "size": len(data)}).Warn("feishu vision: image size out of bounds")
		return ""
	}
	ext := sniffFeishuImageExt(data)
	if ext == "" {
		log.WithField("image_key", imageKey).Warn("feishu vision: unrecognized image format (not png/jpeg/gif/webp/bmp/tiff)")
		return ""
	}
	dir := filepath.Join(config.XbotHome(), "view_images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Warn("feishu vision: create view_images dir failed")
		return ""
	}
	id := uuid.New().String() + "." + ext
	if err := os.WriteFile(filepath.Join(dir, id), data, 0o644); err != nil {
		log.WithError(err).Warn("feishu vision: store image failed")
		return ""
	}
	ref := "/api/files/viewimg/" + id
	log.WithFields(log.Fields{"image_key": imageKey, "stored": ref, "bytes": len(data)}).
		Info("feishu vision: message image stored for multimodal input")
	return ref
}

// sniffFeishuImageExt detects the image format from magic bytes (same set as
// the view_image tool / resolver: png/jpeg/gif/webp/bmp/tiff).
func sniffFeishuImageExt(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return "jpeg"
	case bytes.HasPrefix(data, []byte("GIF87a")), bytes.HasPrefix(data, []byte("GIF89a")):
		return "gif"
	case bytes.HasPrefix(data, []byte("BM")):
		return "bmp"
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return "tiff"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "webp"
	default:
		return ""
	}
}

// feishuImageRef returns the vision-ready markdown reference for a message
// image, or "" when the download failed (caller keeps the legacy tag).
// Shared by the plain "image" message type and rich-text (post) img elements.
func (f *FeishuChannel) feishuImageRef(messageID, imageKey string) string {
	if ref := f.downloadAndStoreFeishuImage(messageID, imageKey); ref != "" {
		return fmt.Sprintf("![图片](%s)", ref)
	}
	return ""
}
