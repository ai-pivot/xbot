package main

// 约束 3（御前令）：多模态消息自动落盘。
//
// 官方字段（autogen/event/c2c_message_create.html → List<MessageAttachment>）：
//
//	url / filename / width / height / size / content_type
//	voice_wav_url / asr_refer_text
//	content_type: voice | image/jpeg | image/png | image/gif | video/mp4 | file
//
// 行为：
//  1. 下载到 $XBOT_HOME/uploads/qq/<YYYY-MM-DD>/（当天目录）
//  2. 文件名 = 内容 sha256 前缀 + 扩展名（防覆盖、可复算）
//  3. 下载失败**不许静默丢消息**：把失败原因写进注入上下文
//     <attachment index="0" error="download failed: 403"/>
//  4. 成功注入：<image path="/abs/path.jpg" mime="image/jpeg" size="12345"/>
//     （沿用仓库既有 image 标签语汇：<image url=...> 的字段形状，改成 path= 指向本地绝对路径）
//  5. 语音优先 voice_wav_url；asr_refer_text 有值一并注入
//
// ⚠️ 已知卡点（不在本插件可改范围内）：host 的 vision resolver
// （serverapp/image_resolver.go#resolveFileRef）只允许 file:// 落在
// agent 工作目录或 ~/.xbot/view_images 下，本插件按任务要求落盘到
// ~/.xbot/uploads/qq/...，因此 file:// 引用不会被内联成视觉输入。
// 注入的是**绝对路径**，agent 可以用 view_image 工具读取（非视觉模型的
// 正规通路）。要真正内联需 host 侧把 uploads 加进 workspaceRoots —— 属主模块改动，
// 本任务禁止，已记为卡点上报。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MessageAttachment 是官方 attachments[] 的一个元素（只镜像用到的字段）。
type MessageAttachment struct {
	URL          string `json:"url"`
	Filename     string `json:"filename"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Size         int64  `json:"size"`
	ContentType  string `json:"content_type"`
	VoiceWavURL  string `json:"voice_wav_url"`
	AsrReferText string `json:"asr_refer_text"`
}

// IsVoice 报告是否语音附件。
func (a MessageAttachment) IsVoice() bool { return a.ContentType == "voice" }

// IsImage 报告是否图片附件。
func (a MessageAttachment) IsImage() bool {
	return strings.HasPrefix(a.ContentType, "image/")
}

// DownloadURL 返回实际要下载的地址：语音优先 voice_wav_url。
func (a MessageAttachment) DownloadURL() string {
	if a.IsVoice() && a.VoiceWavURL != "" {
		return a.VoiceWavURL
	}
	return a.URL
}

// attachmentMaxBytes 是单个附件下载上限（防大文件拖垮渠道进程）。
const attachmentMaxBytes = 64 << 20

// AttachmentStore 负责把附件下载到本地并生成注入片段。
type AttachmentStore struct {
	// UploadsRoot 是 $XBOT_HOME/uploads/qq（测试可注入临时目录）。
	UploadsRoot string
	// Client 是下载用的 HTTP 客户端；nil 用默认（30s 超时）。
	Client *http.Client
	// Now 可注入时钟（目录按日期分）。
	Now  func() time.Time
	logf func(string, ...any)
}

// NewAttachmentStore 构造下载器。xbotHome 为空时退回 $XBOT_HOME 再退回 ~/.xbot。
func NewAttachmentStore(xbotHome string, logf func(string, ...any)) *AttachmentStore {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if xbotHome == "" {
		xbotHome = os.Getenv("XBOT_HOME")
	}
	if xbotHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xbotHome = filepath.Join(home, ".xbot")
		}
	}
	return &AttachmentStore{
		UploadsRoot: filepath.Join(xbotHome, "uploads", "qq"),
		Client:      &http.Client{Timeout: 30 * time.Second},
		Now:         time.Now,
		logf:        logf,
	}
}

// Ingest 下载全部附件并返回注入文本（可能为空串）。
//
// 返回值直接拼在消息正文之后；单条附件失败只影响该条的 error 属性，
// 不影响其他附件与消息本身。
func (s *AttachmentStore) Ingest(ctx context.Context, atts []MessageAttachment) string {
	if len(atts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(atts))
	for i, a := range atts {
		parts = append(parts, s.ingestOne(ctx, i, a))
	}
	return strings.Join(parts, "\n")
}

// ingestOne 处理单个附件。
func (s *AttachmentStore) ingestOne(ctx context.Context, index int, a MessageAttachment) string {
	fail := func(reason string) string {
		s.logf("attachment[%d] failed: %s (content_type=%q filename=%q)", index, reason, a.ContentType, a.Filename)
		return fmt.Sprintf("<attachment index=%q error=%q/>", fmt.Sprint(index), reason)
	}

	url := a.DownloadURL()
	if url == "" {
		return fail("missing url")
	}

	data, err := s.download(ctx, url)
	if err != nil {
		return fail(err.Error())
	}

	written, sum, err := s.store(data, a)
	if err != nil {
		return fail("store failed: " + err.Error())
	}

	s.logf("attachment[%d] stored: %s (%d bytes, sha256=%s mime=%q)",
		index, written, len(data), sum, a.ContentType)
	return renderAttachmentTag(written, a, int64(len(data)), sum)
}

// download 取附件字节（非 2xx 也当失败，错误里带状态码）。
func (s *AttachmentStore) download(ctx context.Context, url string) ([]byte, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 官方 CDN 用 403 表示链接过期/无权限；把状态码原样写进注入文本，
		// 便于排查（不静默丢消息）。
		return nil, fmt.Errorf("download failed: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, attachmentMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) > attachmentMaxBytes {
		return nil, fmt.Errorf("download failed: body exceeds %d bytes", attachmentMaxBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("download failed: empty body")
	}
	return data, nil
}

// store 把字节写到 uploads/qq/<YYYY-MM-DD>/<sha256-16><ext>，返回绝对路径与完整 sha256。
func (s *AttachmentStore) store(data []byte, a MessageAttachment) (string, string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	dir := filepath.Join(s.UploadsRoot, now().Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	name := hexSum[:16] + attachmentExt(a, data)
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", err
	}
	return path, hexSum, nil
}

// attachmentExt 决定落盘扩展名：content_type 优先，其次原文件名，最后按魔数。
func attachmentExt(a MessageAttachment, data []byte) string {
	if ext := extFromContentType(a.ContentType); ext != "" {
		return ext
	}
	if ext := filepath.Ext(a.Filename); ext != "" && len(ext) <= 8 {
		return ext
	}
	return extFromMagic(data)
}

// extFromContentType 把官方 content_type 映射成扩展名。
func extFromContentType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "voice":
		return ".wav" // voice_wav_url 给的是 wav
	}
	return ""
}

// extFromMagic 按文件头猜扩展名（content_type 缺失/未知时兜底）。
func extFromMagic(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return ".jpg"
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png"
	case len(data) >= 6 && string(data[:3]) == "GIF":
		return ".gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return ".wav"
	case len(data) >= 4 && string(data[:4]) == "%PDF":
		return ".pdf"
	}
	return ".bin"
}

// xmlEscape 转义属性值（防止文件名/ASR 文本破坏注入结构）。
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// renderAttachmentTag 生成注入片段。
//
// 图片：<image path="/abs/x.jpg" mime="image/jpeg" size="12345"/>
// 音频：<voice path="..." mime="voice" size="..." asr_refer_text="..."/>
// 其他：<file path="..." mime="..." size="..." name="..." sha256="..."/>
func renderAttachmentTag(path string, a MessageAttachment, size int64, sha string) string {
	base := fmt.Sprintf(`path=%q mime=%q size="%d"`, path, a.ContentType, size)
	switch {
	case a.IsVoice():
		tag := "<voice " + base
		if a.AsrReferText != "" {
			tag += fmt.Sprintf(` asr_refer_text="%s"`, xmlEscape(a.AsrReferText))
		}
		return tag + "/>"
	case a.IsImage():
		return "<image " + base + "/>"
	default:
		name := a.Filename
		if name == "" {
			name = filepath.Base(path)
		}
		return fmt.Sprintf(`<file %s name="%s" sha256="%s"/>`, base, xmlEscape(name), sha)
	}
}
