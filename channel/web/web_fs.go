package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// File System REST API
// ---------------------------------------------------------------------------

const (
	maxFileReadSize    = 2 << 20 // 2MB maximum file content for read endpoint
	maxSearchDepth     = 3       // maximum directory depth for recursive search
	defaultSearchLimit = 50      // default number of search results
	maxSearchLimit     = 200     // maximum number of search results
	binarySniffSize    = 512     // bytes to read for binary detection
)

// langFromExt maps file extensions to language identifiers for syntax highlighting.
var langFromExt = map[string]string{
	".go":         "go",
	".ts":         "typescript",
	".tsx":        "typescriptreact",
	".js":         "javascript",
	".jsx":        "javascriptreact",
	".mjs":        "javascript",
	".cjs":        "javascript",
	".py":         "python",
	".pyw":        "python",
	".md":         "markdown",
	".markdown":   "markdown",
	".json":       "json",
	".jsonc":      "json",
	".yaml":       "yaml",
	".yml":        "yaml",
	".sh":         "shell",
	".bash":       "shell",
	".zsh":        "shell",
	".rs":         "rust",
	".css":        "css",
	".scss":       "scss",
	".sass":       "sass",
	".less":       "less",
	".html":       "html",
	".htm":        "html",
	".xml":        "xml",
	".svg":        "xml",
	".sql":        "sql",
	".toml":       "toml",
	".ini":        "ini",
	".cfg":        "ini",
	".txt":        "plaintext",
	".c":          "c",
	".h":          "c",
	".cpp":        "cpp",
	".cc":         "cpp",
	".cxx":        "cpp",
	".hpp":        "cpp",
	".java":       "java",
	".rb":         "ruby",
	".php":        "php",
	".swift":      "swift",
	".kt":         "kotlin",
	".kts":        "kotlin",
	".scala":      "scala",
	".lua":        "lua",
	".vue":        "vue",
	".svelte":     "svelte",
	".dart":       "dart",
	".groovy":     "groovy",
	".gradle":     "groovy",
	".dockerfile": "dockerfile",
	".makefile":   "makefile",
	".proto":      "proto",
	".thrift":     "thrift",
	".graphql":    "graphql",
	".gql":        "graphql",
}

// langFromFilename maps exact filenames (lowercase) to language identifiers.
var langFromFilename = map[string]string{
	"dockerfile":    "dockerfile",
	"makefile":      "makefile",
	"justfile":      "makefile",
	".bashrc":       "shell",
	".zshrc":        "shell",
	".gitignore":    "ignore",
	".dockerignore": "ignore",
}

// languageFromPath returns the language identifier for a file path based on
// its extension or filename.
func languageFromPath(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if lang, ok := langFromFilename[base]; ok {
		return lang
	}
	ext := strings.ToLower(filepath.Ext(path))
	if lang, ok := langFromExt[ext]; ok {
		return lang
	}
	return ""
}

// errPathTraversal is returned when a path contains ".." traversal.
var errPathTraversal = fmt.Errorf("path traversal detected")

// resolveSafePath normalizes and validates a path for safe filesystem access.
// It rejects paths containing ".." as a path component, then cleans and
// converts to an absolute path. No root restriction — the file browser
// needs to start from "/".
func resolveSafePath(rawPath string) (string, error) {
	if rawPath == "" {
		rawPath = "/"
	}
	// Reject paths containing ".." as a path component (traversal attempt).
	// filepath.ToSlash normalizes Windows backslashes to forward slashes
	// so the split works consistently across platforms — HTTP paths always
	// use "/", but a caller may pass OS-native separators.
	for _, part := range strings.Split(filepath.ToSlash(rawPath), "/") {
		if part == ".." {
			return "", errPathTraversal
		}
	}
	cleaned := filepath.Clean(rawPath)
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	return abs, nil
}

// isHidden returns true if the name starts with a dot (hidden file/dir).
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

// ---------------------------------------------------------------------------
// GET /api/fs/list?path=<abs>
// ---------------------------------------------------------------------------

type fsListEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type fsListResponse struct {
	Entries []fsListEntry `json:"entries"`
}

