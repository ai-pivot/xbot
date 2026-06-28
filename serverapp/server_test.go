package serverapp

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	llm "xbot/llm"
	"xbot/storage/sqlite"
)

func newTestConfig() *config.Config {
	enableAutoCompress := false
	return &config.Config{
		LLM: config.LLMConfig{
			Provider:      "openai",
			APIKey:        "sk-test",
			Model:         "gpt-4.1",
			BaseURL:       "https://api.example.com/v1",
			VanguardModel: "gpt-4.1-pro",
			BalanceModel:  "gpt-4.1",
			SwiftModel:    "gpt-4.1-mini",
		},
		Sandbox: config.SandboxConfig{Mode: "docker"},
		Agent: config.AgentConfig{
			MemoryProvider:     "flat",
			ContextMode:        "manual",
			MaxIterations:      321,
			MaxConcurrency:     7,
			MaxContextTokens:   456789,
			EnableAutoCompress: &enableAutoCompress,
		},
		TavilyAPIKey: "tv-test",
	}
}

// TestHandleCLIRPCAdminAddSubscription_ListRoundTrip verifies that a subscription
// added via adminAddSubscription (SenderID="cli_user") is visible when listing
// with an empty senderID (which falls back to WS auth "admin").
// This was a real bug: openQuickSwitch passes senderID="" → server falls back
// to authSenderID "admin" → svc.List("admin") returns nothing because subs are
// stored under "cli_user".
func TestHandleCLIRPCAdminAddSubscription_ListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add subscription via admin path (same as remote CLI does)
	sub := channel.Subscription{
		Name: "test", Provider: "openai",
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-4",
	}
	addParams, _ := json.Marshal(map[string]any{"sub": sub})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List with empty senderID (simulates openQuickSwitch behavior)
	// Before fix: senderIDFromParams falls back to "admin" → empty list
	// After fix: should return the subscription
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(raw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) == 0 {
		t.Fatal("list_subscriptions returned empty, expected the subscription added by admin")
	}
	if subs[0].Name != "test" {
		t.Fatalf("expected subscription name 'test', got %q", subs[0].Name)
	}
}

// TestHandleCLIRPCAddSubscription_PreservesCredentials verifies that add_subscription
// RPC correctly deserializes base_url and api_key from the snake_case JSON payload.
// This was a real bug: rpc_table.go used sqlite.LLMSubscription (no JSON tags) to
// receive the RPC parameter, but the client sends channelSubscriptionJSON (with
// json:"base_url" / json:"api_key" tags). Go's json package couldn't match the
// fields → base_url and api_key were silently dropped (always empty).
func TestHandleCLIRPCAddSubscription_PreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Use snake_case keys matching channelSubscriptionJSON — the format the real
	// backend sends via RPC (backend_impl.go UpdateSubscription).
	addParams, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name":     "codex",
			"provider": "openai",
			"base_url": "https://api.openai-proxy.org/v1",
			"api_key":  "sk-secret-key-12345",
			"model":    "gpt-5.5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List and verify base_url/api_key are preserved
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(raw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) == 0 {
		t.Fatal("list_subscriptions returned empty")
	}
	// subToChannel masks API key
	if subs[0].BaseURL != "https://api.openai-proxy.org/v1" {
		t.Fatalf("expected base_url 'https://api.openai-proxy.org/v1', got %q", subs[0].BaseURL)
	}
	if subs[0].APIKey != "sk-s****" {
		t.Fatalf("expected masked api_key 'sk-s****', got %q", subs[0].APIKey)
	}
}

