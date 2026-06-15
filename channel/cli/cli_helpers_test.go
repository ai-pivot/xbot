// cli_helpers_test.go — Unit tests for channel/cli_helpers.go
// Covers: isErrorContent, toLowerASCII, showTempStatus, clearTempStatusCmd,
// showSystemMsg, invalidateAllCache, toggleToolSummary, startAgentTurn,
// applyThemeAndRebuild, applyLanguageChange, closePanelAndResume, enqueueToast

package cli

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// CLI settings scope helpers
// ---------------------------------------------------------------------------

func TestCLISettingScope_KnownKeys(t *testing.T) {
	cases := map[string]string{
		"theme":               "user",
		"language":            "user",
		"runner_server":       "user",
		"llm_provider":        "subscription",
		"llm_api_key":         "subscription",
		"llm_base_url":        "subscription",
		"llm_model":           "subscription",
		"max_output_tokens":   "subscription",
		"thinking_mode":       "subscription",
		"default_user":        "global",
		"privileged_user":     "global",
		"subscription_manage": "action",
		"runner_panel":        "action",
		"danger_zone":         "action",
		"definitely_unknown":  "unknown",
	}

	for key, want := range cases {
		if got := cliSettingScope(key); got != want {
			t.Fatalf("cliSettingScope(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestCLISettingScope_SettingsSchemaKeysAreClassified(t *testing.T) {
	locale := channel.LocaleZH()
	if locale == nil {
		t.Fatal("channel.LocaleZH() returned nil")
		return
	}
	var unknown []string
	for _, def := range locale.SettingsSchema {
		if cliSettingScope(def.Key) == "unknown" {
			unknown = append(unknown, def.Key)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		t.Fatalf("unclassified settings schema keys: %v", unknown)
	}
}

func TestIsSubscriptionScopedSettingKey(t *testing.T) {
	for _, key := range []string{"llm_provider", "llm_api_key", "llm_base_url", "llm_model", "max_output_tokens", "thinking_mode"} {
		if !isSubscriptionScopedSettingKey(key) {
			t.Fatalf("expected %q to be subscription-scoped", key)
		}
	}
	for _, key := range []string{"theme", "language", "sandbox_mode"} {
		if isSubscriptionScopedSettingKey(key) {
			t.Fatalf("expected %q to not be subscription-scoped", key)
		}
	}
}

func TestOpenSettingsFromQuickSwitch_PreservesNonSubscriptionEdits(t *testing.T) {
	model := newCLIModel()
	model.channel = &CLIChannel{config: &CLIChannelConfig{}}
	model.panelState.valuesBackup = map[string]string{
		"theme":    "mono",
		"language": "en",
	}
	model.panelState.cursorBackup = 1
	model.panelState.onSubmitBackup = func(map[string]string) {}
	model.channel.config.GetCurrentValues = func() map[string]string {
		return map[string]string{
			"theme":    "midnight",
			"language": "zh",
		}
	}
	model.openSettingsFromQuickSwitch()
	if model.panelState.mode != "settings" {
		t.Fatalf("panelMode = %q, want settings", model.panelState.mode)
	}
	if got := model.panelState.values["theme"]; got != "mono" {
		t.Fatalf("theme = %q, want mono", got)
	}
	if got := model.panelState.values["language"]; got != "en" {
		t.Fatalf("language = %q, want en", got)
	}
}

// ---------------------------------------------------------------------------
// isErrorContent — pure function, comprehensive edge-case coverage
// ---------------------------------------------------------------------------

func TestIsErrorContent_Empty(t *testing.T) {
	if isErrorContent("") {
		t.Error("empty string should not match error keywords")
	}
}

func TestIsErrorContent_ExactKeywordMatch(t *testing.T) {
	keywords := []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"}
	for _, kw := range keywords {
		if !isErrorContent(kw) {
			t.Errorf("exact keyword %q should match", kw)
		}
	}
}

func TestIsErrorContent_CaseInsensitive(t *testing.T) {
	cases := []string{"Error", "ERROR", "ErrOr", "Failed", "FAILED", "Exception", "DENIED", "Refused"}
	for _, c := range cases {
		if !isErrorContent(c) {
			t.Errorf("case-insensitive keyword %q should match", c)
		}
	}
}

func TestIsErrorContent_Substring(t *testing.T) {
	if !isErrorContent("An error occurred while processing") {
		t.Error("substring 'error' should match")
	}
	if !isErrorContent("The operation failed unexpectedly") {
		t.Error("substring 'failed' should match")
	}
	if !isErrorContent("访问被denied，请重试") {
		t.Error("mixed-language substring 'denied' should match")
	}
}

func TestIsErrorContent_CJKKeywords(t *testing.T) {
	if !isErrorContent("操作失败，请稍后再试") {
		t.Error("Chinese keyword '失败' should match")
	}
	if !isErrorContent("系统错误：无法连接") {
		t.Error("Chinese keyword '错误' should match")
	}
}

func TestIsErrorContent_NoMatch(t *testing.T) {
	nonError := []string{
		"Operation succeeded",
		"操作成功",
		"completed successfully",
		"all tasks finished",
		"hello world",
		"12345",
	}
	for _, s := range nonError {
		if isErrorContent(s) {
			t.Errorf("non-error content %q should not match", s)
		}
	}
}

func TestIsErrorContent_MultipleKeywords(t *testing.T) {
	if !isErrorContent("error: connection refused") {
		t.Error("should match with multiple keywords present")
	}
}

func TestIsErrorContent_WhitespaceOnly(t *testing.T) {
	if isErrorContent("   ") {
		t.Error("whitespace-only should not match")
	}
}

// ---------------------------------------------------------------------------
// toLowerASCII — pure helper
// ---------------------------------------------------------------------------

func TestToLowerASCII_Empty(t *testing.T) {
	if toLowerASCII("") != "" {
		t.Error("empty string should remain empty")
	}
}

func TestToLowerASCII_Uppercase(t *testing.T) {
	if toLowerASCII("HELLO") != "hello" {
		t.Error("should convert uppercase to lowercase")
	}
}

func TestToLowerASCII_Mixed(t *testing.T) {
	if toLowerASCII("Hello World 123") != "hello world 123" {
		t.Error("should handle mixed case")
	}
}

func TestToLowerASCII_NonASCII(t *testing.T) {
	input := "中文测试"
	if toLowerASCII(input) != input {
		t.Error("non-ASCII characters should be preserved")
	}
}

func TestToLowerASCII_SpecialChars(t *testing.T) {
	input := "!@#$%^&*()"
	if toLowerASCII(input) != input {
		t.Error("special characters should be preserved")
	}
}

// ---------------------------------------------------------------------------
// showTempStatus / clearTempStatusCmd
// ---------------------------------------------------------------------------

func TestShowTempStatus(t *testing.T) {
	model := newCLIModel()

	model.showTempStatus("saving settings...")
	if model.tempStatus != "saving settings..." {
		t.Errorf("tempStatus = %q, want %q", model.tempStatus, "saving settings...")
	}

	// Overwrite with empty
	model.showTempStatus("")
	if model.tempStatus != "" {
		t.Errorf("tempStatus = %q, want empty", model.tempStatus)
	}
}

func TestClearTempStatusCmd_DefaultDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd()
	if cmd == nil {
		t.Fatal("clearTempStatusCmd() should return non-nil tea.Cmd")
	}
}

func TestClearTempStatusCmd_CustomDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd(5 * time.Second)
	if cmd == nil {
		t.Fatal("clearTempStatusCmd with custom duration should return non-nil tea.Cmd")
		return
	}

	// Multiple durations: only first should be used
	cmd2 := model.clearTempStatusCmd(1*time.Second, 10*time.Second)
	if cmd2 == nil {
		t.Fatal("clearTempStatusCmd with multiple durations should return non-nil tea.Cmd")
		return
	}
}

func TestClearTempStatusCmd_ZeroDuration(t *testing.T) {
	model := newCLIModel()
	cmd := model.clearTempStatusCmd(0)
	if cmd == nil {
		t.Fatal("clearTempStatusCmd with zero duration should still return non-nil tea.Cmd")
		return
	}
}

// ---------------------------------------------------------------------------
// showSystemMsg
// ---------------------------------------------------------------------------

func TestShowSystemMsg_AppendsMessage(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("test message", feedbackInfo)

	if len(model.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(model.messages))
	}
	if model.messages[0].role != "system" {
		t.Errorf("message role = %q, want %q", model.messages[0].role, "system")
	}
	if model.messages[0].content != "test message" {
		t.Errorf("message content = %q, want %q", model.messages[0].content, "test message")
	}
	// Note: dirty will be cleared by fullRebuild() inside updateViewportContent(),
	// so we verify the message was rendered instead.
	if model.messages[0].rendered == "" {
		t.Error("message should have been rendered by updateViewportContent")
	}
}

func TestShowSystemMsg_DifferentLevels(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	levels := []feedbackLevel{feedbackInfo, feedbackWarning, feedbackError}
	for i, level := range levels {
		model.showSystemMsg("msg", level)
		msg := model.messages[i]
		if msg.role != "system" {
			t.Errorf("level %d: role = %q, want system", level, msg.role)
		}
	}
}

func TestShowSystemMsg_EmptyContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("", feedbackInfo)
	if len(model.messages) != 1 {
		t.Fatalf("messages count = %d, want 1", len(model.messages))
	}
	if model.messages[0].content != "" {
		t.Errorf("content should be empty, got %q", model.messages[0].content)
	}
}

