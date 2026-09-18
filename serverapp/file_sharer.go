package serverapp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"xbot/channel/web"
	"xbot/tools"

	"github.com/google/uuid"
)

// webFileSharer implements tools.FileSharer using the web OSSProvider.
// For local storage: copies the file to <uploadRoot>/agent/<uuid>/<name>.
// For cloud storage (qiniu/s3): uploads via provider.Upload and returns a
// signed download URL.
type webFileSharer struct {
	provider   web.OSSProvider // nil = local-only (no cloud OSS)
	uploadRoot string          // <xbotHome>/uploads — where local files live
}

// NewWebFileSharer creates a FileSharer that publishes local files to the
// configured storage provider. The returned URL is web-accessible.
func NewWebFileSharer(provider web.OSSProvider, xbotHome string) tools.FileSharer {
	return &webFileSharer{
		provider:   provider,
		uploadRoot: web.LocalUploadRoot(xbotHome),
	}
}

func (s *webFileSharer) ShareFile(localPath string, displayName string) (string, error) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(localPath))
	if displayName == "" {
		displayName = filepath.Base(localPath)
	}

	// Name: strip any extension the caller supplied, then append the SOURCE
	// file's real extension — so the key ends with exactly one extension that
	// matches the content (the download endpoint derives Content-Type from it).
	// ⛔ 不能无条件 `displayName + ext`：默认显示名就是带扩展名的文件名，
	// 会拼出 `chart.png.png`（单测 TestWebFileSharer_LocalCopiesFileAndReturnsURL 抓到）。
	base := sanitizeFileName(displayName)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	// Key: agent/<uuid>/<name> — namespace separates agent-published
	// files from user uploads (uploads/<uid>/...). The /api/files/download
	// endpoint serves both prefixes.
	key := fmt.Sprintf("agent/%s/%s%s", uuid.New().String(), base, ext)

	// Local storage: write to disk (same root as user uploads — the HTTP
	// handler serves from <uploadRoot>/<key>).
	if s.provider == nil || s.provider.Name() == "local" {
		dest := filepath.Join(s.uploadRoot, key)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", fmt.Errorf("create dir: %w", err)
		}
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return "", fmt.Errorf("write file: %w", err)
		}
		// Return the stable relative URL — the web handler serves it.
		// Images get &inline=1 for browser-side rendering; other files
		// get attachment semantics (no inline).
		ref := "/api/files/download?key=" + url.QueryEscape(key)
		if isImageExt(ext) {
			ref += "&inline=1"
		}
		return ref, nil
	}

	// Cloud storage: upload via provider and return the download URL.
	if err := s.provider.Upload(key, data); err != nil {
		return "", fmt.Errorf("upload to %s: %w", s.provider.Name(), err)
	}
	dlURL, err := s.provider.GetDownloadURL(key)
	if err != nil {
		return "", fmt.Errorf("get download URL: %w", err)
	}
	return dlURL, nil
}

// ── helpers ───────────────────────────────────────────────────────────

func sanitizeFileName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, filepath.Base(name))
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico":
		return true
	}
	return false
}