// TestHandleCLIRPCUpdateSubscription_PreservesCredentials verifies that
// update_subscription RPC correctly deserializes and preserves base_url and api_key.
func TestHandleCLIRPCUpdateSubscription_PreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add a subscription first (using snake_case matching real client)
	addParams, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name":     "codex",
			"provider": "openai",
			"base_url": "https://api.openai-proxy.org/v1",
			"api_key":  "sk-secret-key-12345",
			"model":    "gpt-5.5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// Get the subscription ID via list
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(listRaw, &subs); err != nil || len(subs) == 0 {
		t.Fatalf("unmarshal list: %v", err)
	}
	subID := subs[0].ID

	// Update the subscription with a new name but same credentials
	// Using snake_case matching real client (channelSubscriptionJSON tags)
	updateParams, _ := json.Marshal(map[string]any{
		"id": subID,
		"sub": map[string]any{
			"name":              "codex-updated",
			"provider":          "openai",
			"base_url":          "https://api.openai-proxy.org/v1",
			"api_key":           "sk-secret-key-12345",
			"model":             "gpt-5.5",
			"max_output_tokens": 0,
			"thinking_mode":     "",
		},
	})
	if _, err := HandleCLIRPC(table, "update_subscription", updateParams, "admin"); err != nil {
		t.Fatalf("update_subscription: %v", err)
	}

	// Verify base_url and api_key are preserved
	listRaw2, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list_subscriptions after update: %v", err)
	}
	var subs2 []channel.Subscription
	if err := json.Unmarshal(listRaw2, &subs2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs2) == 0 {
		t.Fatal("list_subscriptions returned empty after update")
	}
	if subs2[0].Name != "codex-updated" {
		t.Fatalf("expected name 'codex-updated', got %q", subs2[0].Name)
	}
	if subs2[0].BaseURL != "https://api.openai-proxy.org/v1" {
		t.Fatalf("expected base_url preserved, got %q", subs2[0].BaseURL)
	}
	if subs2[0].APIKey != "sk-s****" {
		t.Fatalf("expected masked api_key 'sk-s****', got %q", subs2[0].APIKey)
	}
}

func newTestBackendWithSettings(t *testing.T) (*agent.Agent, *sqlite.UserSettingsService) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewUserSettingsService(db)
	agentSvc := agent.NewSettingsService(store)
	ag := &agent.Agent{}
	ag.SetSettingsService(agentSvc)
	return ag, store
}

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SeedsOnlyWhenEmpty(t *testing.T) {
	cfg := newTestConfig()
	ag, store := newTestBackendWithSettings(t)
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, ag, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	seeded, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(seeded) == 0 {
		t.Fatal("expected seeded settings, got none")
	}
	if seeded["context_mode"] != "manual" {
		t.Fatalf("context_mode = %q, want manual", seeded["context_mode"])
	}
	if seeded["theme"] != "midnight" {
		t.Fatalf("theme = %q, want midnight", seeded["theme"])
	}
	if seeded["enable_auto_compress"] != "false" {
		t.Fatalf("enable_auto_compress = %q, want false", seeded["enable_auto_compress"])
	}
	if _, ok := seeded["llm_model"]; ok {
		t.Fatalf("llm_model should not be seeded into user settings: %#v", seeded)
	}
}

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SkipsWhenUserAlreadyHasSettings(t *testing.T) {
	cfg := newTestConfig()
	ag, store := newTestBackendWithSettings(t)
	if err := store.Set("cli", "cli_user", "theme", "mono"); err != nil {
		t.Fatalf("store.Set() error = %v", err)
	}
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, ag, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	vals, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(vals) != 1 || vals["theme"] != "mono" {
		t.Fatalf("expected existing settings to remain untouched, got %#v", vals)
	}
}

func TestApplyRuntimeSetting_UpdatesConfig(t *testing.T) {
	cfg := newTestConfig()
	var ag *agent.Agent // nil is fine — we only test cfg mutation
	// LLM fields (llm_model, llm_base_url) are no longer handled by
	// applyRuntimeSetting — they go through update_subscription RPC.
	// Test a non-LLM config mutation instead.
	applyRuntimeSetting(cfg, ag, "cli_user", "max_concurrency", "99")
	if cfg.Agent.MaxConcurrency != 99 {
		t.Fatalf("max_concurrency = %d, want %d", cfg.Agent.MaxConcurrency, 99)
	}
}

