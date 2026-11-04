package agent

import (
	"testing"

	"xbot/storage/sqlite"
)

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
			// cli 父的镜像（首个 "/" 前是 "cli:"，chatID 为空）必须剥 —— 2026-11-04 御前裁定。
			name:        "cli 父的镜像 agent:cli:/repo:x ⇒ 真实会话 cli:/repo:x",
			parentKey:   "agent:cli:/repo:x",
			wantChannel: "cli", wantChatID: "/repo:x",
		},
		{
			name:        "真子代理地址（rest 含 /）绝不归一化",
			parentKey:   "agent:web:web-4/court.shoufu:main",
			wantChannel: "agent", wantChatID: "web:web-4/court.shoufu:main",
		},
		{
			name:        "同源退化形态 agent:web-4 丢掉了 channel 分隔 ⇒ 不造假父（仍回 (agent,web-4)，已知限制）",
			parentKey:   "agent:web-4",
			wantChannel: "agent", wantChatID: "web-4",
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
		// cli 父的镜像：首个 "/" 前是 "cli:"（chatID 为空）⇒ 剥（2026-11-04 御前裁定）。
		{"agent:cli:/repo:x", "cli:/repo:x"},
		{"agent:agent:cli:/path", "cli:/path"}, // 套镜像逐层剥
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
// 同源配对：iorigin=(channel=agent, chatID=web:web-4) 在 df68f64a 之前会产生
//
//	parentKey = sessionAddress("agent","web:web-4") = "agent:web:web-4"
//	key       = interactiveKey("agent","web:web-4", role, inst) = "agent:web:web-4/<role>:<inst>"
//
// 两者**同源**（parentKey 与 key 出自同一对 origin），这才是存量真实形态；
// 「parentKey=agent:web-4 配 key=web:web-4/…」在代码里产生不出来
// （parentKey 是 agent:web-4 ⇒ key 必为 agent:web-4/<role>:<inst>），门下省 P2 已点出。
func TestListInteractiveSessions_StoredMirrorParentAttaches(t *testing.T) {
	cases := []struct {
		name        string
		key         string
		parentKey   string
		wantChannel string
		wantChatID  string
	}{
		{
			name:        "同源存量镜像（一级子代理，父 = 主会话 web:web-4）",
			key:         "agent:web:web-4/court.gongbu:inst-1",
			parentKey:   "agent:web:web-4",
			wantChannel: "web", wantChatID: "web-4",
		},
		{
			name:        "同源嵌套子代理（parentKey 含 /，是真子代理地址，不剥）",
			key:         "agent:web:web-4/court.shoufu:main/court.gongbu:inst-2",
			parentKey:   "agent:web:web-4/court.shoufu:main",
			wantChannel: "agent", wantChatID: "web:web-4/court.shoufu:main",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{}
			cleanup := a.RegisterInteractiveSessionForTest(InteractiveSessionSeed{
				Key: c.key, ParentKey: c.parentKey, Role: "court.gongbu", Instance: "inst-1", Running: true,
			})
			defer cleanup()

			sessions := a.ListInteractiveSessions("agent", "")
			for _, s := range sessions {
				if s.Key != c.key {
					continue
				}
				if s.ParentChannel != c.wantChannel || s.ParentChatID != c.wantChatID {
					t.Fatalf("session %q (parentKey=%q) parent = (%q,%q), want (%q,%q) — "+
						"否则 serverapp 建树必然 orphan", c.key, c.parentKey,
						s.ParentChannel, s.ParentChatID, c.wantChannel, c.wantChatID)
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

// TestRestoredParentKeyFromRegistryIsNormalized 是 layer 2 的**实测 roundtrip**：
// 注册表里 parent 列被写成镜像形态 ('agent', "web:web-4") 的行，重启后按
// interactive_persist.go:258 `rec.ParentChannel + ":" + rec.ParentChatID` 重建出来的
// ia.parentKey 就是 "agent:web:web-4" —— 原样复现镜像形态，再由归一化吃掉。
//
// 这里真写库、真读回，不是字符串拼接假设。
func TestRestoredParentKeyFromRegistryIsNormalized(t *testing.T) {
	mt, _ := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}
	svc := sqlite.NewInteractiveAgentService(mt.DB().Conn())

	// 模拟 pre-fix 落库：parent 列是镜像形态（现存 0 行，机制仍必须成立）。
	rec := registryRec("agent:court.gongbu:inst-1", "agent", "web:web-4", "court.gongbu", "inst-1",
		sqlite.InteractiveAgentIdle)
	if err := svc.Upsert(rec); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := svc.Get("agent:court.gongbu:inst-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// 与 interactive_persist.go:258 逐字相同地重建 parentKey。
	restoredParentKey := got.ParentChannel + ":" + got.ParentChatID
	if restoredParentKey != "agent:web:web-4" {
		t.Fatalf("restored parentKey = %q, want agent:web:web-4 (roundtrip 形态没复现，本用例前提失效)", restoredParentKey)
	}
	ch, chatID := a.interactiveParentAddress(restoredParentKey)
	if ch != "web" || chatID != "web-4" {
		t.Fatalf("restored parentKey %q → (%q,%q), want (web,web-4)", restoredParentKey, ch, chatID)
	}
}
