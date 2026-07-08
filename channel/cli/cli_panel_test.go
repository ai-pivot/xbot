package cli

import (
	"regexp"
	"strings"
	"testing"
	"xbot/channel"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// mockSubscriptionManager implements SubscriptionManager for testing.
type mockSubscriptionManager struct {
	subs      []channel.Subscription
	defaultID string
	addCalled bool
	setDefID  string
	saveErr   error

	// Track UpdatePerModelConfig calls for assertions.
	pmcUpdates []mockPMCUpdate

	// Per-session LLM state: chatID → (subID, model). Simulates the DB tenants table.
	// GetSessionSubscription returns from this map; empty string = "no entry in DB".
	sessionLLM map[string]sessionLLMEntry
}

type sessionLLMEntry struct {
	subID string
	model string
}

type mockPMCUpdate struct {
	subID  string
	model  string
	config channel.PerModelConfig
}

func (m *mockSubscriptionManager) List(_ string) ([]channel.Subscription, error) {
	return m.subs, nil
}

func (m *mockSubscriptionManager) GetDefault(_ string) (*channel.Subscription, error) {
	for _, s := range m.subs {
		if s.ID == m.defaultID {
			return &s, nil
		}
	}
	return nil, nil
}

func (m *mockSubscriptionManager) Add(sub *channel.Subscription) error {
	m.addCalled = true
	m.subs = append(m.subs, *sub)
	return m.saveErr
}

func (m *mockSubscriptionManager) Remove(id string) error {
	return nil
}

func (m *mockSubscriptionManager) UpsertModel(id, model string, maxContext, maxOutput int, apiType, thinkingMode string) error {
	for i := range m.subs {
		if m.subs[i].ID == id {
			if m.subs[i].PerModelConfigs == nil {
				m.subs[i].PerModelConfigs = make(map[string]channel.PerModelConfig)
			}
			pmc := m.subs[i].PerModelConfigs[model]
			pmc.MaxContext = maxContext
			pmc.MaxOutputTokens = maxOutput
			pmc.APIType = apiType
			pmc.Enabled = true
			m.subs[i].PerModelConfigs[model] = pmc
		}
	}
	return nil
}

func (m *mockSubscriptionManager) RemoveModel(id, model string) error {
	for i := range m.subs {
		if m.subs[i].ID == id {
			delete(m.subs[i].PerModelConfigs, model)
		}
	}
	return nil
}

func (m *mockSubscriptionManager) SetDefault(id, chatID string) error {
	m.setDefID = id
	for i := range m.subs {
		m.subs[i].Active = m.subs[i].ID == id
	}
	return m.saveErr
}

func (m *mockSubscriptionManager) SetModel(id, model string) error {
	return nil
}

func (m *mockSubscriptionManager) Rename(id, name string) error {
	return nil
}

func (m *mockSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	return nil
}

func (m *mockSubscriptionManager) UpdatePerModelConfig(id, model string, pmc channel.PerModelConfig) error {
	m.pmcUpdates = append(m.pmcUpdates, mockPMCUpdate{subID: id, model: model, config: pmc})
	// Also apply to in-memory subs so activeSubscription() sees the update.
	for i := range m.subs {
		if m.subs[i].ID == id {
			if m.subs[i].PerModelConfigs == nil {
				m.subs[i].PerModelConfigs = make(map[string]channel.PerModelConfig)
			}
			m.subs[i].PerModelConfigs[model] = pmc
		}
	}
	return nil
}

func (m *mockSubscriptionManager) GetSessionSubscription(senderID, channelName, chatID string) (string, string, error) {
	if m.sessionLLM != nil {
		if entry, ok := m.sessionLLM[chatID]; ok {
			return entry.subID, entry.model, nil
		}
	}
	return "", "", nil
}

func (m *mockSubscriptionManager) SetModelEnabled(id, model string, enabled bool) error {
	for i := range m.subs {
		if m.subs[i].ID == id {
			if m.subs[i].PerModelConfigs == nil {
				m.subs[i].PerModelConfigs = make(map[string]channel.PerModelConfig)
			}
			pmc := m.subs[i].PerModelConfigs[model]
			pmc.Enabled = enabled
			m.subs[i].PerModelConfigs[model] = pmc
		}
	}
	return nil
}

func (m *mockSubscriptionManager) SetSubscriptionEnabled(id string, enabled bool) error {
	for i := range m.subs {
		if m.subs[i].ID == id {
			m.subs[i].Enabled = enabled
		}
	}
	return nil
}

// openLLMPanelForTest opens the panel. Now that data is read synchronously
// from DB via llmCache.Get(), no drain step is needed — rows are populated
// immediately on open.
func openLLMPanelForTest(t *testing.T, m *cliModel) {
	t.Helper()
	m.openQuickSwitch("")
}

// findLLMRowBySubID returns the row index of the subscription with the given ID
// in the unified LLM panel, or -1 if not found.
func findLLMRowBySubID(m *cliModel, subID string) int {
	for i, r := range m.quickSwitchRows {
		if r.kind == qsSub && r.sub.ID == subID {
			return i
		}
	}
	return -1
}

// TestApplyQuickSwitch tests that applying a subscription row toggles its
// enabled state (model-first: the panel is management-only for subscriptions —
// add / disable / delete — and no longer "switches" the active subscription).
func TestApplyQuickSwitch(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", BaseURL: "https://glm.example.com/v1", APIKey: "key1", Model: "glm-4", Active: true, Enabled: true},
			{ID: "sub2", Name: "gpt", Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key2", Model: "gpt-4.1", Active: false, Enabled: true},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			SwitchLLM: func(provider, baseURL, apiKey, model string) error {
				t.Fatal("SwitchLLM must NOT be called from the subscription panel anymore")
				return nil
			},
			GetCurrentValues: func() map[string]string {
				return map[string]string{"theme": "midnight"}
			},
		},
		modelLister: &fakeModelLister{
			entries: []protocol.ModelEntry{
				{SubID: "sub1", Model: "glm-4", Status: "normal"},
				{SubID: "sub2", Model: "gpt-4.1", Status: "normal"},
			},
		},
	}

	// Open the unified LLM panel.
	openLLMPanelForTest(t, model)
	if model.quickSwitchMode != "llm" {
		t.Fatalf("expected quickSwitchMode=llm, got %s", model.quickSwitchMode)
	}
	// Both subscriptions must be present as rows.
	idx1 := findLLMRowBySubID(model, "sub1")
	idx2 := findLLMRowBySubID(model, "sub2")
	if idx1 < 0 || idx2 < 0 {
		t.Fatalf("expected both subscriptions in rows, got idx1=%d idx2=%d", idx1, idx2)
	}
	// The active sub (sub1) should be pre-selected.
	if model.quickSwitchCursor != idx1 {
		t.Fatalf("expected cursor=%d (active sub), got %d", idx1, model.quickSwitchCursor)
	}

	// Move cursor to sub2 and press D to toggle enabled (true → false).
	model.quickSwitchCursor = idx2
	model.disableCurrentRow()

	if mgr.subs[1].Enabled {
		t.Errorf("expected sub2 disabled after D toggle, got enabled=true")
	}
	// No async subscription switch should be queued.
	for _, c := range model.pendingCmds {
		if _, ok := c().(cliSwitchLLMDoneMsg); ok {
			t.Fatal("a cliSwitchLLMDoneMsg was queued — subscription switching must not happen from the panel")
		}
	}
	// Panel stays open so the user can keep managing.
	if model.quickSwitchMode != "llm" {
		t.Errorf("expected panel to stay open, got quickSwitchMode=%q", model.quickSwitchMode)
	}

	// Toggle again (false → true) via D. Re-resolve the row index (rebuild may have
	// moved it) and apply.
	idx2 = findLLMRowBySubID(model, "sub2")
	model.quickSwitchCursor = idx2
	model.disableCurrentRow()
	if !mgr.subs[1].Enabled {
		t.Errorf("expected sub2 re-enabled after second D toggle, got enabled=false")
	}

	// Verify: → key expands sub2 (Enter also expands/collapses).
	idx2 = findLLMRowBySubID(model, "sub2")
	model.quickSwitchCursor = idx2
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: tea.KeyRight}) // → expand sub2
	if !model.expandedSubs["sub2"] {
		t.Error("expected sub2 expanded after → key, got collapsed")
	}
	// sub2 models should now be visible in rows.
	hasSub2Model := false
	for _, r := range model.quickSwitchRows {
		if r.kind == qsModel && r.subID == "sub2" {
			hasSub2Model = true
			break
		}
	}
	if !hasSub2Model {
		t.Error("expected sub2 model rows visible after expand")
	}
	// ← key collapses sub2
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: tea.KeyLeft}) // ← collapse sub2
	if model.expandedSubs["sub2"] {
		t.Error("expected sub2 collapsed after ← key, got expanded")
	}
}

