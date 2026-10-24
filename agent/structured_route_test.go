package agent

import (
	"context"
	"strings"
	"testing"
)

// structured_route_test.go — 回归 2026-10-22 的「批红打进空壳会话」事故（D-09）
// 与「跨会话越权寻址」（D-10）、「裸 role 放行」（D-11）。
//
// 逻辑地址 agent:role/instance 必须映射到**调用方名下**那个子代理的物理会话 key；
// 解析不出来 / 不属于调用方 / 同名多命中一律报错 + 空串，绝不能退回逻辑地址
// （那会新建一个野会话，消息凭空消失）。

// routeEntry 一条内存注册表条目（物理 key + 声明父键 + role/instance）。
type routeEntry struct {
	key       string
	parentKey string
	role      string
	instance  string
}

// newRouteTestAgent 造一个只带内存注册表的 Agent（不触碰任何 DB）。
func newRouteTestAgent(entries ...routeEntry) *Agent {
	a := &Agent{}
	for _, e := range entries {
		a.interactiveSubAgents.Store(e.key, &interactiveAgent{
			roleName:  e.role,
			instance:  e.instance,
			parentKey: e.parentKey,
		})
	}
	return a
}

// TestResolveStructuredTarget_ScopedByParent：两个父会话各挂一个 tester/main，
// 互不串台 —— 这正是 D-10 的核心（旧实现按 role/instance 全局解析，谁先命中给谁）。
func TestResolveStructuredTarget_ScopedByParent(t *testing.T) {
	a := newRouteTestAgent(
		routeEntry{"web:web-4/tester:main", "web:web-4", "tester", "main"},
		routeEntry{"web:web-5/tester:main", "web:web-5", "tester", "main"},
	)

	// web-4 只能拿到自己那个。
	got, err := a.ResolveStructuredTarget("web", "web-4", "agent", "tester/main")
	if err != nil || got != "web:web-4/tester:main" {
		t.Fatalf("web-4 → (%q,%v), want web:web-4/tester:main", got, err)
	}
	// web-5 同理。
	got, err = a.ResolveStructuredTarget("web", "web-5", "agent", "tester/main")
	if err != nil || got != "web:web-5/tester:main" {
		t.Fatalf("web-5 → (%q,%v), want web:web-5/tester:main", got, err)
	}
	// 别的父会话（web-6）什么都拿不到：不许 fallback 到别人的子代理。
	got, err = a.ResolveStructuredTarget("web", "web-6", "agent", "tester/main")
	if err == nil || got != "" {
		t.Fatalf("web-6 → (%q,%v), want error + empty (no cross-session fallback)", got, err)
	}
	if !strings.Contains(err.Error(), "other parent session") {
		t.Fatalf("error must say the target belongs to other parent sessions, got %v", err)
	}

	// 物理 key 寻址同样受作用域约束：web-4 不许用 web-5 的物理 key 越权。
	got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "web:web-5/tester:main")
	if err == nil || got != "" {
		t.Fatalf("physical key of another parent → (%q,%v), want error + empty", got, err)
	}
	// 自己的物理 key 照常放行。
	got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "web:web-4/tester:main")
	if err != nil || got != "web:web-4/tester:main" {
		t.Fatalf("own physical key → (%q,%v), want unchanged", got, err)
	}
}

