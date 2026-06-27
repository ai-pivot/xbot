package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// newModelFirstTestFactory builds a DB-backed factory wired with subscription +
// tenant services, ready for ResolveLLM/SelectModel tests.
func newModelFirstTestFactory(t *testing.T) (*LLMFactory, *sqlite.LLMSubscriptionService, *sqlite.TenantService) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	subSvc := sqlite.NewLLMSubscriptionService(db)
	tenantSvc := sqlite.NewTenantService(db)
	f := NewLLMFactory(nil, &llm.MockLLM{}, "system-default-model")
	f.SetSubscriptionSvc(subSvc)
	f.SetTenantSvc(tenantSvc)
	return f, subSvc, tenantSvc
}

// TestResolveLLM_SelectModel_PersistsPerSession verifies SelectModel writes the
// per-session (sub, model) to tenants and ResolveLLM reads it back, with the
// client cached per subscription.
func TestResolveLLM_SelectModel_PersistsPerSession(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai",
		BaseURL: "https://api.gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4o",
		IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "gpt-4o-audio-preview", 200000, 8192, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	chatID := "/home/proj:Agent-1"
	if err := f.SelectModel("cli_user", chatID, "cli", sub.ID, "gpt-4o-audio-preview"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}

	client, model, maxCtx, _, maxOut := f.ResolveLLM("cli_user", chatID, "cli")
	if client == nil {
		t.Fatal("ResolveLLM returned nil client")
	}
	if model != "gpt-4o-audio-preview" {
		t.Errorf("model = %q, want gpt-4o-audio-preview", model)
	}
	if maxCtx != 200000 {
		t.Errorf("maxCtx = %d, want 200000 (from subscription_models)", maxCtx)
	}
	if maxOut != 8192 {
		t.Errorf("maxOut = %d, want 8192 (from subscription_models)", maxOut)
	}

	// Client is cached per subscription — a second ResolveLLM (different model,
	// same sub) must reuse the same client.
	c2, _, _, _, _ := f.ResolveLLM("cli_user", chatID, "cli")
	if c2 != client {
		t.Error("client not reused from clientCache")
	}
}

// TestResolveLLM_FallsBackToUserDefaultModel verifies that without a per-session
// mapping, ResolveLLM uses user_default_model.
func TestResolveLLM_FallsBackToUserDefaultModel(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-ds", SenderID: "cli_user", Name: "deepseek", Provider: "openai",
		BaseURL: "https://api.ds.example/v1", APIKey: "sk-ds", Model: "deepseek-v4-pro",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := f.SetUserDefaultModel("cli_user", sub.ID, "deepseek-v4-pro"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}
	// A session with no per-session mapping falls back to the user default.
	_, model, _, _, _ := f.ResolveLLM("cli_user", "/fresh/session", "cli")
	if model != "deepseek-v4-pro" {
		t.Errorf("model = %q, want deepseek-v4-pro (user default)", model)
	}
}

// TestResolveLLM_FallsBackToSystemDefault verifies that with no subscription
// state at all, ResolveLLM returns the system default LLM + model.
func TestResolveLLM_FallsBackToSystemDefault(t *testing.T) {
	f, _, _ := newModelFirstTestFactory(t)
	client, model, _, _, _ := f.ResolveLLM("nobody", "/no/session", "cli")
	if client == nil {
		t.Fatal("expected non-nil system default client")
	}
	if model != "system-default-model" {
		t.Errorf("model = %q, want system-default-model", model)
	}
}

// TestSelectModel_RejectsDisabledModel verifies that selecting a disabled model
// fails and does not persist.
func TestSelectModel_RejectsDisabledModel(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-x", SenderID: "cli_user", Name: "x", Provider: "openai",
		BaseURL: "https://api.x.example/v1", APIKey: "sk-x", Model: "m1",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m1", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	if err := f.SetModelEnabled(sub.ID, "m1", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	if err := f.SelectModel("cli_user", "/chat", "cli", sub.ID, "m1"); err == nil {
		t.Error("SelectModel on disabled model should error")
	}
}

