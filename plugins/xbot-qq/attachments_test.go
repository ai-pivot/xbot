package main

// 约束 3 验收：
//   - 给图（httptest 200）→ 本地文件存在、sha256 可复算、注入含**绝对路径**
//   - 给 404 → 注入含 error= 且消息其余部分正常处理
//   - 语音优先 voice_wav_url；asr_refer_text 有值一并注入
//   - 非白名单消息带附件时仍然**零外部调用**（不下载）

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// pngBytes 是最小的合法 PNG 头（够判类型与算 sha256）。
var pngBytes = []byte("\x89PNG\r\n\x1a\n" + "\x00\x00\x00\rIHDR" + "fake-payload")

// wavBytes 是 RIFF/WAVE 头。
var wavBytes = []byte("RIFF\x24\x00\x00\x00WAVEfmt ")

// newAttachmentHarness 构造一个附件根指向临时目录的 harness。
func newAttachmentHarness(t *testing.T, allow, admins string) *gateHarness {
	t.Helper()
	h := newGateHarness(t, allow, admins)
	h.app.attachments = &AttachmentStore{
		UploadsRoot: t.TempDir(),
		Now:         func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) },
		logf:        t.Logf,
	}
	return h
}

// sendWithAttachments 喂一条带 attachments 的 C2C 消息。
func sendWithAttachments(h *gateHarness, t *testing.T, openid, msgID, content string, atts []map[string]any) {
	t.Helper()
	payload := map[string]any{
		"id":          msgID,
		"content":     content,
		"author":      map[string]any{"user_openid": openid},
		"attachments": atts,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	h.app.handleGatewayEvent(h.enc, context.Background(), GatewayEvent{
		Type: EventC2CMessageCreate,
		ID:   "EVT-" + msgID,
		Data: raw,
	})
}

// inboundContent 取出唯一一个 send_inbound 帧里的 content。
func inboundContent(t *testing.T, out string) string {
	t.Helper()
	frames := decodeFrames(t, out)
	if len(frames) != 1 || frames[0]["method"] != "send_inbound" {
		t.Fatalf("frames = %v, want exactly one send_inbound", frames)
	}
	return frames[0]["params"].(map[string]any)["content"].(string)
}

// TestAttachmentImageDownloadedAndInjected 图片：落盘 + sha256 + 绝对路径注入。
func TestAttachmentImageDownloadedAndInjected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "*", "")
	sendWithAttachments(h, t, "USER-1", "MSG-ATT-1", "看看这张图", []map[string]any{{
		"url":          srv.URL + "/a.png",
		"filename":     "a.png",
		"content_type": "image/png",
		"size":         len(pngBytes),
		"width":        10,
		"height":       10,
	}})

	content := inboundContent(t, h.out.String())
	if !strings.Contains(content, "看看这张图") {
		t.Fatalf("original content lost: %q", content)
	}
	// 绝对路径 + 当天日期目录。
	dayDir := "/2026-10-26/"
	if !strings.Contains(content, `<image path="`) || !strings.Contains(content, dayDir) {
		t.Fatalf("injection missing <image path=...> under date dir: %q", content)
	}
	// 从注入里取出路径，验证文件真实存在、sha256 可复算。
	path := attrValue(t, content, "path")
	if !strings.HasPrefix(path, "/") {
		t.Fatalf("path = %q, want absolute", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	sum := sha256.Sum256(got)
	want := sha256.Sum256(pngBytes)
	if hex.EncodeToString(sum[:]) != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256 mismatch: got %x want %x", sum, want)
	}
	// 文件名必须带 sha256 前缀（防覆盖）。
	if base := path[strings.LastIndex(path, "/")+1:]; !strings.HasPrefix(base, hex.EncodeToString(want[:])[:16]) {
		t.Fatalf("filename %q does not carry sha256 prefix", base)
	}
	if !strings.Contains(content, `mime="image/png"`) || !strings.Contains(content, `size="`+itoa(len(pngBytes))+`"`) {
		t.Fatalf("injection missing mime/size: %q", content)
	}
}