// TestResolveStructuredTarget_NestedParentForms：interactive 子代理的 key 有两种
// 等价写法（自己的 key 直接当 chatID / 带 agent: 前缀），调用方与候选父键都可能
// 落在任一种形态上 —— 必须双向归一后才比较。
func TestResolveStructuredTarget_NestedParentForms(t *testing.T) {
	leadKey := "web:web-4/lead:main"
	// 孙级子代理：物理 key 是 agent 通道 + 父的完整 key 当 chatID；
	// parentKey 记的是父自己的 key（bgParentKey 原样）。
	a := newRouteTestAgent(routeEntry{"agent:" + leadKey + "/tester:sub", leadKey, "tester", "sub"})

	// 调用方是 lead 自己（interactive：channel=agent，chatID=自己的物理 key）。
	got, err := a.ResolveStructuredTarget("agent", leadKey, "agent", "tester/sub")
	if err != nil || got != "agent:"+leadKey+"/tester:sub" {
		t.Fatalf("lead → (%q,%v), want the nested key", got, err)
	}
	// 祖父（顶层会话 web:web-4）→ 孙级：**可达**（2026-10-22 用户拍板：
	// 顶层调用方覆盖整棵子树，否则分层改造后首辅给挂在司礼监下面的六部发
	// 批红/催办会全被挡住 = 批红链路断）。这里曾经断言报错，属语义上调，故意改的。
	if got, err := a.ResolveStructuredTarget("web", "web-4", "agent", "tester/sub"); err != nil || got != "agent:"+leadKey+"/tester:sub" {
		t.Fatalf("grandparent(顶层首辅) → (%q,%v), want the nested key (whole subtree addressable)", got, err)
	}
}

// TestResolveStructuredTarget_SameTopLevelSessionScope —— D-10 放宽后的口径（2026-10-22
// 用户拍板）：**同一顶层会话（如 web:web-4）的子树内可互发；跨顶层会话一律拒**。
//
// 为什么必须放宽：CHARTER §10 要求六部 → 司礼监汇报，而结构化消息是唯一可批红的
// 通道；严格「只认直接父会话」会让分层治理和批红互斥，没有第三条路。
// 注意这里**不校验层级**（地址里没有组织关系）——「只向上报、不许平级串」靠
// CHARTCHECK 纪律 + 都察院抽查，不靠地址校验。所以下面对"兄弟会话可互发"是
// 预期行为，不是漏洞。
func TestResolveStructuredTarget_SameTopLevelSessionScope(t *testing.T) {
	const gongbu = "web:web-4/team.gongbu:main"
	a := newRouteTestAgent(
		// web:web-4 这一棵树（首辅 spawn 的六部 + 工部自己的子代理）
		routeEntry{"web:web-4/team.gongbu:main", "web:web-4", "team.gongbu", "main"},
		routeEntry{"web:web-4/team.sili:main", "web:web-4", "team.sili", "main"},
		routeEntry{"agent:" + gongbu + "/tester:verify", gongbu, "tester", "verify"},
		// 另一棵顶层会话树：web:web-5
		routeEntry{"web:web-5/team.hanlin:main", "web:web-5", "team.hanlin", "main"},
		routeEntry{"web:web-5/team.sili:main", "web:web-5", "team.sili", "main"},
	)

	// ① 六部 → 司礼监（兄弟会话，同一顶层会话）：必须成功，否则批红通道断掉。
	got, err := a.ResolveStructuredTarget("agent", gongbu, "agent", "team.sili/main")
	if err != nil || got != "web:web-4/team.sili:main" {
		t.Fatalf("gongbu → sili/main = (%q,%v), want web:web-4/team.sili:main (same top-level session)", got, err)
	}

	// ② 工部 → 自己名下的子代理：必须成功。
	got, err = a.ResolveStructuredTarget("agent", gongbu, "agent", "tester/verify")
	if err != nil || got != "agent:"+gongbu+"/tester:verify" {
		t.Fatalf("gongbu → tester/verify = (%q,%v), want its own child", got, err)
	}

	// ③ 工部 → web:web-5 下的会话（跨顶层会话）：逻辑地址与物理 key 都必须报错 + 空串。
	got, err = a.ResolveStructuredTarget("agent", gongbu, "agent", "team.hanlin/main")
	if err == nil || got != "" {
		t.Fatalf("gongbu → web-5's hanlin = (%q,%v), want error + empty (cross top-level session)", got, err)
	}
	if !strings.Contains(err.Error(), "other parent session") {
		t.Fatalf("cross-root error must explain the target belongs to another session tree, got %v", err)
	}
	if got, err = a.ResolveStructuredTarget("agent", gongbu, "agent", "web:web-5/team.hanlin:main"); err == nil || got != "" {
		t.Fatalf("gongbu → web-5 physical key = (%q,%v), want error + empty", got, err)
	}

	// 反向不串台：web-5 的顶层会话眼里 team.sili/main 是**它自己那棵树里的**司礼监，
	// 同名 role/instance 绝不能解析到 web-4 名下那个。
	if got, err = a.ResolveStructuredTarget("web", "web-5", "agent", "team.sili/main"); err != nil || got != "web:web-5/team.sili:main" {
		t.Fatalf("web-5 → team.sili/main = (%q,%v), want its OWN sili (web:web-5/team.sili:main)", got, err)
	}
	// 就算拿着 web-4 那棵树的物理 key 也不行：跨 root 一律拒。
	if got, err = a.ResolveStructuredTarget("web", "web-5", "agent", "web:web-4/team.sili:main"); err == nil || got != "" {
		t.Fatalf("web-5 → web-4's sili（物理 key）= (%q,%v), want error + empty (cross root)", got, err)
	}

	// 顶层会话（首辅 web:web-4）→ 直接子级部门：照常可达。
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "team.sili/main"); err != nil || got != "web:web-4/team.sili:main" {
		t.Fatalf("web-4 → sili/main = (%q,%v), want direct child", got, err)
	}
}

