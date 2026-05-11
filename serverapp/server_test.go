package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"xbot/agent"
	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
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
	lb := fakeBackend{factory: factory}
	table := buildRPCTable(aCfg, lb, nil, nil)

	// Add subscription via admin path (same as remote CLI does)
	sub := channel.Subscription{
		Name: "test", Provider: "openai",
		BaseURL: "https://api.openai.com/v1", APIKey: "sk-test", Model: "gpt-4",
	}
	addParams, _ := json.Marshal(map[string]any{"sub": sub})
	if _, err := handleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List with empty senderID (simulates openQuickSwitch behavior)
	// Before fix: senderIDFromParams falls back to "admin" → empty list
	// After fix: should return the subscription
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := handleCLIRPC(table, "list_subscriptions", listParams, "admin")
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
	lb := fakeBackend{factory: factory}
	table := buildRPCTable(aCfg, lb, nil, nil)

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
	if _, err := handleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// List and verify base_url/api_key are preserved
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	raw, err := handleCLIRPC(table, "list_subscriptions", listParams, "admin")
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
	lb := fakeBackend{factory: factory}
	table := buildRPCTable(aCfg, lb, nil, nil)

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
	if _, err := handleCLIRPC(table, "add_subscription", addParams, "admin"); err != nil {
		t.Fatalf("add_subscription: %v", err)
	}

	// Get the subscription ID via list
	listParams, _ := json.Marshal(map[string]string{"sender_id": ""})
	listRaw, err := handleCLIRPC(table, "list_subscriptions", listParams, "admin")
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
	if _, err := handleCLIRPC(table, "update_subscription", updateParams, "admin"); err != nil {
		t.Fatalf("update_subscription: %v", err)
	}

	// Verify base_url and api_key are preserved
	listRaw2, err := handleCLIRPC(table, "list_subscriptions", listParams, "admin")
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

func newTestBackendWithSettings(t *testing.T) (agent.AgentBackend, *sqlite.UserSettingsService) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewUserSettingsService(db)
	agentSvc := agent.NewSettingsService(store)
	return fakeBackend{settingsSvc: agentSvc}, store
}

type fakeBackend struct {
	settingsSvc *agent.SettingsService
	factory     *agent.LLMFactory
}

// Compile-time check: fakeBackend implements agent.AgentBackend.
var _ agent.AgentBackend = fakeBackend{}

