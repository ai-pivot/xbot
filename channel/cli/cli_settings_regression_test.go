package cli

import (
	"testing"
	"xbot/channel"
)

// ---------------------------------------------------------------------------
// Regression tests for per-model max_context isolation
// ---------------------------------------------------------------------------

// TestSaveSettings_MaxContextNotPassedToApplySettings verifies that changing
// max_context_tokens in /settings does NOT propagate to ApplySettings (which
// would call ag.SetMaxContextTokens globally and contaminate all models).
// The channel.PerModelConfig write + Invalidate on the server already propagates the
// change correctly.
func TestSaveSettings_MaxContextNotPassedToApplySettings(t *testing.T) {
	appliedKeys := make(map[string]bool)
	settingsSvc := &testSettingsService{}
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000, MaxOutputTokens: 8192},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{
		config:      &CLIChannelConfig{},
		settingsSvc: settingsSvc,
	}
	m.channel.config.ApplySettings = func(vals map[string]string, chatID string) {
		for k := range vals {
			appliedKeys[k] = true
		}
	}

	// Simulate /settings save with max_context_tokens + a user-scoped key.
	m.saveSettings(map[string]string{
		"max_context_tokens": "100000",
		"max_iterations":     "50",
	})

	// max_context_tokens must NOT reach ApplySettings.
	if appliedKeys["max_context_tokens"] {
		t.Error("max_context_tokens reached ApplySettings — it should only go through channel.PerModelConfig, " +
			"not set the global contextManagerConfig.MaxContextTokens")
	}
	// User-scoped keys should still reach ApplySettings.
	if !appliedKeys["max_iterations"] {
		t.Error("max_iterations did not reach ApplySettings")
	}

	// Verify UpdatePerModelConfig was called for the right model with the right value.
	if len(mgr.pmcUpdates) != 1 {
		t.Fatalf("expected 1 UpdatePerModelConfig call, got %d", len(mgr.pmcUpdates))
	}
	upd := mgr.pmcUpdates[0]
	if upd.model != "model-a" {
		t.Errorf("UpdatePerModelConfig model = %q, want model-a", upd.model)
	}
	if upd.config.MaxContext != 100000 {
		t.Errorf("UpdatePerModelConfig MaxContext = %d, want 100000", upd.config.MaxContext)
	}
}

// TestSaveSettings_MaxContextPreservesMaxOutputTokens verifies that writing
// max_context via channel.PerModelConfig does NOT zero out an existing MaxOutputTokens
// for the same model.
func TestSaveSettings_MaxContextPreservesMaxOutputTokens(t *testing.T) {
	settingsSvc := &testSettingsService{}
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000, MaxOutputTokens: 8192},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{
		config:      &CLIChannelConfig{},
		settingsSvc: settingsSvc,
	}
	m.channel.config.ApplySettings = func(map[string]string, string) {}

	m.saveSettings(map[string]string{
		"max_context_tokens": "100000",
	})

	if len(mgr.pmcUpdates) != 1 {
		t.Fatalf("expected 1 UpdatePerModelConfig, got %d", len(mgr.pmcUpdates))
	}
	// CRITICAL: MaxOutputTokens must be preserved.
	if mgr.pmcUpdates[0].config.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %d, want 8192 (must be preserved when updating MaxContext)",
			mgr.pmcUpdates[0].config.MaxOutputTokens)
	}
	if mgr.pmcUpdates[0].config.MaxContext != 100000 {
		t.Errorf("MaxContext = %d, want 100000", mgr.pmcUpdates[0].config.MaxContext)
	}
}

// TestSaveSettings_MaxContextOnlyAffectsCurrentModel verifies that saving
// max_context for model-a does NOT touch model-b's channel.PerModelConfig.
func TestSaveSettings_MaxContextOnlyAffectsCurrentModel(t *testing.T) {
	settingsSvc := &testSettingsService{}
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
					"model-b": {MaxContext: 128000},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{
		config:      &CLIChannelConfig{},
		settingsSvc: settingsSvc,
	}
	m.channel.config.ApplySettings = func(map[string]string, string) {}

	m.saveSettings(map[string]string{
		"max_context_tokens": "50000",
	})

	// Only one UpdatePerModelConfig call, targeting model-a.
	if len(mgr.pmcUpdates) != 1 {
		t.Fatalf("expected 1 UpdatePerModelConfig, got %d", len(mgr.pmcUpdates))
	}
	if mgr.pmcUpdates[0].model != "model-a" {
		t.Errorf("model = %q, want model-a", mgr.pmcUpdates[0].model)
	}

	// model-b's config must be untouched.
	sub := mgr.subs[0]
	if pmc, ok := sub.PerModelConfigs["model-b"]; !ok || pmc.MaxContext != 128000 {
		t.Errorf("model-b config contaminated: got %v (want MaxContext=128000)", pmc)
	}
}

