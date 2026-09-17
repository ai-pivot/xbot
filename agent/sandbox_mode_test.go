package agent

import (
	"strings"
	"testing"
)

// P0（2026-09-16 用户报告）：新建会话报错
//
//	{"code":"internal_error","message":"CWD sync not supported in docker sandbox mode"}
//
// 根因链（实证）：config.json 里**没有 sandbox 配置** ⇒ cfg.SandboxMode == ""
// （serverapp/server_core.go: SandboxMode = cfg.Sandbox.Mode）⇒ agent.New 把空值
// **写死成 "docker"**（旧 agent/agent.go 的 `if sandboxMode == "" { sandboxMode = "docker" }`）
// ⇒ setCWD 的 `if a.sandboxMode != "none"` 直接拒绝 ⇒ 新建会话（必设 CWD）失败。
//
// 契约（用户要求）：**默认 sandbox 必须是 none**；CWD 是**会话级**状态
// （DB tenants.cwd），与沙箱无关 —— 空值视作 none，且不再存在 docker 模式的
// 拒绝分支（本地 docker sandbox 已整体删除，沙箱统一走 runner/remote 接入）。
func TestSetCWD_NeverRejectsWithDockerSandboxError(t *testing.T) {
	for _, mode := range []string{"", "none", "remote"} {
		a := &Agent{sandboxMode: mode}
		err := a.setCWD("web", "chat-1", "/tmp", true)
		if err != nil && strings.Contains(err.Error(), "CWD sync not supported") {
			t.Fatalf("mode=%q: setCWD rejected a session-level CWD operation: %v", mode, err)
		}
	}
}

// resolveSandboxMode：空值 ⇒ none（绝不再默认 docker）；历史 "docker" ⇒ none；
// remote/none 原样保留。
func TestResolveSandboxMode_DefaultsToNone(t *testing.T) {
	cases := map[string]string{
		"":       "none",
		"none":   "none",
		"docker": "none", // 本地 docker sandbox 已删除 ⇒ 归一化为 none
		"remote": "remote",
	}
	for in, want := range cases {
		if got := resolveSandboxMode(in); got != want {
			t.Fatalf("resolveSandboxMode(%q) = %q, want %q", in, got, want)
		}
	}
}
