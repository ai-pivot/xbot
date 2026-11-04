package agent

import "testing"

// 2026-11-04 打回项（首辅令）：
// 存量镜像 parentKey 未归一化 —— df68f64a 之前 spawn 的会话把
// sessionAddress(agent, "web:web-4") 的旧结果 "agent:web:web-4" 直接写进了
// ia.parentKey（interactive.go:1336 的事故注释）。interactiveParentAddress 的
// 字符串拆分分支对它返回 ("agent","web:web-4")，父会话树里 lookupAgent 必然
// miss ⇒ 运行中的子代理仍 orphan，用户报的「掉出侧边栏树」在存量数据上照旧。

// TestInteractiveParentAddress_ExistingFormsUnchanged 是**保持不变**的硬断言：
// 新格式（map 命中 / canonical 命中 / 普通 channel:chatID）逐字不变。
func TestInteractiveParentAddress_ExistingFormsUnchanged(t *testing.T) {
	a := &Agent{}
	// 注册表里真实存在的两条（嵌套子代理 = 另一个子代理的 map key）。
	const topKey = "web:web-4/court.shoufu:main"
	const nestedKey = "agent:web:web-4/court.shoufu:main/court.gongbu:inst-2"
	for _, k := range []string{topKey, nestedKey} {
		defer a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
			Key: k, Role: "court.gongbu", Instance: "inst-x", Running: true,
		})()
	}

	cases := []struct {
		name        string
		parentKey   string
		wantChannel string
		wantChatID  string
	}{
		{
			name:        "1 一级子代理的父是 web 主会话",
			parentKey:   "web:web-4",
			wantChannel: "web", wantChatID: "web-4",
		},
		{
			name:        "2 一级子代理的父是 cli 会话（chatID 本身含 /）",
			parentKey:   "cli:/home/smith/src/xbot-exp",
			wantChannel: "cli", wantChatID: "/home/smith/src/xbot-exp",
		},
		{
			name:        "3 map 命中：父是 agent 会话（裸 key）",
			parentKey:   topKey,
			wantChannel: "agent", wantChatID: topKey,
		},
		{
			name:        "4 canonical 命中：父是 agent 会话（带 agent: 前缀）",
			parentKey:   "agent:" + topKey,
			wantChannel: "agent", wantChatID: topKey,
		},
		{
			name:        "5 字符串拆分：父是 agent 会话但注册表已 miss（真子代理地址含 /）",
			parentKey:   "agent:web:web-4/court.deadbeef:gone",
			wantChannel: "agent", wantChatID: "web:web-4/court.deadbeef:gone",
		},
		{
			name:        "6 空 parentKey（顶层会话 spawn）",
			parentKey:   "",
			wantChannel: "", wantChatID: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotChannel, gotChatID := a.interactiveParentAddress(c.parentKey)
			if gotChannel != c.wantChannel || gotChatID != c.wantChatID {
				t.Fatalf("interactiveParentAddress(%q) = (%q,%q), want (%q,%q) — 既有行为必须逐字不变",
					c.parentKey, gotChannel, gotChatID, c.wantChannel, c.wantChatID)
			}
		})
	}
}

