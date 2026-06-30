package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestGuessProvider(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4-20250514", "anthropic"},
		{"claude-opus-4-20250115", "anthropic"},
		{"gpt-4o", "openai"},
		{"gpt-4.1", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"deepseek-chat", "deepseek"},
		{"deepseek-reasoner", "deepseek"},
		{"gemini-2.0-flash", "google"},
		{"qwen-max", "qwen"},
		{"unknown-model", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := guessProvider(tt.model)
			if got != tt.want {
				t.Errorf("guessProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

// TestLLMSemAcquireForUser_ReadsMaxConcurrencyFromDB verifies that
// LLMSemAcquireForUser correctly reads the max_concurrency setting from
// the user_settings DB via the correct channel, rather than falling back
// to the hardcoded default. Regression test for the bug where the setting
// key was misspelled ("max_concurrent" vs "max_concurrency").
func TestLLMSemAcquireForUser_ReadsMaxConcurrencyFromDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	settingsSvc := NewSettingsService(store)
	// Write max_concurrency=100 into the DB under channel "cli".
	if err := settingsSvc.SetSetting("cli", "test_user", settingMaxConcurrency, "100"); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	// Create LLMFactory with the settings service.
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.SetSettingsService(settingsSvc)
	mgr := llm.NewLLMSemaphoreManager()
	f.SetLLMSemaphoreManager(mgr)

	// Acquire the semaphore and release immediately to verify capacity.
	acquire := f.LLMSemAcquireForUser("test_user", "cli")
	if acquire == nil {
		t.Fatal("LLMSemAcquireForUser returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Acquire N slots to verify capacity is at least 100 (not the default 5).
	releases := make([]func(), 0, 100)
	for i := 0; i < 100; i++ {
		release := acquire(ctx)
		if release == nil {
			t.Fatalf("failed to acquire slot %d (capacity too low, was it %d?)", i, llm.DefaultLLMConcurrency)
		}
		releases = append(releases, release)
	}
	for _, r := range releases {
		r()
	}
}

// TestSubAgentSemAcquireForUser_ReadsMaxConcurrencyFromDB verifies that
// SubAgentSemAcquireForUser correctly reads max_concurrency from the DB.
func TestSubAgentSemAcquireForUser_ReadsMaxConcurrencyFromDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	settingsSvc := NewSettingsService(store)
	if err := settingsSvc.SetSetting("cli", "test_user", settingMaxConcurrency, "50"); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.SetSettingsService(settingsSvc)
	mgr := llm.NewLLMSemaphoreManager()
	f.SetLLMSemaphoreManager(mgr)

	acquire := f.SubAgentSemAcquireForUser("test_user", "cli")
	if acquire == nil {
		t.Fatal("SubAgentSemAcquireForUser returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	releases := make([]func(), 0, 50)
	for i := 0; i < 50; i++ {
		release := acquire(ctx)
		if release == nil {
			t.Fatalf("failed to acquire subagent slot %d (capacity too low, was it %d?)", i, llm.DefaultLLMConcurrency)
		}
		releases = append(releases, release)
	}
	for _, r := range releases {
		r()
	}
}

// TestSettingKeyConstants_MatchDB verifies that the setting key constants
// used in LLMFactory match the canonical keys stored in user_settings DB.
func TestSettingKeyConstants_MatchDB(t *testing.T) {
	// These constants must match the keys written by settings panel.
	if settingMaxConcurrency != "max_concurrency" {
		t.Errorf("settingMaxConcurrency = %q, want %q", settingMaxConcurrency, "max_concurrency")
	}
	if settingSubAgentMaxConcurrency != "subagent_max_concurrency" {
		t.Errorf("settingSubAgentMaxConcurrency = %q, want %q", settingSubAgentMaxConcurrency, "subagent_max_concurrency")
	}
	if settingLLMMaxConcurrentPersonal != "llm_max_concurrent_personal" {
		t.Errorf("settingLLMMaxConcurrentPersonal = %q, want %q", settingLLMMaxConcurrentPersonal, "llm_max_concurrent_personal")
	}
}

func TestGetLLMForModel_EmptyTarget(t *testing.T) {
	// Empty target model → should return default model name without hitting subscription logic
	f := NewLLMFactory(nil, "default-model")
	f.defaultThinkingMode = "auto"

	// Verify the early return path: targetModel="" should not try to list subscriptions
	// (subscriptionSvc is nil, so if it tried, we'd get a different error)
	_, model, _, tm, _, usedCustom := f.GetLLMForModel("user1", "")
	if model != "default-model" {
		t.Errorf("model = %q, want %q", model, "default-model")
	}
	if usedCustom {
		t.Error("usedCustom should be false for empty target model")
	}
	if tm != "auto" {
		t.Errorf("thinkingMode = %q, want %q", tm, "auto")
	}
}

func TestGetLLMForModel_NilSubscriptionSvc(t *testing.T) {
	f := NewLLMFactory(nil, "default-model")
	f.defaultThinkingMode = "auto"

	// No subscriptionSvc + explicit model → model not found in any subscription,
	// fallback to default client with the RESOLVED model (not the default model).
	_, model, _, _, _, usedCustom := f.GetLLMForModel("user1", "claude-opus-4-20250115")
	if model != "claude-opus-4-20250115" {
		t.Errorf("model = %q, want claude-opus-4-20250115 (resolved model preserved in fallback)", model)
	}
	if usedCustom {
		t.Error("usedCustom should be false when model not found in any subscription")
	}
}

func TestNormalizeModelTier(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"vanguard", "vanguard"},
		{"VANGUARD", "vanguard"},
		{"Vanguard", "vanguard"},
		{"strong", "vanguard"},
		{"Strong", "vanguard"},
		{"balance", "balance"},
		{"medium", "balance"},
		{"swift", "swift"},
		{"weak", "swift"},
		{"gpt-4o", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeModelTier(tt.input)
			if got != tt.want {
				t.Errorf("normalizeModelTier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasCustomLLMChecksSubscriptionSvc(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := NewLLMFactory(&llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-1", SenderID: "cli_user", Name: "s1", Provider: "openai", BaseURL: "https://example.com/v1", APIKey: "sk-test", Model: "m1", IsDefault: true}); err != nil {
		t.Fatalf("add sub: %v", err)
	}
	if !factory.HasCustomLLM("cli_user") {
		t.Fatal("expected HasCustomLLM to return true when default subscription exists")
	}
}

// TestInvalidate_ClearsPerChatCache verifies that Invalidate(senderID) clears
// both user-level and per-chat (senderID:chatID) cache entries.
// This is the fix for: switching sub then changing model in settings was stuck
// on the old model because Invalidate only cleared the user-level key.
func TestInvalidate_ClearsPerChatCache(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")

	senderID := "cli_user"
	chatID := "/home/user/project"
	subA := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-a.com/v1", APIKey: "sk-a",
		Model: "gpt-4o", MaxOutputTokens: 8192,
	}
	subB := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-b.com/v1", APIKey: "sk-b",
		Model: "deepseek-v3", MaxOutputTokens: 4096,
	}

	// Simulate: SwitchSubscription creates both user-level and per-chat caches
	if err := f.SwitchSubscription(senderID, subA, chatID); err != nil {
		t.Fatalf("SwitchSubscription subA: %v", err)
	}

	// Verify both caches exist
	_, modelA, _, _, _ := f.GetLLMForChat(senderID, chatID)
	if modelA != "gpt-4o" {
		t.Fatalf("initial model = %q, want gpt-4o", modelA)
	}

	// Simulate: set_default_subscription calls Invalidate then SwitchSubscription
	// (the actual server handler path for subscription switching)
	f.Invalidate(senderID)
	if err := f.SwitchSubscription(senderID, subB, chatID); err != nil {
		t.Fatalf("SwitchSubscription subB: %v", err)
	}

	_, modelB, _, _, _ := f.GetLLMForChat(senderID, chatID)
	if modelB != "deepseek-v3" {
		t.Errorf("after sub switch, model = %q, want deepseek-v3", modelB)
	}

	// Simulate: update_subscription (settings panel) calls Invalidate + SwitchSubscription
	// with chatID="" — per-chat cache was NOT cleared before the fix
	f.Invalidate(senderID)
	updatedSubB := *subB
	updatedSubB.Model = "deepseek-r1"
	updatedSubB.MaxOutputTokens = 16384
	if err := f.SwitchSubscription(senderID, &updatedSubB, ""); err != nil {
		t.Fatalf("SwitchSubscription updatedSubB: %v", err)
	}

	// GetLLMForChat should NOT return stale per-chat cache
	_, modelUpdated, _, thinkingUpdated, _ := f.GetLLMForChat(senderID, chatID)
	if modelUpdated != "deepseek-r1" {
		t.Errorf("after settings update, model = %q, want deepseek-r1 (stale per-chat cache bug)", modelUpdated)
	}
	// Verify thinking mode is also not stale
	if thinkingUpdated != "" {
		t.Errorf("after settings update, thinkingMode = %q, want empty", thinkingUpdated)
	}
}

// TestSwitchModel_ClearsPerChatCache verifies that SwitchModel clears per-chat
// model caches so GetLLMForChat returns the new model instead of a stale
// per-chat entry.
func TestSwitchModel_ClearsPerChatCache(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")

	senderID := "cli_user"
	chatID := "/home/user/project"
	sub := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.example.com/v1", APIKey: "sk-test",
		Model: "gpt-4o", MaxOutputTokens: 8192,
	}

	// Create per-chat cache via SwitchSubscription
	if err := f.SwitchSubscription(senderID, sub, chatID); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	// Now SwitchModel (e.g., from quick panel model switch)
	f.SwitchModel(senderID, "gpt-4o-mini")

	// GetLLMForChat should return the new model, not the stale per-chat one
	_, model, _, _, _ := f.GetLLMForChat(senderID, chatID)
	if model != "gpt-4o-mini" {
		t.Errorf("after SwitchModel, per-chat model = %q, want gpt-4o-mini (stale per-chat cache bug)", model)
	}
}

// TestInvalidate_DoesNotAffectOtherUsers verifies that Invalidate(senderID) only
// clears entries for that specific sender, not other users.
func TestInvalidate_DoesNotAffectOtherUsers(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")

	subA := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-a.com/v1", APIKey: "sk-a",
		Model: "gpt-4o",
	}
	subB := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-b.com/v1", APIKey: "sk-b",
		Model: "claude-3-opus",
	}

	// User A gets per-chat cache
	if err := f.SwitchSubscription("userA", subA, "/home/a"); err != nil {
		t.Fatalf("SwitchSubscription userA: %v", err)
	}
	// User B gets per-chat cache
	if err := f.SwitchSubscription("userB", subB, "/home/b"); err != nil {
		t.Fatalf("SwitchSubscription userB: %v", err)
	}

	// Invalidate user A — should NOT affect user B
	f.Invalidate("userA")

	// User B's per-chat cache should still work
	_, modelB, _, _, _ := f.GetLLMForChat("userB", "/home/b")
	if modelB != "claude-3-opus" {
		t.Errorf("userB model after Invalidate(userA) = %q, want claude-3-opus", modelB)
	}
}

// TestSwitchSubscription_UpdatesDefaultLLM verifies that SwitchSubscription
// DOES update the global defaultLLM/defaultModel for cli_user. In CLI mode,
// all sessions share senderID "cli_user", so defaultLLM is a user-level
// preference that should follow the user's last subscription choice.
// SubAgent fallback, ListModels(), and GetLLM() for sessions without
// per-session subscriptions should all see the new default.
func TestSwitchSubscription_UpdatesDefaultLLM(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "original-default-model")

	subDeepSeek := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-deep",
		Model: "deepseek-v4-pro",
	}

	// Global default before switch
	if dm := f.GetDefaultModel(); dm != "original-default-model" {
		t.Fatalf("initial default model = %q, want original-default-model", dm)
	}

	// Switch subscription for cli_user (the common CLI senderID)
	if err := f.SwitchSubscription("cli_user", subDeepSeek, ""); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	// Global default SHOULD be updated (user-level preference)
	if dm := f.GetDefaultModel(); dm != "deepseek-v4-pro" {
		t.Errorf("default model after SwitchSubscription = %q, want deepseek-v4-pro", dm)
	}

	// User-level entry should also be updated
	_, model, _, _, _ := f.GetLLM("cli_user")
	if model != "deepseek-v4-pro" {
		t.Errorf("cli_user model = %q, want deepseek-v4-pro", model)
	}
}