// TestAttachmentDownloadFailureIsInjected 404 不许静默丢消息。
func TestAttachmentDownloadFailureIsInjected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusForbidden)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "*", "")
	sendWithAttachments(h, t, "USER-1", "MSG-ATT-404", "图挂了也要回", []map[string]any{{
		"url":          srv.URL + "/missing.png",
		"content_type": "image/png",
	}})

	content := inboundContent(t, h.out.String())
	if !strings.Contains(content, "图挂了也要回") {
		t.Fatalf("message body must survive attachment failure: %q", content)
	}
	if !strings.Contains(content, `error="download failed: 403"`) {
		t.Fatalf("injection must carry the failure reason: %q", content)
	}
	if !strings.Contains(content, `index="0"`) {
		t.Fatalf("injection must carry the attachment index: %q", content)
	}
	if strings.Contains(content, "<image ") {
		t.Fatalf("failed attachment must not be injected as image: %q", content)
	}
}

// TestAttachmentVoicePrefersWavAndCarriesASR 语音：优先 voice_wav_url，ASR 一并注入。
func TestAttachmentVoicePrefersWavAndCarriesASR(t *testing.T) {
	var wavHits, urlHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/voice.wav" {
			wavHits++
			_, _ = w.Write(wavBytes)
			return
		}
		urlHits++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "*", "")
	sendWithAttachments(h, t, "USER-1", "MSG-ATT-VOICE", "听一下", []map[string]any{{
		"url":            srv.URL + "/raw.amr",
		"voice_wav_url":  srv.URL + "/voice.wav",
		"content_type":   "voice",
		"size":           len(wavBytes),
		"asr_refer_text": "你好呀",
	}})

	content := inboundContent(t, h.out.String())
	if wavHits != 1 || urlHits != 0 {
		t.Fatalf("voice must download voice_wav_url (wav=%d raw=%d)", wavHits, urlHits)
	}
	if !strings.Contains(content, "<voice ") || !strings.Contains(content, `asr_refer_text="你好呀"`) {
		t.Fatalf("voice injection missing asr text: %q", content)
	}
	if !strings.HasSuffix(attrValue(t, content, "path"), ".wav") {
		t.Fatalf("voice file should be .wav: %q", content)
	}
}

// TestAttachmentNotDownloadedForNonAllowlisted 非白名单带附件仍然零外部调用。
func TestAttachmentNotDownloadedForNonAllowlisted(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "SOMEONE-ELSE", "")
	sendWithAttachments(h, t, "STRANGER", "MSG-ATT-DENY", "hi", []map[string]any{{
		"url":          srv.URL + "/a.png",
		"content_type": "image/png",
	}})

	if hits != 0 {
		t.Fatalf("non-allowlisted message must not trigger downloads, got %d hits", hits)
	}
	if h.out.Len() != 0 {
		t.Fatalf("non-allowlisted message must not reach host: %s", h.out.String())
	}
	if calls := h.api.Calls(); len(calls) != 0 {
		t.Fatalf("non-allowlisted message must not call API: %+v", calls)
	}
}

// TestAttachmentExtAndSizeHelpers 扩展名与兜底逻辑。
func TestAttachmentExtAndSizeHelpers(t *testing.T) {
	cases := []struct {
		ct   string
		data []byte
		want string
	}{
		{"image/jpeg", nil, ".jpg"},
		{"image/gif", nil, ".gif"},
		{"voice", nil, ".wav"},
		{"video/mp4", nil, ".mp4"},
		{"file", pngBytes, ".png"}, // 未知 ct → 魔数
		{"", wavBytes, ".wav"},
		{"", []byte("%PDF-1.4"), ".pdf"},
		{"", []byte("???"), ".bin"},
	}
	for _, tc := range cases {
		if got := attachmentExt(MessageAttachment{ContentType: tc.ct}, tc.data); got != tc.want {
			t.Fatalf("attachmentExt(%q) = %q, want %q", tc.ct, got, tc.want)
		}
	}
	if d := (MessageAttachment{ContentType: "voice", URL: "u", VoiceWavURL: "w"}).DownloadURL(); d != "w" {
		t.Fatalf("voice DownloadURL = %q, want wav url", d)
	}
}

