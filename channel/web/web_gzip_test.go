package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGzipMiddlewareCompressesJSON(t *testing.T) {
	payload := `{"data":"` + strings.Repeat("x", 1000) + `"}`
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(payload))
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("expected Content-Encoding: gzip, got %q", rec.Header().Get("Content-Encoding"))
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("expected Vary: Accept-Encoding, got %q", rec.Header().Get("Vary"))
	}

	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	body, _ := io.ReadAll(gz)
	if string(body) != payload {
		t.Errorf("decompressed mismatch: got %d bytes, want %d", len(body), len(payload))
	}
}

func TestGzipMiddlewareSkipsSSE(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: hello\n\n"))
	}))

	req := httptest.NewRequest("GET", "/api/sse?chat_id=x&channel=web", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("SSE should not be gzip-compressed")
	}
	if rec.Body.String() != "data: hello\n\n" {
		t.Errorf("expected uncompressed SSE, got %q", rec.Body.String())
	}
}

func TestGzipMiddlewareNoAcceptEncoding(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest("GET", "/api/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("should not compress without Accept-Encoding: gzip")
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("expected uncompressed body, got %q", rec.Body.String())
	}
}

func TestGzipMiddlewareSkipsBinary(t *testing.T) {
	data := []byte{0x89, 0x50, 0x4e, 0x47}
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(data)
	}))

	req := httptest.NewRequest("GET", "/api/files/test.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("PNG should not be gzip-compressed")
	}
	if rec.Body.Bytes()[0] != 0x89 {
		t.Error("binary data corrupted")
	}
}
