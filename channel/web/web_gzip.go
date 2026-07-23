package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipMinSize is the minimum response size (in bytes) before compression kicks
// in. Smaller responses are not worth the CPU cost of gzip — the header
// overhead may even make them larger.
const gzipMinSize = 512

// gzipLevel is the compression level. BestSpeed (1) gives ~70% size reduction
// at ~3× the throughput of BestCompression, which matters for streaming JSON
// endpoints that handle history and session-tree payloads.
const gzipLevel = gzip.BestSpeed

// gzipContentTypes are the MIME types eligible for compression. JSON is the
// primary target; binary formats (images, video) are skipped because they are
// already compressed.
var gzipContentTypes = map[string]bool{
	"application/json":         true,
	"application/javascript":   true,
	"text/html":                true,
	"text/css":                 true,
	"text/plain":               true,
	"application/manifest+json": true,
}

// gzipPool reuses gzip.Writer instances across requests. Each writer holds a
// pre-allocated compression buffer; pooling avoids per-request allocation.
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzipLevel)
		return w
	},
}

// gzipMiddleware wraps an http.Handler with transparent gzip compression for
// eligible responses. It checks Accept-Encoding, sets Content-Encoding: gzip,
// and replaces the ResponseWriter with a gzip writer for responses that are
// both large enough and of a compressible content type.
//
// SSE (/api/sse) and WebSocket (/ws) responses are NOT compressed — they use
// text/event-stream or connection upgrades that must stream unbuffered.
// Already-compressed content (e.g. /api/fs/raw serving binary files) is also
// skipped via the content-type allowlist.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip if the client doesn't accept gzip.
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip SSE and WebSocket — they must stream unbuffered.
		if r.URL.Path == "/api/sse" || r.URL.Path == "/ws" {
			next.ServeHTTP(w, r)
			return
		}

		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.Close()

		next.ServeHTTP(gw, r)
	})
}

// gzipResponseWriter buffers the response so it can decide whether to compress
// based on the Content-Type and size set by the handler. For eligible
// responses it transparently writes gzip-compressed bytes to the underlying
// ResponseWriter.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	contentType string
	buf         []byte // uncompressed buffer used until we decide to compress
	started     bool   // true once we've committed to a write path
}

func (w *gzipResponseWriter) WriteHeader(code int) {
	w.contentType = w.Header().Get("Content-Type")
	// Strip charset etc. to check the base type (e.g. "application/json; charset=utf-8").
	if idx := strings.IndexByte(w.contentType, ';'); idx >= 0 {
		w.contentType = strings.TrimSpace(w.contentType[:idx])
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	// If WriteHeader wasn't called explicitly, resolve content type now
	// (http.ResponseWriter.Write implicitly calls WriteHeader(200)).
	if w.contentType == "" {
		ct := w.Header().Get("Content-Type")
		if idx := strings.IndexByte(ct, ';'); idx >= 0 {
			ct = strings.TrimSpace(ct[:idx])
		}
		w.contentType = ct
	}

	// If content type is not compressible, pass through directly.
	if !gzipContentTypes[w.contentType] {
		w.started = true
		return w.ResponseWriter.Write(b)
	}

	// Lazy-init the gzip writer on first write.
	if w.gz == nil {
		gz := gzipPool.Get().(*gzip.Writer)
		gz.Reset(w.ResponseWriter)
		w.gz = gz
		w.Header().Del("Content-Length") // length changes after compression
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.started = true
	}
	return w.gz.Write(b)
}

func (w *gzipResponseWriter) Close() {
	if w.gz != nil {
		w.gz.Close()
		gzipPool.Put(w.gz)
	}
}

// Flush supports http.Flusher for any handler that flushes mid-response.
// We only flush the underlying writer — gzip streams don't support partial
// flushes cleanly, but this path is not hit for SSE (which is skipped above).
func (w *gzipResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.gz != nil {
			w.gz.Flush()
		}
		f.Flush()
	}
}