// TestGlobalSwitch_PreservesPerSessionEntries verifies that when a global subscription
// switch happens (via InvalidateSender + SwitchSubscription), per-session entries
// belonging to other chatIDs are NOT cleared. This prevents cross-session contamination:
// session A switching to DeepSeek must not destroy session B's per-session GLM override.
func TestGlobalSwitch_PreservesPerSessionEntries(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	senderID := "cli_user" // all CLI sessions share this senderID

	subGLM := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.glm.com/v1", APIKey: "sk-glm",
		Model: "glm-5", PerModelConfigs: map[string]sqlite.PerModelConfig{
			"glm-5": {MaxContext: 200000},
		},
	}
	subDeepSeek := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-deep",
		Model: "deepseek-v4-pro", PerModelConfigs: map[string]sqlite.PerModelConfig{
			"deepseek-v4-pro": {MaxContext: 1000000},
		},
	}

	chatA := "/home/user/src/project-a:Agent-001"
	chatB := "/home/user/src/project-b:Agent-002"

	// Session A gets per-session GLM
	if err := f.SetSessionLLM(senderID, chatA, subGLM); err != nil {
		t.Fatalf("SetSessionLLM A: %v", err)
	}

	// Session B has no per-session entry, uses user-level default

	// User-level default is DeepSeek (e.g. global switch)
	f.InvalidateSender(senderID)
	if err := f.SwitchSubscription(senderID, subDeepSeek, ""); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	// Session A should STILL use GLM (per-session preserved)
	_, modelA, maxCtxA, _, _ := f.GetLLMForChat(senderID, chatA)
	if modelA != "glm-5" {
		t.Errorf("session A model = %q, want glm-5 (per-session must survive global switch)", modelA)
	}
	if maxCtxA != 200000 {
		t.Errorf("session A maxCtx = %d, want 200000 (GLM per-model config)", maxCtxA)
	}

	// Session B should use DeepSeek (from user-level, no per-session override)
	_, modelB, maxCtxB, _, _ := f.GetLLMForChat(senderID, chatB)
	if modelB != "deepseek-v4-pro" {
		t.Errorf("session B model = %q, want deepseek-v4-pro (user-level default)", modelB)
	}
	if maxCtxB != 1000000 {
		t.Errorf("session B maxCtx = %d, want 1000000 (DeepSeek per-model config)", maxCtxB)
	}
}