// handleFsList lists the contents of a directory.
func (wc *WebChannel) handleFsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		rawPath = "/"
	}

	safePath, err := resolveSafePath(rawPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusForbidden, err.Error())
		return
	}

	entries, err := os.ReadDir(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonErrorResponse(w, http.StatusNotFound, "path not found")
			return
		}
		if os.IsPermission(err) {
			jsonErrorResponse(w, http.StatusForbidden, "permission denied")
			return
		}
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	showHidden := r.URL.Query().Has("showHidden") &&
		r.URL.Query().Get("showHidden") == "true"

	result := make([]fsListEntry, 0, len(entries))
	for _, e := range entries {
		if !showHidden && isHidden(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // skip entries we can't stat
		}
		result = append(result, fsListEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	writeJSON(w, http.StatusOK, fsListResponse{Entries: result})
}

// ---------------------------------------------------------------------------
// GET /api/fs/read?path=<abs>
// ---------------------------------------------------------------------------

type fsReadResponse struct {
	Content  string `json:"content"`
	Language string `json:"language"`
	Size     int64  `json:"size"`
	IsBinary bool   `json:"isBinary"`
}

// isBinaryData checks if the given bytes contain NUL bytes (binary indicator).
func isBinaryData(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

// handleFsRead reads the content of a file (text only, max 2MB).
func (wc *WebChannel) handleFsRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "path is required")
		return
	}

	safePath, err := resolveSafePath(rawPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusForbidden, err.Error())
		return
	}

	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonErrorResponse(w, http.StatusNotFound, "file not found")
			return
		}
		if os.IsPermission(err) {
			jsonErrorResponse(w, http.StatusForbidden, "permission denied")
			return
		}
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if info.IsDir() {
		jsonErrorResponse(w, http.StatusBadRequest, "path is a directory, not a file")
		return
	}

	// Open and sniff first N bytes for binary detection.
	f, err := os.Open(safePath)
	if err != nil {
		if os.IsPermission(err) {
			jsonErrorResponse(w, http.StatusForbidden, "permission denied")
			return
		}
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()

	sniff := make([]byte, binarySniffSize)
	n, err := f.Read(sniff)
	if err != nil && err != io.EOF {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	sniff = sniff[:n]

	if isBinaryData(sniff) {
		writeJSON(w, http.StatusOK, fsReadResponse{
			Content:  "",
			Language: "",
			Size:     info.Size(),
			IsBinary: true,
		})
		return
	}

	// Read full content (max 2MB).
	if info.Size() > maxFileReadSize {
		// File is text but too large — return truncated content with a note.
		content := make([]byte, maxFileReadSize)
		copy(content, sniff)
		remaining := make([]byte, maxFileReadSize-n)
		rn, _ := f.Read(remaining)
		copy(content[n:], remaining[:rn])
		writeJSON(w, http.StatusOK, fsReadResponse{
			Content:  string(content[:n+rn]),
			Language: languageFromPath(safePath),
			Size:     info.Size(),
			IsBinary: false,
		})
		return
	}

	// Read entire file.
	content, err := io.ReadAll(io.MultiReader(bytes.NewReader(sniff), f))
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fsReadResponse{
		Content:  string(content),
		Language: languageFromPath(safePath),
		Size:     info.Size(),
		IsBinary: false,
	})
}

// handleFsRaw serves the raw content of a file (for images, etc).
// Unlike handleFsRead which returns JSON, this returns the raw file bytes
// with an appropriate Content-Type based on file extension.
func (wc *WebChannel) handleFsRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	safePath, err := resolveSafePath(rawPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "path is a directory", http.StatusBadRequest)
		return
	}

	// Reject files larger than 50MB to prevent abuse.
	const maxRawFileSize = 50 * 1024 * 1024
	if info.Size() > maxRawFileSize {
		http.Error(w, "file too large (max 50MB)", http.StatusRequestEntityTooLarge)
		return
	}

	// Set Content-Type based on extension
	contentType := contentTypeFromExt(safePath)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, safePath)
}

// contentTypeFromExt returns a MIME type for common image/web file extensions.
func contentTypeFromExt(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".pdf":
		return "application/pdf"
	default:
		return "application/octet-stream"
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// GET /api/fs/search?q=<kw>&path=<dir>&limit=50
// ---------------------------------------------------------------------------

type fsSearchResult struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
}

type fsSearchResponse struct {
	Results []fsSearchResult `json:"results"`
}

// handleFsSearch recursively searches for file names containing the query.
func (wc *WebChannel) handleFsSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := strings.ToLower(r.URL.Query().Get("q"))
	if q == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "query parameter 'q' is required")
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		rawPath = "/"
	}

	safePath, err := resolveSafePath(rawPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusForbidden, err.Error())
		return
	}

	limit := defaultSearchLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
			if limit > maxSearchLimit {
				limit = maxSearchLimit
			}
		}
	}

	showHidden := r.URL.Query().Has("showHidden") &&
		r.URL.Query().Get("showHidden") == "true"

	results := make([]fsSearchResult, 0, limit)
	searchDir(safePath, q, showHidden, &results, limit, 0)

	writeJSON(w, http.StatusOK, fsSearchResponse{Results: results})
}

// searchDir recursively walks directories searching for file names containing q.
// depth is the current recursion depth (0 = root). maxSearchDepth limits recursion.
func searchDir(dir, q string, showHidden bool, results *[]fsSearchResult, limit, depth int) {
	if len(*results) >= limit || depth > maxSearchDepth {
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if len(*results) >= limit {
			return
		}
		if !showHidden && isHidden(e.Name()) {
			continue
		}

		name := e.Name()
		if strings.Contains(strings.ToLower(name), q) {
			fullPath := filepath.Join(dir, name)
			*results = append(*results, fsSearchResult{
				Path:  fullPath,
				Name:  name,
				IsDir: e.IsDir(),
			})
		}

		// Recurse into subdirectories.
		if e.IsDir() && depth < maxSearchDepth {
			searchDir(filepath.Join(dir, name), q, showHidden, results, limit, depth+1)
		}
	}
}

// ---------------------------------------------------------------------------
// GET /api/fs/stat?path=<abs>
// ---------------------------------------------------------------------------

type fsStatResponse struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	Mode    string    `json:"mode"`
}

// handleFsStat returns metadata for a single file or directory.
func (wc *WebChannel) handleFsStat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErrorResponse(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	rawPath := r.URL.Query().Get("path")
	if rawPath == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "path is required")
		return
	}

	safePath, err := resolveSafePath(rawPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusForbidden, err.Error())
		return
	}

	info, err := os.Stat(safePath)
	if err != nil {
		if os.IsNotExist(err) {
			jsonErrorResponse(w, http.StatusNotFound, "path not found")
			return
		}
		if os.IsPermission(err) {
			jsonErrorResponse(w, http.StatusForbidden, "permission denied")
			return
		}
		jsonErrorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, fsStatResponse{
		Name:    filepath.Base(safePath),
		IsDir:   info.IsDir(),
		Size:    info.Size(),
		ModTime: info.ModTime(),
		Mode:    info.Mode().String(),
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