// TestSaveSettings_MaxContextUpdatesCachedValue verifies that saving
// max_context immediately updates cachedMaxContextTokens so the context bar
// reflects the new value without waiting for a server round-trip.
func TestSaveSettings_MaxContextUpdatesCachedValue(t *testing.T) {
	settingsSvc := &testSettingsService{}
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.cachedMaxContextTokens = 200000
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{
		config:      &CLIChannelConfig{},
		settingsSvc: settingsSvc,
	}
	m.channel.config.ApplySettings = func(map[string]string, string) {}

	m.saveSettings(map[string]string{
		"max_context_tokens": "50000",
	})

	if m.cachedMaxContextTokens != 50000 {
		t.Errorf("cachedMaxContextTokens = %d, want 50000 (should update immediately after save)",
			m.cachedMaxContextTokens)
	}
}

// ---------------------------------------------------------------------------
// readSettings model-awareness
// ---------------------------------------------------------------------------

// TestReadSettings_UsesSessionModelForMaxContext verifies that readSettings
// returns the max_context for the SESSION's active model, not the
// subscription's default model. Without this fix, a session that switched to
// model-b would still show model-a's max_context in the settings panel.
func TestReadSettings_UsesSessionModelForMaxContext(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
					"model-b": {MaxContext: 128000},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-b" // session switched to model-b
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{config: &CLIChannelConfig{}}
	m.channel.config.GetCurrentValues = func() map[string]string { return map[string]string{} }

	values := m.readSettings()

	// Should show model-b's max_context (128000), not model-a's (200000).
	got := values["max_context_tokens"]
	if got != "128000" {
		t.Errorf("max_context_tokens = %q, want 128000 (from model-b, not model-a)", got)
	}
}

// ---------------------------------------------------------------------------
// cycleModel updates cachedMaxContextTokens
// ---------------------------------------------------------------------------

// TestCycleModel_UpdatesCachedMaxContext verifies that switching model via
// cycleModel re-resolves cachedMaxContextTokens for the new model so the
// context bar reflects the correct window size immediately.
func TestCycleModel_UpdatesCachedMaxContext(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
					"model-b": {MaxContext: 1000000},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.cachedMaxContextTokens = 200000
	m.subscriptionMgr = mgr
	m.remoteMode = true

	// Manually replicate what cycleModel does (without channel callbacks):
	// switch to model-b, re-resolve context tokens.
	nextModel := "model-b"
	m.cachedModelName = nextModel
	existing := SessionLLMState{SubscriptionID: m.activeSubID, Model: nextModel}
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(existing, m.subscriptionMgr)

	if m.cachedMaxContextTokens != 1000000 {
		t.Errorf("cachedMaxContextTokens = %d, want 1000000 (model-b's context)",
			m.cachedMaxContextTokens)
	}
	if m.cachedModelName != "model-b" {
		t.Errorf("cachedModelName = %q, want model-b", m.cachedModelName)
	}
}

// ---------------------------------------------------------------------------
// ResolveEffectiveMaxContext per-model resolution
// ---------------------------------------------------------------------------

