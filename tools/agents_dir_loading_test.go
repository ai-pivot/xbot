package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agents_dir_loading_test.go — role 的命名空间与声明式 rules（2026-10-22 用户要求）：
//   - role 必须能放在子目录里，不全局可用
//   - 子目录名用 "." 拼进 role 名（不用 "/"，否则会破坏 interactiveKey 解析）
//   - frontmatter 的 rules 列表必须被解析出来

func writeMD(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAgentRoles_Namespaced(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "flat.md"), "---\nname: flat\ndescription: d\n---\nbody")
	writeMD(t, filepath.Join(root, "team", "gongbu.md"), "---\nname: gongbu\ndescription: d\n---\nbody")
	writeMD(t, filepath.Join(root, "proj", "xbot", "dev.md"), "---\nname: dev\ndescription: d\n---\nbody")

	roles, err := LoadAgentRoles(root)
	if err != nil {
		t.Fatalf("LoadAgentRoles: %v", err)
	}
	got := map[string]bool{}
	for _, r := range roles {
		got[r.Name] = true
	}
	for _, want := range []string{"flat", "team.gongbu", "proj.xbot.dev"} {
		if !got[want] {
			t.Fatalf("missing role %q; got %v", want, got)
		}
	}
	// 不允许出现带 "/" 的名字
	for name := range got {
		if filepath.Base(name) != name {
			t.Fatalf("role name %q contains a separator that would break key parsing", name)
		}
	}
}

func TestLoadAgentRoles_ParsesRules(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "team", "x.md"), `---
name: x
description: d
tools:
  - Read
model: balance
rules:
  - team/CHARTER.md
  - /abs/extra.md
---
body`)

	roles, err := LoadAgentRoles(root)
	if err != nil {
		t.Fatalf("LoadAgentRoles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("want 1 role, got %d", len(roles))
	}
	r := roles[0]
	if r.Name != "team.x" {
		t.Fatalf("name = %q", r.Name)
	}
	if len(r.Rules) != 2 || r.Rules[0] != "team/CHARTER.md" || r.Rules[1] != "/abs/extra.md" {
		t.Fatalf("rules = %#v", r.Rules)
	}
	if len(r.AllowedTools) != 1 || r.AllowedTools[0] != "Read" {
		t.Fatalf("tools = %#v", r.AllowedTools)
	}
}

func TestAppendRoleRules(t *testing.T) {
	home := t.TempDir()
	old := xbotHomeDir
	xbotHomeDir = home
	t.Cleanup(func() { xbotHomeDir = old })

	writeMD(t, filepath.Join(home, "team", "CHARTER.md"), "# 条例\n不许重启服务。")

	role := &SubAgentRole{Name: "team.x", SystemPrompt: "我是X", Rules: []string{"team/CHARTER.md", "team/missing.md"}}
	out := appendRoleRules(nil, role)
	if !strings.Contains(out, "我是X") || !strings.Contains(out, "不许重启服务") {
		t.Fatalf("rules not injected: %q", out)
	}
	if strings.Contains(out, "missing") {
		t.Fatalf("missing rule should be skipped silently, got %q", out)
	}

	// 没有声明 rules 的角色一个字都不多读
	plain := &SubAgentRole{Name: "tester", SystemPrompt: "我是测试"}
	if got := appendRoleRules(nil, plain); got != "我是测试" {
		t.Fatalf("role without rules must not be modified, got %q", got)
	}
}

