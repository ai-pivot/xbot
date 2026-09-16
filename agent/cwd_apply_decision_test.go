package agent

import "testing"

// 2026-09-16 用户报告（严重）：
//
//	「我创建会话时传的是**绝对路径**，为什么不用我的路径？」
//	「我不管你用什么方法，那个接口我传的是什么路径就得是什么路径，而不是给我转换。」
//
// 根因：cwd 只有一个字段（TenantSession.cwd → tenants.cwd），但有两个写者：
//
//	① 用户的显式选择（新建会话弹窗 → set_cwd RPC → SetCWDForced）
//	② Agent 自身（agent.go 的 SetCurrentDir(workspaceRoot) —— 事故后 agent.work_dir
//	   丢失 ⇒ workspaceRoot 是**相对路径** `.xbot/users/<uid>/workspace`）
//
// 旧实现里 ① 只在 existingCWD=="" 或旧目录不存在时才写入 ⇒ ② 先写了（且相对服务进程
// cwd 恰好存在）时，① 的绝对路径被**静默丢弃** —— 正是"我传了路径却没用我的"。
//
// 契约（本用例钉死）：
//
//	· force（显式动作）→ **总是采纳**（用户的路径必须生效）；
//	· 非 force（自动路径：终端目录同步 / 重启恢复）→ 不得覆盖已持久化且仍存在的 cwd；
//	· 既有 cwd 已消失 → 采纳新值（修复"目录被删后卡死在不存在路径"）。
func TestCWDApplyDecision(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		exists   bool
		force    bool
		want     bool
	}{
		{"显式动作覆盖已存在 cwd（用户路径优先）", "/home/smith/src/proj", true, true, true},
		{"显式动作在无 cwd 时生效", "", false, true, true},
		{"显式动作对不存在的路径也照样生效（传什么就是什么）", "/gone", false, true, true},
		{"新会话（无 cwd）采纳自动路径", "", false, false, true},
		{"自动路径不得覆盖已存在 cwd（终端目录同步/重启恢复语义）", "/persisted", true, false, false},
		{"既有 cwd 已消失 → 采纳新值", "/deleted", false, false, true},
	}
	for _, c := range cases {
		if got := cwdApplyDecision(c.existing, c.exists, c.force); got != c.want {
			t.Fatalf("%s: cwdApplyDecision(existing=%q, exists=%v, force=%v) = %v, want %v",
				c.name, c.existing, c.exists, c.force, got, c.want)
		}
	}
}