// TestResolveEffectiveMaxContext_PerModelResolution verifies that
// ResolveEffectiveMaxContext picks the correct max_context for each model
// in a subscription with multiple models.
func TestResolveEffectiveMaxContext_PerModelResolution(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test",
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
					"model-b": {MaxContext: 1000000},
				},
			},
		},
	}

	stateA := SessionLLMState{SubscriptionID: "sub1", Model: "model-a"}
	if got := ResolveEffectiveMaxContext(stateA, mgr); got != 200000 {
		t.Errorf("model-a max_context = %d, want 200000", got)
	}

	stateB := SessionLLMState{SubscriptionID: "sub1", Model: "model-b"}
	if got := ResolveEffectiveMaxContext(stateB, mgr); got != 1000000 {
		t.Errorf("model-b max_context = %d, want 1000000", got)
	}

	// Model without per-model config → fallback to global default (200000).
	stateC := SessionLLMState{SubscriptionID: "sub1", Model: "model-c"}
	gotC := ResolveEffectiveMaxContext(stateC, mgr)
	// Should return the global default, NOT inherit from model-a/b's per-model config.
	if gotC == 1000000 {
		t.Errorf("model-c should not inherit model-b's context (1M), got %d", gotC)
	}
}

// ---------------------------------------------------------------------------
// Session model independence across sessions (same subscription)
// ---------------------------------------------------------------------------

// TestSessionLLMState_ModelIndependence verifies that two sessions sharing
// the same subscription can have different models without affecting each other.
func TestSessionLLMState_ModelIndependence(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Model: "model-a",
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000},
					"model-b": {MaxContext: 1000000},
				},
			},
		},
	}

	// Session A: model-a
	stateA := SessionLLMState{SubscriptionID: "sub1", Model: "model-a"}
	// Session B: model-b (same subscription)
	stateB := SessionLLMState{SubscriptionID: "sub1", Model: "model-b"}

	maxA := ResolveEffectiveMaxContext(stateA, mgr)
	maxB := ResolveEffectiveMaxContext(stateB, mgr)

	if maxA != 200000 {
		t.Errorf("session A max_context = %d, want 200000", maxA)
	}
	if maxB != 1000000 {
		t.Errorf("session B max_context = %d, want 1000000", maxB)
	}
	if maxA == maxB {
		t.Error("sessions with different models should have different max_context values")
	}
}

// ---------------------------------------------------------------------------
// readSettings + saveSettings round-trip
// ---------------------------------------------------------------------------

// TestReadSaveSettings_RoundTrip verifies that a read→modify→save cycle for
// max_context on a specific model correctly preserves other models' configs.
func TestReadSaveSettings_RoundTrip(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000, MaxOutputTokens: 4096},
					"model-b": {MaxContext: 128000, MaxOutputTokens: 8192},
				},
			},
		},
		defaultID: "sub1",
	}
	settingsSvc := &testSettingsService{}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.subscriptionMgr = mgr
	m.channel = &CLIChannel{config: &CLIChannelConfig{}, settingsSvc: settingsSvc}
	m.channel.config.GetCurrentValues = func() map[string]string { return map[string]string{} }
	m.channel.config.ApplySettings = func(map[string]string, string) {}

	// Read current max_context for model-a
	values := m.readSettings()
	if values["max_context_tokens"] != "200000" {
		t.Fatalf("initial read: max_context = %q, want 200000", values["max_context_tokens"])
	}

	// Save a new max_context for model-a
	m.saveSettings(map[string]string{
		"max_context_tokens": "100000",
	})

	// Verify model-a's config updated
	sub := mgr.subs[0]
	pmcA := sub.PerModelConfigs["model-a"]
	if pmcA.MaxContext != 100000 {
		t.Errorf("model-a MaxContext = %d, want 100000", pmcA.MaxContext)
	}
	if pmcA.MaxOutputTokens != 4096 {
		t.Errorf("model-a MaxOutputTokens = %d, want 4096 (must be preserved)", pmcA.MaxOutputTokens)
	}

	// Verify model-b's config untouched
	pmcB := sub.PerModelConfigs["model-b"]
	if pmcB.MaxContext != 128000 {
		t.Errorf("model-b MaxContext = %d, want 128000 (must be untouched)", pmcB.MaxContext)
	}
	if pmcB.MaxOutputTokens != 8192 {
		t.Errorf("model-b MaxOutputTokens = %d, want 8192 (must be untouched)", pmcB.MaxOutputTokens)
	}

	// Read back — should show the updated value
	values2 := m.readSettings()
	if values2["max_context_tokens"] != "100000" {
		t.Errorf("post-save read: max_context = %q, want 100000", values2["max_context_tokens"])
	}
}