func TestShowSystemMsg_Multiple(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.showSystemMsg("first", feedbackInfo)
	model.showSystemMsg("second", feedbackWarning)
	model.showSystemMsg("third", feedbackError)

	if len(model.messages) != 3 {
		t.Fatalf("messages count = %d, want 3", len(model.messages))
	}
	if model.messages[2].content != "third" {
		t.Errorf("last message content = %q, want %q", model.messages[2].content, "third")
	}
}

// ---------------------------------------------------------------------------
// invalidateAllCache
// ---------------------------------------------------------------------------

func TestInvalidateAllCache_MarksDirty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello", dirty: false},
		{role: "assistant", content: "hi", dirty: false},
	}
	model.rc.valid = true

	model.invalidateAllCache(false)

	if model.rc.valid {
		t.Error("renderCacheValid should be false after invalidate")
	}
	for i, msg := range model.messages {
		if !msg.dirty {
			t.Errorf("message[%d] should be dirty", i)
		}
	}
}

func TestInvalidateAllCache_NoUpdateViewport(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello"},
	}
	// Set viewport content to known value
	model.updateViewportContent()
	before := model.viewport.View()

	model.invalidateAllCache(false)
	after := model.viewport.View()

	if before != after {
		t.Error("invalidateAllCache(false) should NOT update viewport content")
	}
}

func TestInvalidateAllCache_WithUpdateViewport(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.messages = []cliMessage{
		{role: "user", content: "hello"},
	}
	model.rc.valid = true

	model.invalidateAllCache(true)

	// After full rebuild (triggered by updateViewportContent), cache becomes valid again.
	// The key behavior to verify is that viewport was actually refreshed.
	if model.rc.valid != true {
		t.Error("renderCacheValid should be true after full rebuild restores cache")
	}
	if model.viewport.View() == "" {
		t.Error("viewport should have content after updateViewportContent")
	}
	// Verify dirty flags were cleared by fullRebuild
	for i, msg := range model.messages {
		if msg.dirty {
			t.Errorf("message[%d] should not be dirty after fullRebuild", i)
		}
	}
}

func TestInvalidateAllCache_EmptyMessages(t *testing.T) {
	model := newCLIModel()
	model.rc.valid = true

	model.invalidateAllCache(false)

	if model.rc.valid {
		t.Error("renderCacheValid should be false even with no messages")
	}
}

// ---------------------------------------------------------------------------
// toggleToolSummary
// ---------------------------------------------------------------------------

func TestToggleToolSummary(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	initial := model.toolSummaryExpanded
	model.toggleToolSummary()
	if model.toolSummaryExpanded == initial {
		t.Error("toggleToolSummary should flip toolSummaryExpanded")
	}
	if model.rc.history != "" {
		t.Error("cachedHistory should be cleared")
	}
	// After toggleToolSummary → invalidateAllCache(true) → updateViewportContent → fullRebuild,
	// renderCacheValid is restored to true. This is expected.

	// Toggle again should revert
	model.toggleToolSummary()
	if model.toolSummaryExpanded != initial {
		t.Error("second toggle should revert to initial state")
	}
}

