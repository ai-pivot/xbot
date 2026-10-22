package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// role_rules_test.go — rules 注入的唯一实现 RoleSystemPrompt + 汇聚点兜底 RolePromptForName。
//
// 背景（2026-10-22 review）：rules 注入曾经只覆盖 tools/subagent.go 的 3 处调用点，
// CreateChat 的 4 处与重启恢复（agent/interactive_persist.go）全部裸传 role.SystemPrompt，
// 表现为"部门起得来但不带 CHARTER"。现在注入收敛到 RoleSystemPrompt 一个实现，
// 并在 prompt 汇聚点用 RolePromptForName 兜底 —— 两者都必须有单测钉住语义。

func TestRoleSystemPrompt(t *testing.T) {
	home := t.TempDir()
	old := xbotHomeDir
	xbotHomeDir = home
	t.Cleanup(func() { xbotHomeDir = old })

	writeMD(t, filepath.Join(home, "court", "CHARTER.md"), "# 条例\n不许重启服务。")

	// 1) nil role → 空串（调用方会回落到默认 prompt）
	if got := RoleSystemPrompt(nil); got != "" {
		t.Fatalf("nil role: got %q, want empty", got)
	}

	// 2) 没声明 rules → 一个字都不多读
	plain := &SubAgentRole{Name: "tester", SystemPrompt: "我是测试"}
	if got := RoleSystemPrompt(plain); got != "我是测试" {
		t.Fatalf("role without rules must not be modified, got %q", got)
	}

	// 3) 声明了 rules → 追加，带可读标题
	role := &SubAgentRole{Name: "court.x", SystemPrompt: "我是X", Rules: []string{"court/CHARTER.md"}}
	got := RoleSystemPrompt(role)
	if !strings.HasPrefix(got, "我是X") {
		t.Fatalf("role prompt must be preserved as prefix, got %q", got)
	}
	if !strings.Contains(got, "## 规则文件：court/CHARTER.md") || !strings.Contains(got, "不许重启服务") {
		t.Fatalf("rules not injected: %q", got)
	}

	// 4) 工具层薄包装必须与导出版完全等价（源码级守护测试依赖 appendRoleRules 这个标识符）
	if wrapper := appendRoleRules(nil, role); wrapper != got {
		t.Fatalf("appendRoleRules != RoleSystemPrompt:\n wrapper=%q\n direct =%q", wrapper, got)
	}

	// 5) 被拒绝的路径（绝对路径 / 越界）不得改动 prompt，也不得让 spawn 失败
	bad := &SubAgentRole{Name: "court.x", SystemPrompt: "我是X", Rules: []string{"/etc/passwd", "../../.ssh/id_rsa"}}
	if got := RoleSystemPrompt(bad); got != "我是X" {
		t.Fatalf("rejected rule paths must leave prompt untouched, got %q", got)
	}

	// 6) 缺文件只跳过，不阻断
	missing := &SubAgentRole{Name: "court.x", SystemPrompt: "我是X", Rules: []string{"court/missing.md"}}
	if got := RoleSystemPrompt(missing); got != "我是X" {
		t.Fatalf("missing rule file must be skipped, got %q", got)
	}
}