// TestSetModelEnabled_InvalidatesSubscription verifies that toggling a model
// drops the cached client + session memo for its subscription.
func TestSetModelEnabled_InvalidatesSubscription(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-inv", SenderID: "cli_user", Name: "inv", Provider: "openai",
		BaseURL: "https://api.inv.example/v1", APIKey: "sk-inv", Model: "m1",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m1", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}
	if err := f.SelectModel("cli_user", "/chat", "cli", sub.ID, "m1"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	c1, _, _, _, _ := f.ResolveLLM("cli_user", "/chat", "cli")
	if c1 == nil {
		t.Fatal("expected client")
	}
	// Disable → invalidate. The clientCache entry for this sub must be gone.
	if err := f.SetModelEnabled(sub.ID, "m1", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	f.mu.RLock()
	_, hasClient := f.clientCache[clientCacheKey{subID: sub.ID, apiType: ""}]
	_, hasMemo := f.sessionMemo[chatKey("cli_user", "/chat")]
	f.mu.RUnlock()
	if hasClient {
		t.Error("clientCache entry should be invalidated after SetModelEnabled")
	}
	if hasMemo {
		t.Error("sessionMemo should be invalidated after SetModelEnabled")
	}
}

// TestRefreshSessionEntry_PreservesPerSessionModel is the regression test for the
// 404 bug: a session whose subscription has an empty default model must NOT be
// rebuilt with a poisoned f.defaultModel from a previous subscription.
// RefreshSessionEntry must keep the per-session model (from tenants / cached entry).
func TestRefreshSessionEntry_PreservesPerSessionModel(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	// Subscription whose default model is empty (like the "gpt" sub in the bug).
	subGPT := &sqlite.LLMSubscription{
		ID: "sub-gpt2", SenderID: "cli_user", Name: "gpt", Provider: "openai",
		BaseURL: "https://api.gpt.example/v1", APIKey: "sk-gpt", Model: "",
		IsDefault: true,
	}
	if err := subSvc.Add(subGPT); err != nil {
		t.Fatalf("Add gpt: %v", err)
	}
	if err := subSvc.UpsertModel(subGPT.ID, "gpt-4o-audio-preview", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	chatID := "/home/proj:Agent-fix"
	// Establish a per-session entry on the gpt subscription, then switch its
	// model to gpt-4o-audio-preview (mirrors Ctrl+L per-session model switch).
	if err := f.SwitchSubscription("cli_user", subGPT, chatID); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}
	f.SwitchModel("cli_user", "gpt-4o-audio-preview", chatID)

	// Persist the per-session selection to tenants (as the RPC switch_model does).
	if err := f.tenantSvc.SetTenantSubscription("cli", chatID, subGPT.ID, "gpt-4o-audio-preview"); err != nil {
		t.Fatalf("SetTenantSubscription: %v", err)
	}

	// A previous subscription left f.defaultModel pointing elsewhere. The bug
	// caused RefreshSessionEntry to rebuild with this poisoned default. Simulate
	// by setting f.defaultModel to an unrelated model name.
	f.mu.Lock()
	f.defaultModel = "kimi-k2.7-from-old-sub"
	f.mu.Unlock()

	// RefreshSessionEntry must preserve the per-session model, NOT fall back to
	// the poisoned f.defaultModel.
	f.RefreshSessionEntry("cli_user", chatID, "cli")

	_, model, _, _, _ := f.GetLLMForChat("cli_user", chatID)
	if model != "gpt-4o-audio-preview" {
		t.Errorf("after RefreshSessionEntry, model = %q, want gpt-4o-audio-preview (per-session model lost → 404 bug)", model)
	}
}

// TestCreateEntryFromSub_PicksSubModelNotPoisonedDefault verifies that when a
// subscription has an empty Model but registered subscription_models rows,
// createEntryFromSub picks the sub's model instead of f.defaultModel.
func TestCreateEntryFromSub_PicksSubModelNotPoisonedDefault(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-empty", SenderID: "cli_user", Name: "empty", Provider: "openai",
		BaseURL: "https://api.empty.example/v1", APIKey: "sk-empty", Model: "",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "real-model-a", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel a: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "real-model-b", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel b: %v", err)
	}

	f.mu.Lock()
	f.defaultModel = "poisoned-from-old-sub"
	f.mu.Unlock()

	if err := f.SwitchSubscription("cli_user", sub, ""); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}
	_, model, _, _, _ := f.GetLLM("cli_user")
	if model == "poisoned-from-old-sub" {
		t.Errorf("model = poisoned f.defaultModel %q; should pick a real model from the subscription", model)
	}
	if model != "real-model-a" && model != "real-model-b" {
		t.Errorf("model = %q, want one of the subscription's registered models", model)
	}
}

