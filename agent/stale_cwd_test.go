//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/tools"
)

// fakeCWDStore 是 cwdStore 的最小假实现 —— 守卫只依赖 Get/Set，不必拉真会话。
type fakeCWDStore struct{ cwd string }

func (f *fakeCWDStore) GetCurrentDir() string    { return f.cwd }
func (f *fakeCWDStore) SetCurrentDir(dir string) { f.cwd = dir }

// removedDir 创建目录再删掉，返回已不存在的路径。
func removedDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "gone-workdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	return dir
}

// TestResetStaleCWD_MissingDirResetsAndShellWorks 是本守卫的主用例：
// 持久化 CWD 指向不存在的目录 ⇒ 复位到 workspaceRoot，且复位后 Shell 真的可用。
//
// 先证明"不复位就坏"：在旧 CWD 上跑 Shell 必须失败（issue #457 的症状），
// 这样本测试就不是空转 —— 复位若被注释掉，下面的断言立刻变红。
func TestResetStaleCWD_MissingDirResetsAndShellWorks(t *testing.T) {
	workspaceRoot := t.TempDir()
	gone := removedDir(t)
	store := &fakeCWDStore{cwd: gone}

	// 前提：旧 CWD 上 Shell 确实硬失败（本 bug 的症状）。
	if _, err := (&tools.NoneSandbox{}).Exec(context.Background(), tools.ExecSpec{
		Shell:   true,
		Command: "echo ok",
		Dir:     gone,
		Timeout: 10 * time.Second,
	}); err == nil {
		t.Fatalf("precondition failed: Shell in deleted dir %q must fail", gone)
	}

	got := resetStaleCWD(store, "web:chat_stale_cwd", workspaceRoot)

	if got != workspaceRoot {
		t.Fatalf("resetStaleCWD must return workspaceRoot, got %q (want %q)", got, workspaceRoot)
	}
	if store.cwd != workspaceRoot {
		t.Fatalf("persisted CWD must be reset, got %q (want %q)", store.cwd, workspaceRoot)
	}
	// 复位后才能继续干活：Shell 在新 CWD 上必须可用。
	res, err := (&tools.NoneSandbox{}).Exec(context.Background(), tools.ExecSpec{
		Shell:   true,
		Command: "echo ok",
		Dir:     got,
		Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Shell after reset must work, got error: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		t.Fatalf("Shell after reset: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// TestResetStaleCWD_ExistingDirPreserved 防"一刀切复位"回归：
// 目录存在就必须原样保留，连 SetCurrentDir 都不该被调用。
func TestResetStaleCWD_ExistingDirPreserved(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := &fakeCWDStore{cwd: sub}

	got := resetStaleCWD(store, "web:chat_ok", dir)

	if got != sub {
		t.Fatalf("existing CWD must be preserved, got %q (want %q)", got, sub)
	}
	if store.cwd != sub {
		t.Fatalf("existing CWD must not be rewritten, got %q (want %q)", store.cwd, sub)
	}
}

// TestResetStaleCWD_EmptyCWDUntouched 空 CWD 不是失效，交给既有的
// promptWorkDir fallback 处理，守卫不得越权改写。
func TestResetStaleCWD_EmptyCWDUntouched(t *testing.T) {
	store := &fakeCWDStore{cwd: ""}
	got := resetStaleCWD(store, "web:chat_empty", t.TempDir())
	if got != "" {
		t.Fatalf("empty CWD must stay empty, got %q", got)
	}
	if store.cwd != "" {
		t.Fatalf("empty CWD must not be rewritten, got %q", store.cwd)
	}
}

// TestResetStaleCWD_FileInsteadOfDirResets 路径还在但已不是目录（被替换成文件），
// 同样无法作为 CWD —— 必须复位。
func TestResetStaleCWD_FileInsteadOfDirResets(t *testing.T) {
	workspaceRoot := t.TempDir()
	asFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(asFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	store := &fakeCWDStore{cwd: asFile}

	got := resetStaleCWD(store, "web:chat_file", workspaceRoot)
	if got != workspaceRoot {
		t.Fatalf("CWD pointing at a file must be reset, got %q (want %q)", got, workspaceRoot)
	}
}
