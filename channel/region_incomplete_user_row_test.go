package channel

import (
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// ⛔ 消息不连续零容忍（用户 2026-10-11；AGENTS.md 最高优先级）：**未完整下发**的 turn
// （RegionsBefore > 0，只带了尾部区域窗口）**绝不在消息流里携带 user 行** —— 否则任何
// 「照着数组渲染」的消费者都会把 user msg 渲染在一个**尚未加载的缺口**上方
// （`user msg → 跳 y 条 → iter`）。它必须改挂到该 turn 的 assistant 行（TurnUser）上。
// 全量视图（CLI/RPC，foldView=false）**完全不变**（R5）。
func TestIncompleteTurn_UserRowMovedOutOfMessageStream(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	// 200 个迭代 > HistoryRegionWindow(100) ⇒ 折叠视图必然只下发尾部窗口。
	recs := make([]sqlite.IterationRecord, 0, 200)
	for i := 1; i <= 200; i++ {
		recs = append(recs, sqlite.IterationRecord{
			Iteration: i, Content: "x", CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "提问", TurnID: 7},
		{Role: "assistant", Content: "回复", TurnID: 7},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{7: recs}

	fold := ConvertMessagesToHistoryWithIterationsView(msgs, turnIterMap, true)
	var user, asst *HistoryMessage
	for i := range fold {
		if fold[i].TurnID != 7 {
			continue
		}
		switch fold[i].Role {
		case "user":
			user = &fold[i]
		case "assistant":
			asst = &fold[i]
		}
	}
	if asst == nil || asst.RegionsBefore <= 0 {
		t.Fatalf("前置：折叠视图该 turn 必须 RegionsBefore>0（拿到 %+v）", asst)
	}
	if len(asst.Iterations) == 0 {
		t.Fatal("尾部窗口的迭代必须照常下发（不丢存在性）")
	}
	if user != nil {
		t.Fatalf("未完整下发的 turn 的 user 行绝不允许留在消息流里: %+v", user)
	}
	if asst.TurnUser == nil || asst.TurnUser.Content != "提问" {
		t.Fatalf("user 行必须改挂到 assistant 行的 TurnUser 上: %+v", asst.TurnUser)
	}
	// 同一行不得同时既摘又留（流里恰好 1 行该 turn 的 assistant、0 行 user）。
	if n := len(fold); n != 1 {
		t.Fatalf("折叠视图该 turn 只应剩 1 行（assistant），实际 %d 行: %+v", n, fold)
	}

	// 全量视图（CLI/RPC）：user 行照常在流里、无 TurnUser/RegionsBefore（R5 逐字节不变）。
	full := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)
	var fullUser, fullAsst *HistoryMessage
	for i := range full {
		if full[i].TurnID == 7 && full[i].Role == "user" {
			fullUser = &full[i]
		}
		if full[i].TurnID == 7 && full[i].Role == "assistant" {
			fullAsst = &full[i]
		}
	}
	if fullUser == nil || fullUser.Content != "提问" {
		t.Fatalf("全量视图必须保持原样（user 行在流里）: %+v", full)
	}
	if fullAsst != nil && (fullAsst.TurnUser != nil || fullAsst.RegionsBefore != 0) {
		t.Fatalf("全量视图不得有区域窗口/TurnUser（R5）: %+v", fullAsst)
	}
}

// 已完整下发的 turn（RegionsBefore == 0）**不得**被动 —— user 行照常留在流里、无 TurnUser。
func TestCompleteTurn_UserRowStaysInMessageStream(t *testing.T) {
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	recs := []sqlite.IterationRecord{
		{Iteration: 1, Content: "a", CreatedAt: base},
		{Iteration: 2, Content: "b", CreatedAt: base.Add(time.Minute)},
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "提问", TurnID: 7},
		{Role: "assistant", Content: "回复", TurnID: 7},
	}
	out := ConvertMessagesToHistoryWithIterationsView(msgs, map[uint64][]sqlite.IterationRecord{7: recs}, true)
	var user, asst *HistoryMessage
	for i := range out {
		if out[i].TurnID != 7 {
			continue
		}
		if out[i].Role == "user" {
			user = &out[i]
		}
		if out[i].Role == "assistant" {
			asst = &out[i]
		}
	}
	if user == nil || user.Content != "提问" {
		t.Fatalf("完整 turn 的 user 行必须留在流里: %+v", out)
	}
	if asst != nil && asst.TurnUser != nil {
		t.Fatalf("完整 turn 不得挂 TurnUser: %+v", asst)
	}
}