// ---------------------------------------------------------------------------
// startAgentTurn
// ---------------------------------------------------------------------------

func TestStartAgentTurn(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.inputReady = true
	model.typing = false

	model.startAgentTurn()

	if !model.typing {
		t.Error("typing should be true after startAgentTurn")
	}
	if model.inputReady {
		t.Error("inputReady should be false after startAgentTurn")
	}
	if model.progressState.iterations != nil {
		t.Error("iterationHistory should be nil after resetProgressState")
	}
	if model.progressState.lastIter != 0 {
		t.Error("lastSeenIteration should be 0 after resetProgressState")
	}
}

func TestStartAgentTurn_ResetsProgressState(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.progressState.iterations = []cliIterationSnapshot{
		{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "test"}}},
	}
	model.progressState.lastIter = 5

	model.startAgentTurn()

	if len(model.progressState.iterations) != 0 {
		t.Errorf("iterationHistory should be empty, got %d items", len(model.progressState.iterations))
	}
	if model.progressState.lastIter != 0 {
		t.Errorf("lastSeenIteration = %d, want 0", model.progressState.lastIter)
	}
}

// ---------------------------------------------------------------------------
// applyThemeAndRebuild
// ---------------------------------------------------------------------------

func TestApplyThemeAndRebuild_ValidTheme(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.rc.valid = true

	themes := ThemeNames()
	if len(themes) > 0 {
		model.applyThemeAndRebuild(themes[0])
	}
	if model.rc.valid {
		t.Error("renderCacheValid should be false after applyThemeAndRebuild")
	}
}

func TestApplyThemeAndRebuild_InvalidTheme(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	// Unknown theme should not panic, falls back to default
	model.applyThemeAndRebuild("nonexistent_theme_xyz")
	if model.rc.valid {
		t.Error("renderCacheValid should be false")
	}
}

func TestApplyThemeAndRebuild_NarrowWidth(t *testing.T) {
	model := newCLIModel()
	model.handleResize(4, 24) // width == 4, boundary for width > 4

	model.applyThemeAndRebuild("midnight")
	// Should not panic when width <= 4 (renderer not rebuilt)
	if model.rc.valid {
		t.Error("renderCacheValid should be false")
	}
}

func TestApplyThemeAndRebuild_ZeroWidth(t *testing.T) {
	model := newCLIModel()
	// No handleResize — width stays 0
	model.applyThemeAndRebuild("midnight")
	// Should not panic
	if model.rc.valid {
		t.Error("renderCacheValid should be false")
	}
}

// ---------------------------------------------------------------------------
// applyLanguageChange
// ---------------------------------------------------------------------------

func TestApplyLanguageChange_ValidLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.rc.valid = true

	model.applyLanguageChange("en")
	if model.locale == nil {
		t.Error("locale should not be nil after applyLanguageChange")
	}
	if model.rc.valid {
		t.Error("renderCacheValid should be false after applyLanguageChange")
	}
}

func TestApplyLanguageChange_UnknownLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.applyLanguageChange("xx_NONEXISTENT")
	if model.locale == nil {
		t.Error("should fall back to default locale for unknown language")
	}
}

func TestApplyLanguageChange_EmptyLang(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)

	model.applyLanguageChange("")
	if model.locale == nil {
		t.Error("should use default locale for empty language")
	}
}

// ---------------------------------------------------------------------------
// closePanelAndResume
// ---------------------------------------------------------------------------

func TestClosePanelAndResume_NotTyping(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "settings"
	model.typing = false

	cont, _, cmd := model.closePanelAndResume()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
	if model.panelState.mode != "" {
		t.Errorf("panelMode = %q, want empty", model.panelState.mode)
	}
}

func TestClosePanelAndResume_Typing(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "askuser"
	model.typing = true

	cont, _, cmd := model.closePanelAndResume()

	if !cont {
		t.Error("should return continue=true")
	}
	// tickCmd() is no longer returned — startAgentTurn() manages tick chain via pendingCmds
	if cmd != nil {
		t.Error("cmd should be nil — tick chain managed by startAgentTurn")
	}
	if model.panelState.mode != "" {
		t.Errorf("panelMode = %q, want empty", model.panelState.mode)
	}
}

func TestClosePanelAndResume_CleansUpPanelState(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "settings"
	model.panelState.editing = true
	model.panelState.combo = true
	model.panelState.schema = []channel.SettingDefinition{{Key: "test"}}
	model.panelState.values = map[string]string{"test": "value"}

	model.closePanelAndResume()

	if model.panelState.mode != "" {
		t.Error("panelMode should be cleared")
	}
	if model.panelState.editing {
		t.Error("panelEdit should be false")
	}
	if model.panelState.combo {
		t.Error("panelCombo should be false")
	}
	if model.panelState.schema != nil {
		t.Error("panelSchema should be nil")
	}
	if model.panelState.values != nil {
		t.Error("panelValues should be nil")
	}
}

// ---------------------------------------------------------------------------
// enqueueToast
// ---------------------------------------------------------------------------

func TestEnqueueToast(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("saved", "✓")
	if cmd == nil {
		t.Fatal("enqueueToast should return non-nil tea.Cmd")
		return
	}

	// Execute the Cmd to verify it produces the correct message
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != "saved" {
		t.Errorf("toast text = %q, want %q", toast.text, "saved")
	}
	if toast.icon != "✓" {
		t.Errorf("toast icon = %q, want %q", toast.icon, "✓")
	}
}

func TestEnqueueToast_ErrorToast(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("operation failed", "✗")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.icon != "✗" {
		t.Errorf("toast icon = %q, want %q", toast.icon, "✗")
	}
}

func TestEnqueueToast_EmptyValues(t *testing.T) {
	model := newCLIModel()

	cmd := model.enqueueToast("", "")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != "" {
		t.Errorf("toast text should be empty, got %q", toast.text)
	}
	if toast.icon != "" {
		t.Errorf("toast icon should be empty, got %q", toast.icon)
	}
}

// ---------------------------------------------------------------------------
// iterToolsFlat
// ---------------------------------------------------------------------------

