package agent

// interactive_lastreply_persist_test.go —— 回归守卫：状态变更后 last_reply 必须真的落库。
//
// 背景（G-20261027-21）：修复 32f39fc8 之前，persistInteractiveAgent 只在
// spawn/restore 被调用，而 InteractiveAgentService.MarkStatus 只改 status/turn_id，
// 于是 last_reply 在 spawn 之后**再也没有任何代码写过** —— 内存里 ia.lastReply
// 一直有值，DB 里 last_reply / last_reply_at 却永远停在 spawn 那一刻。修复后
// markPersistedAgentStatus 走全量 upsert（agent/interactive_persist.go:108 →
// :50 → storage/sqlite/interactive_agents.go:112-116）。
//
// 为什么需要本文件：既有测试对 last_reply 的命中**全在读侧**（自己构造
// InteractiveAgentRecord 直接 Upsert 再读），没有任何一条覆盖
// 「ia.lastReply → persistInteractiveAgent → sqlite upsert → DB 列」这条写路径。
// 后果是把 interactive_persist.go 里的 `LastReply: ia.lastReply` 改成
// `LastReply: ""`，全套测试依然全绿 —— 也就是说这个修复此前没有任何回归防护。
// 本文件的用例全部从**内存态**（*interactiveAgent）出发、走真实落库路径，
// 因此上述变异必然把它打红（红绿日志见验收证据）。
//
// 用例 4 是**语义记录**而非缺陷修复：当前实现里空回复会清空 last_reply，
// 与任务书设想的「空回复不覆盖」不同，详见该用例注释。

import (
	"database/sql"
	"testing"
	"time"

	"xbot/storage/sqlite"
)

const (
	lastReplyRole     = "reviewer"
	lastReplyInstance = "lr1"
	lastReplyAgentKey = "agent:reviewer/lr1"
	lastReplyParent   = "cli:/w"
)

// newLastReplyFixture 造一个挂在**临时主库**（t.TempDir）上的 Agent，绝不碰生产库。
func newLastReplyFixture(t *testing.T) (*Agent, *sqlite.InteractiveAgentService, *sql.DB) {
	t.Helper()
	mt, _ := newAgentHistorySession(t)
	conn := mt.DB().Conn()
	return &Agent{multiSession: mt}, sqlite.NewInteractiveAgentService(conn), conn
}

// newLastReplyIA 造一个只带「落库所需最小事实」的内存态子代理。
func newLastReplyIA(reply string) *interactiveAgent {
	return &interactiveAgent{
		roleName:   lastReplyRole,
		instance:   lastReplyInstance,
		parentKey:  lastReplyParent,
		task:       "统计一下今天的数字",
		background: true,
		lastReply:  reply,
	}
}

// TestMarkPersistedAgentStatusPersistsLastReply 是本次修复的主守卫：
// 一次状态变更（→ idle）必须让内存里的 lastReply 出现在 DB 里，且 last_reply_at
// 同步落一个可解析的当前时间。
func TestMarkPersistedAgentStatusPersistsLastReply(t *testing.T) {
	a, svc, _ := newLastReplyFixture(t)
	const reply = "回复：答案是 42"

	before := time.Now().UTC().Add(-time.Second)
	a.markPersistedAgentStatus(newLastReplyIA(reply), sqlite.InteractiveAgentIdle, 0)

	rec, err := svc.Get(lastReplyAgentKey)
	if err != nil {
		t.Fatalf("get %s after status change: %v", lastReplyAgentKey, err)
	}
	if rec.LastReply != reply {
		t.Fatalf("last_reply = %q, want %q (内存态 lastReply 没有落库)", rec.LastReply, reply)
	}
	if rec.LastReplyAt == "" {
		t.Fatal("last_reply_at is empty — 有新回复时必须同步写时间戳")
	}
	at, err := time.Parse(time.RFC3339, rec.LastReplyAt)
	if err != nil {
		t.Fatalf("last_reply_at %q is not RFC3339: %v", rec.LastReplyAt, err)
	}
	if at.Before(before) || at.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("last_reply_at = %v, want within [%v, now]", at, before)
	}
	if rec.Status != sqlite.InteractiveAgentIdle {
		t.Fatalf("status = %q, want %q", rec.Status, sqlite.InteractiveAgentIdle)
	}
	// 落库顺带要保住身份字段，否则重启恢复找不到它。
	if rec.RoleName != lastReplyRole || rec.Instance != lastReplyInstance {
		t.Fatalf("role/instance = %q/%q, want %q/%q", rec.RoleName, rec.Instance, lastReplyRole, lastReplyInstance)
	}
	if rec.ParentChannel != "cli" || rec.ParentChatID != "/w" {
		t.Fatalf("parent = %q:%q, want cli:/w", rec.ParentChannel, rec.ParentChatID)
	}
}

