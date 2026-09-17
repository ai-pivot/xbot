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
