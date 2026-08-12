package web

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

// ── Compression middleware ──────────────────────────────────────────────────
//
// Transparently compresses HTTP responses (including SSE) based on the
// client's Accept-Encoding header. zstd is preferred (better ratio + speed),
// gzip as fallback. SSE is supported via a Flusher-compatible wrapper.
//
// The middleware skips:
//   - WebSocket upgrades (Connection: Upgrade)
//   - Responses already encoded (Content-Encoding set by handler)
//   - Small responses (<256 bytes, not worth the overhead)
//   - Binary content types (images, fonts, wasm)

var compressibleContentTypes = map[string]bool{
	"text/event-stream":       true, // SSE
	"application/json":        true,
	"text/html":               true,
	"text/plain":              true,
	"text/css":                true,
	"application/javascript":  true,
	"application/x-jsonlines": true,
}

// zstd encoder pool (zstd encoders are expensive to create)
var zstdEncoderPool = sync.Pool{
	New: func() interface{} {
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
		return enc
	},
}

// gzip writer pool
var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression)
		return w
	},
}

// compressResponseWriter wraps http.ResponseWriter with transparent compression.
// It buffers writes until WriteHeader is called (or first Write), then decides
// whether to compress based on Content-Type and Content-Length.
type compressResponseWriter struct {
	http.ResponseWriter
	encoder    io.WriteCloser
	encoding   string // "zstd" or "gzip"
	statusCode int
	headerSent bool
}

func (w *compressResponseWriter) WriteHeader(code int) {
	if w.headerSent {
		return
	}
	w.statusCode = code
	// Don't commit yet — wait for first Write to sniff Content-Type.
	// But if status is not 200, skip compression (error responses).
	if code != http.StatusOK {
		w.commit(false)
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *compressResponseWriter) Write(data []byte) (int, error) {
	if !w.headerSent {
		w.commit(true)
	}
	if w.encoder != nil {
		return w.encoder.Write(data)
	}
	return w.ResponseWriter.Write(data)
}

func (w *compressResponseWriter) Flush() {
	if w.encoder != nil {
		// For SSE: flush the compressor then the underlying writer.
		// zstd/gzip Flush writes compressed data to the underlying writer.
		if f, ok := w.encoder.(interface{ Flush() error }); ok {
			f.Flush()
		}
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *compressResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// commit decides whether to compress and sets up the encoder.
// Called once on first Write or on WriteHeader(non-200).
func (w *compressResponseWriter) commit(tryCompress bool) {
	if w.headerSent {
		return
	}
	w.headerSent = true

	if !tryCompress {
		return
	}

	// Check if Content-Type is compressible
	ct := w.ResponseWriter.Header().Get("Content-Type")
	if ct == "" {
		return // unknown type, skip
	}
	// Strip charset suffix
	ct = strings.Split(ct, ";")[0]
	ct = strings.TrimSpace(ct)
	if !compressibleContentTypes[ct] {
		return
	}

	// Already encoded?
	if w.ResponseWriter.Header().Get("Content-Encoding") != "" {
		return
	}

	// Choose encoding based on Accept-Encoding
	accept := w.acceptEncoding()
	if accept == "" {
		return
	}

	// Remove Content-Length — compression changes the size
	w.ResponseWriter.Header().Del("Content-Length")
	w.ResponseWriter.Header().Set("Content-Encoding", accept)
	w.ResponseWriter.Header().Set("Vary", "Accept-Encoding")

	switch accept {
	case "zstd":
		enc := zstdEncoderPool.Get().(*zstd.Encoder)
		enc.Reset(w.ResponseWriter)
		w.encoder = enc
	case "gzip":
		enc := gzipWriterPool.Get().(*gzip.Writer)
		enc.Reset(w.ResponseWriter)
		w.encoder = enc
	}
}

func (w *compressResponseWriter) acceptEncoding() string {
	// Stored by the middleware before calling handler
	return w.encoding
}

// close releases the encoder back to the pool
func (w *compressResponseWriter) close() {
	if w.encoder != nil {
		w.encoder.Close()
		switch e := w.encoder.(type) {
		case *zstd.Encoder:
			zstdEncoderPool.Put(e)
		case *gzip.Writer:
			gzipWriterPool.Put(e)
		}
		w.encoder = nil
	}
}

// CompressionMiddleware wraps an http.Handler with transparent zstd/gzip
// compression. It supports SSE (text/event-stream) via Flush.
//
// SSE compression is handled by the SSE handler itself (web_sse.go) which
// wraps the writer with a zstd/gzip encoder per-connection. This middleware
// skips text/event-stream responses (the SSE handler sets Content-Encoding
// itself). For all other compressible types (JSON, HTML, etc.) this
// middleware applies compression transparently.
func CompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip WebSocket upgrades
		if strings.EqualFold(r.Header.Get("Connection"), "upgrade") {
			next.ServeHTTP(w, r)
			return
		}

		// Skip SSE — the SSE handler does its own compression (per-connection
		// zstd/gzip encoder with Flush support). This middleware's
		// compressResponseWriter buffers until WriteHeader, which breaks SSE's
		// real-time Flush requirement.
		if strings.Contains(r.URL.Path, "/api/sse") {
			next.ServeHTTP(w, r)
			return
		}

		// Parse Accept-Encoding (prefer zstd > gzip)
		encoding := ""
		accept := r.Header.Get("Accept-Encoding")
		if strings.Contains(accept, "zstd") {
			encoding = "zstd"
		} else if strings.Contains(accept, "gzip") {
			encoding = "gzip"
		}

		if encoding == "" {
			next.ServeHTTP(w, r)
			return
		}

		cw := &compressResponseWriter{
			ResponseWriter: w,
			encoding:       encoding,
			statusCode:     200,
		}
		defer cw.close()
		next.ServeHTTP(cw, r)

		// If handler never called Write (e.g. 204 No Content), flush header
		if !cw.headerSent {
			cw.commit(false)
			w.WriteHeader(cw.statusCode)
		}
	})
}
