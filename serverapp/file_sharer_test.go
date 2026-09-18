package serverapp

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/channel/web"
)

// 本地存储后端（默认 —— 无云 OSS 配置）：share_file 必须把文件 copy 到
// <xbotHome>/uploads/agent/<uuid>/<name>（HTTP 处理器按 <root>/<key> 读盘），
// 并返回同源、稳定的下载 URL（不过期）。
func TestWebFileSharer_LocalCopiesFileAndReturnsURL(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "chart.png")
	if err := os.WriteFile(src, []byte("PNGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewWebFileSharer(nil, home) // nil provider ⇒ 本地后端
	u, err := s.ShareFile(src, "my chart.png")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}

	if !strings.HasPrefix(u, "/api/files/download?key=") {
		t.Fatalf("URL must be the same-origin download endpoint, got %q", u)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(u, "/api/files/download?"))
	if err != nil {
		t.Fatalf("parse URL query: %v", err)
	}
	key := q.Get("key")
	if !strings.HasPrefix(key, "agent/") {
		t.Fatalf("key must live in the agent/ namespace (separate from user uploads), got %q", key)
	}
	if q.Get("inline") != "1" {
		t.Fatalf("image URL must carry inline=1 so the browser renders it inline, got %q", u)
	}

	// 文件必须真的落盘 —— 该端点读的是 <uploadRoot>/<key>。
	dest := filepath.Join(web.LocalUploadRoot(home), key)
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("published file not readable at %s: %v", dest, err)
	}
	if string(data) != "PNGDATA" {
		t.Fatalf("published content = %q, want %q", data, "PNGDATA")
	}
	if !strings.HasSuffix(dest, "my chart.png") {
		t.Fatalf("display name must be preserved: %s", dest)
	}
}

// 非图片文件不带 inline=1（attachment 语义 —— 浏览器下载而不是内联渲染）。
func TestWebFileSharer_NonImageHasNoInline(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "report.pdf")
	if err := os.WriteFile(src, []byte("PDF"), 0o600); err != nil {
		t.Fatal(err)
	}

	u, err := NewWebFileSharer(nil, home).ShareFile(src, "")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}
	if strings.Contains(u, "inline=1") {
		t.Fatalf("non-image URL must not be inline: %q", u)
	}
	if !strings.Contains(u, "report.pdf") {
		t.Fatalf("default display name must be the filename: %q", u)
	}
}

// 同名文件的两次发布必须互不覆盖（uuid 命名空间）—— 旧 URL 保持有效。
func TestWebFileSharer_EachPublishGetsItsOwnKey(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "a.txt")
	if err := os.WriteFile(src, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewWebFileSharer(nil, home)

	u1, err := s.ShareFile(src, "a.txt")
	if err != nil {
		t.Fatalf("ShareFile #1: %v", err)
	}
	if err := os.WriteFile(src, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	u2, err := s.ShareFile(src, "a.txt")
	if err != nil {
		t.Fatalf("ShareFile #2: %v", err)
	}
	if u1 == u2 {
		t.Fatalf("each publish must mint a fresh key, got the same URL twice: %q", u1)
	}
	for i, u := range []string{u1, u2} {
		q, _ := url.ParseQuery(strings.TrimPrefix(u, "/api/files/download?"))
		if _, err := os.Stat(filepath.Join(web.LocalUploadRoot(home), q.Get("key"))); err != nil {
			t.Fatalf("published file #%d missing: %v", i+1, err)
		}
	}
}

// 路径分隔符等危险字符不得进入 key 的文件名段（防目录穿越 / 非法文件名）。
func TestWebFileSharer_SanitizesDisplayName(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "x.txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	u, err := NewWebFileSharer(nil, home).ShareFile(src, "../../etc/pa:sswd")
	if err != nil {
		t.Fatalf("ShareFile: %v", err)
	}
	q, _ := url.ParseQuery(strings.TrimPrefix(u, "/api/files/download?"))
	key := q.Get("key")
	if strings.Contains(key, "..") {
		t.Fatalf("key must not contain path traversal: %q", key)
	}
	// 目录分隔符已被替换（'/' 不是合法文件名字符）。
	segs := strings.Split(key, "/")
	if len(segs) != 3 {
		t.Fatalf("key must be exactly agent/<uuid>/<name>, got %q", key)
	}
	if strings.ContainsAny(segs[2], "/\\:") {
		t.Fatalf("filename segment must be sanitized, got %q", segs[2])
	}
}

// 不可读的源文件必须报错（不产生半成品 URL）。
func TestWebFileSharer_MissingSourceFails(t *testing.T) {
	if _, err := NewWebFileSharer(nil, t.TempDir()).ShareFile("/nonexistent/nope.png", ""); err == nil {
		t.Fatal("expected error for a missing source file")
	}
}
