package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewEmptyManager_UsableWhenConfigBroken pins the 2026-10-27 panic fix.
//
// 触发方式（首辅实测）：~/.xbot/hooks.json 写成 {"hooks":[]} —— 数组无法反序列化
// 成 map[string][]EventGroup，NewManager 返回 (nil, err)。agent.New 那时只打一条
// WARN 又"重试"同一个调用，必然同样失败，于是 hookManager 为 nil，紧接着
// RegisterBuiltin 就 nil deref，进程退出码 2。
//
// 正确行为：配置坏了只让 hooks 失效，Manager 本身必须可用。
func TestNewEmptyManager_UsableWhenConfigBroken(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hooks.json"), []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// 前提确认：坏配置确实让 NewManager 失败（否则这个测试就没有意义了）。
	if _, err := NewManager(home, home); err == nil {
		t.Fatal("precondition failed: NewManager accepted {\"hooks\":[]} — " +
			"if this ever parses, the degraded path is no longer exercised")
	}

	m := NewEmptyManager(home, home)
	if m == nil {
		t.Fatal("NewEmptyManager returned nil — the caller would nil-deref on RegisterBuiltin")
	}

	// 必须能正常接受 builtin 注册（这就是原崩溃点）。
	// 这就是原崩溃点：nil Manager 上调用它会 panic。
	m.RegisterBuiltin(LoggingCallback())

	// 默认执行器要齐，否则 hooks.json 修好后 reload 会缺 executor。
	for _, typ := range []string{"command", "http"} {
		if m.getExecutor(typ) == nil {
			t.Errorf("empty manager is missing the %q executor", typ)
		}
	}

	// 配置对象不能是 nil（下游有 config.Hooks 的直接遍历）。
	if m.config == nil || m.config.Hooks == nil {
		t.Fatal("empty manager has a nil config/Hooks map")
	}
	if len(m.config.Hooks) != 0 {
		t.Errorf("expected no hooks, got %d", len(m.config.Hooks))
	}
}

// TestNewManager_AndEmptyShareExecutors guards the refactor: both entry points
// must produce the same default executor set, otherwise a reload after fixing
// the config could behave differently from a fresh start.
func TestNewManager_AndEmptyShareExecutors(t *testing.T) {
	home := t.TempDir() // 没有 hooks.json → LoadHooksConfig 成功且为空

	full, err := NewManager(home, home)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	empty := NewEmptyManager(home, home)

	for _, typ := range []string{"command", "http"} {
		if full.getExecutor(typ) == nil {
			t.Errorf("NewManager missing %q executor", typ)
		}
		if empty.getExecutor(typ) == nil {
			t.Errorf("NewEmptyManager missing %q executor", typ)
		}
	}
	if full.config == nil || empty.config == nil {
		t.Fatal("config must never be nil on either path")
	}
}
