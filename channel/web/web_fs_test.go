package web

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers for FS API
// ---------------------------------------------------------------------------

// startFsTestServer creates a test server with FS routes registered.
func startFsTestServer(t *testing.T, wc *WebChannel) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	mux.HandleFunc("/api/fs/list", wc.authMiddleware(wc.handleFsList))
	mux.HandleFunc("/api/fs/read", wc.authMiddleware(wc.handleFsRead))
	mux.HandleFunc("/api/fs/raw", wc.authMiddleware(wc.handleFsRaw))
	mux.HandleFunc("/api/fs/search", wc.authMiddleware(wc.handleFsSearch))
	mux.HandleFunc("/api/fs/stat", wc.authMiddleware(wc.handleFsStat))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// registerAndGetCookie registers a user and returns the session cookie.
func registerAndGetCookie(t *testing.T, server *httptest.Server) string {
	t.Helper()
	http.Post(server.URL+"/api/auth/register", "application/json",
		strings.NewReader(`{"username":"fstest","password":"p1"}`))
	resp, err := http.Post(server.URL+"/api/auth/login", "application/json",
		strings.NewReader(`{"username":"fstest","password":"p1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == webSessionCookieName {
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie returned")
	return ""
}

// authedGet makes an authenticated GET request and returns the response.
func authedGet(t *testing.T, server *httptest.Server, cookie, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", server.URL+path, nil)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// mustDec decodes JSON from response body into v, closing the body.
func mustDec(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	decodeAPIData(t, resp.Body, v)
}

// ---------------------------------------------------------------------------
// resolveSafePath tests
// ---------------------------------------------------------------------------

func TestResolveSafePath(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty defaults to root", "", false},
		{"root", "/", false},
		{"simple absolute", "/tmp", false},
		{"nested absolute", "/tmp/foo/bar", false},
		{"dot current", "/tmp/./foo", false},
		{"traversal relative", "../../../etc", true},
		{"traversal absolute", "/foo/../bar", true},
		{"traversal mixed", "/foo/../../etc", true},
		{"dotdot in filename not traversal", "/foo..bar", false},
		{"dotdot as component", "/foo/..", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveSafePath(tc.input)
			if tc.wantErr && err == nil {
				t.Errorf("resolveSafePath(%q) expected error, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("resolveSafePath(%q) unexpected error: %v", tc.input, err)
			}
		})
	}
}

func TestResolveSafePathReturnsAbsolute(t *testing.T) {
	abs, err := resolveSafePath("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(abs) {
		t.Errorf("expected absolute path, got %q", abs)
	}
}

// ---------------------------------------------------------------------------
// languageFromPath tests
// ---------------------------------------------------------------------------

func TestLanguageFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"App.tsx", "typescriptreact"},
		{"index.js", "javascript"},
		{"script.py", "python"},
		{"README.md", "markdown"},
		{"config.json", "json"},
		{"values.yaml", "yaml"},
		{"deploy.yml", "yaml"},
		{"run.sh", "shell"},
		{"main.rs", "rust"},
		{"style.css", "css"},
		{"index.html", "html"},
		{"data.xml", "xml"},
		{"query.sql", "sql"},
		{"Dockerfile", "dockerfile"},
		{"Makefile", "makefile"},
		{"unknown.xyz", ""},
		{"noext", ""},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			got := languageFromPath(tc.path)
			if got != tc.want {
				t.Errorf("languageFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// isBinaryData tests
// ---------------------------------------------------------------------------

func TestIsBinaryData(t *testing.T) {
	if isBinaryData([]byte("hello world")) {
		t.Error("plain text should not be binary")
	}
	if !isBinaryData([]byte("hello\x00world")) {
		t.Error("data with NUL byte should be binary")
	}
	if isBinaryData(nil) {
		t.Error("nil should not be binary")
	}
	if isBinaryData([]byte{}) {
		t.Error("empty should not be binary")
	}
}

// ---------------------------------------------------------------------------
// handleFsList tests
// ---------------------------------------------------------------------------

func TestFsListNoAuth(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)

	resp := authedGet(t, server, "", "/api/fs/list?path=/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestFsListValid(t *testing.T) {
	dir := t.TempDir()
	// Create test files
	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("hello"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/list?path="+dir)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsListResponse
	mustDec(t, resp, &result)

	names := make(map[string]bool)
	for _, e := range result.Entries {
		names[e.Name] = true
	}
	if !names["file1.txt"] {
		t.Error("file1.txt should be in entries")
	}
	if !names["subdir"] {
		t.Error("subdir should be in entries")
	}
	if names[".hidden"] {
		t.Error(".hidden should not be in entries (hidden by default)")
	}
}

func TestFsListShowHidden(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("secret"), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/list?path="+dir+"&showHidden=true")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsListResponse
	mustDec(t, resp, &result)

	found := false
	for _, e := range result.Entries {
		if e.Name == ".hidden" {
			found = true
		}
	}
	if !found {
		t.Error(".hidden should be visible with showHidden=true")
	}
}

func TestFsListPathTraversal(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/list?path=../../../../etc")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for path traversal, got %d", resp.StatusCode)
	}
}

func TestFsListNotFound(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/list?path=/nonexistent/path/xyz")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestFsListDefaultPath(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	// Empty path should default to "/".
	resp := authedGet(t, server, cookie, "/api/fs/list")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsListResponse
	mustDec(t, resp, &result)
	if result.Entries == nil {
		t.Error("expected non-nil entries for root listing")
	}
}

// ---------------------------------------------------------------------------
// handleFsRead tests
// ---------------------------------------------------------------------------

func TestFsReadTextFile(t *testing.T) {
	dir := t.TempDir()
	content := "package main\n\nfunc main() {}\n"
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(content), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path="+filepath.Join(dir, "main.go"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsReadResponse
	mustDec(t, resp, &result)
	if result.Content != content {
		t.Errorf("content mismatch: got %q, want %q", result.Content, content)
	}
	if result.Language != "go" {
		t.Errorf("language: got %q, want go", result.Language)
	}
	if result.IsBinary {
		t.Error("should not be binary")
	}
}

func TestFsReadBinaryFile(t *testing.T) {
	dir := t.TempDir()
	// Create a binary file with NUL bytes.
	binary := make([]byte, 256)
	for i := range binary {
		binary[i] = byte(i % 256)
	}
	os.WriteFile(filepath.Join(dir, "binary.dat"), binary, 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path="+filepath.Join(dir, "binary.dat"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsReadResponse
	mustDec(t, resp, &result)
	if !result.IsBinary {
		t.Error("should be detected as binary")
	}
	if result.Content == "" || result.Encoding != "base64" {
		t.Errorf("binary file should contain base64 data, got encoding=%q content=%q", result.Encoding, result.Content)
	}
}

func TestFsReadDirectory(t *testing.T) {
	dir := t.TempDir()

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path="+dir)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for directory read, got %d", resp.StatusCode)
	}
}

func TestFsReadNotFound(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path=/nonexistent/file.txt")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestFsReadPathTraversal(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path=../../../../etc/passwd")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestFsReadLargeTextFile(t *testing.T) {
	dir := t.TempDir()
	// Create a file slightly over 2MB.
	content := strings.Repeat("A", maxFileReadSize+1024)
	os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/read?path="+filepath.Join(dir, "large.txt"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsReadResponse
	mustDec(t, resp, &result)
	if result.IsBinary {
		t.Error("large text file should not be binary")
	}
	// Content should be truncated to maxFileReadSize.
	if len(result.Content) != maxFileReadSize {
		t.Errorf("truncated content length: got %d, want %d", len(result.Content), maxFileReadSize)
	}
	if result.Size != int64(len(content)) {
		t.Errorf("reported size: got %d, want %d", result.Size, len(content))
	}
}

// ---------------------------------------------------------------------------
// handleFsRaw tests (GET /api/fs/raw — serves raw file bytes for <img src>)
// ---------------------------------------------------------------------------

func TestFsRawImage(t *testing.T) {
	dir := t.TempDir()
	// Create a minimal PNG (1×1 transparent).
	pngBytes := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	os.WriteFile(filepath.Join(dir, "test.png"), pngBytes, 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/raw?path="+url.QueryEscape(filepath.Join(dir, "test.png")))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type: got %q, want image/png", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, pngBytes) {
		t.Errorf("body mismatch: got %d bytes, want %d bytes", len(body), len(pngBytes))
	}
}

func TestFsRawNotFound(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/raw?path=/nonexistent/file.png")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestFsRawPathTraversal(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/raw?path=../../../../etc/passwd")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for path traversal, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// handleFsSearch tests
// ---------------------------------------------------------------------------

func TestFsSearchBasic(t *testing.T) {
	dir := t.TempDir()
	// Create files that match "test".
	os.WriteFile(filepath.Join(dir, "test1.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "test2.py"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0644)
	os.Mkdir(filepath.Join(dir, "testdir"), 0755)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/search?q=test&path="+dir)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsSearchResponse
	mustDec(t, resp, &result)

	names := make(map[string]bool)
	for _, r := range result.Results {
		names[r.Name] = true
	}
	if !names["test1.go"] {
		t.Error("test1.go should be in search results")
	}
	if !names["test2.py"] {
		t.Error("test2.py should be in search results")
	}
	if !names["testdir"] {
		t.Error("testdir should be in search results")
	}
	if names["other.txt"] {
		t.Error("other.txt should NOT be in search results")
	}
}

func TestFsSearchCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/search?q=readme&path="+dir)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsSearchResponse
	mustDec(t, resp, &result)
	if len(result.Results) == 0 {
		t.Error("case-insensitive search should find README.md for 'readme'")
	}
}

func TestFsSearchDepthLimit(t *testing.T) {
	dir := t.TempDir()
	// Create nested directories: dir/a/b/c/deep.go
	deep := filepath.Join(dir, "a", "b", "c")
	os.MkdirAll(deep, 0755)
	os.WriteFile(filepath.Join(deep, "target.go"), []byte("x"), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	// depth 0 = dir, depth 1 = dir/a, depth 2 = dir/a/b, depth 3 = dir/a/b/c
	// With maxSearchDepth=3, dir/a/b/c/target.go at depth 3 should be found.
	resp := authedGet(t, server, cookie, "/api/fs/search?q=target&path="+dir)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsSearchResponse
	mustDec(t, resp, &result)
	found := false
	for _, r := range result.Results {
		if r.Name == "target.go" {
			found = true
		}
	}
	if !found {
		t.Error("target.go at depth 3 should be found")
	}

	// Create a file at depth 4 — should NOT be found.
	veryDeep := filepath.Join(dir, "a", "b", "c", "d")
	os.MkdirAll(veryDeep, 0755)
	os.WriteFile(filepath.Join(veryDeep, "toodeep.go"), []byte("x"), 0644)

	resp2 := authedGet(t, server, cookie, "/api/fs/search?q=toodeep&path="+dir)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var result2 fsSearchResponse
	mustDec(t, resp2, &result2)
	if len(result2.Results) != 0 {
		t.Error("toodeep.go at depth 4 should NOT be found (depth limit)")
	}
}

func TestFsSearchLimit(t *testing.T) {
	dir := t.TempDir()
	// Create 10 matching files.
	for i := 0; i < 10; i++ {
		os.WriteFile(filepath.Join(dir, "match"+strconv.Itoa(i)+".txt"), []byte("x"), 0644)
	}

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	// Request limit=5.
	resp := authedGet(t, server, cookie, "/api/fs/search?q=match&path="+dir+"&limit=5")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsSearchResponse
	mustDec(t, resp, &result)
	if len(result.Results) > 5 {
		t.Errorf("results should be capped at 5, got %d", len(result.Results))
	}
	if len(result.Results) == 0 {
		t.Error("expected some results")
	}
}

func TestFsSearchNoQuery(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/search?path=/tmp")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing query, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// handleFsStat tests
// ---------------------------------------------------------------------------

func TestFsStatFile(t *testing.T) {
	dir := t.TempDir()
	content := "hello world"
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte(content), 0644)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/stat?path="+path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsStatResponse
	mustDec(t, resp, &result)
	if result.Name != "test.go" {
		t.Errorf("name: got %q, want test.go", result.Name)
	}
	if result.IsDir {
		t.Error("should not be a directory")
	}
	if result.Size != int64(len(content)) {
		t.Errorf("size: got %d, want %d", result.Size, len(content))
	}
}

func TestFsStatDirectory(t *testing.T) {
	dir := t.TempDir()

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/stat?path="+dir)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result fsStatResponse
	mustDec(t, resp, &result)
	if !result.IsDir {
		t.Error("should be a directory")
	}
	if result.Name != filepath.Base(dir) {
		t.Errorf("name: got %q, want %q", result.Name, filepath.Base(dir))
	}
}

func TestFsStatNotFound(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/stat?path=/nonexistent/xyz")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestFsStatPathTraversal(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)
	cookie := registerAndGetCookie(t, server)

	resp := authedGet(t, server, cookie, "/api/fs/stat?path=../../../../etc/passwd")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Auth tests for all FS endpoints
// ---------------------------------------------------------------------------

func TestFsAllEndpointsRequireAuth(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	server := startFsTestServer(t, wc)

	endpoints := []string{
		"/api/fs/list?path=/",
		"/api/fs/read?path=/etc/hostname",
		"/api/fs/search?q=test&path=/",
		"/api/fs/stat?path=/",
	}
	for _, ep := range endpoints {
		resp := authedGet(t, server, "", ep)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: expected 401 without auth, got %d", ep, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// ---------------------------------------------------------------------------
// searchDir unit test (no HTTP)
// ---------------------------------------------------------------------------

func TestSearchDirUnit(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alpha.go"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "beta.txt"), []byte("x"), 0644)
	os.Mkdir(filepath.Join(dir, "gamma"), 0755)
	os.WriteFile(filepath.Join(dir, "gamma", "alpha.py"), []byte("x"), 0644)

	var results []fsSearchResult
	searchDir(dir, "alpha", false, &results, 50, 0)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	names := make(map[string]bool)
	for _, r := range results {
		names[r.Name] = true
	}
	if !names["alpha.go"] {
		t.Error("alpha.go should be found")
	}
	if !names["alpha.py"] {
		t.Error("alpha.py should be found")
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------