// TestResolveSubscriptionForModel_PrefersOwnerOverDefault verifies that when a
// model belongs to a non-default subscription, the resolver returns the owner
// subscription rather than the default. This is the core fix for the
// cross-subscription cycling 404 (model name from sub B paired with sub A's
// credentials).
func TestResolveSubscriptionForModel_PrefersOwnerOverDefault(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	gptSub := &sqlite.LLMSubscription{
		ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai",
		BaseURL: "https://api.gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4o",
		IsDefault: true,
	}
	kimiSub := &sqlite.LLMSubscription{
		ID: "sub-kimi", SenderID: "cli_user", Name: "kimi", Provider: "openai",
		BaseURL: "https://api.kimi.com/coding/", APIKey: "sk-kimi", Model: "kimi-k2.7",
	}
	if err := subSvc.Add(gptSub); err != nil {
		t.Fatalf("Add gpt: %v", err)
	}
	if err := subSvc.Add(kimiSub); err != nil {
		t.Fatalf("Add kimi: %v", err)
	}
	if err := subSvc.UpsertModel(kimiSub.ID, "kimi-k2.7", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel kimi: %v", err)
	}

	owner, err := f.ResolveSubscriptionForModel("cli_user", "kimi-k2.7")
	if err != nil {
		t.Fatalf("ResolveSubscriptionForModel: %v", err)
	}
	if owner.ID != kimiSub.ID {
		t.Errorf("owner = %q, want %q (the subscription that actually serves the model, not the default)",
			owner.ID, kimiSub.ID)
	}
}

// TestResolveSubscriptionForModel_SkipsDisabledModel verifies a disabled
// subscription_models row is not selected as the owner (so SelectModel later
// rejects the switch rather than pairing disabled creds).
func TestResolveSubscriptionForModel_SkipsDisabledModel(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	sub := &sqlite.LLMSubscription{
		ID: "sub-x", SenderID: "cli_user", Name: "x", Provider: "openai",
		BaseURL: "https://api.x.example/v1", APIKey: "sk-x", Model: "m-on",
		IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m-on", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel m-on: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m-off", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel m-off: %v", err)
	}
	if err := subSvc.SetModelEnabled(sub.ID, "m-off", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}

	if _, err := f.ResolveSubscriptionForModel("cli_user", "m-off"); err == nil {
		t.Error("expected error for disabled model, got nil")
	}

	owner, err := f.ResolveSubscriptionForModel("cli_user", "m-on")
	if err != nil {
		t.Fatalf("ResolveSubscriptionForModel m-on: %v", err)
	}
	if owner.ID != sub.ID {
		t.Errorf("owner = %q, want %q", owner.ID, sub.ID)
	}
}

// TestResolveSubscriptionForModel_FallbackToCachedModels verifies that when a
// model has no subscription_models row, the resolver falls back to CachedModels.
func TestResolveSubscriptionForModel_FallbackToCachedModels(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)

	sub := &sqlite.LLMSubscription{
		ID: "sub-c", SenderID: "cli_user", Name: "c", Provider: "openai",
		BaseURL: "https://api.c.example/v1", APIKey: "sk-c",
		IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpdateCachedModels(sub.ID, []string{"cached-only-model"}); err != nil {
		t.Fatalf("UpdateCachedModels: %v", err)
	}
	owner, err := f.ResolveSubscriptionForModel("cli_user", "cached-only-model")
	if err != nil {
		t.Fatalf("ResolveSubscriptionForModel: %v", err)
	}
	if owner.ID != sub.ID {
		t.Errorf("owner = %q, want %q", owner.ID, sub.ID)
	}
}

// TestListAllModelsForUser_ExcludesDisabled verifies that a model disabled via
// subscription_models is excluded from the unified model list, while enabled
// and loose (CachedModels) models are included.
func TestListAllModelsForUser_ExcludesDisabled(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-l", SenderID: "cli_user", Name: "l", Provider: "openai",
		BaseURL: "https://api.l.example/v1", APIKey: "sk-l", IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m-enabled", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel enabled: %v", err)
	}
	if err := subSvc.UpsertModel(sub.ID, "m-disabled", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel disabled: %v", err)
	}
	if err := subSvc.SetModelEnabled(sub.ID, "m-disabled", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}
	if err := subSvc.UpdateCachedModels(sub.ID, []string{"m-cached"}); err != nil {
		t.Fatalf("UpdateCachedModels: %v", err)
	}

	models := f.ListAllModelsForUser("cli_user")
	contains := func(want string) bool {
		for _, m := range models {
			if m == want {
				return true
			}
		}
		return false
	}
	if !contains("m-enabled") {
		t.Errorf("m-enabled missing from %v", models)
	}
	if contains("m-disabled") {
		t.Errorf("m-disabled should be excluded from %v", models)
	}
	if !contains("m-cached") {
		t.Errorf("m-cached (loose) missing from %v", models)
	}
}
