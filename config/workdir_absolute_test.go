package config

import (
	"os"
	"path/filepath"
	"testing"
)

// 2026-09-16 线上严重 bug（用户报告）：
//
//	「新建 session 报错，而且它的 pwd 不是我创建的时候选择的 pwd」——
//	「用户输入了绝对路径，怎么能当作相对路径处理呢？」
//
// 根因：config.json 被事故重写成默认值 ⇒ `agent.work_dir` 丢失 ⇒ 缺省值曾是 "."；
// 而 work_dir 是 **per-user workspace 的根**：
//
//	tools.UserWorkspaceRoot(workDir, uid) = {workDir}/.xbot/users/{uid}/workspace
//
// ⇒ workDir="." 时产出**相对工作区** `.xbot/users/<uid>/workspace`，于是
//
//	· 会话 cwd 不是用户创建时选择的绝对路径；
//	· 各级目录 `no such file or directory`
//	  （活日志实证：dir=.xbot/users/web-4/workspace/agents … no such file or directory）。
//
// 契约（本用例钉死）：**走 server 的真实入口 `Load()`** 后，缺省 `agent.work_dir`
// 必须是绝对路径（用户 home），永不为相对路径。
//
// 注意：必须用 `Load()`（它会套默认值）——`LoadFromFile()` 只做 JSON 反序列化、
// 不填默认值；用错入口会得出"修复无效"的假结论（本用例第一版就踩了这个坑）。
func TestAgentWorkDirDefaultMustBeAbsolute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home) // 让 Load() 读到隔离的 config.json
	t.Setenv("WORK_DIR", "")    // 环境变量优先级更高，必须置空才能走缺省分支
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	cfg := Load()
	if cfg == nil {
		t.Fatal("Load() 返回 nil")
	}
	if cfg.Agent.WorkDir == "" {
		t.Fatal("agent.work_dir 缺省不得为空")
	}
	if !filepath.IsAbs(cfg.Agent.WorkDir) {
		t.Fatalf("agent.work_dir 缺省必须是绝对路径，实际 %q —— 相对路径会让工作区退化成 .xbot/users/<uid>/workspace（2026-09-16 线上 bug）", cfg.Agent.WorkDir)
	}
	if want, err := os.UserHomeDir(); err == nil && want != "" && cfg.Agent.WorkDir != want {
		t.Fatalf("agent.work_dir 缺省应为用户 home %q，实际 %q", want, cfg.Agent.WorkDir)
	}
}
