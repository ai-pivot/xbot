package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestRole 在 dir 下写一个最小 agent 定义（frontmatter: name + description）。
// isolateRoleDirs 隔离全局 agentsDir（枚举里还有内嵌角色，用独特名字避免碰撞）。
// 用户要求（2026-09-16）：role 缺省/拼写不准都**尽量匹配**，只有有歧义才报错。

// TestResolveSubAgentRole_NoInference 锁定「任何地方都不能推断」（用户 2026-09-17）：
// role 必须显式且精确；缺 role / 拼错 / 名字不存在一律报错，绝不猜。
func TestResolveSubAgentRole_NoInference(t *testing.T) {
	dir := t.TempDir()
	writeRole := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\nname: "+name+"\ndescription: test role\n---\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeRole("explore")
	writeRole("code-reviewer")

	// 缺 role ⇒ 报错（提示 role 必填），且角色/autoMatched 都为空
	role, auto, err := ResolveSubAgentRoleSandbox(context.Background(), "", "look into the flaky test", nil, "u", dir)
	if err == nil || role != nil || auto || !strings.Contains(err.Error(), "role is required") {
		t.Fatalf("缺 role 必须报错且不猜：role=%v auto=%v err=%v", role, auto, err)
	}
	// 拼错的 role ⇒ 报错（不做容错匹配）
	role, auto, err = ResolveSubAgentRoleSandbox(context.Background(), "explorr", "look into the flaky test", nil, "u", dir)
	if err == nil || role != nil || auto || !strings.Contains(err.Error(), "unknown SubAgent role") {
		t.Fatalf("拼错的 role 不得被容错匹配：role=%v auto=%v err=%v", role, auto, err)
	}
	// 精确（含大小写/分隔符规范化）⇒ 命中
	role, auto, err = ResolveSubAgentRoleSandbox(context.Background(), "Code Reviewer", "x", nil, "u", dir)
	if err != nil || role == nil || auto || role.Name != "code-reviewer" {
		t.Fatalf("规范化精确匹配应命中：role=%v auto=%v err=%v", role, auto, err)
	}
}
