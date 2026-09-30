package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-16 用户要求：
//
//	① 数据安全「xbot-cli 运行可能覆盖已有的 config … 一定要避免」；
//	② **「一定不能让正常情况无法启动」**（fail-closed 打在写配置路径上会把正常启动挂掉）。
//
// 契约（本文件钉死 config 侧的两条硬约束）：
//
//	① 覆盖前**尽力**留带时间戳的备份（内容 == 覆盖前原文）；
//	② 写入**绝不丢弃**现有文件的任何顶层 key，也**绝不因为数据安全守卫而拒绝写入**。
func TestSaveToFile_BacksUpExistingConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"sentinel_user_key":"keep-me","agent":{"work_dir":"/home/smith"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agent.WorkDir = "/home/smith"
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile 不得因数据安全守卫而失败（会阻塞正常启动）: %v", err)
	}

	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) == 0 {
		t.Fatal("覆盖既有 config 前必须留下 *.bak-<时间戳> 备份（用户要求：永远不可能丢配置）")
	}
	bak, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		t.Fatal(err)
	}
	if string(bak) != original {
		t.Fatalf("备份内容必须是覆盖前的原文，实际: %s", string(bak))
	}
}

func TestSaveToFile_NeverDropsExistingTopLevelKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{"sentinel_user_key":"keep-me","agent":{"work_dir":"/home/smith"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	cfg.Agent.WorkDir = "/home/smith"
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("写入必须成功（保全而非拒绝）: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("写入后的 config 必须仍是合法 JSON: %v", err)
	}
	v, ok := got["sentinel_user_key"]
	if !ok {
		t.Fatalf("写入不得丢弃既有顶层 key，实际 keys=%v", keysOf(got))
	}
	if string(v) != `"keep-me"` {
		t.Fatalf("既有顶层 key 必须原值保留，实际 %s", string(v))
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPruneConfigBackups_KeepsNewestTen — 轮转契约（2026-09-30 用户要求
// 「遗留的 Config 不要超过 10 个」）：备份每次保存写一份、不轮转会无界堆积
// （实测 ~/.xbot 两个月堆了 1189 个）。预置 15 份假备份（时间戳命名保证
// 字典序 == 时间序）→ 轮转后只留最新 10 份。确定性测试 —— 不依赖真实时间推移。
func TestPruneConfigBackups_KeepsNewestTen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for i := 1; i <= configBackupKeep+5; i++ {
		bak := filepath.Join(dir, fmt.Sprintf("config.json.bak-20260101-%06d", i))
		if err := os.WriteFile(bak, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pruneConfigBackups(path)

	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) != configBackupKeep {
		t.Fatalf("after prune: %d backups, want %d", len(matches), configBackupKeep)
	}
	// 最老的 5 份被删。
	for i := 1; i <= 5; i++ {
		gone := filepath.Join(dir, fmt.Sprintf("config.json.bak-20260101-%06d", i))
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("oldest backup %s should have been pruned", gone)
		}
	}
	// 最新 10 份保留（06..15）。
	for i := 6; i <= configBackupKeep+5; i++ {
		keep := filepath.Join(dir, fmt.Sprintf("config.json.bak-20260101-%06d", i))
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("newest backup %s must survive: %v", keep, err)
		}
	}
}

// TestSaveToFile_RotatesBackupPileOnSave — 保存路径的端到端轮转：预置超限的
// 备份堆（15 份假 + 1 份即将写入的新备份 = 16）→ SaveToFile → 堆被裁回上限，
// 且本次写入的新备份必须在（它是最新的）。轮转绝不能阻塞保存。
func TestSaveToFile_RotatesBackupPileOnSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"agent":{"work_dir":"/x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= configBackupKeep+5; i++ {
		bak := filepath.Join(dir, fmt.Sprintf("config.json.bak-20250101-%06d", i))
		if err := os.WriteFile(bak, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &Config{}
	cfg.Agent.WorkDir = "/x"
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile must never be blocked by rotation: %v", err)
	}

	matches, _ := filepath.Glob(path + ".bak-*")
	if len(matches) != configBackupKeep {
		t.Fatalf("after save: %d backups, want %d (rotation must bound the pile)", len(matches), configBackupKeep)
	}
	// 2025-01..06 被裁掉；本次保存产生的新备份（2026 时间戳）必须在。
	if _, err := os.Stat(filepath.Join(dir, "config.json.bak-20250101-000001")); !os.IsNotExist(err) {
		t.Fatal("oldest fake backup should have been rotated away by the save")
	}
	var hasNew bool
	for _, m := range matches {
		if filepath.Base(m) > "config.json.bak-2026" {
			hasNew = true
		}
	}
	if !hasNew {
		t.Fatalf("the just-written backup must survive rotation: %v", matches)
	}
}