// TestRolePromptForName —— prompt 汇聚点兜底：入口漏注入时补上，已注入时不重复追加。
func TestRolePromptForName(t *testing.T) {
	home := t.TempDir()
	agentsRoot := filepath.Join(home, "agents")
	writeMD(t, filepath.Join(agentsRoot, "court", "x.md"), `---
name: x
description: d
rules:
  - court/CHARTER.md
---
role body`)
	writeMD(t, filepath.Join(home, "court", "CHARTER.md"), "# 条例\n不许重启服务。")

	oldAgents, oldHome := agentsDir, xbotHomeDir
	t.Cleanup(func() { agentsDir, xbotHomeDir = oldAgents, oldHome })
	if err := InitAgentRoles(agentsRoot); err != nil {
		t.Fatalf("InitAgentRoles: %v", err)
	}
	role, ok := GetSubAgentRole("court.x")
	if !ok {
		t.Fatal("role court.x not found")
	}

	// 1) 调用方漏注入（裸传磁盘上的 SystemPrompt）→ 兜底补上
	if got := RolePromptForName("court.x", role.SystemPrompt); !strings.Contains(got, "不许重启服务") {
		t.Fatalf("fallback did not inject rules: %q", got)
	}

	// 2) 调用方已注入 → 原样返回，且规则只出现一次（不重复追加）
	injected := RoleSystemPrompt(role)
	got := RolePromptForName("court.x", injected)
	if got != injected {
		t.Fatalf("already-injected prompt must be returned unchanged")
	}
	if n := strings.Count(got, "不许重启服务"); n != 1 {
		t.Fatalf("rules injected %d times, want 1", n)
	}

	// 3) 自定义 prompt（与磁盘定义不同）→ 不碰
	if got := RolePromptForName("court.x", "自定义角色描述"); got != "自定义角色描述" {
		t.Fatalf("custom prompt must be left alone, got %q", got)
	}

	// 4) 未知 role / 空名 / 空 prompt → 原样返回（兜底不能凭空造 prompt）
	for _, tc := range []struct{ name, prompt string }{
		{"no-such-role", "x"},
		{"", "x"},
		{"court.x", ""},
	} {
		if got := RolePromptForName(tc.name, tc.prompt); got != tc.prompt {
			t.Fatalf("RolePromptForName(%q, %q) = %q, want unchanged", tc.name, tc.prompt, got)
		}
	}

	// 5) 无 rules 的 role 也不动（避免给所有子代理塞空段落）
	writeMD(t, filepath.Join(agentsRoot, "court", "plain.md"), "---\nname: plain\ndescription: d\n---\nplain body")
	if got := RolePromptForName("court.plain", "plain body"); got != "plain body" {
		t.Fatalf("role without rules must not be modified, got %q", got)
	}

	// 6) 声明了 rules 但一个都读不到 → 原样返回：
	//    RoleSystemPrompt 已逐个告警，兜底不能再误报"调用方漏注入"。
	writeMD(t, filepath.Join(agentsRoot, "court", "brokenrule.md"), `---
name: br
description: d
rules:
  - court/NOPE.md
---
br body`)
	if got := RolePromptForName("court.br", "br body"); got != "br body" {
		t.Fatalf("unreadable rules must leave prompt untouched, got %q", got)
	}

	// 7) role 正文里本来就含 marker 文本 → 不当作"已注入"
	writeMD(t, filepath.Join(agentsRoot, "court", "marker.md"), `---
name: mk
description: d
rules:
  - court/CHARTER.md
---
正文里写着 ## 规则文件： 这几个字`)
	got = RolePromptForName("court.mk", "正文里写着 ## 规则文件： 这几个字")
	if !strings.Contains(got, "不许重启服务") {
		t.Fatalf("role body containing the marker must still get rules injected, got %q", got)
	}
}

// TestSkippedRoleDefinitions_Counted —— 坏定义被跳过时必须留痕（否则只表现为运行期 unknown role）。
func TestSkippedRoleDefinitions_Counted(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "court", "ok.md"), "---\nname: ok\ndescription: d\n---\nbody")
	writeMD(t, filepath.Join(root, "court", "broken.md"), "no frontmatter here")

	skippedRoleDefs.Store(0)
	roles, err := LoadAgentRoles(root)
	if err != nil {
		t.Fatalf("LoadAgentRoles must not fail on a broken namespaced definition: %v", err)
	}
	if len(roles) != 1 || roles[0].Name != "court.ok" {
		t.Fatalf("roles = %v, want only court.ok", roles)
	}
	if n := skippedRoleDefs.Load(); n != 1 {
		t.Fatalf("skippedRoleDefs = %d, want 1", n)
	}
}
