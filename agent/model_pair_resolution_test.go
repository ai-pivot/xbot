package agent

import (
	"testing"

	"xbot/storage/sqlite"
)

// ⛔ 模型解析必须带订阅 id。裸模型名一律不解析 —— 按名找订阅在同名模型挂多个
// 订阅时必须猜（真实数据：glm-5.3 在 mint(1M)/mintcn(0)/openai mint(0) 下都有行，
// 猜错就把别的订阅的 per-model 配置当成该模型的配置）。显式 "<subID>|<model>"
// 是唯一被接受的"具体模型"形式（tier 值本来就是这种 pair）。
func TestGetLLMForModel_RequiresSubscriptionPair(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	sub := &sqlite.LLMSubscription{
		ID: "sub-pair", SenderID: "cli_user", Name: "pair",
		Provider: "openai", BaseURL: "https://api.pair.example/v1", APIKey: "sk-pair",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "pair-model", 1_000_000, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// 裸模型名：不解析（即使只有一个订阅提供它）。
	if _, _, model, maxCtx, _, _, ok := f.GetLLMForModel("cli_user", "pair-model"); ok {
		t.Fatalf("bare model name resolved (model=%s maxCtx=%d) — 严禁按模型名解析", model, maxCtx)
	}

	// 显式 (subID, model) pair：解析并带上该模型自己的 per-model 配置。
	_, subID, model, maxCtx, _, _, ok := f.GetLLMForModel("cli_user", "sub-pair|pair-model")
	if !ok {
		t.Fatal("explicit (subID, model) pair must resolve")
	}
	if subID != "sub-pair" || model != "pair-model" {
		t.Fatalf("resolved sub=%s model=%s, want sub=sub-pair model=pair-model", subID, model)
	}
	if maxCtx != 1_000_000 {
		t.Fatalf("maxCtx=%d, want 1000000（该模型自己的配置）", maxCtx)
	}
}