// TestResolveStructuredTarget_TopLevelSubtree —— 顶层调用方覆盖**整棵子树**的验收
// （2026-10-22 用户拍板：这是阻断性缺陷，不是边界）。
//
// 分层改造后的拓扑：首辅(web:web-4) → 司礼监(web:web-4/team.sili:main) → 六部
// (agent:web:web-4/team.sili:main/team.gongbu:main)。六部**不是**首辅的直接子级，
// 若只认 callerScope+rootForms，首辅给六部发批红/催办会全部被挡。
func TestResolveStructuredTarget_TopLevelSubtree(t *testing.T) {
	const (
		sili   = "web:web-4/team.sili:main"
		gongbu = "agent:web:web-4/team.sili:main/team.gongbu:main" // 两级深：挂在司礼监下面
	)
	a := newRouteTestAgent(
		routeEntry{sili, "web:web-4", "team.sili", "main"},
		routeEntry{gongbu, sili, "team.gongbu", "main"},
		routeEntry{"web:web-5/team.hanlin:main", "web:web-5", "team.hanlin", "main"},
	)

	// ① 首辅 → 直接子级（司礼监）：成功。
	got, err := a.ResolveStructuredTarget("web", "web-4", "agent", "team.sili/main")
	if err != nil || got != sili {
		t.Fatalf("首辅 → 司礼监 = (%q,%v), want %q", got, err, sili)
	}

	// ② 首辅 → 司礼监名下的六部（两级深）：**必须成功**，否则批红链路断。
	got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "team.gongbu/main")
	if err != nil || got != gongbu {
		t.Fatalf("首辅 → 司礼监的子代理(两级深) = (%q,%v), want %q (whole subtree addressable)", got, err, gongbu)
	}
	// 用物理 key 寻址同样可达。
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", gongbu); err != nil || got != gongbu {
		t.Fatalf("首辅 → 六部(物理 key) = (%q,%v), want unchanged", got, err)
	}
	// 司礼监 → 自己名下的六部：成功（新拓扑下的直接子级）。
	if got, err = a.ResolveStructuredTarget("agent", sili, "agent", "team.gongbu/main"); err != nil || got != gongbu {
		t.Fatalf("司礼监 → 六部 = (%q,%v), want %q", got, err, gongbu)
	}

	// ③ 首辅 → 另一个顶层会话（web:web-5）名下的会话：报错 + 空串。
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "team.hanlin/main"); err == nil || got != "" {
		t.Fatalf("首辅 → web-5 的会话 = (%q,%v), want error + empty (cross top-level session)", got, err)
	}
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "web:web-5/team.hanlin:main"); err == nil || got != "" {
		t.Fatalf("首辅 → web-5 的物理 key = (%q,%v), want error + empty", got, err)
	}

	// ③bis 安全边界（用户点名要能回答的那条）：**web-5 的部门打不到 web-4 的司礼监**。
	// 反方向也不行 —— 子树前缀只对"自己的顶层会话"生效，跨 root 一律拒。
	if got, err = a.ResolveStructuredTarget("agent", "web:web-5/team.hanlin:main", "agent", "team.sili/main"); err == nil || got != "" {
		t.Fatalf("web-5 的部门 → web-4 的司礼监 = (%q,%v), want error + empty (cross root)", got, err)
	}
	// 同一 root 内则允许：web-5 的部门 → 同一棵树里的兄弟会话。
	if got, err = a.ResolveStructuredTarget("agent", "web:web-5/team.sili:main", "agent", "team.hanlin/main"); err != nil || got != "web:web-5/team.hanlin:main" {
		t.Fatalf("web-5 的部门 → 同 root 兄弟 = (%q,%v), want %q", got, err, "web:web-5/team.hanlin:main")
	}

	// ④ 首辅 → 完全无关的野会话（别的渠道/别的根，不在任何子树里）：报错 + 空串。
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "cli:/home/smith/ws/tester:main"); err == nil || got != "" {
		t.Fatalf("首辅 → 无关野会话(物理 key) = (%q,%v), want error + empty", got, err)
	}
	if got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "tester/ghost"); err == nil || got != "" {
		t.Fatalf("首辅 → 未注册 role/instance = (%q,%v), want error + empty", got, err)
	}
}

