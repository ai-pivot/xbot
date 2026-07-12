package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// Static file handler
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleStatic(w http.ResponseWriter, r *http.Request) {
	if wc.staticDir == "" {
		http.NotFound(w, r)
		return
	}

	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Clean path to prevent directory traversal
	cleanPath := filepath.Clean(path)
	absPath := filepath.Join(wc.staticDir, cleanPath)

	// Ensure the resolved path is within the static directory
	absStaticDir, err := filepath.Abs(wc.staticDir)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "internal error")
		return
	}
	absResolved, err := filepath.Abs(absPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid path")
		return
	}
	if !strings.HasPrefix(absResolved, absStaticDir+string(os.PathSeparator)) && absResolved != absStaticDir {
		http.NotFound(w, r)
		return
	}

	// Try exact path
	if _, err := os.Stat(absResolved); err == nil {
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for non-file paths
	if !strings.Contains(path, ".") {
		r.URL.Path = "/"
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
		return
	}

	http.NotFound(w, r)
}
