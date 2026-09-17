package config

import (
	"encoding/json"
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
