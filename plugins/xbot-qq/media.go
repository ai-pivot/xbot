package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Markdown 图片一律走**平台托管**（qqbot-media://）。
//
// 2026-10-27 定论（用户实测确认）：外部图床彻底不需要了。
//
// · 公网 URL（无论是否签名）会被平台「下载转存」，转存副本有自己的时效，
//   客户端最终显示「资源已过期」——7 天签名和 10 年签名都一样过期，
//   因为过期的是**转存副本**，不是我们的 URL。
// · qqbot-media:// 是平台自己托管的资源：90 天滑动过期，每被引用一次就顺延，
//   不依赖任何外链、CDN、签名。这是官方为 markdown 图片设计的通道。
//
// 因此七牛 / S3 / 自建图床 / 签名逻辑**全部删除**（用户指令：「这些逻辑完全没用」）。

// MediaUploadError 用于把平台侧的错误码带上来，便于上层决定是否降级。
type MediaUploadError struct {
	Op  string
	Err error
}

func (e *MediaUploadError) Error() string { return "media " + e.Op + ": " + e.Err.Error() }
func (e *MediaUploadError) Unwrap() error { return e.Err }

// UploadMarkdownImage 把图片二进制上传到 QQ 平台，返回 qqbot-media:// 引用。
//
// 三步（官方《素材管理》）：
//  1. POST /v2/media-images/prepare  {file_name, file_size, content_type, md5, sha1}
//     → upload_id + presigned_url（4 小时内有效）
//  2. PUT <presigned_url>  直接传二进制；Content-Type 必须与 prepare 声明的完全一致，
//     否则 COS 返回 403。响应头 ETag 去掉引号即为文件 MD5。
//  3. POST /v2/media-images/complete {upload_id, md5}
//     → url: qqbot-media://image/v1/<token>，ttl 默认 7776000 秒（90 天，滑动）
func (c *APIClient) UploadMarkdownImage(ctx context.Context, data []byte, fileName, contentType string) (string, error) {
	if len(data) == 0 {
		return "", &MediaUploadError{Op: "prepare", Err: fmt.Errorf("empty image")}
	}
	if contentType == "" {
		contentType = "image/png"
	}
	// 平台只校验扩展名与 content_type 是否匹配，所以文件名的扩展名必须跟着 content_type 走。
	if !strings.HasSuffix(strings.ToLower(fileName), extForContentType(contentType)) {
		fileName = strings.TrimSuffix(fileName, filepathExt(fileName)) + extForContentType(contentType)
	}

	sumMD5 := md5.Sum(data)
	sumSHA1 := sha1.Sum(data)
	md5Hex := hex.EncodeToString(sumMD5[:])
	sha1Hex := hex.EncodeToString(sumSHA1[:])

	var prep struct {
		UploadID     string `json:"upload_id"`
		PresignedURL string `json:"presigned_url"`
		ExpiresIn    uint32 `json:"expires_in"`
	}
	if err := c.do(ctx, "media_prepare", http.MethodPost, "/v2/media-images/prepare", map[string]any{
		"file_name":    fileName,
		"file_size":    len(data),
		"content_type": contentType,
		"md5":          md5Hex,
		"sha1":         sha1Hex,
	}, &prep); err != nil {
		return "", &MediaUploadError{Op: "prepare", Err: err}
	}
	if prep.PresignedURL == "" || prep.UploadID == "" {
		return "", &MediaUploadError{Op: "prepare", Err: fmt.Errorf("platform returned empty upload_id/presigned_url")}
	}

	// PUT 二进制。注意：这里**不能**带 Authorization（是 COS 预签名，不认 QQBot token）。
	upCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(upCtx, http.MethodPut, prep.PresignedURL, bytes.NewReader(data))
	if err != nil {
		return "", &MediaUploadError{Op: "put", Err: err}
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &MediaUploadError{Op: "put", Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return "", &MediaUploadError{Op: "put", Err: fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, body)}
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		// 没有 ETag 时退回本地算的 MD5（官方说明二者应一致）。
		etag = md5Hex
	}

	var done struct {
		ImageID string `json:"image_id"`
		URL     string `json:"url"`
		TTL     uint32 `json:"ttl"`
	}
	if err := c.do(ctx, "media_complete", http.MethodPost, "/v2/media-images/complete", map[string]any{
		"upload_id": prep.UploadID,
		"md5":       etag,
	}, &done); err != nil {
		return "", &MediaUploadError{Op: "complete", Err: err}
	}
	if !strings.HasPrefix(done.URL, "qqbot-media://") {
		return "", &MediaUploadError{Op: "complete", Err: fmt.Errorf("unexpected url %q (want qqbot-media://)", done.URL)}
	}
	return done.URL, nil
}

// extForContentType 返回平台允许的扩展名。平台只支持 png/jpeg/gif/webp。
func extForContentType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func filepathExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

// imageDimensions 从图片字节里读出像素宽高。
//
// 为什么需要：QQ 的 markdown 图片语法要求**两个尺寸都给**——
// `![说明 #宽px #高px](url)`。只写一个（`![图 #300px]`）时客户端不渲染，
// 表现为「消息发出去了但没图」，且平台侧完全无错（2026-10-27 实测差异）。
//
// 只支持 png/gif/jpeg（平台允许的格式里这三种最常见），读不出时返回 0,0。
func imageDimensions(data []byte) (int, int) {
	if len(data) < 24 {
		return 0, 0
	}
	// PNG: 8 字节签名 + IHDR，宽高在 offset 16..24（大端）
	if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
		w := int(binary.BigEndian.Uint32(data[16:20]))
		h := int(binary.BigEndian.Uint32(data[20:24]))
		return w, h
	}
	// GIF: "GIF87a"/"GIF89a"，宽高在 offset 6..10（小端）
	if len(data) >= 10 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F' {
		w := int(binary.LittleEndian.Uint16(data[6:8]))
		h := int(binary.LittleEndian.Uint16(data[8:10]))
		return w, h
	}
	// JPEG: 扫 SOFn 段
	if data[0] == 0xFF && data[1] == 0xD8 {
		i := 2
		for i+9 < len(data) {
			if data[i] != 0xFF {
				i++
				continue
			}
			marker := data[i+1]
			if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
				i += 2
				continue
			}
			if i+3 >= len(data) {
				break
			}
			segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
			// SOF0..SOF15，排除 DHT(C4)/JPG(C8)/DAC(CC)
			if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
				if i+9 < len(data) {
					h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
					w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
					return w, h
				}
				break
			}
			i += 2 + segLen
		}
	}
	return 0, 0
}

// markdownImageTag 生成 QQ 能渲染的 markdown 图片标签。
//
// 两个尺寸都要给（见 imageDimensions 注释）。读不出尺寸时退到一个保守默认值，
// 宁可显示得小一点，也不能因为缺尺寸导致整张图不渲染。
func markdownImageTag(ref string, w, h int) string {
	if w <= 0 || h <= 0 {
		w, h = 300, 200
	}
	// 限个上限：超大图在手机上会撑爆，按 600px 宽等比缩放。
	const maxW = 600
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	if h < 1 {
		h = 1
	}
	return fmt.Sprintf("![图片 #%dpx #%dpx](%s)", w, h, ref)
}