func TestPanelBoxLeftAlign(t *testing.T) {
	// Verify that settings panel content is left-aligned after PanelBox wrapping.
	// Regression test: lipgloss v2 Width() defaults to centering content.
	m := newCLIModel()
	m.width = 80
	m.styles = buildStyles(80) // rebuild with test width

	// Simulate a settings panel with a short selected line
	schema := []channel.SettingDefinition{
		{Key: "name", Label: "Name", Type: channel.SettingTypeText, Category: "Test"},
		{Key: "provider", Label: "Provider", Type: channel.SettingTypeText, DefaultValue: "openai", Category: "Test"},
	}
	m.panelState.settings.schema = schema
	m.panelState.settings.values = map[string]string{"provider": "openai"}
	m.panelState.cursor = 0
	m.panelState.mode = "settings"

	raw := m.viewPanel()
	// Wrap in PanelBox like cli_view.go does
	boxed := m.styles.PanelBox.Render(raw)

	t.Log("=== Boxed panel output (stripped) ===")
	lines := splitANSILines(boxed)
	for i, line := range lines {
		t.Logf("  [%d] len=%d: %q", i, len(stripANSI(line)), stripANSI(line))
	}
	// Find the line containing "Name:" and verify position
	for _, line := range lines {
		stripped := stripANSI(line)
		if idx := indexOfStr(stripped, "Name:"); idx >= 0 {
			// After PanelBox border ("│") + padding (" ") + cursor ("▸") + space = ~4 visible chars
			// "Name:" should appear at column ~4 in the stripped string
			// (may be higher due to multi-byte UTF-8 in cursor char + ANSI-wrapped styling)
			if idx > 20 {
				t.Errorf("'Name:' at column %d (expected <= 6, looks centered).\nLine: %q", idx, stripped)
			}
			return
		}
	}
	t.Error("could not find 'Name:' in panel output")
}