// TestInvalidate_ClearsPerSessionEntries verifies that Invalidate (full) DOES clear
// per-session entries. This is the old behavior that caused cross-session contamination
// and should NOT be used for global subscription switches.
func TestInvalidate_ClearsPerSessionEntries(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	senderID := "cli_user"

	subGLM := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.glm.com/v1", APIKey: "sk-glm",
		Model: "glm-5",
	}
	subDeepSeek := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-deep",
		Model: "deepseek-v4-pro",
	}

	chatA := "/home/user/src/project-a:Agent-001"

	// Session A gets per-session GLM
	if err := f.SetSessionLLM(senderID, chatA, subGLM); err != nil {
		t.Fatalf("SetSessionLLM: %v", err)
	}

	// Verify A has GLM
	_, modelA, _, _, _ := f.GetLLMForChat(senderID, chatA)
	if modelA != "glm-5" {
		t.Fatalf("session A model before Invalidate = %q, want glm-5", modelA)
	}

	// Full Invalidate (old behavior) destroys per-session entry
	f.Invalidate(senderID)
	if err := f.SwitchSubscription(senderID, subDeepSeek, ""); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	// Session A now falls back to user-level DeepSeek (per-session was wiped)
	_, modelA2, _, _, _ := f.GetLLMForChat(senderID, chatA)
	if modelA2 != "deepseek-v4-pro" {
		t.Errorf("session A model after Invalidate = %q, want deepseek-v4-pro (per-session was cleared)", modelA2)
	}
}

