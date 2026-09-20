package agent

import (
	"strings"
	"testing"
)

// TestMatchInteractiveSessionsBestEffort 锁定「漏传 role 也要尽量匹配」的契约。
//
// 用户 2026-09-17 现场：`{"action":"send","instance":"perf-slot"}`（没带 role）⇒
// 旧实现只用 `interactiveKey(caller..., role="", instance)` 精确查 → 必然 miss →
// 报出误导性的 "no active interactive session for role \"\" / use interactive=true"。
// 新契约：精确键未命中 → 按已给的维度 best-effort 匹配；唯一命中即用；0 个（完全
// 匹配不上）或 >1 个（歧义）才报错。
func TestMatchInteractiveSessionsBestEffort(t *testing.T) {
	a := &Agent{}
	a.interactiveSubAgents.Store(interactiveKey("web", "chat_A", "explore", "perf-slot"),
		&interactiveAgent{roleName: "explore", instance: "perf-slot"})
	a.interactiveSubAgents.Store(interactiveKey("web", "chat_A", "build", "b1"),
		&interactiveAgent{roleName: "build", instance: "b1"})

	// ① 只给 instance（用户这次的形态）→ 唯一命中
	got := a.matchInteractiveSessions("", "perf-slot")
	if len(got) != 1 || !strings.Contains(got[0], "perf-slot") {
		t.Fatalf("① instance-only: got %v, want exactly the perf-slot session", got)
	}

	// ② 只给 role → 唯一命中
	if got := a.matchInteractiveSessions("build", ""); len(got) != 1 || !strings.Contains(got[0], "build") {
		t.Fatalf("② role-only: got %v, want exactly the build session", got)
	}

	// ③ 两者都给 → 精确
	if got := a.matchInteractiveSessions("explore", "perf-slot"); len(got) != 1 {
		t.Fatalf("③ exact: got %v, want 1", got)
	}

	// ④ 都不给 → 全部（调用方按「多个即歧义」报错）
	if got := a.matchInteractiveSessions("", ""); len(got) != 2 {
		t.Fatalf("④ wildcard: got %v, want all 2", got)
	}

	// ⑤ 完全匹配不上 → 0（调用方报错并列出 available）
	if got := a.matchInteractiveSessions("nope", ""); len(got) != 0 {
		t.Fatalf("⑤ no-match: got %v, want 0", got)
	}
	if got := a.matchInteractiveSessions("explore", "wrong-instance"); len(got) != 0 {
		t.Fatalf("⑤b no-match(instance): got %v, want 0", got)
	}

	// ⑥ 同名两端不同 instance：只给 instance ⇒ 仍是唯一（instance 是更强的维度）
	a.interactiveSubAgents.Store(interactiveKey("web", "chat_B", "explore", "perf-fp8"),
		&interactiveAgent{roleName: "explore", instance: "perf-fp8"})
	if got := a.matchInteractiveSessions("", "perf-fp8"); len(got) != 1 {
		t.Fatalf("⑥: got %v, want exactly 1 (chat_B/perf-fp8)", got)
	}
}

// TestResolveInteractiveSessionKey_TreeScopedInstanceMatch 复现并锁定 2026-09-17 现场：
// 会话 `explore:fuse-moe` 是**发起者(chat_A)自己的子代理**，而调用方传的 role 是错的
// （`qa` —— 由旧的 role 自动推断补出来的）。契约：
//
//	· 在**自己的子代理树内**按 **instance** 唯一命中 ⇒ 成功；
//	· **别的会话（chat_B）绝不允许**匹配到 chat_A 树里的会话（用户：严禁这种自动 fallback）；
//	· 需要跨树时只能用**完整地址**寻址。
func TestResolveInteractiveSessionKey_TreeScopedInstanceMatch(t *testing.T) {
	a := &Agent{}
	caller := qualifyChatID("web", "chat_A")
	realKey := interactiveKey("web", "chat_A", "explore", "fuse-moe")
	a.interactiveSubAgents.Store(realKey, &interactiveAgent{roleName: "explore", instance: "fuse-moe", parentKey: caller})

	// ① 树内：role 写错（qa）+ instance 正确 → 唯一命中
	if got, err := a.resolveInteractiveSessionKey("web", "chat_A", "qa", "fuse-moe"); err != nil || got != realKey {
		t.Fatalf("树内按 instance 应命中：got=%q err=%v want=%q", got, err, realKey)
	}
	// ② 树内：漏传 role，仅 instance → 命中
	if got, err := a.resolveInteractiveSessionKey("web", "chat_A", "", "fuse-moe"); err != nil || got != realKey {
		t.Fatalf("树内仅 instance 应命中：got=%q err=%v", got, err)
	}
	// ③ ⛔ 严禁跨树：chat_B 不得命中 chat_A 树里的会话
	if got, err := a.resolveInteractiveSessionKey("web", "chat_B", "qa", "fuse-moe"); err == nil {
		t.Fatalf("严禁跨树匹配：chat_B 不应命中 %q（got=%q）", realKey, got)
	}
	// ④ 跨树的合法途径：完整地址
	if got, err := a.resolveInteractiveSessionKey("web", "chat_B", "explore", realKey); err != nil || got != realKey {
		t.Fatalf("完整地址寻址应可用：got=%q err=%v", got, err)
	}
}

