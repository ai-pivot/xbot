package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// ⛔ 数据目录遮蔽（2026-10-12 P0，用户定位）：`plugin/plog.go` 的插件日志目录建在
// 扫描目录之内（`<xbotHome>/plugins/<id>/logs/`）⇒ 那个目录同样 `IsDir()`，旧
// `findPluginDir` 只判 `IsDir()` ⇒ 数据目录遮蔽了真正的插件目录（`builtin/<id>`）⇒
// stdio/grpc 插件 `state=error`、reload 报 `read manifest .../plugin.json: no such
// file or directory`。判据必须是**清单存在**。
func TestFindPluginDir_DataDirWithoutManifestMustNotShadowRealDir(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")       // 模拟 <xbotHome>/plugins（数据/日志根）
	builtinDir := filepath.Join(root, "builtin") // 模拟 builtin（真插件）
	const id = "xbot.genui"

	// 数据目录里只有 <id>/logs/ —— plog.go 的产物，没有 plugin.json
	if err := os.MkdirAll(filepath.Join(dataDir, id, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 真插件目录含清单
	if err := os.MkdirAll(filepath.Join(builtinDir, id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtinDir, id, "plugin.json"), []byte(`{"id":"`+id+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	pm := &PluginManager{}
	if got, want := pm.findPluginDir([]string{dataDir, builtinDir}, id), filepath.Join(builtinDir, id); got != want {
		t.Fatalf("数据目录（无 plugin.json）不得遮蔽真正的插件目录：got %q, want %q", got, want)
	}

	// 反向：所有候选都没有清单 ⇒ 退回第一个存在的目录（旧行为，不比以前差）
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, id, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := pm.findPluginDir([]string{empty}, id), filepath.Join(empty, id); got != want {
		t.Fatalf("没有清单时退回第一个存在的目录：got %q, want %q", got, want)
	}

	// dirs 为空 ⇒ 原样返回 pluginID（语义不变）
	if got := pm.findPluginDir(nil, id); got != id {
		t.Fatalf("dirs 为空时必须原样返回 pluginID：got %q", got)
	}

	// dirs 非空但候选都不存在 ⇒ dirs[0]/<id>（语义不变）
	none := t.TempDir()
	if got, want := pm.findPluginDir([]string{none}, id), filepath.Join(none, id); got != want {
		t.Fatalf("候选不存在时回退 dirs[0]/<id>：got %q, want %q", got, want)
	}
}