// TestAllSpawnCallSitesInjectRoleRules —— 源码级守护：tools 包内**每一处**
// spawn/send 交互子代理的调用点都必须走 appendRoleRules。
//
// 背景：首版只改了 tools/subagent.go，漏掉 tools/create_chat.go 的 4 处
// （CreateChat 的 agent 类型与 group 类型），于是「靠 CreateChat 起的部门
// 不读 CHARTER」。这种漏改不会编译失败、单测也不会红，只能靠源码级断言兜。
func TestAllSpawnCallSitesInjectRoleRules(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.(SpawnInteractive|SendInteractive)\(`)
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue // 测试里的 fake manager / adapter 不算生产调用点
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !call.MatchString(line) {
				continue
			}
			checked++
			// appendRoleRules 是工具层薄包装；RoleSystemPrompt 是它的导出版（同一个实现）。
			// 两者都算"注入了 rules"，但其它写法（裸传 role.SystemPrompt）必须红。
			if !strings.Contains(line, "appendRoleRules") && !strings.Contains(line, "RoleSystemPrompt") {
				t.Errorf("%s:%d spawn/send 调用点未注入 role rules（绕过路径）: %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if checked == 0 {
		t.Fatal("未找到任何 spawn/send 调用点 —— 正则失效，守护测试等于没测")
	}
}

// TestResolveRoleRulePath — 2026-10-22 兵部审计：rules 不能越界读任意文件。
func TestResolveRoleRulePath(t *testing.T) {
	home := t.TempDir()
	writeMD(t, filepath.Join(home, "team", "CHARTER.md"), "# ok")

	if _, err := resolveRoleRulePath(home, "team/CHARTER.md"); err != nil {
		t.Fatalf("legal rule rejected: %v", err)
	}
	for _, bad := range []string{"../.ssh/id_rsa", "/etc/passwd", "team/../../../etc/shadow", ""} {
		if _, err := resolveRoleRulePath(home, bad); err == nil {
			t.Fatalf("must reject %q", bad)
		}
	}
	// symlink 必须被拒
	link := filepath.Join(home, "team", "link.md")
	if err := os.Symlink("/etc/hostname", link); err == nil {
		if _, err := resolveRoleRulePath(home, "team/link.md"); err == nil {
			t.Fatal("symlink rule must be rejected")
		}
	}
}

// TestLoadAgentRolesSandbox_Namespaced —— sandbox 版加载器必须与本地版同语义。
//
// 首版只改了 LoadAgentRoles（本地 os.*），LoadAgentRolesSandbox 里
// `entry.IsDir → continue`：远程/容器 sandbox 用户看不到 agents/team/*.md，
// 同一份 agents 目录在两类用户下解析出不同的 role 集（且静默，不报错）。
func TestLoadAgentRolesSandbox_Namespaced(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "flat.md"), "---\nname: flat\ndescription: d\n---\nbody")
	writeMD(t, filepath.Join(root, "team", "gongbu.md"), `---
name: gongbu
description: d
rules:
  - team/CHARTER.md
---
body`)

	roles, err := LoadAgentRolesSandbox(context.Background(), root, &NoneSandbox{}, "u")
	if err != nil {
		t.Fatalf("LoadAgentRolesSandbox: %v", err)
	}
	got := map[string]SubAgentRole{}
	for _, role := range roles {
		got[role.Name] = role
	}
	for _, want := range []string{"flat", "team.gongbu"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing role %q from sandbox loader; got %v", want, got)
		}
	}
	if rules := got["team.gongbu"].Rules; len(rules) != 1 || rules[0] != "team/CHARTER.md" {
		t.Fatalf("sandbox-loaded rules = %#v", rules)
	}
	if _, ok := got["gongbu"]; ok {
		t.Fatal("namespaced role must not also appear unprefixed")
	}
}

// TestLoadAgentRoles_SkipsHiddenDirs —— 隐藏目录（`.git` 等）不得成为命名空间。
//
// agents 目录本身通常是个 git 仓库（$XBOT_HOME/agents/.git 真实存在）。递归进去今天
// 可能碰巧没有 *.md，但只要有人放一个带 frontmatter 的 md（.git/xxx.md、docs/README.md），
// 就会凭空多出一个 role（`.git.evil`）—— 那种 role 能被 spawn，绕过命名空间意图。
func TestLoadAgentRoles_SkipsHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeMD(t, filepath.Join(root, "flat.md"), "---\nname: flat\ndescription: d\n---\nbody")
	writeMD(t, filepath.Join(root, "team", "gongbu.md"), "---\nname: gongbu\ndescription: d\n---\nbody")
	// 顶层隐藏目录（模拟 .git）
	writeMD(t, filepath.Join(root, ".git", "objects", "ab", "cdef.md"), "---\nname: evil\ndescription: d\n---\nbody")
	// 命名空间内部的隐藏子目录
	writeMD(t, filepath.Join(root, "team", ".cache", "sneaky.md"), "---\nname: sneaky\ndescription: d\n---\nbody")

	for _, tc := range []struct {
		name string
		load func() ([]SubAgentRole, error)
	}{
		{"local", func() ([]SubAgentRole, error) { return LoadAgentRoles(root) }},
		{"sandbox", func() ([]SubAgentRole, error) {
			return LoadAgentRolesSandbox(context.Background(), root, &NoneSandbox{}, "u")
		}},
	} {
		roles, err := tc.load()
		if err != nil {
			t.Fatalf("%s: load: %v", tc.name, err)
		}
		got := map[string]bool{}
		for _, r := range roles {
			got[r.Name] = true
		}
		if !got["flat"] || !got["team.gongbu"] {
			t.Fatalf("%s: 正常 role 丢失: %v", tc.name, got)
		}
		for _, bad := range []string{"evil", ".git.evil", "sneaky", "team..cache.sneaky"} {
			if got[bad] {
				t.Fatalf("%s: 隐藏目录被当成命名空间，产生了 role %q: %v", tc.name, bad, got)
			}
		}
	}
}
