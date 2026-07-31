package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"xbot/channel"
	"xbot/protocol"
)

// fakeModelLister implements ModelLister for testing without real RPC.
type fakeModelLister struct {
	entries   []protocol.ModelEntry
	refreshed bool
}

func (f *fakeModelLister) ListModels() []string {
	var models []string
	for _, e := range f.entries {
		models = append(models, e.Model)
	}
	return models
}

func (f *fakeModelLister) ListAllModels() []string {
	return f.ListModels()
}

func (f *fakeModelLister) ListAllModelEntries() []protocol.ModelEntry {
	return f.entries
}

func (f *fakeModelLister) EnsureModelsLoaded() {}

func (f *fakeModelLister) RefreshModelEntries() []protocol.ModelEntry {
	f.refreshed = true
	return f.entries
}

func newQuickSwitchTestModel() *cliModel {
	mgr := &mockSubscriptionManager{
		subs: []channel.Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", BaseURL: "https://glm.example.com/v1", APIKey: "key1", Model: "glm-4", Active: true, Enabled: true},
			{ID: "sub2", Name: "gpt", Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key2", Model: "gpt-4.1", Active: false, Enabled: true},
		},
	}
	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.activeSubID = "sub1"
	model.cachedModelName = "glm-4"
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{},
		modelLister: &fakeModelLister{
			entries: []protocol.ModelEntry{
				{SubID: "sub1", Model: "glm-4", Status: "normal"},
				{SubID: "sub1", Model: "glm-4-flash", Status: "normal"},
				{SubID: "sub2", Model: "gpt-4.1", Status: "normal"},
			},
		},
	}
	return model
}

// countModelRows counts visible qsModel rows in the panel.
func countModelRows(m *cliModel) int {
	n := 0
	for _, r := range m.quickSwitchRows {
		if r.kind == qsModel {
			n++
		}
	}
	return n
}

// TestTreePanel_DefaultCollapsed verifies that subscriptions appear collapsed
// by default (no model rows visible) except the active subscription which
// auto-expands.
func TestTreePanel_DefaultCollapsed(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	// Active sub (sub1) should be auto-expanded.
	if !model.expandedSubs["sub1"] {
		t.Fatal("expected active sub1 auto-expanded on open")
	}
	// Non-active sub (sub2) should be collapsed.
	if model.expandedSubs["sub2"] {
		t.Fatal("expected sub2 collapsed by default")
	}
	// Model rows from sub1 should be visible; sub2 models should NOT.
	sub1Models := 0
	sub2Models := 0
	for _, r := range model.quickSwitchRows {
		if r.kind == qsModel {
			switch r.subID {
			case "sub1":
				sub1Models++
			case "sub2":
				sub2Models++
			}
		}
	}
	if sub1Models == 0 {
		t.Fatal("expected sub1 model rows visible (auto-expanded)")
	}
	if sub2Models > 0 {
		t.Fatal("expected sub2 model rows NOT visible (collapsed)")
	}
}

// TestTreePanel_ArrowKeysExpandCollapse verifies that →/← expand/collapse
// subscriptions and model rows appear/disappear. Enter also toggles expand/collapse.
func TestTreePanel_ArrowKeysExpandCollapse(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	// Collapse sub1 (currently expanded via auto-expand) with ← key.
	idx1 := findLLMRowBySubID(model, "sub1")
	model.quickSwitchCursor = idx1
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: tea.KeyLeft}) // ← collapse
	if model.expandedSubs["sub1"] {
		t.Fatal("expected sub1 collapsed after ← key")
	}
	if countModelRows(model) != 0 {
		t.Fatalf("expected 0 model rows after collapse, got %d", countModelRows(model))
	}

	// Expand sub1 with → key.
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: tea.KeyRight}) // → expand
	if !model.expandedSubs["sub1"] {
		t.Fatal("expected sub1 expanded after → key")
	}
	if countModelRows(model) == 0 {
		t.Fatal("expected model rows visible after expand")
	}
}

// TestTreePanel_DKeyTogglesEnabled verifies that D on a sub row toggles
// enabled/disabled (moved from Enter).
func TestTreePanel_DKeyTogglesEnabled(t *testing.T) {
	model := newQuickSwitchTestModel()
	mgr := model.subscriptionMgr.(*mockSubscriptionManager)

	model.openQuickSwitch("") // open panel to populate rows
	idx2 := findLLMRowBySubID(model, "sub2")
	model.quickSwitchCursor = idx2
	model.disableCurrentRow() // D → toggle enabled
	if mgr.subs[1].Enabled {
		t.Fatal("expected sub2 disabled after D")
	}

	// D again → re-enable
	idx2 = findLLMRowBySubID(model, "sub2")
	model.quickSwitchCursor = idx2
	model.disableCurrentRow()
	if !mgr.subs[1].Enabled {
		t.Fatal("expected sub2 re-enabled after second D")
	}
}