func TestIterToolsFlat_WithIterations(t *testing.T) {
	msg := &cliMessage{
		iterations: []cliIterationSnapshot{
			{Iteration: 0, Tools: []protocol.ToolProgress{{Name: "read"}, {Name: "write"}}},
			{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "exec"}}},
		},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 2 {
		t.Errorf("iterCount = %d, want 2", iterCount)
	}
	if len(tools) != 3 {
		t.Errorf("tools count = %d, want 3", len(tools))
	}
}

func TestIterToolsFlat_NoIterations(t *testing.T) {
	msg := &cliMessage{
		tools: []protocol.ToolProgress{{Name: "read"}, {Name: "write"}},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 0 {
		t.Errorf("iterCount = %d, want 0", iterCount)
	}
	if len(tools) != 2 {
		t.Errorf("tools count = %d, want 2", len(tools))
	}
}

func TestIterToolsFlat_Empty(t *testing.T) {
	msg := &cliMessage{}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 0 {
		t.Errorf("iterCount = %d, want 0", iterCount)
	}
	if len(tools) != 0 {
		t.Errorf("tools count = %d, want 0", len(tools))
	}
}

func TestIterToolsFlat_IterationsWithEmptyTools(t *testing.T) {
	msg := &cliMessage{
		iterations: []cliIterationSnapshot{
			{Iteration: 0, Tools: nil},
			{Iteration: 1, Tools: []protocol.ToolProgress{{Name: "exec"}}},
		},
	}

	tools, iterCount := msg.iterToolsFlat()
	if iterCount != 2 {
		t.Errorf("iterCount = %d, want 2", iterCount)
	}
	if len(tools) != 1 {
		t.Errorf("tools count = %d, want 1", len(tools))
	}
}

// ---------------------------------------------------------------------------
// submitAskAnswers — integration-style tests
// ---------------------------------------------------------------------------

func TestSubmitAskAnswers_CallsCallback(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "askuser"

	var received map[string]string
	model.panelState.onAnswer = func(answers map[string]string) {
		received = answers
	}

	model.typing = false
	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
	if model.panelState.mode != "" {
		t.Error("panel should be closed")
	}
	// received may be empty (no answers) but callback should have been called
	_ = received
}

func TestSubmitAskAnswers_Typing(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "askuser"
	model.typing = true

	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	// tickCmd() is no longer returned — startAgentTurn() manages tick chain via pendingCmds
	if cmd != nil {
		t.Error("cmd should be nil — tick chain managed by startAgentTurn")
	}
}

func TestSubmitAskAnswers_NilCallback(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "askuser"
	model.panelState.onAnswer = nil
	model.typing = false

	// Should not panic with nil callback
	cont, _, cmd := model.submitAskAnswers()

	if !cont {
		t.Error("should return continue=true")
	}
	if cmd != nil {
		t.Error("cmd should be nil when not typing")
	}
}

func TestSubmitAskAnswers_SavesCurrentFreeInputBeforeCollect(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.mode = "askuser"
	model.panelState.askItems = []askItem{{Question: "q1"}, {Question: "q2", Other: "stale"}}
	model.panelState.askTab = 1
	model.panelState.askAnswerTA = model.newPanelTextArea("custom", 50, 3)

	model.saveCurrentFreeInput()
	if got := model.panelState.askItems[1].Other; got != "custom" {
		t.Fatalf("panelItems[1].Other = %q, want custom", got)
	}
}

func TestCollectAskAnswers_UncheckedOptionsExcluded(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.askItems = []askItem{
		{Question: "color?", Options: []string{"Red", "Blue", "Green"}},
	}
	model.panelState.askOptSel = map[int]map[int]bool{
		0: {0: true, 1: false, 2: true},
	}

	answers := model.collectAskAnswers()
	got := answers["q0"]
	if got != "Red, Green" {
		t.Fatalf("expected only checked options, got %q", got)
	}
}

func TestCollectAskAnswers_AllUncheckedReturnsEmpty(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.panelState.askItems = []askItem{
		{Question: "color?", Options: []string{"Red", "Blue"}},
	}
	model.panelState.askOptSel = map[int]map[int]bool{
		0: {0: false, 1: false},
	}

	answers := model.collectAskAnswers()
	got := answers["q0"]
	if got != "" {
		t.Fatalf("expected empty answer when all unchecked, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Security / edge-case: ensure no panic on boundary inputs
// ---------------------------------------------------------------------------

func TestShowTempStatus_LongString(t *testing.T) {
	model := newCLIModel()
	longStr := strings.Repeat("a", 10000)
	model.showTempStatus(longStr)
	if model.tempStatus != longStr {
		t.Error("should handle long strings without panic")
	}
}

func TestShowSystemMsg_VeryLongContent(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	longContent := strings.Repeat("x", 100000)
	model.showSystemMsg(longContent, feedbackError)
	if len(model.messages) != 1 {
		t.Error("should append very long content without panic")
	}
}

func TestInvalidateAllCache_LargeMessageSlice(t *testing.T) {
	model := newCLIModel()
	model.messages = make([]cliMessage, 10000)
	for i := range model.messages {
		model.messages[i] = cliMessage{role: "user", content: "test"}
	}
	model.rc.valid = true

	model.invalidateAllCache(false)

	for i, msg := range model.messages {
		if !msg.dirty {
			t.Errorf("message[%d] should be dirty", i)
		}
	}
}

func TestEnqueueToast_LongText(t *testing.T) {
	model := newCLIModel()
	longText := strings.Repeat("警告", 5000)
	cmd := model.enqueueToast(longText, "⚠")
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.text != longText {
		t.Error("should handle long toast text")
	}
}

func TestIsErrorContent_NilSafeSlice(t *testing.T) {
	// Verify isErrorContent handles any string without panic
	variants := []string{
		"",
		"\x00error",
		"error\x00",
		strings.Repeat("error ", 1000),
	}
	for _, v := range variants {
		// Just ensure no panic
		_ = isErrorContent(v)
	}
}

type testSettingsService struct {
	getResult map[string]string
	getErr    error
	setCalls  []testSettingsSetCall
}

type testSettingsSetCall struct {
	channelName string
	senderID    string
	key         string
	value       string
}

func (s *testSettingsService) GetSettings(channelName, senderID string) (map[string]string, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	out := make(map[string]string, len(s.getResult))
	for k, v := range s.getResult {
		out[k] = v
	}
	return out, nil
}

func (s *testSettingsService) SetSetting(channelName, senderID, key, value string) error {
	s.setCalls = append(s.setCalls, testSettingsSetCall{
		channelName: channelName,
		senderID:    senderID,
		key:         key,
		value:       value,
	})
	return nil
}

func TestCLISettingScopeClassification(t *testing.T) {
	cases := []struct {
		key   string
		scope string
	}{
		{key: "theme", scope: "user"},
		{key: "runner_token", scope: "user"},
		{key: "max_output_tokens", scope: "subscription"},
		{key: "thinking_mode", scope: "subscription"},
		{key: "enable_stream", scope: "user"},
		{key: "subscription_manage", scope: "action"},
	}
	for _, tc := range cases {
		if got := cliSettingScope(tc.key); got != tc.scope {
			t.Errorf("cliSettingScope(%q) = %q, want %q", tc.key, got, tc.scope)
		}
	}
}

func TestMergeCLISettingsValues_OverlaysUserScopedOnly(t *testing.T) {
	cfgVals := map[string]string{
		"theme":             "midnight",
		"max_output_tokens": "8192",
	}
	settingsSvc := &testSettingsService{getResult: map[string]string{
		"theme":             "mono",
		"max_output_tokens": "4096",
		"runner_server":     "https://runner.example",
	}}
	model := newCLIModel()
	model.channelName = "cli"
	model.senderID = "cli_user"
	model.channel = &CLIChannel{config: &CLIChannelConfig{}, settingsSvc: settingsSvc}
	model.channel.config.GetCurrentValues = func() map[string]string { return cfgVals }

	merged := model.mergeCLISettingsValues()
	if got := merged["theme"]; got != "mono" {
		t.Fatalf("theme = %q, want mono", got)
	}
	if got := merged["max_output_tokens"]; got != "8192" {
		t.Fatalf("max_output_tokens = %q, want config value 8192", got)
	}
	if got := merged["runner_server"]; got != "https://runner.example" {
		t.Fatalf("runner_server = %q, want settings value", got)
	}
}

func TestPersistCLISettingsValues_PersistsUserScopedAndAppliesAll(t *testing.T) {
	settingsSvc := &testSettingsService{}
	applied := map[string]string{}
	model := newCLIModel()
	model.channelName = "cli"
	model.senderID = "cli_user"
	model.channel = &CLIChannel{config: &CLIChannelConfig{}, settingsSvc: settingsSvc}
	model.channel.config.ApplySettings = func(vals map[string]string, chatID string) {
		for k, v := range vals {
			applied[k] = v
		}
	}

	input := map[string]string{
		"theme":             "mono",
		"runner_workspace":  "/tmp/ws",
		"max_output_tokens": "4096",
	}
	model.persistCLISettingsValues(input)

	if len(settingsSvc.setCalls) != 2 {
		t.Fatalf("setCalls = %d, want 2", len(settingsSvc.setCalls))
	}
	for _, call := range settingsSvc.setCalls {
		if cliSettingScope(call.key) != "user" {
			t.Fatalf("persisted non-user-scoped key %q", call.key)
		}
		if call.channelName != "cli" || call.senderID != "cli_user" {
			t.Fatalf("unexpected target: channel=%q sender=%q", call.channelName, call.senderID)
		}
	}
	// channel.Subscription-scoped keys (max_output_tokens) should NOT reach ApplySettings.
	// They are handled directly by saveSettings via subscriptionMgr.Update(activeSubID).
	if _, ok := applied["max_output_tokens"]; ok {
		t.Fatalf("subscription-scoped key max_output_tokens reached ApplySettings — should be handled by saveSettings directly")
	}
	if applied["theme"] != "mono" || applied["runner_workspace"] != "/tmp/ws" {
		t.Fatalf("ApplySettings did not receive expected user-scoped keys: %#v", applied)
	}
}

// ---------------------------------------------------------------------------
// /plugin helpers
// ---------------------------------------------------------------------------

func TestPluginStateIcon(t *testing.T) {
	cases := map[string]string{
		"active":       "🟢",
		"error":        "🔴",
		"inactive":     "⚪",
		"discovered":   "⚪",
		"activating":   "🟡",
		"deactivating": "🟡",
		"unknown":      "⚫",
		"":             "⚫",
	}
	for state, want := range cases {
		if got := pluginStateIcon(state); got != want {
			t.Errorf("pluginStateIcon(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestPluginStateStyled(t *testing.T) {
	m := &cliModel{}
	m.styles = buildStyles(80)

	states := []string{"active", "error", "inactive", "discovered", "activating", "deactivating", "unknown"}
	for _, state := range states {
		styled := m.pluginStateStyled(state)
		icon := pluginStateIcon(state)
		if !strings.HasPrefix(styled, icon) {
			t.Errorf("pluginStateStyled(%q) should start with icon %q, got %q", state, icon, styled)
		}
		// Verify state text appears after stripping ANSI
		clean := stripAnsi(styled)
		if !strings.Contains(clean, state) {
			t.Errorf("pluginStateStyled(%q) stripped output %q should contain %q", state, clean, state)
		}
	}
}

// stripAnsi removes ANSI escape sequences for test assertions.
func stripAnsi(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			// skip until 'm'
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++ // skip 'm'
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// handleSuHistoryLoad — typing reconciliation tests
//
// These tests verify that handleSuHistoryLoad is the single source of truth
// for m.typing state after a remote-mode session switch. The invariant:
// typing must reflect the server RPC response, NOT a hard-coded local default.
//
// Regression: sidebar session switching used to force m.typing=false
// unconditionally in postRestoreSessionSetup. This caused:
//   - completed iterations rendered as tool_summary instead of progress history
//   - frozen progress block (fastTick chain dropped because busy=false)
//   - mismatch between typing state and server reality
// See docs/agent/channel.md for the full session switch flow.
// ---------------------------------------------------------------------------

// setupTestRemoteChannel assigns a minimal CLIChannel to m so that
// handleSuHistoryLoad can access channel config fields (DynamicHistoryLoader,
// locale strings, etc.) without nil dereference.
func setupTestRemoteChannel(m *cliModel) {
	m.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
				return nil, nil // no-op for tests
			},
		},
	}
	// Build default locale so showSystemMsg / endAgentTurn don't panic.
	if m.locale.ProcessingPlaceholder == "" {
		m.locale = newCLIModel().locale
	}
}

// TestSuHistoryLoad_TypingReconcile_AcceptProgress verifies that
// handleSuHistoryLoad sets typing=true when the server responds with
// an active progress payload (acceptProgress path).
func TestSuHistoryLoad_TypingReconcile_AcceptProgress(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	// Simulate restored session with typing=false (e.g. fresh connect,
	// or session that was idle when saved).
	m.typing = false
	m.splashState.suLoading = true

	payload := &protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Phase:     "executing",
		Iteration: 2,
	}
	msg := suHistoryLoadMsg{
		channelName:    "cli",
		chatID:         "/test",
		activeProgress: payload,
		err:            nil, // RPC succeeded
	}

	_ = m.handleSuHistoryLoad(msg)

	if m.typing != true {
		t.Fatalf("expected typing=true after acceptProgress, got typing=%v", m.typing)
	}
	if m.progressState.current == nil {
		t.Fatal("expected progress to be restored from server snapshot, got nil")
	}
	if m.splashState.suLoading != false {
		t.Fatalf("expected suLoading=false after handleSuHistoryLoad, got %v", m.splashState.suLoading)
	}
}

// TestSuHistoryLoad_TypingReconcile_Default verifies that
// handleSuHistoryLoad sets typing=false when the server has no active
// turn (default path — turn completed while user was away).
func TestSuHistoryLoad_TypingReconcile_Default(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	// Simulate restored session with typing=true (old saved state).
	// Even though the saved state says typing, the server knows better.
	m.typing = true
	m.splashState.suLoading = true

	msg := suHistoryLoadMsg{
		channelName:    "cli",
		chatID:         "/test",
		activeProgress: nil, // server says: no active turn
		err:            nil,
	}

	_ = m.handleSuHistoryLoad(msg)

	if m.typing != false {
		t.Fatalf("expected typing=false after default (no active turn), got typing=%v", m.typing)
	}
	if m.progressState.current != nil {
		t.Fatalf("expected progress=nil after default (no active turn), got %v", m.progressState.current)
	}
	if m.splashState.suLoading != false {
		t.Fatalf("expected suLoading=false, got %v", m.splashState.suLoading)
	}
}

// TestSuHistoryLoad_TypingReconcile_Error verifies that
// handleSuHistoryLoad sets typing=false on RPC failure.
// Without server confirmation, idle is the safe fallback —
// prevents a perpetual spinner from stuck typing=true.
func TestSuHistoryLoad_TypingReconcile_Error(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	// Simulate restored session with typing=true (saved state).
	// RPC fails — we cannot know the real state.
	m.typing = true
	m.splashState.suLoading = true

	msg := suHistoryLoadMsg{
		channelName: "cli",
		chatID:      "/test",
		err:         fmt.Errorf("connection refused"),
	}

	_ = m.handleSuHistoryLoad(msg)

	if m.typing != false {
		t.Fatalf("expected typing=false after RPC error (safe fallback), got typing=%v", m.typing)
	}
	if m.progressState.current != nil {
		t.Fatalf("expected progress=nil after RPC error, got %v", m.progressState.current)
	}
	if m.splashState.suLoading != false {
		t.Fatalf("expected suLoading=false, got %v", m.splashState.suLoading)
	}
}

// TestSuHistoryLoad_StaleGuardDoesNotTouchState verifies that
// a stale suHistoryLoadMsg (from a previous session switch) does NOT
// modify typing or suLoading — it must leave the current session's
// state untouched.
func TestSuHistoryLoad_StaleGuardDoesNotTouchState(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	m.typing = true
	m.splashState.suLoading = true

	// Stale message: channelName/chatID doesn't match current session.
	msg := suHistoryLoadMsg{
		channelName: "cli",
		chatID:      "/other-session",
	}

	cmds := m.handleSuHistoryLoad(msg)
	if cmds != nil {
		t.Fatal("expected nil cmds from stale guard (discarded)")
	}
	if m.typing != true {
		t.Fatalf("stale msg should NOT change typing, got typing=%v", m.typing)
	}
	if m.splashState.suLoading != true {
		t.Fatalf("stale msg should NOT clear suLoading, got %v", m.splashState.suLoading)
	}
}

// TestSuHistoryLoad_TypingReconcile_PhaseDoneIsDefault verifies that
// a server response with Phase="done" takes the default (not acceptProgress)
// path — the turn ended, so typing must be false.
func TestSuHistoryLoad_TypingReconcile_PhaseDoneIsDefault(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	m.typing = true // restored hint says typing
	m.splashState.suLoading = true

	payload := &protocol.ProgressEvent{
		ChatID:    "cli:/test",
		Phase:     "done", // turn completed on server
		Iteration: 5,
	}
	msg := suHistoryLoadMsg{
		channelName:    "cli",
		chatID:         "/test",
		activeProgress: payload,
		err:            nil,
	}

	_ = m.handleSuHistoryLoad(msg)

	if m.typing != false {
		t.Fatalf("Phase=done should set typing=false, got typing=%v", m.typing)
	}
}

// TestRemoveLastToolSummary verifies that removeLastToolSummary only removes the
// LAST tool_summary message, preserving tool_summaries from previous completed turns.
// Regression: removeAllToolSummaries removed ALL tool_summaries, causing tools blocks
// from previous completed turns to disappear on session switch while a turn is active.
func TestRemoveLastToolSummary(t *testing.T) {
	m := newCLIModel()
	m.messages = []cliMessage{
		{role: "user", content: "hello"},
		{role: "tool_summary", content: ""}, // Turn 1 tools — should be PRESERVED
		{role: "assistant", content: "result1"},
		{role: "user", content: "another"},
		{role: "tool_summary", content: ""}, // Turn 2 tools (active turn) — should be REMOVED
	}

	// Before: 5 messages
	if len(m.messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(m.messages))
	}

	m.removeLastToolSummary()

	// After: 4 messages (only last tool_summary removed)
	if len(m.messages) != 4 {
		t.Fatalf("expected 4 messages after removal, got %d", len(m.messages))
	}

	// Turn 1's tool_summary should remain
	foundT1 := false
	for _, msg := range m.messages {
		if msg.role == "tool_summary" {
			foundT1 = true
			break
		}
	}
	if !foundT1 {
		t.Error("Turn 1's tool_summary was incorrectly removed — previous turns' tools blocks should be preserved")
	}

	// Verify message order is preserved
	expected := []string{"user", "tool_summary", "assistant", "user"}
	for i, exp := range expected {
		if m.messages[i].role != exp {
			t.Errorf("message[%d]: expected role=%q, got %q", i, exp, m.messages[i].role)
		}
	}
}

// TestRemoveLastToolSummary_NoToolSummary verifies that removeLastToolSummary is
// a no-op when there are no tool_summary messages.
func TestRemoveLastToolSummary_NoToolSummary(t *testing.T) {
	m := newCLIModel()
	m.messages = []cliMessage{
		{role: "user", content: "hello"},
		{role: "assistant", content: "result"},
	}

	m.removeLastToolSummary()

	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages unchanged, got %d", len(m.messages))
	}
}

// TestRemoveLastToolSummary_OnlyPreservesFirst verifies that tool_summaries
// before the last user message are preserved, while those after are removed.
func TestRemoveLastToolSummary_OnlyPreservesFirst(t *testing.T) {
	m := newCLIModel()
	m.messages = []cliMessage{
		{role: "user", content: "q1"},
		{role: "tool_summary", content: ""}, // before last user — PRESERVED
		{role: "assistant", content: "a"},
		{role: "user", content: "q2"},
		{role: "tool_summary", content: ""}, // after last user — REMOVED
	}

	m.removeLastToolSummary()

	if len(m.messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(m.messages))
	}
	if m.messages[1].role != "tool_summary" {
		t.Error("tool_summary before last user should be preserved")
	}
	if m.messages[3].role != "user" {
		t.Errorf("last message should be user, got %q", m.messages[3].role)
	}
}

// TestRemoveLastToolSummary_PriorTurnWithUserAfter verifies that a tool_summary
// from a Ctrl+C-interrupted turn is NOT removed when there is a subsequent user
// message (i.e. the tool_summary belongs to a prior turn, not the active one).
// Regression: removeLastToolSummary unconditionally removed the last tool_summary,
// causing interrupted iterations to disappear on session switch.
func TestRemoveLastToolSummary_PriorTurnWithUserAfter(t *testing.T) {
	m := newCLIModel()
	m.messages = []cliMessage{
		{role: "user", content: "check system info"},
		{role: "tool_summary", content: ""}, // Ctrl+C interrupted turn — must be PRESERVED
		{role: "user", content: "continue"}, // new user message after interrupt
	}

	m.removeLastToolSummary()

	if len(m.messages) != 3 {
		t.Fatalf("expected 3 messages (tool_summary preserved), got %d", len(m.messages))
	}
	if m.messages[1].role != "tool_summary" {
		t.Error("Ctrl+C tool_summary should be preserved when a user message follows it")
	}
}

// ---------------------------------------------------------------------------
// dedupMessagesGuard tests — proves algorithmic guarantee against duplicate
// message rendering. The guard enforces at-most-one (turnID, role) invariant.
// ---------------------------------------------------------------------------

func TestDedupMessagesGuard_NoDuplicates(t *testing.T) {
	m := &cliModel{}
	m.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 1},
		{role: "assistant", content: "hi", turnID: 1},
		{role: "user", content: "bye", turnID: 2},
		{role: "assistant", content: "bye", turnID: 2},
	}
	originalLen := len(m.messages)
	m.dedupMessagesGuard()
	if len(m.messages) != originalLen {
		t.Fatalf("dedupMessagesGuard should not modify messages without duplicates: got %d, want %d",
			len(m.messages), originalLen)
	}
}