// TestGetLLMForModel_ConfigSubExactMatch verifies the config.json subscription path:
// when configSubsFn returns a subscription whose Model matches the resolved model,
// GetLLMForModel should use that subscription (usedCustom=true).
func TestGetLLMForModel_ConfigSubExactMatch(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	// Set up configSubsFn with a matching subscription
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-1",
				Name:     "test-sub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
		}
	})

	client, model, _, _, _, usedCustom := f.GetLLMForModel("user1", "gpt-4o")
	if !usedCustom {
		t.Error("usedCustom should be true when config sub matches resolved model")
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q, want %q", model, "gpt-4o")
	}
	if client == nil {
		t.Error("client should not be nil when config sub matches")
	}
}

// TestGetLLMForModel_ConfigSubNoMatch verifies that when configSubsFn returns
// subscriptions with different Model fields, it still tries to use them with
// the model name (OpenAI-compatible endpoints can serve any model).
func TestGetLLMForModel_ConfigSubNoMatch(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	// Config sub has a different model — but still usable with gpt-4o
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-1",
				Name:     "other-sub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-test",
				Model:    "other-model",
			},
		}
	})

	client, model, _, _, _, usedCustom := f.GetLLMForModel("user1", "gpt-4o")
	if !usedCustom {
		t.Error("usedCustom should be true when config sub can serve the resolved model")
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q, want %q (resolved model)", model, "gpt-4o")
	}
	if client == nil {
		t.Error("client should not be nil")
	}
}

