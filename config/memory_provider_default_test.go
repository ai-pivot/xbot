package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 用户决策 2026-09-16：memory_provider 缺省应为内置 **xbot**（原缺省为 "flat"）。
//
// 背景：注册的 provider 只有 memory/{xbot,flat,letta}（见各 provider 的
// init() → memory.RegisterProviderFactory），而 memory.CreateProvider 对**未注册名
// 返回 nil** —— 缺省若落在一个不存在的名字上，等于静默失去记忆能力。所以缺省必须
// 是已注册的 xbot。
//
// 契约（本用例钉死）：走 server 真实入口 Load()（会套默认值），缺省即 "xbot"。
// 注意不要用 LoadFromFile()（它只做 JSON 反序列化、不填默认值）。
func TestMemoryProviderDefaultIsXbot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	t.Setenv("MEMORY_PROVIDER", "") // env 优先级更高，置空才能走缺省分支
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg := Load()
	if cfg == nil {
		t.Fatal("Load() 返回 nil")
	}
	if cfg.Agent.MemoryProvider != "xbot" {
		t.Fatalf("memory_provider 缺省应为 xbot，实际 %q", cfg.Agent.MemoryProvider)
	}
}