func TestAskUserQuestionWrapPreservesTextWithScrollbar(t *testing.T) {
	m := newCLIModel()
	m.handleResize(80, 12)
	m.panelState.mode = "askuser"

	// Use the same wrap width that viewAskUserPanel uses internally.
	// Text at exactly this width must survive applyScrollbar without truncation.
	qWrapWidth := m.askUserQuestionWrapWidth()
	question := strings.Repeat("a", qWrapWidth-lipgloss.Width("❓ ")+5) // slightly more than 1 line
	m.panelState.askUser.askItems = []askItem{{
		Question: question,
		Options:  []string{"one", "two", "three", "four", "five", "six"},
	}}
	m.panelState.askUser.askTab = 0
	m.panelState.askUser.askOptCursor = map[int]int{0: 0}
	m.panelState.askUser.askOptSel = map[int]map[int]bool{0: {}}

	rendered := m.layoutAskUser("")
	got := strings.Count(stripANSI(rendered), "a")
	if got != len(question) {
		t.Fatalf("askuser question lost text at wrap boundary: got %d %q chars, want %d", got, "a", len(question))
	}
}

func TestAskUserLongOptionWraps(t *testing.T) {
	m := newCLIModel()
	m.handleResize(80, 24)
	m.panelState.mode = "askuser"

	// Create an option that exceeds the panel content width
	qWrapWidth := m.askUserQuestionWrapWidth()
	// prefixW = ansi.StringWidth("▸ ☑ ") = 4
	prefixW := 4
	optWrapW := qWrapWidth - prefixW
	longOpt := strings.Repeat("x", optWrapW+20) // longer than one line
	m.panelState.askUser.askItems = []askItem{{
		Question: "Pick one",
		Options:  []string{longOpt, "short"},
	}}
	m.panelState.askUser.askTab = 0
	m.panelState.askUser.askOptCursor = map[int]int{0: 0}
	m.panelState.askUser.askOptSel = map[int]map[int]bool{0: {}}

	raw := m.viewAskUserPanel()
	lines := strings.Split(raw, "\n")

	// Find option lines (after question + blank line)
	// The long option should produce multiple lines
	// Count how many lines contain the long option's 'x' characters
	optLineCount := 0
	foundOpt := false
	for _, line := range lines {
		stripped := stripANSI(line)
		if strings.Contains(stripped, "☐") || strings.Contains(stripped, "☑") {
			foundOpt = true
		}
		if foundOpt && strings.Contains(stripped, "x") {
			optLineCount++
		}
	}

	if optLineCount < 2 {
		t.Errorf("long option should wrap to multiple lines, got %d lines containing 'x'", optLineCount)
	}

	// Verify no line exceeds the panel content width
	rendered := m.layoutAskUser("")
	renderedLines := strings.Split(rendered, "\n")
	for i, line := range renderedLines {
		visW := lipgloss.Width(line)
		if visW > m.chatWidth() {
			t.Errorf("line %d exceeds chatWidth: visW=%d chatWidth=%d line=%q",
				i, visW, m.chatWidth(), stripANSI(line))
		}
	}

	// Verify total 'x' count matches the original option length
	totalX := strings.Count(stripANSI(rendered), "x")
	if totalX != len(longOpt) {
		t.Errorf("lost option text after wrap: got %d 'x', want %d", totalX, len(longOpt))
	}
}

