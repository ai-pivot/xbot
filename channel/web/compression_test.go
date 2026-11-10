package web

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestCompressionMiddlewareJavaScriptMIMETypes(t *testing.T) {
	payload := []byte(strings.Repeat("export const message = 'JavaScript should be compressed';\n", 1024))
	for _, contentType := range []string{
		"text/javascript",
		"text/javascript; charset=utf-8",
		"application/javascript",
	} {
		for _, encoding := range []string{"zstd", "gzip", "identity"} {
			t.Run(contentType+"/"+encoding, func(t *testing.T) {
				handler := CompressionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", contentType)
					http.ServeContent(w, r, "index.js", time.Time{}, bytes.NewReader(payload))
				}))
				request := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
				request.Header.Set("Accept-Encoding", encoding)
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, request)
				response := recorder.Result()
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("status = %d, want 200", response.StatusCode)
				}
				if got := response.Header.Get("Content-Type"); got != contentType {
					t.Fatalf("Content-Type = %q, want %q", got, contentType)
				}
				wantEncoding := encoding
				if encoding == "identity" {
					wantEncoding = ""
				}
				if got := response.Header.Get("Content-Encoding"); got != wantEncoding {
					t.Fatalf("Content-Encoding = %q, want %q", got, wantEncoding)
				}
				if wantEncoding != "" {
					if got := response.Header.Get("Vary"); got != "Accept-Encoding" {
						t.Fatalf("Vary = %q, want Accept-Encoding", got)
					}
					if got := response.Header.Get("Content-Length"); got != "" {
						t.Fatalf("uncompressed Content-Length retained: %q", got)
					}
					if recorder.Body.Len() >= len(payload) {
						t.Fatalf("compressed body = %d bytes, original = %d", recorder.Body.Len(), len(payload))
					}
				}
				var decoded io.Reader = response.Body
				switch encoding {
				case "zstd":
					reader, err := zstd.NewReader(response.Body)
					if err != nil {
						t.Fatal(err)
					}
					defer reader.Close()
					decoded = reader
				case "gzip":
					reader, err := gzip.NewReader(response.Body)
					if err != nil {
						t.Fatal(err)
					}
					defer reader.Close()
					decoded = reader
				}
				body, err := io.ReadAll(decoded)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(body, payload) {
					t.Fatal("decoded JavaScript differs from original")
				}
			})
		}
	}
}
