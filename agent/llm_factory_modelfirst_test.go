package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/protocol"
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
	f := NewLLMFactory(&llm.MockLLM{}, "system-default-model")
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

// TestResolveLLM_ThinkingMode_GlobalUserSetting verifies thinking_mode is now a
// global per-user setting read from user_settings (canonical channel), NOT from
// sub.ThinkingMode. Priority: per-model override → global user setting → "".
func TestResolveLLM_ThinkingMode_GlobalUserSetting(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	// Wire a settings service on a sibling connection to the same DB so
	// ResolveLLM can read the global user setting.
	db2, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	t.Cleanup(func() { db2.Close() })
	settingsSvc := NewSettingsService(sqlite.NewUserSettingsService(db2))
	f.SetSettingsService(settingsSvc)

	sub := &sqlite.LLMSubscription{
		ID: "sub-think", SenderID: "cli_user", Name: "ds", Provider: "openai",
		BaseURL: "https://api.ds.example/v1", APIKey: "sk-ds", Model: "deepseek-v4-pro",
		ThinkingMode: "disabled", // must be IGNORED — thinking is global now
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	chatID := "/home/proj:Agent-1"
	if err := f.SelectModel("cli_user", chatID, "cli", sub.ID, "deepseek-v4-pro"); err != nil {
		t.Fatalf("SelectModel: %v", err)
	}

	// No global setting → "" (auto), even though sub.ThinkingMode="disabled".
	if _, _, _, tm, _ := f.ResolveLLM("cli_user", chatID, "cli"); tm != "" {
		t.Errorf("thinkingMode = %q, want \"\" (auto) when global unset; sub.ThinkingMode must be ignored", tm)
	}

	// Set global thinking_mode=enabled under the canonical channel.
	if err := settingsSvc.SetSetting(thinkingModeChannel, "cli_user", "thinking_mode", "enabled"); err != nil {
		t.Fatalf("set thinking_mode: %v", err)
	}
	f.invalidateUserMemos("cli_user")
	if _, _, _, tm, _ := f.ResolveLLM("cli_user", chatID, "cli"); tm != "enabled" {
		t.Errorf("thinkingMode = %q, want \"enabled\" from global user setting", tm)
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
	// model to gpt-4o-audio-preview (mirrors Ctrl+N per-session model switch).
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
	// List is /models-driven (CachedModels + sub.Model); subscription_models rows
	// only carry params/enabled. Put all three in CachedModels so the disabled
	// one is actually present-then-excluded (not just absent).
	if err := subSvc.UpdateCachedModels(sub.ID, []string{"m-enabled", "m-disabled", "m-cached"}); err != nil {
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

// TestSetSubscriptionEnabled_SkipsEverywhere verifies the v40 subscription-level
// enabled flag: a disabled subscription contributes no models to ListAllModelsForUser,
// is never resolved as a model's owner, and rejects explicit SelectModel.
func TestSetSubscriptionEnabled_SkipsEverywhere(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-d", SenderID: "cli_user", Name: "d", Provider: "openai",
		BaseURL: "https://api.d.example/v1", APIKey: "sk-d", Model: "d-model",
		IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// subscription_models row carries params only; the list is /models-driven
	// (CachedModels + sub.Model), so the model appears via sub.Model.
	if err := subSvc.UpsertModel(sub.ID, "d-model", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel: %v", err)
	}

	// Enabled by default: model visible and resolvable.
	models := f.ListAllModelsForUser("cli_user")
	if !containsModel(models, "d-model") {
		t.Fatalf("d-model should be visible before disable, got %v", models)
	}
	owner, err := f.ResolveSubscriptionForModel("cli_user", "d-model")
	if err != nil || owner.ID != sub.ID {
		t.Fatalf("owner before disable = %v, %v (want %s)", owner, err, sub.ID)
	}

	// Disable the subscription.
	if err := f.SetSubscriptionEnabled(sub.ID, false); err != nil {
		t.Fatalf("SetSubscriptionEnabled(false): %v", err)
	}

	// ListAllModelsForUser skips the disabled subscription entirely.
	if models = f.ListAllModelsForUser("cli_user"); containsModel(models, "d-model") {
		t.Errorf("d-model should be hidden after sub disable, got %v", models)
	}
	// ListAllModelEntriesForUser (picker) also skips disabled subscriptions.
	for _, e := range f.ListAllModelEntriesForUser("cli_user") {
		if e.Model == "d-model" {
			t.Errorf("d-model should not appear in entries after sub disable, got %+v", e)
		}
	}
	// ResolveSubscriptionForModel no longer resolves the disabled subscription as owner.
	if _, err := f.ResolveSubscriptionForModel("cli_user", "d-model"); err == nil {
		t.Error("ResolveSubscriptionForModel should fail for disabled subscription's model")
	}
	// SelectModel rejects the disabled subscription.
	chatID := "/home/proj:Agent-2"
	if err := f.SelectModel("cli_user", chatID, "cli", sub.ID, "d-model"); err == nil {
		t.Error("SelectModel should reject disabled subscription")
	}

	// Re-enable: everything works again (lossless).
	if err := f.SetSubscriptionEnabled(sub.ID, true); err != nil {
		t.Fatalf("SetSubscriptionEnabled(true): %v", err)
	}
	if models = f.ListAllModelsForUser("cli_user"); !containsModel(models, "d-model") {
		t.Errorf("d-model should reappear after re-enable, got %v", models)
	}
}

func containsModel(models []string, want string) bool {
	for _, m := range models {
		if m == want {
			return true
		}
	}
	return false
}

// TestMakeOnModelsLoaded_PersistsCachedModels verifies the OnModelsLoaded
// callback (re-wired into createClientFromSub in the model-first path) persists
// a subscription's API-discovered models to CachedModels. This is the fix for
// the "incomplete model list" bug where fetched /models never reached the DB.
func TestMakeOnModelsLoaded_PersistsCachedModels(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-c", SenderID: "cli_user", Name: "charlie", Provider: "openai",
		BaseURL: "https://api.c.example/v1", APIKey: "sk-c", IsDefault: true,
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cb := f.makeOnModelsLoaded(sub.ID)
	if cb == nil {
		t.Fatal("makeOnModelsLoaded returned nil for a real subscription")
	}
	cb([]string{"c-1", "c-2", "c-3"})

	got, err := subSvc.Get(sub.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.CachedModels) != 3 || got.CachedModels[0] != "c-1" {
		t.Errorf("CachedModels = %v, want [c-1 c-2 c-3]", got.CachedModels)
	}
	// The persisted models now appear in the entry list with the owner's name.
	entries := f.ListAllModelEntriesForUser("cli_user")
	want := map[string]string{"c-1": "charlie", "c-2": "charlie", "c-3": "charlie"}
	gotMap := map[string]string{}
	for _, e := range entries {
		gotMap[e.Model] = e.SubName
	}
	for m, s := range want {
		if gotMap[m] != s {
			t.Errorf("entry %q = subName %q, want %q (entries=%v)", m, gotMap[m], s, entries)
		}
	}

	// Callback for a non-existent subscription must be safe (no nil deref).
	cb2 := f.makeOnModelsLoaded("does-not-exist")
	if cb2 != nil {
		cb2([]string{"x"}) // must not panic
	}
}

// TestRefreshModelEntriesForUser_NoLoaderGraceful verifies RefreshModelEntriesForUser
// degrades gracefully when LLM clients don't implement ModelLoader (e.g. MockLLM
// in tests): it returns the current entry list without error.
func TestRefreshModelEntriesForUser_NoLoaderGraceful(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	sub := &sqlite.LLMSubscription{
		ID: "sub-r", SenderID: "cli_user", Name: "romeo", Provider: "openai",
		BaseURL: "http://127.0.0.1:1", APIKey: "sk-r", Model: "r-default",
	}
	if err := subSvc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before := f.ListAllModelEntriesForUser("cli_user")
	after := f.RefreshModelEntriesForUser("cli_user")
	if len(before) != len(after) {
		t.Fatalf("refresh changed entry count: before=%v after=%v", before, after)
	}
	// r-default (sub.Model) must still be present.
	found := false
	for _, e := range after {
		if e.Model == "r-default" {
			found = true
		}
	}
	if !found {
		t.Errorf("r-default missing after refresh: %v", after)
	}
}

// model with its owning subscription's name (for "订阅名 · 模型名" display), carry
// the per-(sub,model) availability Status (normal/offline/disabled), include all
// DB items (fetched + sub.Model + manually-added records), and skip disabled
// subscriptions. Anything not disabled is selectable.
func TestListAllModelEntriesForUser_PairsSubName(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	subA := &sqlite.LLMSubscription{
		ID: "sub-a", SenderID: "cli_user", Name: "alpha", Provider: "openai",
		BaseURL: "https://api.a.example/v1", APIKey: "sk-a", Model: "a-model",
		IsDefault: true,
	}
	subB := &sqlite.LLMSubscription{
		ID: "sub-b", SenderID: "cli_user", Name: "beta", Provider: "openai",
		BaseURL: "https://api.b.example/v1", APIKey: "sk-b", Model: "b-model",
	}
	if err := subSvc.Add(subA); err != nil {
		t.Fatalf("Add A: %v", err)
	}
	if err := subSvc.Add(subB); err != nil {
		t.Fatalf("Add B: %v", err)
	}
	// subB fetched list includes b-fetched (normal, no row) and b-disabled (row disabled).
	if err := subSvc.UpdateCachedModels(subB.ID, []string{"b-fetched", "b-disabled"}); err != nil {
		t.Fatalf("UpdateCachedModels: %v", err)
	}
	// b-manual: a record but NOT fetched → offline (selectable).
	if err := subSvc.UpsertModel(subB.ID, "b-manual", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel b-manual: %v", err)
	}
	// b-disabled: fetched but row disabled → disabled (not selectable).
	if err := subSvc.UpsertModel(subB.ID, "b-disabled", 0, 0, "", ""); err != nil {
		t.Fatalf("UpsertModel b-disabled: %v", err)
	}
	if err := subSvc.SetModelEnabled(subB.ID, "b-disabled", false); err != nil {
		t.Fatalf("SetModelEnabled: %v", err)
	}

	entries := f.ListAllModelEntriesForUser("cli_user")
	got := map[string]protocol.ModelEntry{}
	for _, e := range entries {
		got[e.Model] = e
	}
	wantStatus := map[string]string{
		"a-model":    "normal",   // sub.Model
		"b-model":    "normal",   // sub.Model
		"b-fetched":  "normal",   // in CachedModels, no row
		"b-manual":   "offline",  // record, not fetched
		"b-disabled": "disabled", // row enabled=0
	}
	for model, wantSt := range wantStatus {
		e, ok := got[model]
		if !ok {
			t.Errorf("missing entry for %q (entries=%+v)", model, entries)
			continue
		}
		if e.Status != wantSt {
			t.Errorf("status for %q = %q, want %q", model, e.Status, wantSt)
		}
	}
	if e, ok := got["b-manual"]; !ok || e.SubName != "beta" {
		t.Errorf("b-manual should be owned by beta, got %+v", e)
	}

	// ListAllModelsForUser = selectable entries (normal + offline), in
	// entry order, excluding disabled.
	names := f.ListAllModelsForUser("cli_user")
	selectable := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Status != "disabled" {
			selectable = append(selectable, e.Model)
		}
	}
	if len(names) != len(selectable) {
		t.Fatalf("len mismatch: ListAllModels=%v selectable=%v", names, selectable)
	}
	for i, n := range names {
		if selectable[i] != n {
			t.Errorf("position %d: selectable=%q != ListAllModels=%q", i, selectable[i], n)
		}
	}
	if containsModel(names, "b-disabled") {
		t.Errorf("ListAllModelsForUser should exclude disabled b-disabled, got %v", names)
	}
	if !containsModel(names, "b-manual") {
		t.Errorf("ListAllModelsForUser should include offline b-manual (selectable), got %v", names)
	}

	// Disabling subscription B hides all its models entirely.
	if err := f.SetSubscriptionEnabled(subB.ID, false); err != nil {
		t.Fatalf("SetSubscriptionEnabled(false): %v", err)
	}
	entries = f.ListAllModelEntriesForUser("cli_user")
	for _, e := range entries {
		if e.SubID == subB.ID {
			t.Errorf("disabled subscription B should contribute no entries, got %v", e)
		}
	}
}

// TestListAllModelEntriesForUser_ListsSameModelPerSubscription verifies the
// picker lists the same model name once per subscription that serves it, NOT
// deduped by model name. The user must be able to pick the exact subscription
// (e.g. "system · deepseek-v4-pro" vs "deepseek · deepseek-v4-pro").
func TestListAllModelEntriesForUser_ListsSameModelPerSubscription(t *testing.T) {
	f, subSvc, _ := newModelFirstTestFactory(t)
	subA := &sqlite.LLMSubscription{
		ID: "sub-a", SenderID: "cli_user", Name: "alpha", Provider: "openai",
		BaseURL: "https://api.a.example/v1", APIKey: "sk-a", Model: "shared-model",
		IsDefault: true,
	}
	subB := &sqlite.LLMSubscription{
		ID: "sub-b", SenderID: "cli_user", Name: "beta", Provider: "openai",
		BaseURL: "https://api.b.example/v1", APIKey: "sk-b", Model: "shared-model",
	}
	if err := subSvc.Add(subA); err != nil {
		t.Fatalf("Add A: %v", err)
	}
	if err := subSvc.Add(subB); err != nil {
		t.Fatalf("Add B: %v", err)
	}
	// Both subs serve the same model name (sub.Model). The picker must emit two
	// distinct entries — one per subscription — so the user can disambiguate.
	entries := f.ListAllModelEntriesForUser("cli_user")
	var owners []string
	for _, e := range entries {
		if e.Model == "shared-model" {
			owners = append(owners, e.SubID)
		}
	}
	if len(owners) != 2 {
		t.Fatalf("expected shared-model to appear once per subscription (2 entries), got %d: %v", len(owners), entries)
	}
	seen := map[string]bool{}
	for _, id := range owners {
		if id != subA.ID && id != subB.ID {
			t.Errorf("unexpected owner %q for shared-model", id)
		}
		if seen[id] {
			t.Errorf("owner %q listed twice for shared-model", id)
		}
		seen[id] = true
	}

	// ListAllModelsForUser is the selectable model-name set (for tier selectors);
	// it stays deduped by model name, so shared-model appears once.
	names := f.ListAllModelsForUser("cli_user")
	count := 0
	for _, n := range names {
		if n == "shared-model" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ListAllModelsForUser should list shared-model once, got %d: %v", count, names)
	}
}
