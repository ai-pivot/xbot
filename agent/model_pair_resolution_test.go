package agent

import (
	"testing"

	"xbot/config"
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

// tier 名（vanguard/balance/swift，含 strong/medium/weak 别名）**仍然允许**，并且
// 默认就是 balance —— 它们不是"裸模型名"，而是由 tier 配置（值恒为 "subID|model"
// 对）解析。本次改动只删除了"裸的具体模型名"解析，绝不能影响 tier 路径。
func TestGetLLMForModel_TierNamesStillResolve(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	db2, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	t.Cleanup(func() { db2.Close() })
	settingsSvc := NewSettingsService(sqlite.NewUserSettingsService(db2))
	f.SetSettingsService(settingsSvc)

	sub := &sqlite.LLMSubscription{
		ID: "sub-tier", SenderID: "cli_user", Name: "tier-sub",
		Provider: "openai", BaseURL: "https://api.tier.example/v1", APIKey: "sk-tier",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "tier-model", 1_000_000, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	if err := settingsSvc.SetSetting(thinkingModeChannel, "cli_user", "tier_balance", sub.ID+"|tier-model"); err != nil {
		t.Fatalf("set tier_balance: %v", err)
	}

	// tier 名（含别名）解析到 tier 配置里的 (subID, model) 对 + 该模型自己的配置。
	for _, tier := range []string{"balance", "medium"} {
		_, subID, model, maxCtx, _, _, ok := f.GetLLMForModel("cli_user", tier)
		if !ok || subID != sub.ID || model != "tier-model" || maxCtx != 1_000_000 {
			t.Fatalf("tier %q → sub=%s model=%s maxCtx=%d ok=%v, want sub=%s model=tier-model maxCtx=1000000 ok=true",
				tier, subID, model, maxCtx, ok, sub.ID)
		}
	}

	// 未配置的 tier 走 fallback 链（swift → balance），不是报错也不是按名猜。
	if _, subID, model, _, _, _, ok := f.GetLLMForModel("cli_user", "swift"); !ok || subID != sub.ID || model != "tier-model" {
		t.Fatalf("swift → sub=%s model=%s ok=%v, want fallback to balance tier", subID, model, ok)
	}

	// 而裸的具体模型名依旧不解析（tier 名 ≠ 模型名）。
	if _, _, _, _, _, _, ok := f.GetLLMForModel("cli_user", "tier-model"); ok {
		t.Fatal("bare concrete model name must NOT resolve")
	}
}