func TestAllRuntimeKeysHaveHandlers(t *testing.T) {
	missing := missingHandlerKeys()
	if len(missing) > 0 {
		t.Errorf("settingHandlerRegistry is missing handlers for keys in channel.CLIRuntimeSettingKeys: %v\n"+
			"Add entries to settingHandlerRegistry in setting_handlers.go for each missing key.", missing)
	}
}

func TestApplyRuntimeSetting_WarnsOnUnknownKey(t *testing.T) {
	cfg := newTestConfig()
	var ag *agent.Agent
	applyRuntimeSetting(cfg, ag, "cli_user", "totally_unknown_key", "value")
	// Should not panic, just log a warning
}

func TestHandleCLIRPCSetDefaultSubscriptionRefreshesSenderCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	// Admin's subscriptions are stored under cliSenderID ("cli_user") in production.
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai", BaseURL: "https://gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4.1", IsDefault: true}); err != nil {
		t.Fatalf("add gpt: %v", err)
	}
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-glm", SenderID: "cli_user", Name: "glm", Provider: "openai", BaseURL: "https://glm.example/v1", APIKey: "sk-glm", Model: "glm-5.1", IsDefault: false}); err != nil {
		t.Fatalf("add glm: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)
	_, model, _, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model, got %q", model)
	}

	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_subscription: %v", err)
	}
	_, model, _, _, _ = factory.GetLLM("cli_user")
	if model != "glm-5.1" {
		t.Fatalf("expected switched glm model, got %q", model)
	}
}

// TestHandleCLIRPCSetDefaultSubscription_CrossIdentity verifies that when
// the WS auth identity ("admin") differs from the subscription's business
// senderID ("cli_user"), the LLM factory cache is still updated correctly.
// This was a real bug: the server used senderIDFromParams (→ "admin") as
// the cache key instead of sub.SenderID ("cli_user"), so GetLLM("cli_user")
// kept returning the old client after a subscription switch.
func TestHandleCLIRPCSetDefaultSubscription_CrossIdentity(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	// Subscriptions belong to "cli_user" (business identity)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-gpt", SenderID: "cli_user", Name: "gpt", Provider: "openai", BaseURL: "https://gpt.example/v1", APIKey: "sk-gpt", Model: "gpt-4.1", IsDefault: true}); err != nil {
		t.Fatalf("add gpt: %v", err)
	}
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-glm", SenderID: "cli_user", Name: "glm", Provider: "openai", BaseURL: "https://glm.example/v1", APIKey: "sk-glm", Model: "glm-5.1", IsDefault: false}); err != nil {
		t.Fatalf("add glm: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)
	// Agent calls GetLLM with "cli_user" (business identity)
	_, model, _, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model for cli_user, got %q", model)
	}

	// RPC call with WS auth "admin", no sender_id in params (matches real CLI behavior)
	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("HandleCLIRPC set_default_subscription: %v", err)
	}
	// The key assertion: GetLLM("cli_user") must see the new model
	_, model, _, _, _ = factory.GetLLM("cli_user")
	if model != "glm-5.1" {
		t.Fatalf("expected switched glm model for cli_user, got %q (LLM factory cached under wrong key)", model)
	}
}

// TestHandleCLIRPCGetSessionSubscription verifies the get_session_subscription RPC.
// Tests the fallback path (LLMFactory cache) since MultiSession is not wired in this test.
func TestHandleCLIRPCGetSessionSubscription(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-a", SenderID: "cli_user", Name: "sub-a", Provider: "openai", BaseURL: "https://a.example/v1", APIKey: "sk-a", Model: "gpt-4o", IsDefault: true}); err != nil {
		t.Fatalf("add sub-a: %v", err)
	}

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	chatID := "/home/test/project:Agent-001"

	// Set per-session subscription via set_default_subscription (LLMFactory cache only, no DB)
	params, _ := json.Marshal(map[string]string{"id": "sub-a", "chat_id": chatID})
	if _, err := HandleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("set_default_subscription: %v", err)
	}

	// get_session_subscription uses LLMFactory fallback when no MultiSession
	params, _ = json.Marshal(map[string]string{"chat_id": chatID})
	raw, err := HandleCLIRPC(table, "get_session_subscription", params, "admin")
	if err != nil {
		t.Fatalf("get_session_subscription: %v", err)
	}
	var res map[string]string
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m, ok := res["model"]; !ok || m != "gpt-4o" {
		t.Errorf("expected model 'gpt-4o', got %q", res["model"])
	}
}

