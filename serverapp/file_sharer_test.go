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
	// 名字必须被**统一 sanitize** 成 URL 安全片段（空格 → `_`）：key 一旦含空格，
	// `url.QueryEscape` 就会编出 `+`，而 `+` 只在按 query 语义解码的客户端里是空格
	// ⇒ 换个客户端（字面 `+` / `%2B` / 二次编码）就变成另一个 key ⇒ share 链接 404
	// （用户 2026-09-19 实测根因）。对用户可见的名字由 markdown 标签承载（tools 层
	// 的 display name 不变），key 只负责可移植 —— 见 urlSafeKeyName。
	if !strings.HasSuffix(dest, "my_chart.png") {
		t.Fatalf("key name must be URL-safe (space → _), got: %s", dest)
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

// 用户 2026-09-19：AI share 出来的链接打不开（**带鉴权**仍返回 not_found）——
// 根因是 key 里含**空格**，`url.QueryEscape` 把它编码成 `+`；而 `+` 只在"按 query
// 语义解码"的客户端里等于空格，换个客户端（字面 `+` / `%2B` / 二次编码）服务端就
// 收到**另一个 key** ⇒ 404。守护：key 必须 unreserved-only（URL 里不出现 `+`/空白），
// 且「+ 语义」与「%20 语义」两种解码必须得到**同一个 key**（编码无关）。
func TestWebFileSharer_KeyIsURLSafeAndEncodingAgnostic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Ferrite 专用高性能推理引擎架构改进方案.md")
	if err := os.WriteFile(src, []byte("MD"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewWebFileSharer(nil, t.TempDir())
	u, err := s.ShareFile(src, "")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "/api/files/download?key="
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("url = %q", u)
	}
	if strings.ContainsAny(u, "+ \t") {
		t.Fatalf("URL 不得含 '+' 或空白（客户端/解码器语义分歧会导致 404）：%q", u)
	}
	raw := strings.TrimPrefix(u, prefix)
	// 「+ 语义」（Go Query().Get / JS URLSearchParams）与「%20 语义」（PathUnescape）
	// 必须解出同一个 key —— 这正是本 bug 的判别点（旧实现含空格 ⇒ 前者 `Ferrite 专用…`、
	// 后者 `Ferrite+专用…`，两者不等）。
	plusKey, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	pathKey, err := url.PathUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	if plusKey != pathKey {
		t.Fatalf("两种解码语义解出不同 key：\n  + 语义  = %q\n  %%20 语义 = %q", plusKey, pathKey)
	}
	if strings.ContainsAny(plusKey, " \t") {
		t.Fatalf("key 必须是 URL 安全（unreserved-only），got %q", plusKey)
	}
	// 同名文件（不同会话各自分享）不得冲突：key 路径里带新铸的 uuid 目录。
	u2, err := s.ShareFile(src, "")
	if err != nil {
		t.Fatal(err)
	}
	if u2 == u {
		t.Fatal("同名文件的两次分享必须是不同的 key（uuid 目录承担唯一性）")
	}
}
