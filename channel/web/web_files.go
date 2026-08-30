/**
 * Plugin file storage — 每插件独立目录 + 鉴权 serve。
 *
 * 提供通用文件上传/列表/删除/下载 API（所有插件共用，无特化）：
 *   POST   /api/plugin-files/upload               (multipart: file + plugin_id)
 *   GET    /api/plugin-files/{plugin_id}/{file}   (serve, Cache-Control immutable)
 *   GET    /api/plugin-files/{plugin_id}          (list)
 *   DELETE /api/plugin-files/{plugin_id}/{file}   (delete)
 *
 * 存储路径：{xbotHome}/plugin-files/{plugin_id}/{filename}
 * 安全：路径穿越防护（plugin_id/filename 白名单 [a-zA-Z0-9._-]）+ 原子写入。
 * 图片压缩：服务端 canvas 不存在——保持原始文件（浏览器端插件已压缩）。
 */
package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── 常量 ────────────────────────────────────────────────────────────────────

const (
	pluginFilesDirName = "plugin-files"
)

var pluginFileIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var pluginFileNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,255}\.[a-zA-Z0-9]{1,10}$`)

// PluginFileMeta describes an uploaded file.
type PluginFileMeta struct {
	Filename    string `json:"filename"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	ModTime     string `json:"modTime"`
}

// ── 路径解析与安全 ──────────────────────────────────────────────────────────

// pluginFilesDir returns the plugin files storage root directory.
// Priority: XBOT_HOME env → ~/.xbot/ (both + plugin-files/).
func (wc *WebChannel) pluginFilesDir() string {
	home := os.Getenv("XBOT_HOME")
	if home == "" {
		if u, err := os.UserHomeDir(); err == nil {
			home = u + "/.xbot"
		} else {
			home = "/tmp/xbot"
		}
	}
	return filepath.Join(home, pluginFilesDirName)
}

// pluginFileDir returns {xbotHome}/plugin-files/{pluginID}/.
func (wc *WebChannel) pluginFileDir(pluginID string) string {
	return filepath.Join(wc.pluginFilesDir(), pluginID)
}

// validatePluginFileID checks a plugin ID is safe (no path traversal).
func validatePluginFileID(id string) bool {
	return pluginFileIDRe.MatchString(id) && !strings.Contains(id, "..")
}

// validatePluginFileName checks a filename is safe (no path traversal).
func validatePluginFileName(name string) bool {
	return pluginFileNameRe.MatchString(name) && !strings.Contains(name, "..")
}

// contentTypeFor returns the MIME type for a file extension.
func contentTypeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webm":
		return "video/webm"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".json":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// ── API handlers ────────────────────────────────────────────────────────────

// handlePluginFileUpload — POST /api/plugin-files/upload (multipart: file + plugin_id)
func (wc *WebChannel) handlePluginFileUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 大小不设限制（用户明确要求：壁纸上传无上限）。ParseMultipartForm 的
	// 参数只是 multipart 内存缓冲上限——超出部分自动落盘临时文件，不是
	// 请求体限制；不包 http.MaxBytesReader，任意大小直传磁盘（原子写 .tmp）。
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		httpJSON(w, http.StatusBadRequest, fmt.Sprintf("parse multipart: %v", err))
		return
	}

	pluginID := r.FormValue("plugin_id")
	if !validatePluginFileID(pluginID) {
		httpJSON(w, http.StatusBadRequest, "invalid plugin_id")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		httpJSON(w, http.StatusBadRequest, fmt.Sprintf("missing file: %v", err))
		return
	}
	defer file.Close()

	filename := filepath.Base(header.Filename)
	if !validatePluginFileName(filename) {
		// Generate a safe name if the original is unsafe.
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if len(ext) > 10 || len(ext) < 2 {
			ext = ".bin"
		}
		filename = fmt.Sprintf("file-%d%s", time.Now().UnixNano(), ext)
	}

	// 原子写入：先写 .tmp 再 rename（大小无上限——用户要求不设限制）。
	dir := wc.pluginFileDir(pluginID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("mkdir: %v", err))
		return
	}

	tmpPath := filepath.Join(dir, "."+filename+".tmp")
	dstPath := filepath.Join(dir, filename)
	out, err := os.Create(tmpPath)
	if err != nil {
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("create tmp: %v", err))
		return
	}
	written, err := io.Copy(out, file)
	out.Close()
	if err != nil {
		os.Remove(tmpPath)
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("write: %v", err))
		return
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("rename: %v", err))
		return
	}

	// 返回文件元数据。
	url := fmt.Sprintf("/api/plugin-files/%s/%s", pluginID, filename)
	meta := PluginFileMeta{
		Filename:    filename,
		URL:         url,
		Size:        written,
		ContentType: contentTypeFor(filename),
		ModTime:     time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": meta})
}

// handlePluginFiles — GET/DELETE /api/plugin-files/{plugin_id}[/{filename}]
func (wc *WebChannel) handlePluginFiles(w http.ResponseWriter, r *http.Request) {
	// 路径解析：/api/plugin-files/{plugin_id} 或 /api/plugin-files/{plugin_id}/{filename}
	path := strings.TrimPrefix(r.URL.Path, "/api/plugin-files/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || !validatePluginFileID(parts[0]) {
		httpJSON(w, http.StatusBadRequest, "invalid plugin_id")
		return
	}
	pluginID := parts[0]

	// GET /api/plugin-files/{plugin_id} → list files
	if len(parts) == 1 || parts[1] == "" {
		if r.Method != http.MethodGet {
			httpJSON(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		wc.pluginFileList(w, pluginID)
		return
	}

	filename := parts[1]
	if !validatePluginFileName(filename) {
		httpJSON(w, http.StatusBadRequest, "invalid filename")
		return
	}

	switch r.Method {
	case http.MethodGet:
		wc.pluginFileServe(w, r, pluginID, filename)
	case http.MethodDelete:
		wc.pluginFileDelete(w, pluginID, filename)
	default:
		httpJSON(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// pluginFileList — GET /api/plugin-files/{plugin_id}
func (wc *WebChannel) pluginFileList(w http.ResponseWriter, pluginID string) {
	dir := wc.pluginFileDir(pluginID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": []PluginFileMeta{}})
			return
		}
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("read dir: %v", err))
		return
	}
	files := make([]PluginFileMeta, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, PluginFileMeta{
			Filename:    e.Name(),
			URL:         fmt.Sprintf("/api/plugin-files/%s/%s", pluginID, e.Name()),
			Size:        info.Size(),
			ContentType: contentTypeFor(e.Name()),
			ModTime:     info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime > files[j].ModTime })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": files})
}

// pluginFileServe — GET /api/plugin-files/{plugin_id}/{filename}
func (wc *WebChannel) pluginFileServe(w http.ResponseWriter, r *http.Request, pluginID, filename string) {
	path := filepath.Join(wc.pluginFileDir(pluginID), filename)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		httpJSON(w, http.StatusNotFound, "file not found")
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(filename))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, path)
}

// pluginFileDelete — DELETE /api/plugin-files/{plugin_id}/{filename}
func (wc *WebChannel) pluginFileDelete(w http.ResponseWriter, pluginID, filename string) {
	path := filepath.Join(wc.pluginFileDir(pluginID), filename)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			httpJSON(w, http.StatusNotFound, "file not found")
			return
		}
		httpJSON(w, http.StatusInternalServerError, fmt.Sprintf("delete: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// httpJSON writes a simple JSON error response.
func httpJSON(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