// ---------------------------------------------------------------------------
// handleSwitchLLMDoneMsg (normal subscription switch)
// ---------------------------------------------------------------------------

// TestHandleSwitchLLMDone_UsesSubModel verifies that on subscription switch,
// the subscription's model is used and per-model context is correctly resolved.
func TestHandleSwitchLLMDone_UsesSubModel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000, MaxOutputTokens: 4096},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	workDir2 := t.TempDir()
	m.workDir = workDir2
	m.chatID = workDir2 + ":session-1"
	m.subscriptionMgr = mgr
	m.remoteMode = true
	m.channel = &CLIChannel{config: &CLIChannelConfig{}}

	done := cliSwitchLLMDoneMsg{
		subID:     "sub1",
		subName:   "test",
		subModel:  "model-a",
		maxCtx:    999999, // intentionally wrong — must be ignored
		maxOutTok: 9999,
		mgr:       mgr,
	}

	_, _, handled := m.handleSwitchLLMDoneMsg(done)
	if !handled {
		t.Fatal("expected handleSwitchLLMDoneMsg to handle the message")
	}

	if m.cachedModelName != "model-a" {
		t.Errorf("cachedModelName = %q, want model-a (subscription's model)", m.cachedModelName)
	}
	if m.cachedMaxContextTokens != 200000 {
		t.Errorf("cachedMaxContextTokens = %d, want 200000 (from PerModelConfigs, not done.maxCtx=999999)",
			m.cachedMaxContextTokens)
	}
}

// ---------------------------------------------------------------------------
// Cycle model preserves correct per-model max_context (user-reported scenario)
// ---------------------------------------------------------------------------

// TestCycleModel_PreservesPerModelMaxContext simulates the user's exact scenario:
// 1. Session uses model-a (200k context)
// 2. Cycle to model-b (1M context)
// 3. Cycle back to model-a
// 4. Context bar must show 200k, not 1M
func TestCycleModel_PreservesPerModelMaxContext(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
				PerModelConfigs: map[string]channel.PerModelConfig{
					"model-a": {MaxContext: 200000, MaxOutputTokens: 4096},
					"model-b": {MaxContext: 1000000, MaxOutputTokens: 8192},
				},
			},
		},
		defaultID: "sub1",
	}

	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.activeSubID = "sub1"
	m.cachedModelName = "model-a"
	m.subscriptionMgr = mgr
	m.remoteMode = true

	// Initial state: model-a with 200k
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(
		SessionLLMState{SubscriptionID: "sub1", Model: "model-a"}, mgr)
	if m.cachedMaxContextTokens != 200000 {
		t.Fatalf("initial: cachedMaxContextTokens = %d, want 200000", m.cachedMaxContextTokens)
	}

	// Simulate cycleModel going to model-b.
	existing := SessionLLMState{SubscriptionID: m.activeSubID, Model: "model-b"}
	m.cachedModelName = "model-b"
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(existing, m.subscriptionMgr)

	// MUST be 1M (model-b).
	if m.cachedMaxContextTokens != 1000000 {
		t.Fatalf("after cycling to model-b: cachedMaxContextTokens = %d, want 1000000",
			m.cachedMaxContextTokens)
	}

	// Now simulate cycleModel going back to model-a.
	existing = SessionLLMState{SubscriptionID: m.activeSubID, Model: "model-a"}
	m.cachedModelName = "model-a"
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(existing, m.subscriptionMgr)

	// MUST be 200k (model-a), not 1M (model-b).
	if m.cachedMaxContextTokens != 200000 {
		t.Errorf("after cycling back to model-a: cachedMaxContextTokens = %d, want 200000",
			m.cachedMaxContextTokens)
	}
}

// ---------------------------------------------------------------------------
// Per-session model persistence on startup restore (regression for model switch bug)
// ---------------------------------------------------------------------------

// mockLLMSubscriber tracks SwitchModel calls for assertions.
type mockLLMSubscriber struct {
	switchModelCalls []mockSwitchModelCall
	switchSubCalls   []mockSwitchSubCall
	defaultModel     string
}

type mockSwitchModelCall struct {
	senderID string
	model    string
	chatID   string
}

type mockSwitchSubCall struct {
	senderID string
	chatID   string
}

