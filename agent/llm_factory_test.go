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

// ── getGlobalSetting 三层链（canonical user 维度修复）─────────────────────────
//
// 复现 bug：tier 配置由 RPC 设置面板经 SetByUserID 写入（行 sender_id='user-N'、
// user_id=N），而 getGlobalSetting 旧链只查 (channel, senderID) + 'cli_user'
// fallback——user 维度行永远读不到。web 渠道 sender（如 web-4）spawn SubAgent
// 时 resolveTierModel 拿不到 tier 配置 → "model not found for SubAgent,
// falling back to main model (model=vanguard)"（vanguard tier 前端明明已配置）。

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

// TestGetGlobalSetting_UserDimensionRead 守护 canonical user 维度读取：
// *ByUserID 写入的行必须能被任意身份（sender）经 userResolver 解析后读到。
// 含旧链两跳 miss 的对照断言（sender 精确 + cli_user fallback 都读不到 user
// 维度行——证明读取必须走 user 维度）。
func TestGetGlobalSetting_UserDimensionRead(t *testing.T) {
	f, svc, _ := newSettingsTestFactory(t)
	f.SetUserResolver(func(senderID string) (int64, bool) {
		if senderID == "web-4" {
			return 1, true
		}
		return 0, false
	})
	// 写入：RPC 设置面板路径（SetByUserID → 行 sender_id='user-1', user_id=1）。
	if err := svc.SetByUserID(thinkingModeChannel, 1, "tier_vanguard", "sub-123|glm-5.3"); err != nil {
		t.Fatalf("SetByUserID: %v", err)
	}
	// 对照：旧 sender 维度链两跳都读不到 user 维度行（bug 根源）。
	if got := f.getSetting("web-4", thinkingModeChannel, "tier_vanguard"); got != "" {
		t.Errorf("sender-dimension read (web-4 exact) = %q, want empty", got)
	}
	if got := f.getSetting(canonicalSettingsSender, thinkingModeChannel, "tier_vanguard"); got != "" {
		t.Errorf("sender-dimension read (cli_user fallback) = %q, want empty", got)
	}
	// 修复后：web-4（user 1 的另一身份）→ user 维度命中。
	if got := f.userTierModel("web-4", "vanguard"); got != "sub-123|glm-5.3" {
		t.Errorf("userTierModel(web-4, vanguard) = %q, want sub-123|glm-5.3 (user_id dimension read)", got)
	}
	// resolveTierModel 端到端（GetLLMForModel 的 tier 入口解析）。
	subID, model, fromTier := f.resolveTierModel("web-4", "vanguard")
	if subID != "sub-123" || model != "glm-5.3" || !fromTier {
		t.Errorf("resolveTierModel(web-4, vanguard) = (%q, %q, %v), want (sub-123, glm-5.3, true)", subID, model, fromTier)
	}
}

// TestGetGlobalSetting_SenderFallbackChainWithoutResolver 守护兼容链：
// resolver 不可用（本地 CLI / 未注入）时，sender 精确行 + cli_user 兜底仍工作。
func TestGetGlobalSetting_SenderFallbackChainWithoutResolver(t *testing.T) {
	f, _, store := newSettingsTestFactory(t) // 无 userResolver
	// sender 维度行（本地 CLI 写入路径：Set → user_id=NULL）。
	if err := store.Set(thinkingModeChannel, "cli_user", "tier_swift", "sub-cli|model-cli"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// 任意 sender → cli_user 兜底命中（既有继承语义）。
	if got := f.userTierModel("web-4", "swift"); got != "sub-cli|model-cli" {
		t.Errorf("userTierModel(web-4, swift) = %q, want sub-cli|model-cli (cli_user fallback)", got)
	}
	// 未解析身份的 sender 也能走旧链（三层链的层 3）。
	if got := f.userTierModel("unknown-sender", "swift"); got != "sub-cli|model-cli" {
		t.Errorf("userTierModel(unknown-sender, swift) = %q, want sub-cli|model-cli", got)
	}
}

// TestGetGlobalSetting_MultiUserIsolation 多用户：user 维度各自隔离，不串。
func TestGetGlobalSetting_MultiUserIsolation(t *testing.T) {
	f, svc, _ := newSettingsTestFactory(t)
	f.SetUserResolver(func(senderID string) (int64, bool) {
		switch senderID {
		case "web-4":
			return 1, true
		case "web-5":
			return 2, true
		}
		return 0, false
	})
	if err := svc.SetByUserID(thinkingModeChannel, 1, "tier_vanguard", "sub-1|model-one"); err != nil {
		t.Fatalf("SetByUserID(user 1): %v", err)
	}
	if err := svc.SetByUserID(thinkingModeChannel, 2, "tier_vanguard", "sub-2|model-two"); err != nil {
		t.Fatalf("SetByUserID(user 2): %v", err)
	}
	if got := f.userTierModel("web-4", "vanguard"); got != "sub-1|model-one" {
		t.Errorf("web-4 read = %q, want sub-1|model-one", got)
	}
	if got := f.userTierModel("web-5", "vanguard"); got != "sub-2|model-two" {
		t.Errorf("web-5 read = %q, want sub-2|model-two", got)
	}
}

// TestGetGlobalSetting_UserDimensionWinsOverLegacy 优先级：user 维度（v45+
// 规范路径、面板最新写入）必须赢过 cli_user sender 兜底旧行。
func TestGetGlobalSetting_UserDimensionWinsOverLegacy(t *testing.T) {
	f, svc, store := newSettingsTestFactory(t)
	f.SetUserResolver(func(senderID string) (int64, bool) { return 1, true })
	// 旧 sender 行 + 新 user 维度行并存。
	if err := store.Set(thinkingModeChannel, "cli_user", "tier_vanguard", "sub-old|legacy-model"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := svc.SetByUserID(thinkingModeChannel, 1, "tier_vanguard", "sub-new|panel-model"); err != nil {
		t.Fatalf("SetByUserID: %v", err)
	}
	if got := f.userTierModel("anyone", "vanguard"); got != "sub-new|panel-model" {
		t.Errorf("userTierModel = %q, want sub-new|panel-model (user dimension wins over legacy cli_user row)", got)
	}
}
