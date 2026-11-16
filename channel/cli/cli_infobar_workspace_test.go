package cli

import (
	"strings"
	"testing"
)

// TestRenderWorkspaceIndicator 锁定信息栏左侧的 workspace 指示：
// 默认会话显示 "main workspace"，命名会话显示**自己的会话名**。
//
// 回归背景：删除工作树工具时把 renderWorkspaceIndicator 整个换成了硬编码的
// "🏠 main workspace"，于是命名会话再也看不到自己的名字（用户无法分辨在哪个会话）。
// 工作树的 🌿 分支该走，但"命名会话显示会话名"是独立行为，必须留下。
func TestRenderWorkspaceIndicator(t *testing.T) {
	const workDir = "/home/smith/src/xbot"
	cases := []struct {
		name   string
		chatID string
		want   string
	}{
		{"default session", workDir, "main workspace"},
		{"empty chatID", "", "main workspace"},
		{"named session", workDir + ":review-branch", "review-branch"},
		{"named session with channel prefix", "cli:" + workDir + ":agent-main", "agent-main"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &cliModel{chatID: c.chatID, workDir: workDir}
			m.styles = buildStyles(80)

			got := stripAnsi(m.renderWorkspaceIndicator())
			if !strings.Contains(got, c.want) {
				t.Fatalf("renderWorkspaceIndicator() = %q, want it to contain %q", got, c.want)
			}
			// 名称必须原样出现，不能只剩一个图标
			if strings.TrimSpace(strings.TrimPrefix(got, "🏠")) == "" {
				t.Fatalf("renderWorkspaceIndicator() = %q, workspace part is empty", got)
			}
		})
	}
}

// TestRenderWorkspaceIndicator_NoIsolationGlyph 删除工作树工具后，
// 任何会话都不应再出现 🌿（教用户一个不存在的隔离状态）。
func TestRenderWorkspaceIndicator_NoIsolationGlyph(t *testing.T) {
	const workDir = "/home/smith/src/xbot"
	for _, chatID := range []string{workDir, workDir + ":review-branch"} {
		m := &cliModel{chatID: chatID, workDir: workDir}
		m.styles = buildStyles(80)
		if got := stripAnsi(m.renderWorkspaceIndicator()); strings.Contains(got, "🌿") {
			t.Errorf("chatID %q: workspace indicator must not show the removed isolation glyph, got %q", chatID, got)
		}
	}
}

// TestRenderInfoBar_UsesWorkspaceIndicator 端到端守住本次回归本身：
// 信息栏（而不只是辅助函数）必须显示会话名。
func TestRenderInfoBar_UsesWorkspaceIndicator(t *testing.T) {
	const workDir = "/home/smith/src/xbot"

	named := &cliModel{chatID: workDir + ":review-branch", workDir: workDir}
	named.styles = buildStyles(80)
	got := stripAnsi(named.renderInfoBar())
	if !strings.Contains(got, "review-branch") {
		t.Fatalf("info bar for named session must show the session name, got %q", got)
	}
	if strings.Contains(got, "main workspace") {
		t.Fatalf("info bar for named session must not say main workspace, got %q", got)
	}

	def := &cliModel{chatID: workDir, workDir: workDir}
	def.styles = buildStyles(80)
	if got := stripAnsi(def.renderInfoBar()); !strings.Contains(got, "main workspace") {
		t.Fatalf("info bar for default session must say main workspace, got %q", got)
	}
}
