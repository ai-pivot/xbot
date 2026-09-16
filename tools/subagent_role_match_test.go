package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestRole 在 dir 下写一个最小 agent 定义（frontmatter: name + description）。
func writeTestRole(t *testing.T, dir, file, name, desc string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write role %s: %v", name, err)
	}
}

// isolateRoleDirs 隔离全局 agentsDir（枚举里还有内嵌角色，用独特名字避免碰撞）。
func isolateRoleDirs(t *testing.T) {
	t.Helper()
	old := agentsDir
	agentsDir = ""
	t.Cleanup(func() { agentsDir = old })
}

// 用户要求（2026-09-16）：role 缺省/拼写不准都**尽量匹配**，只有有歧义才报错。
func TestResolveSubAgentRole_BestEffort(t *testing.T) {
	dir := t.TempDir()
	writeTestRole(t, dir, "auditor.md", "auditor", "database audit and schema review")
	writeTestRole(t, dir, "writer.md", "writer", "文档撰写与文字润色")
	writeTestRole(t, dir, "renderer.md", "renderer", "render pipeline profiling")
	writeTestRole(t, dir, "render-farm.md", "render-farm", "render farm scheduling")
	ctx := context.Background()
	dirs := []string{dir}

	t.Run("exact name (case/separator insensitive)", func(t *testing.T) {
		isolateRoleDirs(t)
		role, auto, err := ResolveSubAgentRoleSandbox(ctx, "AU_DITOR", "", nil, "", dirs...)
		if err != nil || role == nil {
			t.Fatalf("expected exact match, got role=%v err=%v", role, err)
		}
		if role.Name != "auditor" || auto {
			t.Fatalf("got role=%q auto=%v, want auditor/false", role.Name, auto)
		}
	})

	t.Run("typo → unique fuzzy match (auto)", func(t *testing.T) {
		isolateRoleDirs(t)
		role, auto, err := ResolveSubAgentRoleSandbox(ctx, "audit", "", nil, "", dirs...)
		if err != nil || role == nil {
			t.Fatalf("expected fuzzy match, got role=%v err=%v", role, err)
		}
		if role.Name != "auditor" || !auto {
			t.Fatalf("got role=%q auto=%v, want auditor/true", role.Name, auto)
		}
	})

	t.Run("unknown name matching ≥2 roles → ambiguous error", func(t *testing.T) {
		isolateRoleDirs(t)
		_, _, err := ResolveSubAgentRoleSandbox(ctx, "render", "", nil, "", dirs...)
		if err == nil {
			t.Fatal("expected ambiguity error for 'render' (renderer + render-farm)")
		}
		if !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "renderer") || !strings.Contains(err.Error(), "render-farm") {
			t.Fatalf("ambiguity error must list both candidates, got: %v", err)
		}
	})

	t.Run("no role + task infers the role", func(t *testing.T) {
		isolateRoleDirs(t)
		role, auto, err := ResolveSubAgentRoleSandbox(ctx, "", "请帮我做一次 database audit，检查索引设计", nil, "", dirs...)
		if err != nil || role == nil {
			t.Fatalf("expected inference from task, got role=%v err=%v", role, err)
		}
		if role.Name != "auditor" || !auto {
			t.Fatalf("got role=%q auto=%v, want auditor/true", role.Name, auto)
		}
	})

	t.Run("no role + CJK task infers via description bigrams", func(t *testing.T) {
		isolateRoleDirs(t)
		role, _, err := ResolveSubAgentRoleSandbox(ctx, "", "帮我看看这段文字润色是否通顺", nil, "", dirs...)
		if err != nil || role == nil {
			t.Fatalf("expected CJK inference, got role=%v err=%v", role, err)
		}
		if role.Name != "writer" {
			t.Fatalf("got role=%q, want writer", role.Name)
		}
	})

	t.Run("no role + tied scores → ambiguous error (never guess)", func(t *testing.T) {
		isolateRoleDirs(t)
		tieDir := t.TempDir()
		writeTestRole(t, tieDir, "a.md", "zztie-a", "shared keyword xyzzy")
		writeTestRole(t, tieDir, "b.md", "zztie-b", "shared keyword xyzzy")
		_, _, err := ResolveSubAgentRoleSandbox(ctx, "", "please use the shared keyword xyzzy", nil, "", tieDir)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("expected ambiguity error on tied scores, got: %v", err)
		}
	})

	t.Run("no role + nothing matches → error listing candidates", func(t *testing.T) {
		isolateRoleDirs(t)
		_, _, err := ResolveSubAgentRoleSandbox(ctx, "", "zzzqqq", nil, "", dirs...)
		if err == nil || !strings.Contains(err.Error(), "cannot infer") {
			t.Fatalf("expected 'cannot infer' error, got: %v", err)
		}
		if !strings.Contains(err.Error(), "auditor") {
			t.Fatalf("error must list available roles, got: %v", err)
		}
	})
}