// attrValue 从注入文本里取第一个 key="..." 的值。
func attrValue(t *testing.T, content, key string) string {
	t.Helper()
	marker := key + `="`
	i := strings.Index(content, marker)
	if i < 0 {
		t.Fatalf("attribute %q not found in %q", key, content)
	}
	rest := content[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		t.Fatalf("unterminated attribute %q", key)
	}
	return rest[:j]
}

// itoa 避免额外 import strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// TestAttachmentSameContentSameFilename 同内容 → 同名（只一份）；不同内容 → 不同文件。
//
// 这条用例对"文件名带 sha256"有判别力：把名字改成固定值（如 fixed.png）时，
// 两份不同内容会互相覆盖，目录里只剩 1 个文件 → 本用例变红。
func TestAttachmentSameContentSameFilename(t *testing.T) {
	pngB := []byte("\x89PNG\r\n\x1a\n" + "different-payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b.png" {
			_, _ = w.Write(pngB)
			return
		}
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "*", "")
	sendWithAttachments(h, t, "USER-1", "MSG-ATT-DUP", "三张图", []map[string]any{
		{"url": srv.URL + "/1.png", "content_type": "image/png"},
		{"url": srv.URL + "/2-different-name.png", "content_type": "image/png"}, // 与上一张同内容
		{"url": srv.URL + "/b.png", "content_type": "image/png"},                // 不同内容
	})
	content := inboundContent(t, h.out.String())

	paths := allAttrValues(content, "path")
	if len(paths) != 3 {
		t.Fatalf("paths = %v, want 3 injections", paths)
	}
	if paths[0] != paths[1] {
		t.Fatalf("identical content must map to the same file: %v", paths)
	}
	if paths[0] == paths[2] {
		t.Fatalf("different content must map to different files: %v", paths)
	}
	dir := paths[0][:strings.LastIndex(paths[0], "/")]
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir %s has %d files %v, want 2 (same sha256 → same name, different → different)", dir, len(entries), names)
	}
}

// allAttrValues 取注入文本里所有 key="..." 的值。
func allAttrValues(content, key string) []string {
	marker := key + `="`
	var out []string
	for {
		i := strings.Index(content, marker)
		if i < 0 {
			return out
		}
		rest := content[i+len(marker):]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			return out
		}
		out = append(out, rest[:j])
		content = rest[j+1:]
	}
}

// TestImageOnlyMessageIsAccepted 纯图片消息（content 为空、只有 attachments）必须被处理。
//
// 2026-10-26 故障复盘：官方图片/语音消息的 content 是空的，而 Inbound.Valid()
// 要求正文非空 ⇒ 整条消息被判 incomplete 直接丢弃，用户侧表现为「发图没反应/像卡死」。
// 这条用例锁住修复：content 为空但有 attachments 时，依然要走下载 + 注入 + send_inbound。
func TestImageOnlyMessageIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	h := newAttachmentHarness(t, "*", "")
	// 注意：正文传空字符串 —— 这就是官方图片消息的真实形态。
	sendWithAttachments(h, t, "USER-1", "MSG-IMG-ONLY", "", []map[string]any{{
		"url":          srv.URL + "/only.png",
		"filename":     "only.png",
		"content_type": "image/png",
		"size":         len(pngBytes),
	}})

	out := h.out.String()
	if out == "" {
		t.Fatalf("纯图片消息被丢弃：没有 send_inbound 帧（日志会出现 incomplete / empty content）")
	}
	content := inboundContent(t, out)
	if !strings.Contains(content, `<image path="`) {
		t.Fatalf("纯图片消息未注入图片路径: %q", content)
	}
	path := attrValue(t, content, "path")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("图片未落盘: %v", err)
	}
	if len(got) != len(pngBytes) {
		t.Fatalf("落盘大小 = %d, want %d", len(got), len(pngBytes))
	}
	// 身份头仍然要在（图片消息也要有发送者/管理员标注）。
	if !strings.Contains(content, "【QQ") {
		t.Fatalf("纯图片消息缺少身份头: %q", content)
	}
}
