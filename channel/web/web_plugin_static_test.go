package web

// web_plugin_static_test.go — /plugins/<id>/web/* 静态服务的首个覆盖（此前
// handlePluginStatic 零测试："插件 web entry 是否真被服务(200)"无人断言，
// 运行时缺文件只是静默 404 —— xbot.iteration-stats 产物丢失事故的盲区之一）。
//
// 覆盖：
//  1. 已存在的 web entry → 200 且内容正确（固定名入口带 no-cache 协商缓存；
//     chunk-* 内容哈希文件带 immutable）；
//  2. 缺失的文件 / 缺失的 web 目录 → 404 且打含 "plugin-static-miss" 的 WARN
//     （WARN 是排查"装了插件但产物没跟上"的唯一运行时线索）；
//  3. 目录解析遵循 plugin.DefaultPluginDirs（$XBOT_HOME/plugins 用户层优先，
//     $XBOT_HOME/plugins/builtin 次之）。
//
// 判别力：删掉 web.go miss 分支的 log.Warn → TestPluginStaticMissWarns404 必红。

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"

	"xbot/config"
	"xbot/plugin"
)

// staticCaptureHook 抓取 logrus 全局 logger 的输出（xbot/logger 包装的即是
// 它；handlePluginStatic 的 miss-WARN 经此可见）。
type staticCaptureHook struct {
	mu      sync.Mutex
	entries []string
}

func (h *staticCaptureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *staticCaptureHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	line, err := e.String()
	if err != nil {
		line = e.Message
	}
	h.entries = append(h.entries, line)
	return nil
}

func (h *staticCaptureHook) contains(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.entries {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

// installStaticHook redirects the global logrus output into a capture hook for
// the duration of the test (restored afterwards) so miss-WARNs are assertable
// without polluting the test log.
func installStaticHook(t *testing.T) *staticCaptureHook {
	t.Helper()
	hook := &staticCaptureHook{}
	prevHooks := logrus.StandardLogger().ReplaceHooks(logrus.LevelHooks{})
	prevOut := logrus.StandardLogger().Out
	logrus.SetOutput(io.Discard)
	logrus.AddHook(hook)
	t.Cleanup(func() {
		logrus.StandardLogger().ReplaceHooks(prevHooks)
		logrus.SetOutput(prevOut)
	})
	return hook
}

// writePluginWebFile creates <root>/<pluginID>/web/<name> with content.
func writePluginWebFile(t *testing.T, root, pluginID, name, content string) {
	t.Helper()
	dir := filepath.Join(root, pluginID, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func getPluginStatic(t *testing.T, wc *WebChannel, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	wc.handlePluginStatic(rec, req)
	return rec
}

// TestPluginStaticServesExistingEntry — 存在的插件 web entry ⇒ 200 + 内容正确。
func TestPluginStaticServesExistingEntry(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)

	root := t.TempDir()
	const entry = "export default function Badge() { return null }\n"
	writePluginWebFile(t, root, "xbot.iteration-stats", "index.js", entry)
	wc.SetPluginDirs([]string{root})

	rec := getPluginStatic(t, wc, "/plugins/xbot.iteration-stats/web/index.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("existing web entry must serve 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != entry {
		t.Fatalf("served content mismatch:\n got: %q\nwant: %q", got, entry)
	}
	// 固定名入口：no-cache 协商缓存（immutable 会让插件热更新永不生效）。
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("fixed-name entry must be Cache-Control: no-cache, got %q", cc)
	}

	// chunk-* 内容哈希文件：immutable 长缓存。
	writePluginWebFile(t, root, "xbot.iteration-stats", "chunk-ABCD1234.js", "export const x = 1\n")
	rec = getPluginStatic(t, wc, "/plugins/xbot.iteration-stats/web/chunk-ABCD1234.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("chunk file must serve 200, got %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("chunk-* file must be immutable-cached, got %q", cc)
	}
}

// TestPluginStaticMissWarns404 — 缺失的文件 / 缺失的 web 目录 ⇒ 404 且
// 日志含 "plugin-static-miss"（含插件 id）。删掉 web.go 的 log.Warn 此测试必红。
func TestPluginStaticMissWarns404(t *testing.T) {
	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)
	hook := installStaticHook(t)

	root := t.TempDir()
	// 插件目录存在但只装了别的文件 —— 请求缺失的产物（真实事故形态）。
	writePluginWebFile(t, root, "xbot.iteration-stats", "some-other.js", "// not the entry\n")
	if err := os.MkdirAll(filepath.Join(root, "xbot.genui"), 0o755); err != nil {
		t.Fatal(err)
	}
	wc.SetPluginDirs([]string{root})

	cases := []struct {
		name string
		path string
	}{
		{"file-missing", "/plugins/xbot.iteration-stats/web/index.js"},
		{"webdir-missing", "/plugins/xbot.genui/web/index.js"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := getPluginStatic(t, wc, tc.path)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: want 404 for missing artifact, got %d", tc.path, rec.Code)
			}
			if !hook.contains("plugin-static-miss") {
				t.Fatalf("%s: 404 for a missing plugin artifact must log a WARN containing %q", tc.path, "plugin-static-miss")
			}
			if !hook.contains("xbot.iteration-stats") && !hook.contains("xbot.genui") {
				t.Fatalf("%s: miss WARN must name the plugin id", tc.path)
			}
		})
	}
}

// TestPluginStaticDefaultDirsResolution — 目录解析遵循 plugin.DefaultPluginDirs：
// $XBOT_HOME/plugins（用户层）优先于 $XBOT_HOME/plugins/builtin（发布层）。
func TestPluginStaticDefaultDirsResolution(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)

	db := newTestDB(t)
	wc, _ := newTestWebChannel(t, db)

	userDir := filepath.Join(home, "plugins")
	builtinDir := filepath.Join(home, "plugins", "builtin")
	writePluginWebFile(t, userDir, "xbot.iteration-stats", "index.js", "// user-layer\n")
	writePluginWebFile(t, builtinDir, "xbot.iteration-stats", "index.js", "// builtin-layer\n")
	writePluginWebFile(t, builtinDir, "xbot.only-builtin", "index.js", "// builtin only\n")

	wc.SetPluginDirs(plugin.DefaultPluginDirs(config.XbotHome()))

	// builtin 独有插件：仅 builtin 层有 → 200（builtin 目录被解析到）。
	rec := getPluginStatic(t, wc, "/plugins/xbot.only-builtin/web/index.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "// builtin only\n" {
		t.Fatalf("builtin layer must be scanned by DefaultPluginDirs (code=%d body=%q)", rec.Code, rec.Body.String())
	}

	// 两层都有：用户层（扫描序在前）优先。
	rec = getPluginStatic(t, wc, "/plugins/xbot.iteration-stats/web/index.js")
	if rec.Code != http.StatusOK || rec.Body.String() != "// user-layer\n" {
		t.Fatalf("user dir must take precedence over builtin (code=%d body=%q)", rec.Code, rec.Body.String())
	}
}
