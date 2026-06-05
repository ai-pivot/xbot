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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
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