// TestTreePanel_FilterAutoExpandsMatchingSubs verifies that filter mode
// auto-expands subscriptions with matching models, showing only matching models.
func TestTreePanel_FilterAutoExpandsMatchingSubs(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	// Enter filter mode and type "flash" — should match glm-4-flash in sub1.
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: '/', Text: "/"})
	model.quickSwitchFilterInput.SetValue("flash")
	model.rebuildLLMRows()

	// sub1 should be expanded (has matching model).
	if !model.expandedSubs["sub1"] {
		t.Fatal("expected sub1 auto-expanded in filter mode (matching model)")
	}
	// Only matching model (glm-4-flash) should be visible.
	flashCount := 0
	otherCount := 0
	for _, r := range model.quickSwitchRows {
		if r.kind == qsModel {
			if r.model.Model == "glm-4-flash" {
				flashCount++
			} else {
				otherCount++
			}
		}
	}
	if flashCount != 1 {
		t.Fatalf("expected 1 glm-4-flash row, got %d", flashCount)
	}
	if otherCount != 0 {
		t.Fatalf("expected 0 non-matching model rows, got %d", otherCount)
	}
	// sub2 should not appear (no matching models).
	idx2 := findLLMRowBySubID(model, "sub2")
	if idx2 >= 0 {
		t.Fatal("expected sub2 hidden in filter mode (no matching models)")
	}
}

// TestTreePanel_LeftArrowOnModelJumpsToParentSub verifies that pressing ←
// on a model row collapses the parent sub and moves cursor to the sub row.
func TestTreePanel_LeftArrowOnModelJumpsToParentSub(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	// sub1 is auto-expanded. Find a model row under sub1.
	var modelIdx = -1
	for i, r := range model.quickSwitchRows {
		if r.kind == qsModel && r.subID == "sub1" {
			modelIdx = i
			break
		}
	}
	if modelIdx < 0 {
		t.Fatal("no model row found under sub1")
	}

	model.quickSwitchCursor = modelIdx
	model.handleQuickSwitchKey(tea.KeyPressMsg{Code: tea.KeyLeft}) // ← collapse

	// sub1 should be collapsed.
	if model.expandedSubs["sub1"] {
		t.Fatal("expected sub1 collapsed after ← on model row")
	}
	// Cursor should be on the sub1 row.
	if model.quickSwitchCursor >= len(model.quickSwitchRows) {
		t.Fatal("cursor out of bounds")
	}
	if model.quickSwitchRows[model.quickSwitchCursor].kind != qsSub {
		t.Fatalf("expected cursor on sub row after ←, got kind=%v", model.quickSwitchRows[model.quickSwitchCursor].kind)
	}
	if model.quickSwitchRows[model.quickSwitchCursor].sub.ID != "sub1" {
		t.Fatal("expected cursor on sub1 row")
	}
}

// TestTreePanel_CacheSyncRead is the core regression test for issue #199.
// Panel opens and reads from DB synchronously via cache — no loading placeholder.
func TestTreePanel_CacheSyncRead(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	if model.quickSwitchMode != "llm" {
		t.Fatalf("expected mode=llm, got %s", model.quickSwitchMode)
	}
	// Sub rows should be visible immediately — sync DB read.
	subCount := 0
	for _, r := range model.quickSwitchRows {
		if r.kind == qsSub {
			subCount++
		}
	}
	if subCount != 2 {
		t.Fatalf("expected 2 sub rows immediately, got %d", subCount)
	}
	if !model.llmCache.Loaded() {
		t.Fatal("expected llmCache.Loaded()=true after open")
	}
}

// TestTreePanel_ActiveModelCursor verifies that cursor lands on the active model.
func TestTreePanel_ActiveModelCursor(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	if model.quickSwitchCursor >= len(model.quickSwitchRows) {
		t.Fatal("cursor out of bounds")
	}
	r := model.quickSwitchRows[model.quickSwitchCursor]
	if r.kind != qsModel {
		t.Fatalf("expected cursor on model row, got kind=%v", r.kind)
	}
	if r.model.Model != "glm-4" {
		t.Fatalf("expected cursor on glm-4, got %s", r.model.Model)
	}
}

