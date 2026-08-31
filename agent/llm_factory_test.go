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
}

func TestGetLLMForModel_EmptyTarget(t *testing.T) {
	// Empty target model → should return default model name without hitting subscription logic
	f := NewLLMFactory(nil, "default-model")
	f.defaultThinkingMode = "auto"

	// Verify the early return path: targetModel="" should not try to list subscriptions
	// (subscriptionSvc is nil, so if it tried, we'd get a different error)
	_, _, model, _, tm, _, usedCustom := f.GetLLMForModel("user1", "")
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

	// No subscriptionSvc + bare model name → no owning subscription can be
	// resolved → deployment-default fallback (defaultModel), NOT the requested
	// bare name (v62: no "any subscription + arbitrary model" hard-tries).
	_, subID, model, _, _, _, usedCustom := f.GetLLMForModel("user1", "claude-opus-4-20250115")
	if model != "default-model" {
		t.Errorf("model = %q, want %q (deployment default; bare names without an owner fall back)", model, "default-model")
	}
	if subID != "" {
		t.Errorf("subID = %q, want empty (deployment defaultLLM has no subscription)", subID)
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

// TestSwitchSubscription_DoesNotTouchDefaultLLM guards the v62 invariant:
// the deployment-level defaultLLM/defaultModel is built once from cfg.LLM at
// NewLLMFactory time and is NEVER re-pointed at a user's subscription —
// one user's model choice must not leak into every other user's fallback
// (the old cli_user sync caused exactly that in multi-user deployments).
// SwitchSubscription is a no-op hook kept for RPC/callback call sites.
func TestSwitchSubscription_DoesNotTouchDefaultLLM(t *testing.T) {
	f := NewLLMFactory(&llm.MockLLM{}, "original-default-model")

	subDeepSeek := &sqlite.LLMSubscription{
		ID: "sub-ds", Provider: "openai", BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-deep",
		Model: "deepseek-v4-pro",
	}

	// Global default before switch
	if dm := f.GetDefaultModel(); dm != "original-default-model" {
		t.Fatalf("initial default model = %q, want original-default-model", dm)
	}

	// Switch subscription for cli_user — must NOT touch defaultLLM/defaultModel.
	if err := f.SwitchSubscription("cli_user", subDeepSeek, ""); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	if dm := f.GetDefaultModel(); dm != "original-default-model" {
		t.Errorf("default model after SwitchSubscription = %q, want original-default-model (deployment fallback is immutable)", dm)
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

// ── getGlobalSetting（operator 坍缩后的全局设置链）─────────────────────────
//
// Multi-user removal: the user_id dimension and userResolver are gone.
// getGlobalSetting reads the sender-dimension row (every UserContext sender
// collapses to "cli_user" — the sender dimension IS the operator dimension)
// with a cli_user fallback for direct caller-site passes. Legacy user-N rows
// (from the removed *ByUserID write path) are merged into the cli_user row
// by the v63 migration.

// newSettingsTestFactory 构造带真实 sqlite settings 的 LLMFactory。
func newSettingsTestFactory(t *testing.T) (*LLMFactory, *SettingsService, *sqlite.UserSettingsService) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)
	f := NewLLMFactory(&llm.MockLLM{}, "default-model")
	f.SetSettingsService(svc)
	return f, svc, store
}

// TestGetGlobalSetting_SenderFallbackChain 守护 operator 坍缩后的全局设置链：
// sender 精确行命中 → cli_user 兜底。多用户删除后所有渠道 sender 的全局设置
// （tier/thinking_mode）都存储在 cli_user 行（v63 迁移归一）。
func TestGetGlobalSetting_SenderFallbackChain(t *testing.T) {
	f, _, store := newSettingsTestFactory(t)
	// operator 行（唯一写入路径：SetSetting(cli_user)）。
	if err := store.Set(thinkingModeChannel, "cli_user", "tier_swift", "sub-cli|model-cli"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// 任意 sender → cli_user 兜底命中（operator 继承语义）。
	if got := f.userTierModel("web-4", "swift"); got != "sub-cli|model-cli" {
		t.Errorf("userTierModel(web-4, swift) = %q, want sub-cli|model-cli (cli_user fallback)", got)
	}
	// operator 自身直接命中。
	if got := f.userTierModel("cli_user", "swift"); got != "sub-cli|model-cli" {
		t.Errorf("userTierModel(cli_user, swift) = %q, want sub-cli|model-cli", got)
	}
}
