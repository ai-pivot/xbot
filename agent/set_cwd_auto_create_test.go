package agent

import (
	"os"
	"path/filepath"
	"testing"

	"xbot/bus"
)

// 2026-09-20 用户要求：「创建新会话时如果选择了一个不存在的目录则自动创建」。
//
// 现状（修复前）：显式工作目录（新建会话弹窗 / 会话信息里改路径 → set_cwd force
// → SetCWDForced）在目录不存在时**只打一条 Warn 然后原样落库** ⇒ 会话卡在一个
// 不存在的 cwd 上：终端/工具/Cd 全都在错的地方，用户以为"路径没生效"。
//
// 契约（本用例钉死）：
//   - 目录不存在 ⇒ **自动创建**（含多级父目录）并把 cwd 设为它；
//   - 目录已存在 ⇒ 照旧生效（不报错）；
//   - 路径被文件占住（真错误）⇒ **显式报错**，绝不静默落一个坏 cwd。
func TestSetCWDForced_CreatesMissingDirectory(t *testing.T) {
	dir := t.TempDir()
	ag, err := New(Config{
		WorkDir:        dir,
		DBPath:         filepath.Join(dir, "xbot.db"),
		XbotHome:       dir,
		SandboxMode:    "none",
		MemoryProvider: "flat",
		Bus:            bus.NewMessageBus(),
	})
	if err != nil {
		t.Fatalf("new agent: %v", err)
	}
	t.Cleanup(func() { _ = ag.Close() })

	ch, chatID := "cli", filepath.Join(dir, "session-A")
	target := filepath.Join(dir, "new", "nested", "work")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s must not exist yet (stat err=%v)", target, err)
	}

	// ① 不存在的目录 ⇒ 自动创建 + cwd 生效
	if err := ag.SetCWDForced(ch, chatID, target); err != nil {
		t.Fatalf("SetCWDForced: %v", err)
	}
	info, statErr := os.Stat(target)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("directory %s was not created: info=%v err=%v", target, info, statErr)
	}
	sess, err := ag.MultiSession().GetOrCreateSession(ch, chatID)
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	if got := sess.GetCurrentDir(); got != target {
		t.Errorf("cwd = %q, want %q", got, target)
	}

	// ② 已存在的目录 ⇒ 照旧生效，不报错
	if err := ag.SetCWDForced(ch, chatID, target); err != nil {
		t.Errorf("existing directory must be accepted as-is: %v", err)
	}

	// ③ 路径被普通文件占住 ⇒ 必须显式失败（否则会话卡在一个"不是目录"的 cwd 上）
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	if err := ag.SetCWDForced(ch, chatID, filepath.Join(blocker, "sub")); err == nil {
		t.Error("a path under a regular file must fail loudly, got nil")
	}
}