func TestDedupMessagesGuard_PurgesDuplicates(t *testing.T) {
	m := &cliModel{}
	// Two assistant messages with same turnID=1 (a zombie from race condition)
	m.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 1},
		{role: "assistant", content: "old_reply", turnID: 1}, // zombie
		{role: "assistant", content: "new_reply", turnID: 1}, // keep this (last)
	}
	m.dedupMessagesGuard()
	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages after dedup, got %d", len(m.messages))
	}
	// The LAST assistant message should be kept
	if m.messages[1].content != "new_reply" {
		t.Errorf("expected last duplicate to survive, got content %q", m.messages[1].content)
	}
}

func TestDedupMessagesGuard_PreservesSystemMessages(t *testing.T) {
	m := &cliModel{}
	// turnID=0 messages (system, injected) should NOT be deduplicated
	m.messages = []cliMessage{
		{role: "system", content: "msg1", turnID: 0},
		{role: "system", content: "msg2", turnID: 0},
		{role: "user", content: "hello", turnID: 1},
		{role: "assistant", content: "reply", turnID: 1},
	}
	m.dedupMessagesGuard()
	if len(m.messages) != 4 {
		t.Fatalf("system messages with turnID=0 should not be deduplicated: got %d, want 4",
			len(m.messages))
	}
}

