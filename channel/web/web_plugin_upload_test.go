package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPluginFileUploadLargeBody — 回归：authMiddleware 曾对 /api/plugin-files/upload
// 包 1MB MaxBytesReader，壁纸/插件文件上传（普遍 >1MB）被 middleware 拦 400，
// 好不到 handler（handler 自身无上限——multipart 直传磁盘）。
//
// startTestServer 不注册 /api/plugin-files/upload（404 假绿）——本测试自建 server
// 注册真实路由（authMiddleware 包 handlePluginFileUpload，与 web.go Start 同款），
// 验证 2MB body 不被 1MB middleware 拦截。
func TestPluginFileUploadLargeBody(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)

	// 自建 server：注册真实路由（authMiddleware → handlePluginFileUpload）
	// ——与 web.go Start() 的 /api/plugin-files/upload 完全同款
	mux := http.NewServeMux()
	mux.HandleFunc("/api/plugin-files/upload", wc.authMiddleware(wc.handlePluginFileUpload))
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Register + Login → session cookie
	http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(`{"username":"u9","password":"p9"}`))
	loginResp, err := http.Post(server.URL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"u9","password":"p9"}`))
	if err != nil {
		t.Fatal(err)
	}
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == webSessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie from login")
	}

	// 2MB body（> 1MB middleware 限制）——壁纸图片常见大小
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("plugin_id", "xbot.ambience"); err != nil {
		t.Fatal(err)
	}
	fw, err := w.CreateFormFile("file", "wallpaper.png")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte("\x89PNG\r\n\x1a\n"))
	fw.Write(bytes.Repeat([]byte{0}, 2<<20)) // 2MB padding
	w.Close()

	req, _ := http.NewRequest("POST", server.URL+"/api/plugin-files/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 修复前：authMiddleware 包 1MB MaxBytesReader → ParseMultipartForm 报
	// "http: request body too large" → 400。修复后：请求到达 handler（200 或
	// 业务错误如 plugin ID 不存在——只要不是 middleware 的 body too large）。
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "request body too large") {
		t.Fatalf("2MB body blocked by authMiddleware MaxBytesReader (status=%d body=%s) — /api/plugin-files/upload must be exempt from 1MB middleware limit", resp.StatusCode, body)
	}
	if resp.StatusCode >= 500 {
		t.Fatalf("unexpected server error (status=%d body=%s)", resp.StatusCode, body)
	}
	t.Logf("plugin-files upload 2MB body → status=%d body=%s (middleware bypass ✓)", resp.StatusCode, bytes.TrimSpace(body))
}