// TestSubscriptionGenerationGuard tests that stale per-subscription values
// (provider, api_key, base_url, model, max_output_tokens)
// are NEVER written back after a subscription switch.
// thinking_mode is intentionally NOT in this list — it is a global per-user
// setting now (Ctrl+M toggle), so it must survive a subscription switch.
// This is the structural guarantee against the subscription overwrite bug.
func TestSubscriptionGenerationGuard(t *testing.T) {
	model := newCLIModel()
	model.subGeneration = 5

	// Simulate: settings panel opens with generation 5
	model.panelState.settings.subGeneration = model.subGeneration

	// Simulate: user edits some values
	values := map[string]string{
		"llm_provider":      "openai",
		"llm_api_key":       "sk-old-key",
		"llm_base_url":      "https://old.example.com",
		"llm_model":         "old-model",
		"max_output_tokens": "8192",
		"thinking_mode":     "auto",
	}

	// Simulate: subscription switch happens (generation increments)
	model.subGeneration = 6

	// Simulate: the onSubmit callback runs (this is what the guard checks)
	// After switch, stale subscription-scoped fields should be stripped
	if model.panelState.settings.subGeneration != model.subGeneration {
		for k := range values {
			if isSubscriptionScopedSettingKey(k) {
				delete(values, k)
			}
		}
	}

	// Verify: per-subscription fields are GONE
	for _, k := range []string{"llm_provider", "llm_api_key", "llm_base_url", "llm_model", "max_output_tokens"} {
		if _, exists := values[k]; exists {
			t.Errorf("BUG: stale subscription field %q should have been deleted after subscription switch", k)
		}
	}

	// Verify: thinking_mode (global user setting) is PRESERVED across subscription switch
	if values["thinking_mode"] != "auto" {
		t.Errorf("global thinking_mode should be preserved across subscription switch, got %q", values["thinking_mode"])
	}
}

// TestSubscriptionGenerationGuardNoSwitch tests that when subscription does NOT change,
// all subscription-scoped fields are preserved (no false positives).
func TestSubscriptionGenerationGuardNoSwitch(t *testing.T) {
	model := newCLIModel()
	model.subGeneration = 5
	model.panelState.settings.subGeneration = 5 // same generation = no switch

	values := map[string]string{
		"llm_provider":      "openai",
		"llm_api_key":       "sk-test-key",
		"llm_base_url":      "https://api.example.com",
		"llm_model":         "gpt-4",
		"max_output_tokens": "8192",
		"thinking_mode":     "auto",
	}

	// Guard should NOT strip anything
	if model.panelState.settings.subGeneration != model.subGeneration {
		for k := range values {
			if isSubscriptionScopedSettingKey(k) {
				delete(values, k)
			}
		}
	}

	// All fields should still be present
	for _, k := range []string{"llm_provider", "llm_api_key", "llm_base_url", "llm_model", "max_output_tokens", "thinking_mode"} {
		if _, exists := values[k]; !exists {
			t.Errorf("subscription field %q should NOT be deleted when subscription hasn't changed", k)
		}
	}
}

// TestApplyQuickSwitchNilChannel tests that nil channel doesn't crash when
// toggling a subscription row.
func TestApplyQuickSwitchNilChannel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true, Enabled: true},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	// channel is nil!

	openLLMPanelForTest(t, model)
	if idx := findLLMRowBySubID(model, "sub1"); idx >= 0 {
		model.quickSwitchCursor = idx
	}
	model.applyQuickSwitch() // should NOT panic

	// SetDefault should NOT be called because SwitchLLM is unreachable (nil channel)
	if mgr.setDefID != "" {
		t.Errorf("expected SetDefault NOT called with nil channel, got %s", mgr.setDefID)
	}
}