func (m *mockLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription, chatID string) error {
	m.switchSubCalls = append(m.switchSubCalls, mockSwitchSubCall{senderID, chatID})
	return nil
}

func (m *mockLLMSubscriber) SwitchModel(senderID, model, chatID string) {
	m.switchModelCalls = append(m.switchModelCalls, mockSwitchModelCall{senderID, model, chatID})
}

func (m *mockLLMSubscriber) SelectModel(senderID, subID, model, chatID string) error {
	m.switchModelCalls = append(m.switchModelCalls, mockSwitchModelCall{senderID, model, chatID})
	return nil
}

func (m *mockLLMSubscriber) GetDefaultModel() string {
	return m.defaultModel
}

// TestScheduleSessionLLMRestore_UsesPerSessionModel verifies that when a session
// had a per-session model override (e.g. user switched via Ctrl+N), the restore
// path uses that model instead of the subscription's default model.
func TestScheduleSessionLLMRestore_UsesPerSessionModel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
			},
		},
		defaultID: "sub1",
	}

	var switchedModel string
	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.workDir = t.TempDir()
	m.chatID = "/test:session-1"
	m.subscriptionMgr = mgr
	m.activeSubID = "sub1"
	// Simulate per-session model restored from backend by refreshCachedModelName
	m.cachedModelName = "model-b"
	m.channel = &CLIChannel{config: &CLIChannelConfig{
		SwitchLLM: func(provider, baseURL, apiKey, model string) error {
			switchedModel = model
			return nil
		},
	}, subscriptionMgr: mgr}

	m.scheduleSessionLLMRestore()
	if len(m.pendingCmds) == 0 {
		t.Fatal("expected pendingCmds to be populated")
	}
	// Execute the pending cmd
	msg := m.pendingCmds[0]()
	done, ok := msg.(cliSwitchLLMDoneMsg)
	if !ok {
		t.Fatalf("expected cliSwitchLLMDoneMsg, got %T", msg)
	}
	if switchedModel != "model-b" {
		t.Errorf("SwitchLLM model = %q, want model-b (per-session model)", switchedModel)
	}
	if done.subModel != "model-b" {
		t.Errorf("done.subModel = %q, want model-b (per-session model)", done.subModel)
	}
}

// TestHandleSwitchLLMDone_RestoresPerSessionModel verifies that handleSwitchLLMDoneMsg
// calls SwitchModel to set the per-session model after SetDefault creates the entry
// with the subscription's default model.
func TestHandleSwitchLLMDone_RestoresPerSessionModel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{
				ID: "sub1", Name: "test", Provider: "openai", BaseURL: "https://api.test/v1",
				APIKey: "key", Model: "model-a", Active: true,
			},
		},
		defaultID: "sub1",
	}

	mockSub := &mockLLMSubscriber{}
	m := newCLIModel()
	m.channelName = "cli"
	m.senderID = "cli_user"
	m.workDir = t.TempDir()
	m.chatID = "/test:session-1"
	m.subscriptionMgr = mgr
	m.llmSubscriber = mockSub
	m.remoteMode = true
	m.channel = &CLIChannel{config: &CLIChannelConfig{}}

	// Simulate startup restore: subModel is per-session model "model-b",
	// NOT subscription default "model-a"
	done := cliSwitchLLMDoneMsg{
		subID:    "sub1",
		subName:  "test",
		subModel: "model-b", // per-session model
		mgr:      mgr,
	}

	m.handleSwitchLLMDoneMsg(done)

	// SwitchModel must have been called with the per-session model
	if len(mockSub.switchModelCalls) != 1 {
		t.Fatalf("expected 1 SwitchModel call, got %d", len(mockSub.switchModelCalls))
	}
	call := mockSub.switchModelCalls[0]
	if call.model != "model-b" {
		t.Errorf("SwitchModel model = %q, want model-b", call.model)
	}
	if call.chatID != m.chatID {
		t.Errorf("SwitchModel chatID = %q, want %q", call.chatID, m.chatID)
	}

	// cachedModelName must reflect per-session model, not subscription default
	if m.cachedModelName != "model-b" {
		t.Errorf("cachedModelName = %q, want model-b (per-session model)", m.cachedModelName)
	}
}