// TestResolveStructuredTarget_AmbiguousRejected：同一作用域下 ≥2 命中 = 歧义 = 拒。
//
// 重复注册怎么来的：同一个逻辑子代理（role/instance 相同）被 spawn 过两次，两次的
// 父会话写法不同 —— 一次是 web 通道直连（chatID=web-4，内存 key
// "web:web-4/tester:main"），一次是 agent 通道把父会话地址当 chatID 投递
// （内存 key "agent:web:web-4/tester:main"）。两个 key 归一后父作用域都是
// web:web-4，但它们是**两个真实租户会话**（DB 里两份），谁都不是"那个"目标 ——
// 猜错就把 report/decision 投进没人读的空壳，所以必须报错并列候选。
func TestResolveStructuredTarget_AmbiguousRejected(t *testing.T) {
	a := newRouteTestAgent(
		routeEntry{"web:web-4/tester:main", "web:web-4", "tester", "main"},
		routeEntry{"agent:web:web-4/tester:main", "web:web-4", "tester", "main"},
	)

	got, err := a.ResolveStructuredTarget("web", "web-4", "agent", "tester/main")
	if err == nil {
		t.Fatalf("ambiguous target must be rejected, got %q", got)
	}
	if got != "" {
		t.Fatalf("ambiguous target must return empty chatID, got %q", got)
	}
	for _, want := range []string{"ambiguous", "web:web-4/tester:main", "agent:web:web-4/tester:main"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must list candidate key %q for triage, got %v", want, err)
		}
	}
}

// TestResolveStructuredTarget_UnregisteredAndForeignParents：未注册 / 父不匹配。
func TestResolveStructuredTarget_UnregisteredAndForeignParents(t *testing.T) {
	a := newRouteTestAgent(routeEntry{"web:web-5/tester:main", "", "tester", "main"}) // parentKey 为空：只靠 key 里的父段

	// 未注册：报错 + 空串（绝不回落逻辑地址）。
	got, err := a.ResolveStructuredTarget("web", "web-4", "agent", "tester/ghost")
	if err == nil || got != "" {
		t.Fatalf("unregistered → (%q,%v), want error + empty", got, err)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered error should say so, got %v", err)
	}

	// 同名但父会话不同：报错要说清「属于其他父会话」，而不是含糊的 not registered。
	got, err = a.ResolveStructuredTarget("web", "web-4", "agent", "tester/main")
	if err == nil || got != "" {
		t.Fatalf("foreign parent → (%q,%v), want error + empty", got, err)
	}
	if !strings.Contains(err.Error(), "other parent session") {
		t.Fatalf("foreign-parent error must mention the other parent session, got %v", err)
	}
}