// TestApplyQuickSwitchNilSwitchLLM tests that nil SwitchLLM doesn't crash when
// toggling a subscription row.
func TestApplyQuickSwitchNilSwitchLLM(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true, Enabled: true},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			// SwitchLLM is nil
			GetCurrentValues: func() map[string]string {
				return map[string]string{}
			},
		},
	}

	openLLMPanelForTest(t, model)
	if idx := findLLMRowBySubID(model, "sub1"); idx >= 0 {
		model.quickSwitchCursor = idx
	}
	model.applyQuickSwitch() // should NOT panic, should NOT call SwitchLLM

	// SetDefault should NOT be called because SwitchLLM is nil
	if mgr.setDefID != "" {
		t.Errorf("expected SetDefault NOT called with nil SwitchLLM, got %s", mgr.setDefID)
	}
}

// TestOpenQuickSwitchWithEmptySubs tests that the add-subscription action row is
// shown even with no subscriptions.
func TestOpenQuickSwitchWithEmptySubs(t *testing.T) {
	mgr := &mockSubscriptionManager{subs: nil}

	model := newCLIModel()
	model.subscriptionMgr = mgr

	openLLMPanelForTest(t, model)

	if model.quickSwitchMode != "llm" {
		t.Fatalf("expected mode=llm, got %s", model.quickSwitchMode)
	}

	foundAdd := false
	for _, r := range model.quickSwitchRows {
		if r.kind == qsAddSub {
			foundAdd = true
			break
		}
	}
	if !foundAdd {
		t.Error("expected an add-subscription action row in the panel")
	}
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func splitANSILines(s string) []string {
	return strings.Split(s, "\n")
}

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func indexOfStr(s, substr string) int {
	return strings.Index(s, substr)
}

func TestSanitizeOutputLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "carriage return keeps last frame",
			input: "Loading 10%\rLoading 50%\rLoading 100%",
			want:  "Loading 100%",
		},
		{
			name:  "ANSI color codes stripped",
			input: "\x1b[32mSuccess\x1b[0m",
			want:  "Success",
		},
		{
			name:  "tqdm-style progress bar",
			input: "Map:  71%|\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u258d  | 2118/2967 [00:00<00:00, 81922.37 examples/s]",
			want:  "Map:  71%|\u2588\u2588\u2588\u2588\u2588\u2588\u2588\u258d  | 2118/2967 [00:00<00:00, 81922.37 examples/s]",
		},
		{
			name:  "ANSI + carriage return combined",
			input: "\x1b[32m100%\r\x1b[0mDone",
			want:  "Done",
		},
		{
			name:  "empty after carriage return",
			input: "something\r   ",
			want:  "   ",
		},
		{
			name:  "multiple carriage returns",
			input: "a\rb\rc\rdone",
			want:  "done",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeOutputLine(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeOutputLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeOutputLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "simple lines",
			input: "line1\nline2\nline3",
			want:  []string{"line1", "line2", "line3"},
		},
		{
			name:  "tqdm progress with carriage return newlines",
			input: "Map: 100%|\u2588\u2588\u2588| 2967/2967\r\nMap:  71%|\u2588\u2588| 2118/2967\r\nDone loading",
			want:  []string{"Done loading"},
		},
		{
			name:  "tqdm carriage return within line keeps last frame",
			input: "Map: 100%|\u2588\u2588\u2588| 2967/2967\rMap:  71%|\u2588\u2588| 2118/2967",
			want:  []string{"Map:  71%|\u2588\u2588| 2118/2967"},
		},
		{
			name:  "lines that become empty after sanitization are filtered",
			input: "visible\r   \nalso visible",
			want:  []string{"also visible"},
		},
		{
			name:  "ANSI colored lines",
			input: "\x1b[32mGreen\x1b[0m\n\x1b[31mRed\x1b[0m",
			want:  []string{"Green", "Red"},
		},
		{
			name:  "truly empty lines filtered",
			input: "a\n\n\nb",
			want:  []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeOutputLines(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("sanitizeOutputLines(%q) = %q, want %q", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sanitizeOutputLines(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}