// TestHandleCLIRPCGetSessionSubscription_Empty verifies get_session_subscription
// handles sessions with no prior subscription mapping gracefully (returns empty/fallback).
func TestHandleCLIRPCGetSessionSubscription_Empty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	factory.SetSubscriptionSvc(sqlite.NewLLMSubscriptionService(db))

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Query for a session that has never been registered
	params, _ := json.Marshal(map[string]string{"chat_id": "/no/such/session"})
	raw, err := HandleCLIRPC(table, "get_session_subscription", params, "admin")
	if err != nil {
		t.Fatalf("get_session_subscription should not error for unknown session: %v", err)
	}
	// Without MultiSession, the handler falls back to LLMFactory's default model.
	// subscription_id should be empty (no DB mapping), model comes from fallback.
	var res map[string]string
	json.Unmarshal(raw, &res)
	if res["subscription_id"] != "" {
		t.Errorf("subscription_id should be empty for unknown session, got %q", res["subscription_id"])
	}
	// Model from LLMFactory fallback is expected; we just verify subscription_id is empty.
}

// TestSetDefaultSubscription_GlobalSwitch_PreservesPerSession verifies that a global
// subscription switch (chatID="") does NOT destroy other sessions' per-session
// subscriptions. This was a critical cross-session contamination bug:
// the old code used Invalidate() which wiped ALL per-chat entries, causing
// session A's per-session GLM to be lost when session B switched globally to DeepSeek.
func TestSetDefaultSubscription_GlobalSwitch_PreservesPerSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add two subscriptions: GLM and DeepSeek
	addGLM, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "glm", "provider": "openai",
			"base_url": "https://glm.api/v1", "api_key": "sk-glm", "model": "glm-5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addGLM, "admin"); err != nil {
		t.Fatalf("add glm: %v", err)
	}
	addDS, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "deepseek", "provider": "openai",
			"base_url": "https://deepseek.api/v1", "api_key": "sk-ds", "model": "deepseek-v4-pro",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addDS, "admin"); err != nil {
		t.Fatalf("add deepseek: %v", err)
	}

	// Get subscription IDs
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var subs []channel.Subscription
	if err := json.Unmarshal(listRaw, &subs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(subs) < 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	var glmID, dsID string
	for _, s := range subs {
		if s.Model == "glm-5" {
			glmID = s.ID
		}
		if s.Model == "deepseek-v4-pro" {
			dsID = s.ID
		}
	}

	// Step 1: Set per-session GLM for chatA
	setSessParams, _ := json.Marshal(map[string]any{
		"id":      glmID,
		"chat_id": "/home/user/src/proj-a:Agent-001",
	})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setSessParams, "admin"); err != nil {
		t.Fatalf("set per-session GLM: %v", err)
	}

	// Verify: chatA has per-session GLM
	_, modelA, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-a:Agent-001")
	if modelA != "glm-5" {
		t.Fatalf("chatA model after per-session set = %q, want glm-5", modelA)
	}

	// Step 2: Global switch to DeepSeek (chatID="")
	setGlobalParams, _ := json.Marshal(map[string]any{
		"id":      dsID,
		"chat_id": "",
	})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setGlobalParams, "admin"); err != nil {
		t.Fatalf("global switch to deepseek: %v", err)
	}

	// Step 3: Verify: chatA STILL has per-session GLM (must not be wiped)
	_, modelA2, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-a:Agent-001")
	if modelA2 != "glm-5" {
		t.Errorf("chatA model after global switch = %q, want glm-5 (per-session must survive)", modelA2)
	}

	// Step 4: Verify: chatB (no per-session) uses DeepSeek (global default)
	_, modelB, _, _, _ := factory.GetLLMForChat("cli_user", "/home/user/src/proj-b:Agent-002")
	if modelB != "deepseek-v4-pro" {
		t.Errorf("chatB model after global switch = %q, want deepseek-v4-pro (user-level)", modelB)
	}

	// Step 5: Verify: defaultLLM updated to DeepSeek (user-level preference)
	if dm := factory.GetDefaultModel(); dm != "deepseek-v4-pro" {
		t.Errorf("defaultModel after global switch = %q, want deepseek-v4-pro", dm)
	}
}

