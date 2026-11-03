package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 2026-11-03 用户要求：content 未变时 SaveToFile 必须**完全不落盘**。
//
// 现场：某 3 分钟内 config.json 被保存 11 次，11 个 .bak-* 之间 diff 全 0 行 ——
// 内容一个字没变，却每次都 写临时文件 → 备份 → rename → 轮转。
//
// 判定方式（要硬）：包内 saveWriteObserver 钩子（生产为 nil）**计成功落盘的次数**，
// 再加 mtime / inode / 内容 / .bak-* 计数 / .tmp 残留做旁证。
// 只比 mtime 是不够的：同一秒内两次写入 mtime 可能完全相同。

// installSaveWriteCounter 装上落盘计数器，返回「已落盘次数」的读取函数。
func installSaveWriteCounter(t *testing.T) func() int {
	t.Helper()
	prev := saveWriteObserver
	writes := 0
	saveWriteObserver = func(_ string, _ int) { writes++ }
	t.Cleanup(func() { saveWriteObserver = prev })
	return func() int { return writes }
}

func countBackupFiles(t *testing.T, dir, base string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, base+".bak-*"))
	if err != nil {
		t.Fatalf("glob %s.bak-*: %v", base, err)
	}
	return len(matches)
}

func countTmpFiles(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob tmp files: %v", err)
	}
	return len(matches)
}

// TestSaveToFile_SkipsWriteWhenContentUnchanged 是本 bug 的回归用例：
// 同一个 cfg 连续保存两次（内容相同）⇒ 只发生 **1 次** 写入（第二次 0 次），
// 且第二次不产生 .bak-*、不动 mtime、不换 inode、不改文件内容。
func TestSaveToFile_SkipsWriteWhenContentUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	base := filepath.Base(path)

	written := installSaveWriteCounter(t)

	cfg := &Config{Server: ServerConfig{Host: "127.0.0.1", Port: 23456}}
	cfg.Agent.WorkDir = "/tmp/p11-config-nowrite"

	// ── 第一次：文件不存在 ⇒ 必须真的写 ──
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("首次 SaveToFile: %v", err)
	}
	firstWrites := written()
	if firstWrites != 1 {
		t.Fatalf("首次保存必须落盘 1 次，实际 %d 次", firstWrites)
	}

	st1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after first save: %v", err)
	}
	content1, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first save: %v", err)
	}
	baks1 := countBackupFiles(t, dir, base)

	// ── 第二次：内容一字未变 ⇒ 必须完全短路 ──
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("第二次 SaveToFile（内容未变）: %v", err)
	}
	if got := written(); got != firstWrites {
		t.Fatalf("内容未变时第二次保存不得落盘：第一次=%d 次，两次合计=%d 次（应为 %d）",
			firstWrites, got, firstWrites)
	}

	st2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after second save: %v", err)
	}
	if !st2.ModTime().Equal(st1.ModTime()) {
		t.Errorf("内容未变时不得改动 mtime：before=%v after=%v", st1.ModTime(), st2.ModTime())
	}
	if !os.SameFile(st1, st2) {
		t.Errorf("内容未变时不得替换文件（inode 应保持不变）")
	}
	content2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second save: %v", err)
	}
	if !bytes.Equal(content1, content2) {
		t.Errorf("内容未变时文件字节不得改变")
	}
	if baks2 := countBackupFiles(t, dir, base); baks2 != baks1 {
		t.Errorf("内容未变时不得新增 .bak-*：before=%d after=%d", baks1, baks2)
	}
	if n := countTmpFiles(t, dir); n != 0 {
		t.Errorf("目录里不得留下 .tmp 临时文件，实际 %d 个", n)
	}

	// ── 反证：内容真的变了 ⇒ 必须照常写（短路不得变成"永远不写"）──
	cfg.Server.Port = 23457
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("内容变化的 SaveToFile: %v", err)
	}
	if got := written(); got != firstWrites+1 {
		t.Fatalf("内容变化时必须落盘：合计 %d 次（应为 %d）", got, firstWrites+1)
	}
	content3, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after changed save: %v", err)
	}
	if bytes.Equal(content2, content3) {
		t.Errorf("内容变化后文件必须更新")
	}
}

// TestSaveToFile_SecondSaveSkipsEvenAfterUnknownFieldMerge —
// 首次保存会把 struct 的字段序写成文件，第二次合并路径产生的是 map 键序；
// 两者内容相同、字节序不同 —— 也必须短路（否则本 bug 还剩一半：每次启动多写一次）。
// 同时验证未知顶层 key 在短路之后依然在文件里（短路不能变成丢数据）。
func TestSaveToFile_SecondSaveSkipsEvenAfterUnknownFieldMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"sentinel_user_key":"keep-me","agent":{"work_dir":"/home/smith"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	written := installSaveWriteCounter(t)

	cfg := &Config{}
	cfg.Agent.WorkDir = "/home/smith"

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("首次 SaveToFile: %v", err)
	}
	if got := written(); got != 1 {
		t.Fatalf("既有文件内容不同 ⇒ 首次必须落盘 1 次，实际 %d 次", got)
	}

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("第二次 SaveToFile: %v", err)
	}
	if got := written(); got != 1 {
		t.Fatalf("第二次内容未变必须短路：合计落盘 %d 次（应为 1）", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"sentinel_user_key": "keep-me"`)) {
		t.Fatalf("短路不得丢既有顶层 key，实际内容:\n%s", raw)
	}
}

// TestSaveToFile_UnreadableExistingFileStillWrites — 边界：读不到既有文件
// （权限问题）时**必须回落原写入逻辑**。绝不能因为「读不到」就跳过写入，否则丢配置。
func TestSaveToFile_UnreadableExistingFileStillWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 身份运行时 chmod 0000 无法制造读取失败")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"work_dir":"/old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	written := installSaveWriteCounter(t)

	cfg := &Config{}
	cfg.Agent.WorkDir = "/new"
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("读不到既有文件时必须回落原写入逻辑（不得返回错误）: %v", err)
	}
	if got := written(); got != 1 {
		t.Fatalf("读不到既有文件也必须照常写入：落盘 %d 次（应为 1）", got)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`/new`)) {
		t.Fatalf("回落写入必须真的写进新内容，实际:\n%s", raw)
	}
}
