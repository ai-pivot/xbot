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

import (
	"path/filepath"
	"testing"
)

const testUserWorkspace = "/home/smith/.xbot/users/web-4/workspace"

// TestJoinPathIdempotent 覆盖需求给定的两种 rel 形态：
//   - rel 已是绝对路径（历史配置 / 子代理上下文）⇒ 原样返回，不再前缀；
//   - rel 是相对路径 ⇒ 拼到 base 下。
func TestJoinPathIdempotent(t *testing.T) {
	cases := []struct {
		name string
		base string
		rel  string
		want string
	}{
		{
			name: "absolute rel already points at the target",
			base: testUserWorkspace,
			rel:  testUserWorkspace + "/agents",
			want: testUserWorkspace + "/agents",
		},
		{
			name: "relative rel is joined under base",
			base: testUserWorkspace,
			rel:  "agents",
			want: testUserWorkspace + "/agents",
		},
		{
			name: "empty rel keeps base",
			base: testUserWorkspace,
			rel:  "",
			want: testUserWorkspace,
		},
		{
			name: "dot-segments are cleaned",
			base: testUserWorkspace,
			rel:  "./agents/../agents",
			want: testUserWorkspace + "/agents",
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
	// 真实故障输入：workDir 已经是该用户的 workspace（绝对路径）。
	if got, want := UserAgentsRoot(testUserWorkspace, "web-4"), testUserWorkspace+"/agents"; got != want {
		t.Fatalf("UserAgentsRoot(workspace, web-4) = %q, want %q (path prefixed twice?)", got, want)
	}
	// 已经是 user root 的输入同样幂等。
	if got, want := UserAgentsRoot("/home/smith/.xbot/users/web-4", "web-4"), testUserWorkspace+"/agents"; got != want {
		t.Fatalf("UserAgentsRoot(user root, web-4) = %q, want %q", got, want)
	}
	// 正常输入（服务端全局 workDir）行为不变。
	if got, want := UserAgentsRoot("/home/smith", "web-4"), testUserWorkspace+"/agents"; got != want {
		t.Fatalf("UserAgentsRoot(/home/smith, web-4) = %q, want %q", got, want)
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
	if got, want := UserRoot(testUserWorkspace, "bob"), testUserWorkspace+"/.xbot/users/bob"; got != want {
		t.Fatalf("UserRoot(workspace, bob) = %q, want %q", got, want)
	}
}
