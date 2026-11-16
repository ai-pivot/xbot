package tools

// workspace_scope_test.go — 用户私有目录拼接的幂等性守护。
//
// 生产现象（2026-11-02，每日 6~7 条）：
//
//	Failed to load user agent roles, skipping directory
//	read agents dir /home/smith/.xbot/users/web-4/workspace/.xbot/users/web-4/workspace/agents
//
// 根因：per-user workspace 被当作 workDir 再前缀一次。回归判据是「两种输入
// （绝对 / 相对）都必须落到同一个规范路径」，以及 UserRoot 对已是本用户
// workspace 的 workDir 幂等。
//
// 平台注意：用例不能硬编码 POSIX 路径字面量。Windows 上 filepath.IsAbs("/home/…")
// 为 false（没有盘符），那样 "绝对 rel" 会被当成相对路径拼两次，测的就不再是
// 生产语义，而是平台的路径解析差异。下面统一用 t.TempDir() 构造平台原生的
// 绝对路径，Linux 与 Windows 都覆盖同一套逻辑。

import (
	"path/filepath"
	"testing"
)

// testWorkspace 返回一个平台原生的绝对路径，形状与生产的
// {root}/.xbot/users/web-4/workspace 完全一致（UserRoot / UserAgentsRoot
// 只依赖 .xbot/users/<key> 这几段，与盘符无关）。
func testWorkspace(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".xbot", "users", "web-4", "workspace")
}

// TestJoinPathIdempotent 覆盖需求给定的两种 rel 形态：
//   - rel 已是绝对路径（历史配置 / 子代理上下文）⇒ 原样返回，不再前缀；
//   - rel 是相对路径 ⇒ 拼到 base 下。
func TestJoinPathIdempotent(t *testing.T) {
	base := testWorkspace(t)
	target := filepath.Join(base, "agents")

	cases := []struct {
		name string
		base string
		rel  string
		want string
	}{
		{
			name: "absolute rel already points at the target",
			base: base,
			rel:  target,
			want: target,
		},
		{
			name: "relative rel is joined under base",
			base: base,
			rel:  "agents",
			want: target,
		},
		{
			name: "empty rel keeps base",
			base: base,
			rel:  "",
			want: base,
		},
		{
			name: "dot-segments are cleaned",
			base: base,
			rel:  "./agents/../agents",
			want: target,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinPathIdempotent(tc.base, tc.rel); got != tc.want {
				t.Fatalf("joinPathIdempotent(%q, %q) = %q, want %q", tc.base, tc.rel, got, tc.want)
			}
		})
	}
}

// TestUserAgentsRootIdempotent 是本次 bug 的直接回归用例：
// 子代理上下文把 per-user workspace 当 WorkingDir 传进来时，
// 目录必须仍是 .../workspace/agents，而不是 .../workspace/.xbot/users/<id>/workspace/agents。
func TestUserAgentsRootIdempotent(t *testing.T) {
	workspace := testWorkspace(t)
	userRoot := filepath.Dir(workspace)
	want := filepath.Join(workspace, "agents")

	// 真实故障输入：workDir 已经是该用户的 workspace（绝对路径）。
	if got := UserAgentsRoot(workspace, "web-4"); got != want {
		t.Fatalf("UserAgentsRoot(workspace, web-4) = %q, want %q (path prefixed twice?)", got, want)
	}
	// 已经是 user root 的输入同样幂等。
	if got := UserAgentsRoot(userRoot, "web-4"); got != want {
		t.Fatalf("UserAgentsRoot(user root, web-4) = %q, want %q", got, want)
	}
	// 正常输入（服务端全局 workDir）行为不变。
	globalWorkDir := t.TempDir()
	wantUnderGlobal := filepath.Join(globalWorkDir, ".xbot", "users", "web-4", "workspace", "agents")
	if got := UserAgentsRoot(globalWorkDir, "web-4"); got != wantUnderGlobal {
		t.Fatalf("UserAgentsRoot(global workdir, web-4) = %q, want %q", got, wantUnderGlobal)
	}
}

// TestUserRootKeepsNormalLayout 确保幂等短路没有破坏原有的逐用户目录布局。
func TestUserRootKeepsNormalLayout(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "data")
	if got, want := UserRoot(workDir, "alice"), filepath.Join(workDir, ".xbot", "users", "alice"); got != want {
		t.Fatalf("UserRoot(%q, alice) = %q, want %q", workDir, got, want)
	}
	// 幂等：对同一个用户重复计算，结果不变。
	once := UserRoot(workDir, "alice")
	if twice := UserRoot(once, "alice"); twice != once {
		t.Fatalf("UserRoot not idempotent: %q vs %q", once, twice)
	}
	// 别的 sender 仍走正常拼接（不得误判为已存在）。
	workspace := testWorkspace(t)
	if got, want := UserRoot(workspace, "bob"), filepath.Join(workspace, ".xbot", "users", "bob"); got != want {
		t.Fatalf("UserRoot(workspace, bob) = %q, want %q", got, want)
	}
}