// TestInteractiveParentAddress_StoredMirrorCollapses 是本次修复的**判别力测试**：
// 存量镜像 parentKey 必须归一化成真实会话地址，而不是 ("agent", ...) 这种必然
// orphan 的父身份。
//
// 变异自证：把 interactiveParentAddress 里 normalizeStoredParentKey 那个分支
// 注释掉 ⇒ 第 1 条变红（("agent","web:web-4") != ("web","web-4")）；
// 恢复 ⇒ 绿。日志见 court/evidence/gongbu/branch-fix-running-20261104/。
func TestInteractiveParentAddress_StoredMirrorCollapses(t *testing.T) {
	a := &Agent{}
	cases := []struct {
		name        string
		parentKey   string
		wantChannel string
		wantChatID  string
	}{
		{
			name:        "存量镜像 agent:web:web-4 ⇒ 真实会话 web:web-4",
			parentKey:   "agent:web:web-4",
			wantChannel: "web", wantChatID: "web-4",
		},
		{
			name:        "cli 父的存量镜像与真子代理地址同形（rest 含 /）⇒ 按判据不归一化（已知限制）",
			parentKey:   "agent:cli:/repo:x",
			wantChannel: "agent", wantChatID: "cli:/repo:x",
		},
		{
			name:        "真子代理地址（rest 含 /）绝不归一化",
			parentKey:   "agent:web:web-4/court.shoufu:main",
			wantChannel: "agent", wantChatID: "web:web-4/court.shoufu:main",
		},
		{
			name:        "退化形态 agent:web-4 不含可还原 channel ⇒ 不造假父（回落 key 推父）",
			parentKey:   "agent:web-4",
			wantChannel: "", wantChatID: "",
		},
		{
			name:        "镜像套镜像 agent:agent:web:web-4 ⇒ web:web-4",
			parentKey:   "agent:agent:web:web-4",
			wantChannel: "web", wantChatID: "web-4",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotChannel, gotChatID := a.interactiveParentAddress(c.parentKey)
			if gotChannel != c.wantChannel || gotChatID != c.wantChatID {
				t.Fatalf("interactiveParentAddress(%q) = (%q,%q), want (%q,%q)",
					c.parentKey, gotChannel, gotChatID, c.wantChannel, c.wantChatID)
			}
		})
	}
}

// TestNormalizeStoredParentKey 直接锁住剥层函数本身（互不相关的单元边界）。
func TestNormalizeStoredParentKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"agent:web:web-4", "web:web-4"},
		{"agent:web:web-4/role:inst", "agent:web:web-4/role:inst"}, // 真子代理地址
		// cli 父的存量镜像 "agent:cli:/repo:x" 与真子代理地址字符串不可分
		// （rest 含 "/" 即判为非镜像）—— 保持不归一化，见 FIX.md「已知限制」。
		{"agent:cli:/repo:x", "agent:cli:/repo:x"},
		{"web:web-4", "web:web-4"},
		{"agent:web-4", "agent:web-4"}, // 非镜像（isMirrorSessionAddress 的语义）
		{"", ""},
	}
	for _, c := range cases {
		if got := normalizeStoredParentKey(c.in); got != c.want {
			t.Fatalf("normalizeStoredParentKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestListInteractiveSessions_StoredMirrorParentAttaches 是**行级**端到端：
// 存量镜像 parentKey 的运行中子代理，在 ListInteractiveSessions 里必须报告
// 真实父身份 (web, web-4) —— 这正是 serverapp 建树时 lookupAgent/合成父要用
// 的那一对值。
//
// 其中 parentKey="agent:web-4" 是任务书里点名的字面串：它不含可还原的 channel，
// 单独解析只能返回空，由 ListInteractiveSessions 回落到「从子代理自己的 key 推父」
// （interactive.go:parseInteractiveKeyParent），最终结果同样是 (web, web-4)。
func TestListInteractiveSessions_StoredMirrorParentAttaches(t *testing.T) {
	cases := []struct {
		name      string
		key       string
		parentKey string
	}{
		{"存量镜像（df68f64a 之前 spawn）", "web:web-4/court.gongbu:inst-1", "agent:web:web-4"},
		{"任务书点名的退化形态", "web:web-4/court.gongbu:inst-1", "agent:web-4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{}
			cleanup := a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
				Key: c.key, ParentKey: c.parentKey, Role: "court.gongbu", Instance: "inst-1", Running: true,
			})
			defer cleanup()

			sessions := a.ListInteractiveSessions("web", "")
			for _, s := range sessions {
				if s.Key != c.key {
					continue
				}
				if s.ParentChannel != "web" || s.ParentChatID != "web-4" {
					t.Fatalf("session %q (parentKey=%q) parent = (%q,%q), want (web,web-4) — "+
						"否则 serverapp 建树必然 orphan", c.key, c.parentKey, s.ParentChannel, s.ParentChatID)
				}
				if !s.Running {
					t.Fatalf("session %q must report Running=true", c.key)
				}
				return
			}
			t.Fatalf("session %q not listed: %#v", c.key, sessions)
		})
	}
}
