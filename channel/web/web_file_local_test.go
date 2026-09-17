package web

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 2026-09-16 用户报告：web 端上传图片报 `image.png: file storage not configured`。
// 要求：**默认 storage 是本地 static server**（免配置可用），而不是"未配置"。
//
// 契约（本文件钉死）：
//
//	① provider 为 nil（未配置云 OSS）时，上传必须成功 —— 落盘 <xbotHome>/uploads/<key>；
//	② 下载/内联必须由同源读盘返回字节（不再 302 到签名 URL）：
//	   · 默认 Content-Disposition: attachment（与云侧 attname 安全语义一致）
//	   · ?inline=1 才 inline（编辑器/历史里的 <img src>）
//	   · 始终带 X-Content-Type-Options: nosniff
//	③ key 不存在 → 404（不泄露路径）。
func newLocalUploadTestChannel(t *testing.T) (*WebChannel, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	wc := &WebChannel{} // 显式 nil provider —— 正是"未配置云 OSS"的现场
	return wc, home
}

func multipartBody(t *testing.T, filename string, data []byte) (*multipart.Writer, *strings.Reader, string) {
	t.Helper()
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return mw, strings.NewReader(buf.String()), mw.FormDataContentType()
}

func TestLocalUpload_DefaultStorage_NoOSSConfigured(t *testing.T) {
	wc, home := newLocalUploadTestChannel(t)
	png := []byte("\x89PNG\r\n\x1a\nfakepng")
	_, body, ctype := multipartBody(t, "image.png", png)

	req := httptest.NewRequest(http.MethodPost, "/api/files/upload", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	wc.handleFileUpload(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("upload with nil provider must succeed (default local static), got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			UploadKey string `json:"upload_key"`
			Name      string `json:"name"`
			Size      int    `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	out := env.Data
	if !strings.HasPrefix(out.UploadKey, "uploads/") {
		t.Fatalf("upload_key must be an uploads/ key, got %q", out.UploadKey)
	}
	onDisk := filepath.Join(home, "uploads", filepath.FromSlash(out.UploadKey))
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("upload must be written to %s: %v", onDisk, err)
	}
}

func TestLocalDownload_InlineVsAttachment(t *testing.T) {
	wc, home := newLocalUploadTestChannel(t)
	key := "uploads/1/abcdef.png"
	dst := filepath.Join(home, "uploads", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("\x89PNG\r\n\x1a\nbytes")
	if err := os.WriteFile(dst, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	// 默认：强制附件下载（与云侧 attname 语义一致）
	req := httptest.NewRequest(http.MethodGet, "/api/files/download?key="+key, nil)
	rec := httptest.NewRecorder()
	wc.handleFileDownload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("download must serve from disk, got %d: %s", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("default download must force attachment, got Content-Disposition=%q", cd)
	}
	if got := rec.Body.Bytes(); string(got) != string(payload) {
		t.Fatalf("body mismatch: %q", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff must always be set")
	}

	// ?inline=1：内联渲染（编辑器 <img src>）
	req2 := httptest.NewRequest(http.MethodGet, "/api/files/download?key="+key+"&inline=1", nil)
	rec2 := httptest.NewRecorder()
	wc.handleFileDownload(rec2, req2)
	if cd := rec2.Header().Get("Content-Disposition"); cd != "inline" {
		t.Fatalf("inline=1 must render inline, got Content-Disposition=%q", cd)
	}
	if rec2.Code != http.StatusOK {
		t.Fatalf("inline download code=%d", rec2.Code)
	}
}

func TestLocalDownload_MissingKey_NotFound(t *testing.T) {
	wc, _ := newLocalUploadTestChannel(t)
	req := httptest.NewRequest(http.MethodGet, "/api/files/download?key=uploads/9/nope.png", nil)
	rec := httptest.NewRecorder()
	wc.handleFileDownload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file must 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// 目录穿越必须继续被拦住（key 校验在 handler 入口）。
func TestLocalDownload_RejectsTraversal(t *testing.T) {
	wc, _ := newLocalUploadTestChannel(t)
	for _, bad := range []string{"../etc/passwd", "uploads/../../etc/passwd", "other/x.png"} {
		req := httptest.NewRequest(http.MethodGet, "/api/files/download?key="+bad, nil)
		rec := httptest.NewRecorder()
		wc.handleFileDownload(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("key %q must be rejected with 400, got %d", bad, rec.Code)
		}
	}
}