// TestResolveStructuredTarget_NonAgentPassthrough：非 agent 通道原样返回。
func TestResolveStructuredTarget_NonAgentPassthrough(t *testing.T) {
	a := &Agent{}
	for _, tc := range []struct{ channel, chatID string }{
		{"web", "web-4"},
		{"cli", "/home/smith/ws"},
		{"feishu", "ou_abc"},
	} {
		got, err := a.ResolveStructuredTarget("web", "web-4", tc.channel, tc.chatID)
		if err != nil || got != tc.chatID {
			t.Fatalf("%s:%s → (%q,%v), want unchanged", tc.channel, tc.chatID, got, err)
		}
	}
}

// TestResolveStructuredTarget_AgentAddressWithoutInstance：D-11 —— 旧的
// `if !strings.Contains(chatID, "/") { return chatID, nil }` 放行分支已删除。
// 裸 role（"tester"）没有父会话信息，放行等于把逻辑地址当物理 chatID 用 →
// 又回到「每次都新建一个空壳会话」的老坑，必须报错 + 空串。
func TestResolveStructuredTarget_AgentAddressWithoutInstance(t *testing.T) {
	a := newRouteTestAgent(routeEntry{"web:web-4/tester:main", "web:web-4", "tester", "main"})

	for _, addr := range []string{"tester", "web:web-4", "team.hubu"} {
		got, err := a.ResolveStructuredTarget("web", "web-4", "agent", addr)
		if err == nil {
			t.Fatalf("bare agent chatID %q must be rejected, got %q", addr, got)
		}
		if got != "" {
			t.Fatalf("bare agent chatID %q must resolve to empty, got %q", addr, got)
		}
	}
}

// TestBuildToolContextCarriesCallerSessionKey —— D-07 贯通：RunConfig.CallerSessionKey
// 必须真的进 ToolContext（tools/send_message.go 只读 ToolContext.CallerSessionKey，
// engine 不填 → caller.SessionKey 恒为空 → serverapp 的自激守卫永不触发 = D-07 没修）。
func TestBuildToolContextCarriesCallerSessionKey(t *testing.T) {
	cfg := &RunConfig{
		AgentID:          "team.hubu",
		Channel:          "agent",
		ChatID:           "web:web-4/team.hubu:main", // interactive 子代理：物理 key 就是 ChatID
		SessionKey:       "web-4/team.hubu",          // 工具激活键（与物理 key 毫不相干）
		CallerSessionKey: "web:web-4/team.hubu:main",
	}
	tc := buildToolContext(context.Background(), cfg)
	if tc.CallerSessionKey != cfg.CallerSessionKey {
		t.Fatalf("ToolContext.CallerSessionKey = %q, want %q (D-07 自激守卫靠它)", tc.CallerSessionKey, cfg.CallerSessionKey)
	}
	if tc.CallerSessionKey == tc.SessionKey {
		t.Fatal("CallerSessionKey must not be conflated with SessionKey (physical session key vs tool-activation key)")
	}

	// 主会话：CallerSessionKey 为空（无发起者），不得被填成 SessionKey。
	main := buildToolContext(context.Background(), &RunConfig{AgentID: "main", Channel: "web", ChatID: "web-4", SessionKey: "web:web-4"})
	if main.CallerSessionKey != "" {
		t.Fatalf("main session CallerSessionKey = %q, want empty", main.CallerSessionKey)
	}
}