// TestResolveInteractiveSessionKey_GrandchildInstanceMatch 复现用户现场（2026-09-18）：
//
//	interactive send failed: no sub-agent with instance="mma4-tcgen05" in your
//	sub-agent tree (role=""; …)          ← 补上 role="explore" 后同一次调用成功
//
// 现场形态：`mma4-tcgen05` 是**发起者子树里的"孙级"**（由同一棵树里的另一个子代理
// spawn，例如 mma3 → mma4-tcgen05），调用方漏传 role。旧实现里 best-effort 的作用域是
// **严格直接子级**（`pk == callerKey`）⇒ 孙级 0 命中 ⇒ 报错；而补上 role 后走「精确
// 地址键」（`channel:chatID/role:instance`，与深度无关）⇒ 命中。**两条路径作用域不一致
// = 本 bug**（契约：best-effort 的作用域 = 发起者的**整棵子树**；别的会话的树仍严格排除）。
func TestResolveInteractiveSessionKey_GrandchildInstanceMatch(t *testing.T) {
	a := &Agent{}
	caller := qualifyChatID("web", "chat_A")

	// 直接子级：mma3（parentKey = 发起者）
	mma3 := interactiveKey("web", "chat_A", "explore", "mma3")
	a.interactiveSubAgents.Store(mma3, &interactiveAgent{roleName: "explore", instance: "mma3", parentKey: caller})

	// 孙级：mma4-tcgen05（parentKey = mma3，而不是发起者）
	grandchild := interactiveKey("web", "chat_A", "explore", "mma4-tcgen05")
	a.interactiveSubAgents.Store(grandchild, &interactiveAgent{
		roleName: "explore", instance: "mma4-tcgen05", parentKey: mma3,
	})

	// ① 用户现场：只给 instance（漏传 role）⇒ 必须命中孙级
	if got, err := a.resolveInteractiveSessionKey("web", "chat_A", "", "mma4-tcgen05"); err != nil || got != grandchild {
		t.Fatalf("孙级仅 instance 必须命中：got=%q err=%v want=%q", got, err, grandchild)
	}
	// ② 带 role 也必须命中（精确地址键路径）
	if got, err := a.resolveInteractiveSessionKey("web", "chat_A", "explore", "mma4-tcgen05"); err != nil || got != grandchild {
		t.Fatalf("带 role 必须命中：got=%q err=%v", got, err)
	}
	// ③ ⛔ 跨树仍严禁：chat_B 用 instance-only 不得命中 chat_A 树里的孙级
	if got, err := a.resolveInteractiveSessionKey("web", "chat_B", "", "mma4-tcgen05"); err == nil {
		t.Fatalf("严禁跨树：chat_B 不应命中 %q（got=%q）", grandchild, got)
	}
	// ④ 孙级也出现在"本树可用列表"里（错误信息不再漏列）
	tree := a.matchInteractiveSessionsInTree(caller, "", "")
	if len(tree) != 2 {
		t.Fatalf("本树（含孙级）应列出 2 个会话，got %v", tree)
	}
}

// TestResolveInteractiveSessionKey_TreeOnlyUnavailable：树内没有匹配时，错误里只列
// **本树**的可用会话（并提示可用完整地址），而不是把别处的会话端上来。
func TestResolveInteractiveSessionKey_TreeOnlyUnavailable(t *testing.T) {
	a := &Agent{}
	caller := qualifyChatID("web", "chat_A")
	mine := interactiveKey("web", "chat_A", "explore", "slot-1")
	a.interactiveSubAgents.Store(mine, &interactiveAgent{roleName: "explore", instance: "slot-1", parentKey: caller})
	// 别人的树里的同名 instance：绝不能被本树匹配到
	other := interactiveKey("web", "chat_B", "explore", "fuse-moe")
	a.interactiveSubAgents.Store(other, &interactiveAgent{roleName: "explore", instance: "fuse-moe", parentKey: qualifyChatID("web", "chat_B")})

	_, err := a.resolveInteractiveSessionKey("web", "chat_A", "explore", "fuse-moe")
	if err == nil {
		t.Fatal("本树内没有 fuse-moe ⇒ 必须报错（不得 fallback 到 chat_B 的树）")
	}
	if !strings.Contains(err.Error(), mine) || strings.Contains(err.Error(), other) {
		t.Fatalf("错误应只列本树会话，got: %v", err)
	}
}