// TestGetLLMForModel_ConfigSubSkipsEmptyCredentials verifies that config
// subscriptions with matching Model but empty BaseURL or APIKey are skipped,
// and the function falls through to the default LLM.
func TestGetLLMForModel_ConfigSubSkipsEmptyCredentials(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	tests := []struct {
		name string
		sub  config.SubscriptionConfig
	}{
		{
			name: "empty BaseURL",
			sub: config.SubscriptionConfig{
				ID:       "sub-empty-url",
				Name:     "no-url",
				Provider: "openai",
				BaseURL:  "",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
		},
		{
			name: "empty APIKey",
			sub: config.SubscriptionConfig{
				ID:       "sub-empty-key",
				Name:     "no-key",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "",
				Model:    "gpt-4o",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.SetConfigSubs(func() []config.SubscriptionConfig {
				return []config.SubscriptionConfig{tt.sub}
			})

			_, model, _, _, _, usedCustom := f.GetLLMForModel("user1", "gpt-4o")
			if usedCustom {
				t.Error("usedCustom should be false when config sub has empty credentials")
			}
			if model != "gpt-4o" {
				t.Errorf("model = %q, want %q (resolved model preserved in fallback)", model, "gpt-4o")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for buildModelSubscriptionMap & configSubToLLMSubscription
// ---------------------------------------------------------------------------

// TestBuildModelSubscriptionMap_ConfigSubs verifies that config subscriptions
// with different models each produce an entry in the model→subscription map.
func TestBuildModelSubscriptionMap_ConfigSubs(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")

	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-a",
				Name:     "OpenAI",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "sk-openai",
				Model:    "gpt-4o",
			},
			{
				ID:       "sub-b",
				Name:     "Anthropic",
				Provider: "anthropic",
				BaseURL:  "https://api.anthropic.com",
				APIKey:   "sk-ant-key",
				Model:    "claude-sonnet-4-20250514",
			},
		}
	})

	m := f.buildModelSubscriptionMap("user1")

	if len(m) != 2 {
		t.Fatalf("map size = %d, want 2", len(m))
	}
	if sub, ok := m["gpt-4o"]; !ok {
		t.Error("missing gpt-4o entry")
	} else if sub.ID != "sub-a" {
		t.Errorf("gpt-4o mapped to sub %q, want sub-a", sub.ID)
	}
	if sub, ok := m["claude-sonnet-4-20250514"]; !ok {
		t.Error("missing claude-sonnet-4-20250514 entry")
	} else if sub.ID != "sub-b" {
		t.Errorf("claude-sonnet-4-20250514 mapped to sub %q, want sub-b", sub.ID)
	}
}

// TestBuildModelSubscriptionMap_ConfigSubsSkipsEmptyCredentials verifies that
// config subscriptions with empty BaseURL or APIKey are not added to the map.
func TestBuildModelSubscriptionMap_ConfigSubsSkipsEmptyCredentials(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")

	// Sub with matching Model but empty BaseURL — must be skipped.
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-empty-url",
				Name:     "No URL",
				Provider: "openai",
				BaseURL:  "",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
			{
				ID:       "sub-empty-key",
				Name:     "No Key",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "",
				Model:    "gpt-4o-mini",
			},
		}
	})

	m := f.buildModelSubscriptionMap("user1")

	if len(m) != 0 {
		t.Fatalf("map size = %d, want 0 (both subs have empty credentials)", len(m))
	}
}

