package plugin

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

// captureHook 收集测试期间的日志条目（message + data 拼成一行便于 substring 断言）。
type captureHook struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *captureHook) Fire(e *logrus.Entry) error {
	line := e.Message
	if len(e.Data) > 0 {
		for k, v := range e.Data {
			line += " " + k + "=" + toString(v)
		}
	}
	h.mu.Lock()
	h.msgs = append(h.msgs, line)
	h.mu.Unlock()
	return nil
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		return ""
	}
}

func (h *captureHook) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// installCaptureHook 在测试期间接管全局 logrus 输出。
func installCaptureHook(t *testing.T) *captureHook {
	t.Helper()
	hook := &captureHook{}
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

// TestDiscoverPlugins_SilentOnResidueDirWithoutManifest pins the 2026-10-31 finding:
// the production serve window logged 40× "Skipping invalid plugin: read manifest
// …/plugin.json: no such file or directory" — all of them from leftover
// directories that contain only logs/ (genui 20, qq 20).
//
// Such a directory is NOT a broken plugin, it is simply not a plugin. Only a
// directory that HAS a plugin.json but fails to load is worth a warning.
func TestDiscoverPlugins_SilentOnResidueDirWithoutManifest(t *testing.T) {
	// 不使用 t.Parallel()：替换的是全局 logrus hook。
	hook := installCaptureHook(t)

	plugins := filepath.Join(t.TempDir(), "plugins")
	residue := filepath.Join(plugins, "qq")
	if err := os.MkdirAll(filepath.Join(residue, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverPlugins([]string{plugins})

	if len(got) != 0 {
		t.Fatalf("残留目录被当成插件发现: %d 个", len(got))
	}
	if n := hook.count("Skipping invalid plugin"); n != 0 {
		t.Fatalf("残留目录（无 plugin.json）仍打出 %d 条 'Skipping invalid plugin' warning，生产会长期拉红", n)
	}
	if n := hook.count("Failed to stat plugin manifest"); n != 0 {
		t.Fatalf("stat 正常路径不应产生 warning，实测 %d 条", n)
	}
}

// TestDiscoverPlugins_WarnsOnBrokenManifest 是上一条的判别力对照：
// 「有 plugin.json 但内容非法」必须仍然告警 —— 否则本次修复会把真问题一起静音。
func TestDiscoverPlugins_WarnsOnBrokenManifest(t *testing.T) {
	hook := installCaptureHook(t)

	plugins := filepath.Join(t.TempDir(), "plugins")
	broken := filepath.Join(plugins, "broken.plugin")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "plugin.json"), []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := DiscoverPlugins([]string{plugins})

	if len(got) != 0 {
		t.Fatalf("非法 manifest 不应被发现为插件: %d 个", len(got))
	}
	if n := hook.count("Skipping invalid plugin"); n != 1 {
		t.Fatalf("坏 manifest 必须恰好 1 条 warning，实测 %d 条", n)
	}
}