// TestSetDefaultSubscription_PerSessionSwitch_DoesNotAffectOtherSessions verifies
// that setting a per-session subscription for chatA does not change the model
// used by chatB (which has no per-session override).
func TestSetDefaultSubscription_PerSessionSwitch_DoesNotAffectOtherSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := agent.NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)

	aCfg := &config.Config{}
	ag := &agent.Agent{}
	ag.SetLLMFactory(factory)
	table := BuildRPCTable(aCfg, ag, nil, nil, nil)

	// Add GLM subscription and set as global default
	addGLM, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "glm", "provider": "openai",
			"base_url": "https://glm.api/v1", "api_key": "sk-glm", "model": "glm-5",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addGLM, "admin"); err != nil {
		t.Fatalf("add glm: %v", err)
	}

	// Add DeepSeek subscription
	addDS, _ := json.Marshal(map[string]any{
		"sub": map[string]any{
			"name": "deepseek", "provider": "openai",
			"base_url": "https://deepseek.api/v1", "api_key": "sk-ds", "model": "deepseek-v4-pro",
		},
	})
	if _, err := HandleCLIRPC(table, "add_subscription", addDS, "admin"); err != nil {
		t.Fatalf("add deepseek: %v", err)
	}

	// Get IDs
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := HandleCLIRPC(table, "list_subscriptions", listParams, "admin")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var subs []channel.Subscription
	json.Unmarshal(listRaw, &subs)
	var glmID, dsID string
	for _, s := range subs {
		if s.Model == "glm-5" {
			glmID = s.ID
		}
		if s.Model == "deepseek-v4-pro" {
			dsID = s.ID
		}
	}

	// Set GLM as global default
	setGlobalGLM, _ := json.Marshal(map[string]any{"id": glmID, "chat_id": ""})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setGlobalGLM, "admin"); err != nil {
		t.Fatalf("set global default: %v", err)
	}

	// Set per-session DeepSeek for chatA
	setSessDS, _ := json.Marshal(map[string]any{"id": dsID, "chat_id": "/proj-a:Agent-001"})
	if _, err := HandleCLIRPC(table, "set_default_subscription", setSessDS, "admin"); err != nil {
		t.Fatalf("set per-session deepseek: %v", err)
	}

	// Verify: chatA uses DeepSeek (per-session)
	_, modelA, _, _, _ := factory.GetLLMForChat("cli_user", "/proj-a:Agent-001")
	if modelA != "deepseek-v4-pro" {
		t.Errorf("chatA = %q, want deepseek-v4-pro", modelA)
	}

	// Verify: chatB uses GLM (global default, not affected by chatA's per-session)
	_, modelB, _, _, _ := factory.GetLLMForChat("cli_user", "/proj-b:Agent-002")
	if modelB != "glm-5" {
		t.Errorf("chatB = %q, want glm-5 (global default, not affected by chatA)", modelB)
	}

	// Verify: defaultLLM is DeepSeek (last global SetDefault from handleSwitchLLMDoneMsg)
	// Note: The per-session switch doesn't change global default in this test
	// because we don't call the TUI callback path. The RPC only sets per-chat entry.
	if dm := factory.GetDefaultModel(); dm != "glm-5" {
		t.Errorf("defaultModel = %q, want glm-5 (set by global switch)", dm)
	}
}
