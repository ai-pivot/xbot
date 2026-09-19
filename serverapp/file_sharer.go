package serverapp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

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

	// Name: 统一规范化成 **URL 安全**片段（unreserved-only，见 urlSafeKeyName），
	// 再剥掉调用方给的扩展名、补上**源文件真实扩展名** —— 保证 key 恰好以一个与
	// 内容一致的扩展名结尾（下载端点据它推导 Content-Type）。
	// ⛔ 不能无条件 `displayName + ext`：默认显示名就是带扩展名的文件名，
	// 会拼出 `chart.png.png`（单测 TestWebFileSharer_LocalCopiesFileAndReturnsURL 抓到）。
	base := urlSafeKeyName(displayName)
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

// urlSafeKeyName 把展示名规范化成**统一 URL 安全**的 key 片段：只保留 RFC 3986
// unreserved 字符（A-Za-z0-9 . _ -），其余（空白、CJK、`+`、`%`、引号…）一律折叠为
// 单个 '_'，并裁掉首尾的 . _ -。
//
// 为什么必须"统一 sanitize"（用户 2026-09-19 报告：AI share 后给出的链接打不开、
// 带鉴权访问返回 not_found 的根因）：key 里一旦出现**空格**，`url.QueryEscape` 会把它
// 编码成 `+` —— 而 `+` 只在"按 query 语义解码"的客户端里等于空格；换个客户端（把 `+`
// 当字面加号、或二次编码成 `%2B`）服务端就收到**另一个 key** ⇒ /api/files/download
// 返回 not_found。现场实测正是如此：URL 文本解出的 key 与磁盘路径**逐字节相同**，
// 但带鉴权请求仍 404 ⇒ 差异发生在传输途中的 `+` 语义分歧。规范化成 unreserved-only
// 之后，`QueryEscape` / `%20` / `encodeURIComponent` 对同一个 key 产出完全相同的字节
// ⇒ 任何客户端、任何解码器都解析到同一路径。
//
// ⚠️ 同名文件不冲突：key 路径形如 `agent/<uuid>/<name>`，uuid 是**每次发布新铸**的，
// 所以"不同会话分享同名文件"天然各自独立；本函数只规范"名字片段"的可移植性，
// **不承担唯一性**（唯一性由 uuid 目录承担）。
func urlSafeKeyName(name string) string {
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(base, filepath.Ext(base))

	// 折叠规则 = 只处理**真正造成编码分歧/敌意**的字符：空白与 `+`（`QueryEscape`
	// 把空格编成 `+`，而按字面理解 `+` 的解码器会把它读成加号 —— 这就是 share 链接
	// 404 的分歧来源）以及路径/外壳敌意字符 `/\:*?"<>|`。**其余字符（含 CJK）保留**：
	// `QueryEscape` 与 `encodeURIComponent` 对它们产出完全相同的百分号编码 ⇒ 任何
	// 解码器都解析到同一路径，同时保住人类可读的下载文件名（`Ferrite 专用…方案.md`
	// → `Ferrite_专用高性能推理引擎架构改进方案.md`）。连续被替换字符折叠为单个 '_'。
	bad := func(r rune) bool {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '+' {
			return true
		}
		return strings.ContainsRune(`/\:*?"<>|`, r)
	}
	safe := func(s string) string {
		var b strings.Builder
		prevReplaced := false
		for _, r := range s {
			if bad(r) {
				if !prevReplaced {
					b.WriteByte('_')
					prevReplaced = true
				}
				continue
			}
			b.WriteRune(r)
			prevReplaced = false
		}
		return strings.Trim(b.String(), "._-")
	}

	stemSafe := safe(stem)
	if stemSafe == "" {
		stemSafe = "file"
	}
	// 名字可能含 CJK ⇒ 截断必须**按 rune**（按字节切会造出非法 UTF-8 文件名）。
	if r := []rune(stemSafe); len(r) > 80 {
		stemSafe = strings.Trim(string(r[:80]), "._-")
	}
	extSafe := safe(strings.TrimPrefix(ext, "."))
	if extSafe == "" {
		return stemSafe
	}
	return stemSafe + "." + extSafe
}

func isImageExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico":
		return true
	}
	return false
}
