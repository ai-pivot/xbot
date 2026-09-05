package agent

import (
	"testing"

	"xbot/config"
	"xbot/tools"
)

// allow_self_compact runtime handler：切换设置时 compact_context 工具
// 动态注册/注销（web 设置开关的运行时生效层）。off = 模型永远看不见该工具
// （未注册）；阈值驱动的自动压缩不受影响。
// 链路：set_setting RPC → DB 写入 + applyRuntimeSetting → 本 handler
// （ApplyConfig 写 config.json 内存态 + saveServerConfig 持久化 → 重启时
// server_core.go 的启动注册读 config.json 恢复状态，无 boot 空洞）。
func TestAllowSelfCompactSettingTogglesTool(t *testing.T) {
	a := &Agent{tools: tools.NewRegistry()}
	cfg := &config.Config{}

	// off：工具未注册。
	ApplyRuntimeSetting(cfg, a, "cli_user", "allow_self_compact", "false")
	if _, ok := a.tools.GetForSession("compact_context", 0, ""); ok {
		t.Errorf("compact_context must NOT be registered when allow_self_compact=false")
	}
	if cfg.Agent.AllowSelfCompact {
		t.Errorf("cfg.Agent.AllowSelfCompact must be false after applying false")
	}

	// on：工具注册（LLM 可见）。
	ApplyRuntimeSetting(cfg, a, "cli_user", "allow_self_compact", "true")
	if _, ok := a.tools.GetForSession("compact_context", 0, ""); !ok {
		t.Errorf("compact_context must be registered when allow_self_compact=true")
	}
	if !cfg.Agent.AllowSelfCompact {
		t.Errorf("cfg.Agent.AllowSelfCompact must be true after applying true")
	}

	// off again：注销（DisableTools 路径）。
	ApplyRuntimeSetting(cfg, a, "cli_user", "allow_self_compact", "false")
	if _, ok := a.tools.GetForSession("compact_context", 0, ""); ok {
		t.Errorf("compact_context must be unregistered when toggled back to false")
	}
}

// nil agent（远程 CLI 模式）不 panic —— ApplyAgent 的 ag==nil guard。
func TestAllowSelfCompactSettingNilAgentSafe(t *testing.T) {
	cfg := &config.Config{}
	ApplyRuntimeSetting(cfg, nil, "cli_user", "allow_self_compact", "true")
	if !cfg.Agent.AllowSelfCompact {
		t.Errorf("ApplyConfig must still run with nil agent")
	}
}