func (b fakeBackend) Start(_ context.Context) error          { return nil }
func (b fakeBackend) Stop()                                  {}
func (b fakeBackend) SendInbound(_ bus.InboundMessage) error { return nil }
func (b fakeBackend) Subscribe(_ protocol.EventPattern, _ protocol.EventHandler) func() {
	return func() {}
}
func (b fakeBackend) Bus() *bus.MessageBus                                      { return nil }
func (b fakeBackend) IsRemote() bool                                            { return false }
func (b fakeBackend) IsProcessing(_, _ string) bool                             { return false }
func (b fakeBackend) GetActiveProgress(_, _ string) *channel.CLIProgressPayload { return nil }
func (b fakeBackend) GetTodos(_, _ string) []channel.CLITodoItem                { return nil }
func (b fakeBackend) SetTUIControlHandler(_ func(action string, params map[string]string) (map[string]string, error)) {
}
func (b fakeBackend) ConnState() string                                                  { return "connected" }
func (b fakeBackend) ServerURL() string                                                  { return "" }
func (b fakeBackend) Agent() *agent.Agent                                                { return nil }
func (b fakeBackend) LLMFactory() *agent.LLMFactory                                      { return b.factory }
func (b fakeBackend) SettingsService() *agent.SettingsService                            { return b.settingsSvc }
func (b fakeBackend) MultiSession() *session.MultiTenantSession                          { return nil }
func (b fakeBackend) BgTaskManager() *tools.BackgroundTaskManager                        { return nil }
func (b fakeBackend) HookManager() *hooks.Manager                                        { return nil }
func (b fakeBackend) ApprovalState() *hooks.ApprovalState                                { return nil }
func (b fakeBackend) BindChat(_ string) error                                            { return nil }
func (b fakeBackend) SetDirectSend(_ func(bus.OutboundMessage) (string, error))          {}
func (b fakeBackend) SetChannelFinder(_ func(string) (channel.Channel, bool))            {}
func (b fakeBackend) SetChannelPromptProviders(_ ...agent.ChannelPromptProvider)         {}
func (b fakeBackend) RegisterCoreTool(_ tools.Tool)                                      {}
func (b fakeBackend) IndexGlobalTools()                                                  {}
func (b fakeBackend) CountInteractiveSessions(_, _ string) int                           { return 0 }
func (b fakeBackend) ListInteractiveSessions(_, _ string) []agent.InteractiveSessionInfo { return nil }
func (b fakeBackend) InspectInteractiveSession(_ context.Context, _, _, _, _ string, _ int) (string, error) {
	return "", nil
}
func (b fakeBackend) GetSessionMessages(_, _, _, _ string) ([]agent.SessionMessage, bool) {
	return nil, false
}
func (b fakeBackend) GetAgentSessionDump(_, _, _, _ string) (*agent.AgentSessionDump, bool) {
	return nil, false
}
func (b fakeBackend) GetAgentSessionDumpByFullKey(_ string) (*agent.AgentSessionDump, bool) {
	return nil, false
}
func (b fakeBackend) SetContextMode(_ string) error                                  { return nil }
func (b fakeBackend) SetCWD(_, _, _ string) error                                    { return nil }
func (b fakeBackend) SetMaxIterations(_ int)                                         {}
func (b fakeBackend) SetMaxConcurrency(_ int)                                        {}
func (b fakeBackend) SetMaxContextTokens(_ int)                                      {}
func (b fakeBackend) SetCompressionThreshold(_ float64)                              {}
func (b fakeBackend) SetSandbox(_ tools.Sandbox, _ string)                           {}
func (b fakeBackend) GetCardBuilder() *tools.CardBuilder                             { return nil }
func (b fakeBackend) SetEventRouter(_ *event.Router)                                 {}
func (b fakeBackend) RegisterTool(_ tools.Tool)                                      {}
func (b fakeBackend) RegistryManager() *agent.RegistryManager                        { return nil }
func (b fakeBackend) SetProxyLLM(_ string, _ *llm.ProxyLLM, _ string)                {}
func (b fakeBackend) ClearProxyLLM(_ string)                                         {}
func (b fakeBackend) GetDefaultModel() string                                        { return "" }
func (b fakeBackend) SetUserModel(_, _ string) error                                 { return nil }
func (b fakeBackend) SwitchModel(_, _ string) error                                  { return nil }
func (b fakeBackend) GetUserMaxContext(_ string) int                                 { return 0 }
func (b fakeBackend) SetUserMaxContext(_ string, _ int) error                        { return nil }
func (b fakeBackend) GetUserMaxOutputTokens(_ string) int                            { return 0 }
func (b fakeBackend) SetUserMaxOutputTokens(_ string, _ int) error                   { return nil }
func (b fakeBackend) GetUserThinkingMode(_ string) string                            { return "" }
func (b fakeBackend) SetUserThinkingMode(_, _ string) error                          { return nil }
func (b fakeBackend) ListModels() []string                                           { return nil }
func (b fakeBackend) ListAllModels() []string                                        { return nil }
func (b fakeBackend) GetSettings(_, _ string) (map[string]string, error)             { return nil, nil }
func (b fakeBackend) SetSetting(_, _, _, _ string) error                             { return nil }
func (b fakeBackend) ListSubscriptions(_ string) ([]channel.Subscription, error)     { return nil, nil }
func (b fakeBackend) GetDefaultSubscription(_ string) (*channel.Subscription, error) { return nil, nil }
func (b fakeBackend) AddSubscription(_ string, _ channel.Subscription) error         { return nil }
func (b fakeBackend) RemoveSubscription(_ string) error                              { return nil }
func (b fakeBackend) SetDefaultSubscription(_ string, _ string) error                { return nil }
func (b fakeBackend) RenameSubscription(_, _ string) error                           { return nil }
func (b fakeBackend) UpdateSubscription(_ string, _ channel.Subscription) error      { return nil }
func (b fakeBackend) SetSubscriptionModel(_, _ string) error                         { return nil }
func (b fakeBackend) LLMGenerate(_ context.Context, _, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	return nil, nil
}
func (b fakeBackend) LLMModels(_ context.Context, _ string) ([]string, error)            { return nil, nil }
func (b fakeBackend) SetModelTiers(_ config.LLMConfig) error                             { return nil }
func (b fakeBackend) SetDefaultThinkingMode(_ string) error                              { return nil }
func (b fakeBackend) ClearMemory(_ context.Context, _, _, _, _ string) error             { return nil }
func (b fakeBackend) GetMemoryStats(_ context.Context, _, _, _ string) map[string]string { return nil }
func (b fakeBackend) GetUserTokenUsage(_ string) (map[string]any, error)                 { return nil, nil }
func (b fakeBackend) GetDailyTokenUsage(_ string, _ int) ([]map[string]any, error)       { return nil, nil }
func (b fakeBackend) GetBgTaskCount(_ string) int                                        { return 0 }
func (b fakeBackend) ListBgTasks(_ string) ([]agent.BgTaskJSON, error)                   { return nil, nil }
func (b fakeBackend) KillBgTask(_ string) error                                          { return nil }
func (b fakeBackend) CleanupCompletedBgTasks(_ string)                                   {}
func (b fakeBackend) ListTenants() ([]agent.TenantInfo, error)                           { return nil, nil }
func (b fakeBackend) GetHistory(_, _ string) ([]channel.HistoryMessage, error)           { return nil, nil }
func (b fakeBackend) GetTokenState(_, _ string) (int64, int64, error)                    { return 0, 0, nil }
func (b fakeBackend) TrimHistory(_, _ string, _ time.Time) error                         { return nil }
func (b fakeBackend) ResetTokenState()                                                   {}
func (b fakeBackend) GetChannelConfigs() (map[string]map[string]string, error)           { return nil, nil }
func (b fakeBackend) SetChannelConfig(channel string, values map[string]string) error    { return nil }
func (b fakeBackend) SetChannelReconfigureFn(func(string))                               {}
func (b fakeBackend) Close() error                                                       { return nil }
func (b fakeBackend) CallRPC(string, any) (json.RawMessage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (b fakeBackend) Run(_ context.Context) error             { return nil }
func (b fakeBackend) GetLLMConcurrency(_ string) int          { return 0 }
func (b fakeBackend) SetLLMConcurrency(_ string, _ int) error { return nil }
func (b fakeBackend) SetTUICallbacks(_ func(action string, params map[string]string) (map[string]string, error), _ func(key string) (string, error), _ func(key, value string) (string, error)) {
}
func (b fakeBackend) OnTUIControlRequest(_ func(action string, params map[string]string) (map[string]string, error)) {
}
func (b fakeBackend) GetContextMode() string               { return "" }
func (b fakeBackend) PluginManager() *plugin.PluginManager { return nil }

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SeedsOnlyWhenEmpty(t *testing.T) {
	cfg := newTestConfig()
	backend, store := newTestBackendWithSettings(t)
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, "cli", "cli_user"); err != nil {
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
	backend, store := newTestBackendWithSettings(t)
	if err := store.Set("cli", "cli_user", "theme", "mono"); err != nil {
		t.Fatalf("store.Set() error = %v", err)
	}
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, "cli", "cli_user"); err != nil {
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
	var backend agent.AgentBackend // nil is fine — we only test cfg mutation
	// LLM fields (llm_model, llm_base_url) are no longer handled by
	// applyRuntimeSetting — they go through update_subscription RPC.
	// Test a non-LLM config mutation instead.
	applyRuntimeSetting(cfg, backend, "cli_user", "max_concurrency", "99")
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
	var backend agent.AgentBackend
	applyRuntimeSetting(cfg, backend, "cli_user", "totally_unknown_key", "value")
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
	lb := fakeBackend{factory: factory}
	table := buildRPCTable(aCfg, lb, nil, nil)
	_, model, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model, got %q", model)
	}

	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := handleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("handleCLIRPC set_default_subscription: %v", err)
	}
	_, model, _, _ = factory.GetLLM("cli_user")
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
	lb := fakeBackend{factory: factory}
	table := buildRPCTable(aCfg, lb, nil, nil)
	// Agent calls GetLLM with "cli_user" (business identity)
	_, model, _, _ := factory.GetLLM("cli_user")
	if model != "gpt-4.1" {
		t.Fatalf("expected initial gpt model for cli_user, got %q", model)
	}

	// RPC call with WS auth "admin", no sender_id in params (matches real CLI behavior)
	params, _ := json.Marshal(map[string]string{"id": "sub-glm"})
	if _, err := handleCLIRPC(table, "set_default_subscription", params, "admin"); err != nil {
		t.Fatalf("handleCLIRPC set_default_subscription: %v", err)
	}
	// The key assertion: GetLLM("cli_user") must see the new model
	_, model, _, _ = factory.GetLLM("cli_user")
	if model != "glm-5.1" {
		t.Fatalf("expected switched glm model for cli_user, got %q (LLM factory cached under wrong key)", model)
	}
}
