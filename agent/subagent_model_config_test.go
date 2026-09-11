package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// 同名模型被多个订阅提供时（同一个上游模型挂在两个账号下很常见），按模型名
// 解析归属必须选中【真正给这个模型配了配置】的订阅 —— 否则该模型的 per-model
// 配置（max_context）对这个调用方不可见，上层回落到 agent 级全局默认（200k），
// 表现为"设置里是 1M，子代理却只有 200k"（子代理走裸模型名解析；主 agent 走
// 会话绑定所以没事）。
//
// 真实数据现场：glm-5.3 在 mint(1M) / mintcn(0) / openai mint(0) 三个订阅下都有
// 行；deepseek-flash 在 dpsk(0) / dpsk mint(1M) 下都有行 —— 取"第一个"就选到 0。
func TestResolveSubscriptionForModel_PrefersConfiguredOwner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	subSvc := sqlite.NewLLMSubscriptionService(db)
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.SetSubscriptionSvc(subSvc)

	// 订阅 A：先加入，列了 shared-model 但没有配置（max_context=0）→ 旧逻辑选中它。
	unconfigured := &sqlite.LLMSubscription{Provider: "test", BaseURL: "http://a", APIKey: "sk-a", Model: "shared-model"}
	if err := subSvc.Add(unconfigured); err != nil {
		t.Fatalf("Add A: %v", err)
	}
	subSvc.UpsertModel(unconfigured.ID, "shared-model", 0, 0, "", "")

	// 订阅 B：后加入，same model 名字，但配了 1M。
	configured := &sqlite.LLMSubscription{Provider: "test", BaseURL: "http://b", APIKey: "sk-b", Model: "shared-model"}
	if err := subSvc.Add(configured); err != nil {
		t.Fatalf("Add B: %v", err)
	}
	subSvc.UpsertModel(configured.ID, "shared-model", 1_000_000, 32_768, "", "")

	owner, err := f.ResolveSubscriptionForModel("cli_user", "shared-model")
	if err != nil {
		t.Fatalf("ResolveSubscriptionForModel: %v", err)
	}
	if owner.ID != configured.ID {
		t.Fatalf("owner=%s (%s), want %s —— 必须选中给该模型配了 max_context 的订阅",
			owner.ID, owner.Name, configured.ID)
	}

	// 子代理路径（裸模型名 → GetLLMForModel）必须拿到该模型自己的 1M。
	_, subID, resolvedModel, maxCtx, _, _, ok := f.GetLLMForModel("cli_user", "shared-model")
	if !ok {
		t.Fatal("GetLLMForModel ok=false")
	}
	if subID != configured.ID || resolvedModel != "shared-model" {
		t.Fatalf("GetLLMForModel sub=%s model=%s, want sub=%s model=shared-model", subID, resolvedModel, configured.ID)
	}
	if maxCtx != 1_000_000 {
		t.Fatalf("GetLLMForModel maxCtx=%d, want 1000000（该模型自己的配置；0 会让上层回落到 agent 级 200k）", maxCtx)
	}
	// 与会话路径同一份实现（model, subID 参数顺序不同，必须同源）。
	if got := f.resolveEffectiveContext("shared-model", configured.ID); got != 1_000_000 {
		t.Fatalf("resolveEffectiveContext=%d, want 1000000（子代理与会话路径必须同源）", got)
	}
}