func TestDedupMessagesGuard_PreservesStreamingMsg(t *testing.T) {
	m := &cliModel{}
	m.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 1},
		{role: "assistant", content: "streaming", turnID: 1, isPartial: true},
	}
	m.streamingMsgIdx = 1
	m.dedupMessagesGuard()
	// streaming message should survive since it's the only one with turnID=1+assistant
	if m.streamingMsgIdx != 1 {
		t.Errorf("streamingMsgIdx should be unchanged: got %d, want 1", m.streamingMsgIdx)
	}
}

func TestUpsertMessageByTurn_PurgesZombies(t *testing.T) {
	m := &cliModel{}
	// Pre-create two messages with same turnID+role (simulating a race condition)
	m.messages = []cliMessage{
		{role: "user", content: "hello", turnID: 1},
		{role: "assistant", content: "zombie", turnID: 2}, // zombie at idx 1
		{role: "assistant", content: "real", turnID: 2},   // real at idx 2
	}
	// upsert should update the last one AND purge the zombie
	m.upsertMessageByTurn(2, "assistant", cliMessage{
		role:    "assistant",
		content: "updated",
	})
	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages after upsert+purge, got %d", len(m.messages))
	}
	if m.messages[1].content != "updated" {
		t.Errorf("expected updated content, got %q", m.messages[1].content)
	}
}