// TestPersistLastReplyAtAdvancesOnNewReply：回复文本变化时 last_reply_at 必须前进。
//
// 先把 last_reply_at 手工回拨到一个固定旧值（这只是**造场景**，不绕过被测写路径），
// 再用新回复走一次状态变更：若 CASE 分支写错（写成常量 / 忘了 excluded.updated_at），
// 时间戳会停在旧值上，本用例打红。
func TestPersistLastReplyAtAdvancesOnNewReply(t *testing.T) {
	a, svc, conn := newLastReplyFixture(t)
	const stale = "2020-01-01T00:00:00Z"

	ia := newLastReplyIA("第一轮回复")
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)
	first, err := svc.Get(lastReplyAgentKey)
	if err != nil {
		t.Fatalf("get after first persist: %v", err)
	}
	if first.LastReply != "第一轮回复" || first.LastReplyAt == "" {
		t.Fatalf("first persist: last_reply=%q last_reply_at=%q", first.LastReply, first.LastReplyAt)
	}
	if _, err := conn.Exec(
		`UPDATE interactive_agents SET last_reply_at = ? WHERE agent_key = ?`, stale, lastReplyAgentKey); err != nil {
		t.Fatalf("backdate last_reply_at: %v", err)
	}

	ia.lastReply = "第二轮回复"
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)
	second, err := svc.Get(lastReplyAgentKey)
	if err != nil {
		t.Fatalf("get after second persist: %v", err)
	}
	if second.LastReply != "第二轮回复" {
		t.Fatalf("last_reply = %q, want %q (回复更新没有落库)", second.LastReply, "第二轮回复")
	}
	if second.LastReplyAt == stale {
		t.Fatalf("last_reply_at 仍是旧值 %q —— 回复变了却没有前进", stale)
	}
	// upsert 里 last_reply_at 取的就是同一条语句算出的 updated_at，两者必须逐字相同。
	if second.LastReplyAt != second.UpdatedAt {
		t.Fatalf("last_reply_at = %q, updated_at = %q; 回复变化时应取 excluded.updated_at",
			second.LastReplyAt, second.UpdatedAt)
	}
}

// TestPersistLastReplyAtStableWhenReplyUnchanged：回复文本没变（只是状态变了）时
// last_reply_at 不许前进 —— 否则它会退化成 updated_at 的副本（v77 的 CASE 语义）。
func TestPersistLastReplyAtStableWhenReplyUnchanged(t *testing.T) {
	a, svc, conn := newLastReplyFixture(t)
	const stale = "2020-01-01T00:00:00Z"

	ia := newLastReplyIA("同一轮回复")
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)
	if _, err := conn.Exec(
		`UPDATE interactive_agents SET last_reply_at = ? WHERE agent_key = ?`, stale, lastReplyAgentKey); err != nil {
		t.Fatalf("backdate last_reply_at: %v", err)
	}

	// idle → running，回复文本不变（时间戳不该因为它前进）。
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentRunning, 0)
	got, err := svc.Get(lastReplyAgentKey)
	if err != nil {
		t.Fatalf("get after status-only change: %v", err)
	}
	if got.LastReply != "同一轮回复" {
		t.Fatalf("last_reply = %q, want %q", got.LastReply, "同一轮回复")
	}
	if got.LastReplyAt != stale {
		t.Fatalf("last_reply_at = %q, want untouched %q（回复没变，时间戳不该前进）", got.LastReplyAt, stale)
	}
	if got.Status != sqlite.InteractiveAgentRunning {
		t.Fatalf("status = %q, want %q", got.Status, sqlite.InteractiveAgentRunning)
	}
}

// TestPersistEmptyLastReplySemantics 记录 **b9779d36 的实际语义**，任务书里的
// 「空回复不覆盖已有 last_reply」并不成立：
//
//   - last_reply 在 sqlite Upsert 里是 `last_reply=excluded.last_reply`，
//     **无条件覆盖**，所以空回复会把已有的 last_reply 清成空字符串；
//   - last_reply_at 有 CASE 保护（excluded.last_reply 为空串或与旧值相同时不前进），
//     空回复不会清它，旧时间戳保持。
//
// 真实触发路径：一轮 Run 以空 content 结束（取消 / 只有错误）时，
// interactive.go:2709 的 markPersistedAgentStatus 会用 ia.lastReply="" 落库。
//
// 本用例按实际实现断言，作用是把这个不一致**钉在明面上**：若将来决定改成
// 「空回复不覆盖」（改 sqlite Upsert 的 SET 子句），请同步改本用例 —— 那是行为
// 变更，不属于本次「只加回归测试」的范围。
func TestPersistEmptyLastReplySemantics(t *testing.T) {
	a, svc, conn := newLastReplyFixture(t)
	const stale = "2020-01-01T00:00:00Z"

	ia := newLastReplyIA("上一次的回复")
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)
	if _, err := conn.Exec(
		`UPDATE interactive_agents SET last_reply_at = ? WHERE agent_key = ?`, stale, lastReplyAgentKey); err != nil {
		t.Fatalf("backdate last_reply_at: %v", err)
	}

	ia.lastReply = ""
	a.markPersistedAgentStatus(ia, sqlite.InteractiveAgentIdle, 0)
	got, err := svc.Get(lastReplyAgentKey)
	if err != nil {
		t.Fatalf("get after empty reply: %v", err)
	}
	if got.LastReply != "" {
		t.Fatalf("last_reply = %q, want %q —— 当前实现是「无条件覆盖」；"+
			"若这里失败，说明 Upsert 已改成「空回复不覆盖」，请同步本用例注释", got.LastReply, "")
	}
	if got.LastReplyAt != stale {
		t.Fatalf("last_reply_at = %q, want untouched %q（空回复不该推进时间戳）", got.LastReplyAt, stale)
	}
}
