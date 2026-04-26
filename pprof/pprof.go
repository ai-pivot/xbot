package pprof

import (
	"context"
	"fmt"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"
	log "xbot/logger"
)

// Config holds pprof configuration
type Config struct {
	Enable bool   // Whether to enable pprof
	Host   string // Listen address
	Port   int    // Listen port
}

// DefaultConfig returns the default pprof configuration
func DefaultConfig() Config {
	return Config{
		Enable: false,
		Host:   "localhost", // Default: localhost only for security
		Port:   6060,
	}
}

// Server is a pprof HTTP server
type Server struct {
	config Config
	server *http.Server
}

// NewServer creates a new pprof server
func NewServer(cfg Config) *Server {
	return &Server{
		config: cfg,
	}
}

// Start starts the pprof server
func (s *Server) Start() error {
	if !s.config.Enable {
		log.Info("pprof server is disabled")
		return nil
	}

	mux := http.NewServeMux()

	// Register pprof routes
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Add runtime stats endpoint
	mux.HandleFunc("/debug/stats", s.statsHandler)

	// Add GC trigger endpoint
	mux.HandleFunc("/debug/gc", s.gcHandler)

	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second, // profiling may take a long time
	}

	log.Infof("pprof server started on http://%s/debug/pprof/", addr)
	log.Info("Available endpoints:")
	log.Info("  /debug/pprof/         - pprof index")
	log.Info("  /debug/pprof/heap     - heap profile")
	log.Info("  /debug/pprof/goroutine - goroutine profile")
	log.Info("  /debug/pprof/profile  - CPU profile (30s)")
	log.Info("  /debug/pprof/trace    - execution trace")
	log.Info("  /debug/stats          - runtime statistics")
	log.Info("  /debug/gc             - trigger GC")

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("pprof server error: %v", err)
		}
	}()

	return nil
}

// Shutdown stops the pprof server
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	log.Info("Shutting down pprof server...")
	return s.server.Shutdown(ctx)
}

// statsHandler returns runtime statistics
func (s *Server) statsHandler(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	stats := fmt.Sprintf(`Runtime Statistics
==================

Goroutines: %d
NumCPU: %d
GOMAXPROCS: %d

Memory Statistics
-----------------
Alloc: %d MB (bytes allocated and still in use)
TotalAlloc: %d MB (total bytes allocated)
Sys: %d MB (bytes obtained from system)
NumGC: %d (number of completed GC cycles)
LastGC: %s

Heap Statistics
---------------
HeapAlloc: %d MB
HeapSys: %d MB
HeapIdle: %d MB
HeapInuse: %d MB
HeapReleased: %d MB
HeapObjects: %d

Stack Statistics
----------------
StackInuse: %d KB
StackSys: %d KB
`,
		runtime.NumGoroutine(),
		runtime.NumCPU(),
		runtime.GOMAXPROCS(0),
		m.Alloc/1024/1024,
		m.TotalAlloc/1024/1024,
		m.Sys/1024/1024,
		m.NumGC,
		time.Unix(0, int64(m.LastGC)).Format(time.RFC3339),
		m.HeapAlloc/1024/1024,
		m.HeapSys/1024/1024,
		m.HeapIdle/1024/1024,
		m.HeapInuse/1024/1024,
		m.HeapReleased/1024/1024,
		m.HeapObjects,
		m.StackInuse/1024,
		m.StackSys/1024,
	)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(stats))
}

// gcHandler manually triggers GC
// SECURITY NOTE: This endpoint has no authentication. It is protected only by
// binding to localhost (default Config.Host). If the server is exposed on a
// public interface, add authentication middleware (e.g., API key or token check).
func (s *Server) gcHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed. Use POST to trigger GC.", http.StatusMethodNotAllowed)
		return
	}

	var m1, m2 runtime.MemStats
	runtime.ReadMemStats(&m1)

	runtime.GC()

	runtime.ReadMemStats(&m2)

	result := fmt.Sprintf(`GC Triggered Successfully

Before GC:
  HeapAlloc: %d MB
  HeapInuse: %d MB
  HeapObjects: %d

After GC:
  HeapAlloc: %d MB
  HeapInuse: %d MB
  HeapObjects: %d

Freed:
  HeapAlloc: %d MB
  HeapObjects: %d
`,
		m1.HeapAlloc/1024/1024,
		m1.HeapInuse/1024/1024,
		m1.HeapObjects,
		m2.HeapAlloc/1024/1024,
		m2.HeapInuse/1024/1024,
		m2.HeapObjects,
		(m1.HeapAlloc-m2.HeapAlloc)/1024/1024,
		m1.HeapObjects-m2.HeapObjects,
	)

	log.Info("GC triggered via /debug/gc endpoint")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(result))
}