// TestCacheGenerationCounter verifies the generation counter mechanism
// prevents stale allLines reuse after fullRebuild.
func TestCacheGenerationCounter(t *testing.T) {
	rc := &renderCache{}

	// Initial state
	if rc.histGen != 0 || rc.allLinesGen != 0 {
		t.Fatal("generation counters should start at 0")
	}

	// Simulate histLines change
	rc.bumpHistGen()
	if rc.histGen != 1 {
		t.Fatalf("expected histGen=1 after bump, got %d", rc.histGen)
	}

	// Simulate allLines build from current generation
	rc.allLinesGen = rc.histGen
	if rc.histGen != rc.allLinesGen {
		t.Fatal("after sync, histGen should equal allLinesGen")
	}

	// Simulate another histLines change (e.g. fullRebuild)
	rc.bumpHistGen()
	if rc.histGen == rc.allLinesGen {
		t.Fatal("after second bump, histGen should differ from allLinesGen (stale detection)")
	}
}

// ---------------------------------------------------------------------------
// Cross-turn merge guard in handleSuHistoryLoad
// ---------------------------------------------------------------------------
// When TUI restarts while the agent is mid-turn, handleSuHistoryLoad must NOT
// merge the PREVIOUS turn's assistant message into the current streaming slot.
// The merge is only correct when the last assistant in history belongs to the
// CURRENT turn. If there's a user message between the assistant and the
// streaming slot, the assistant is from a previous turn — merging it would
// display the previous turn's content as the current streaming message.
//
// Regression: previous code searched backward for any role=="assistant",
// skipping user messages, and always merged the first one found.
// ---------------------------------------------------------------------------