// TestTreePanel_CacheHitOnReopen verifies subsequent opens use cache.
func TestTreePanel_CacheHitOnReopen(t *testing.T) {
	model := newQuickSwitchTestModel()

	model.openQuickSwitch("")
	subCount := 0
	for _, r := range model.quickSwitchRows {
		if r.kind == qsSub {
			subCount++
		}
	}
	model.quickSwitchMode = ""
	model.openQuickSwitch("")
	subCount2 := 0
	for _, r := range model.quickSwitchRows {
		if r.kind == qsSub {
			subCount2++
		}
	}
	if subCount != subCount2 {
		t.Fatalf("expected %d sub rows on reopen, got %d", subCount, subCount2)
	}
}

// TestTreePanel_RefreshUpdatesCache verifies /models refresh updates cache.
func TestTreePanel_RefreshUpdatesCache(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	newEntries := []protocol.ModelEntry{
		{SubID: "sub1", Model: "glm-4", Status: "normal"},
		{SubID: "sub1", Model: "glm-4-flash", Status: "normal"},
		{SubID: "sub2", Model: "gpt-4.1", Status: "normal"},
	}
	model.handleModelEntriesRefreshed(cliModelEntriesRefreshedMsg{entries: newEntries})

	if len(model.llmCache.Get().entries) != len(newEntries) {
		t.Fatalf("expected %d cached entries after refresh, got %d",
			len(newEntries), len(model.llmCache.Get().entries))
	}
}

// TestTreePanel_FreshReopenSeesAddedSubscription covers the regression where
// adding a subscription updated storage but the open Models panel kept rendering
// the already-loaded llmCache until the CLI restarted.
func TestTreePanel_FreshReopenSeesAddedSubscription(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	mgr := model.subscriptionMgr.(*mockSubscriptionManager)
	mgr.subs = append(mgr.subs, channel.Subscription{ID: "sub3", Name: "new-sub", Provider: "openai", Enabled: true})

	model.reopenLLMPanelOn("sub3", "")

	if idx := findLLMRowBySubID(model, "sub3"); idx < 0 {
		t.Fatal("expected newly added subscription row after fresh reopen")
	}
	if got := len(model.llmCache.Get().subs); got != 3 {
		t.Fatalf("expected cache to contain 3 subscriptions, got %d", got)
	}
}

// TestTreePanel_RefreshReReadsSubscriptions verifies a background /models
// refresh does not combine fresh model entries with a stale subscription list.
func TestTreePanel_RefreshReReadsSubscriptions(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	mgr := model.subscriptionMgr.(*mockSubscriptionManager)
	mgr.subs = append(mgr.subs, channel.Subscription{ID: "sub3", Name: "new-sub", Provider: "openai", Enabled: true})
	newEntries := []protocol.ModelEntry{
		{SubID: "sub1", Model: "glm-4", Status: "normal"},
		{SubID: "sub3", Model: "new-model", Status: "offline"},
	}
	model.handleModelEntriesRefreshed(cliModelEntriesRefreshedMsg{entries: newEntries})

	if idx := findLLMRowBySubID(model, "sub3"); idx < 0 {
		t.Fatal("expected refreshed panel to include subscription added while refresh was in flight")
	}
	if got := len(model.llmCache.Get().subs); got != 3 {
		t.Fatalf("expected cache to contain 3 subscriptions after refresh, got %d", got)
	}
	if got := len(model.llmCache.Get().entries); got != len(newEntries) {
		t.Fatalf("expected %d refreshed entries, got %d", len(newEntries), got)
	}
}

// TestTreePanel_SessionSwitchClearsCacheAndExpanded verifies that switching
// sessions invalidates both cache and expansion state.
func TestTreePanel_SessionSwitchClearsCacheAndExpanded(t *testing.T) {
	model := newQuickSwitchTestModel()
	model.openQuickSwitch("")

	model.llmCache.Invalidate()
	model.expandedSubs = make(map[string]bool)

	if model.llmCache.Loaded() {
		t.Fatal("expected cache invalidated")
	}
	if len(model.expandedSubs) != 0 {
		t.Fatal("expected expandedSubs cleared")
	}

	model.openQuickSwitch("")
	if !model.llmCache.Loaded() {
		t.Fatal("expected cache reloaded after reopen")
	}
	// Active sub should be re-expanded.
	if !model.expandedSubs["sub1"] {
		t.Fatal("expected active sub auto-expanded after reopen")
	}
}