// TestBuildModelSubscriptionMap_EmptySenderID verifies that with an empty
// senderID and nil subscriptionSvc, only config subs are included.
func TestBuildModelSubscriptionMap_EmptySenderID(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	// subscriptionSvc is nil by default — no DB path at all.

	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "cfg-1",
				Name:     "ConfigSub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-cfg",
				Model:    "gpt-4o",
			},
		}
	})

	// Empty senderID — DB path is also gated by senderID != ""
	m := f.buildModelSubscriptionMap("")

	if len(m) != 1 {
		t.Fatalf("map size = %d, want 1 (only config sub)", len(m))
	}
	if _, ok := m["gpt-4o"]; !ok {
		t.Error("missing gpt-4o entry from config sub")
	}
}

// TestConfigSubToLLMSubscription verifies that configSubToLLMSubscription
// correctly maps every field from SubscriptionConfig to LLMSubscription.
func TestConfigSubToLLMSubscription(t *testing.T) {
	cs := config.SubscriptionConfig{
		ID:              "sub-42",
		Name:            "My Sub",
		Provider:        "deepseek",
		BaseURL:         "https://api.deepseek.com/v1",
		APIKey:          "sk-deep",
		Model:           "deepseek-chat",
		MaxOutputTokens: 4096,
		ThinkingMode:    "enabled",
	}

	sub := configSubToLLMSubscription(cs)

	if sub.ID != "sub-42" {
		t.Errorf("ID = %q, want %q", sub.ID, "sub-42")
	}
	if sub.Name != "My Sub" {
		t.Errorf("Name = %q, want %q", sub.Name, "My Sub")
	}
	if sub.Provider != "deepseek" {
		t.Errorf("Provider = %q, want %q", sub.Provider, "deepseek")
	}
	if sub.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want %q", sub.BaseURL, "https://api.deepseek.com/v1")
	}
	if sub.APIKey != "sk-deep" {
		t.Errorf("APIKey = %q, want %q", sub.APIKey, "sk-deep")
	}
	if sub.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q", sub.Model, "deepseek-chat")
	}
	if sub.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", sub.MaxOutputTokens)
	}
	if sub.ThinkingMode != "enabled" {
		t.Errorf("ThinkingMode = %q, want %q", sub.ThinkingMode, "enabled")
	}
}

// --- chatKey tests ---

func TestChatKey(t *testing.T) {
	tests := []struct {
		name     string
		senderID string
		chatID   string
		want     string
	}{
		{"normal", "user123", "chat456", "user123:chat456"},
		{"empty senderID", "", "chat456", ":chat456"},
		{"empty chatID", "user123", "", "user123:"},
		{"both empty", "", "", ":"},
		{"colons in values", "user:1", "chat:2", "user:1:chat:2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chatKey(tt.senderID, tt.chatID)
			if got != tt.want {
				t.Errorf("chatKey(%q, %q) = %q, want %q", tt.senderID, tt.chatID, got, tt.want)
			}
		})
	}
}

// --- parseOrDefault tests ---

func TestParseOrDefault(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultVal int
		want       int
	}{
		{"empty string returns default", "", 42, 42},
		{"valid positive int", "100", 42, 100},
		{"zero returns default", "0", 42, 42},
		{"negative returns default", "-5", 42, 42},
		{"non-numeric returns default", "abc", 42, 42},
		{"whitespace-padded number", "  7", 42, 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOrDefault(tt.input, tt.defaultVal)
			if got != tt.want {
				t.Errorf("parseOrDefault(%q, %d) = %d, want %d", tt.input, tt.defaultVal, got, tt.want)
			}
		})
	}
}