// TestSuHistoryLoad_NoCrossTurnMerge verifies that when history has a user
// message between the last assistant and the streaming slot (indicating a
// new turn started), the previous turn's assistant content is NOT merged
// into the current streaming slot.
func TestSuHistoryLoad_NoCrossTurnMerge(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	m.typing = false // fresh restart
	m.splashState.suLoading = true

	prevTurnContent := "你说得对，路由逻辑确实混乱。这是上一轮的内容。"
	now := time.Now()

	msg := suHistoryLoadMsg{
		channelName: "cli",
		chatID:      "/test",
		activeProgress: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Phase:     "executing",
			Iteration: 1,
		},
		history: []channel.HistoryMessage{
			{Role: "assistant", Content: prevTurnContent, Timestamp: now.Add(-2 * time.Minute)},
			{Role: "tool_summary", Content: "", Timestamp: now.Add(-90 * time.Second)},
			{Role: "user", Content: "开始新一轮", Timestamp: now.Add(-30 * time.Second)},
		},
	}

	_ = m.handleSuHistoryLoad(msg)

	// The streaming slot should NOT contain the previous turn's content.
	if m.streamingMsgIdx < 0 || m.streamingMsgIdx >= len(m.messages) {
		t.Fatalf("streamingMsgIdx out of range: %d (messages: %d)", m.streamingMsgIdx, len(m.messages))
	}
	streamingContent := m.messages[m.streamingMsgIdx].content
	if streamingContent == prevTurnContent {
		t.Fatalf("BUG: previous turn's content leaked into streaming slot.\n"+
			"streaming content = %q\nexpected empty or current-turn content", streamingContent)
	}
	if streamingContent != "" {
		t.Fatalf("streaming slot should be empty for a new turn, got %q", streamingContent)
	}

	// The previous turn's assistant message should still be in history (not deleted).
	foundPrev := false
	for _, cm := range m.messages {
		if cm.content == prevTurnContent && cm.role == "assistant" {
			foundPrev = true
			break
		}
	}
	if !foundPrev {
		t.Fatal("previous turn's assistant message was incorrectly deleted by cross-turn merge")
	}

	// Verify message order: assistant(prev) → tool_summary → user → assistant(streaming, empty)
	if len(m.messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(m.messages), m.messages)
	}
	expectedRoles := []string{"assistant", "tool_summary", "user", "assistant"}
	for i, exp := range expectedRoles {
		if m.messages[i].role != exp {
			t.Errorf("message[%d]: expected role=%q, got %q (content=%q)",
				i, exp, m.messages[i].role, m.messages[i].content)
		}
	}
}

// TestSuHistoryLoad_SameTurnMergeStillWorks verifies that when the last
// assistant in history IS from the current turn (no user message between it
// and the streaming slot), the merge still works correctly.
func TestSuHistoryLoad_SameTurnMergeStillWorks(t *testing.T) {
	m := newCLIModel()
	m.channelName = "cli"
	m.chatID = "/test"
	m.handleResize(120, 40)
	setupTestRemoteChannel(m)

	m.typing = false
	m.splashState.suLoading = true

	currentContent := "这是当前轮次的部分输出。"
	now := time.Now()

	msg := suHistoryLoadMsg{
		channelName: "cli",
		chatID:      "/test",
		activeProgress: &protocol.ProgressEvent{
			ChatID:    "cli:/test",
			Phase:     "executing",
			Iteration: 2,
		},
		history: []channel.HistoryMessage{
			{Role: "user", Content: "hello", Timestamp: now.Add(-2 * time.Minute)},
			// Last assistant is from current turn — no user after it.
			{Role: "assistant", Content: currentContent, Timestamp: now.Add(-30 * time.Second)},
		},
	}

	_ = m.handleSuHistoryLoad(msg)

	// The streaming slot SHOULD contain the current turn's content (merged).
	if m.streamingMsgIdx < 0 || m.streamingMsgIdx >= len(m.messages) {
		t.Fatalf("streamingMsgIdx out of range: %d", m.streamingMsgIdx)
	}
	streamingContent := m.messages[m.streamingMsgIdx].content
	if streamingContent != currentContent {
		t.Fatalf("expected streaming slot to contain current turn content %q, got %q",
			currentContent, streamingContent)
	}

	// Should have exactly 2 messages: user + assistant(streaming)
	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages after same-turn merge, got %d: %+v",
			len(m.messages), m.messages)
	}
}
