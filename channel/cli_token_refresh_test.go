package channel

import (
	"testing"

	"xbot/protocol"
)

// TestTokenRefreshSessionGuard verifies that cliTokenRefreshMsg is rejected
// when it arrives from a different session (stale async goroutine from a
// previous compression). Without this guard, a late-arriving refresh could
// overwrite the current session's token usage with stale data, causing the
// context indicator to "jump back" after Ctrl+C (Bug 1).
func TestTokenRefreshSessionGuard(t *testing.T) {
	model := initTestModel()
	model.channelName = "cli"
	model.chatID = "/session-A"

	// Set current token usage from engine progress events
	model.lastTokenUsage = &protocol.TokenUsage{
		PromptTokens:     50000,
		CompletionTokens: 10000,
		TotalTokens:      60000,
	}

	// Simulate a stale refresh from a different session
	msg := cliTokenRefreshMsg{
		channelName:     "cli",
		chatID:          "/session-B",
		tokenPrompt:     20000,
		tokenCompletion: 0,
	}

	model.Update(msg)

	// Should NOT overwrite — different session
	if model.lastTokenUsage.PromptTokens != 50000 {
		t.Errorf("stale session refresh should be rejected, got PromptTokens=%d, want 50000",
			model.lastTokenUsage.PromptTokens)
	}
}

// TestTokenRefreshAcceptsHigherValue verifies that cliTokenRefreshMsg is
// accepted when the DB value is genuinely higher (newer data) for the
// same session.
func TestTokenRefreshAcceptsHigherValue(t *testing.T) {
	model := initTestModel()
	model.channelName = "cli"
	model.chatID = "/session-A"

	model.lastTokenUsage = &protocol.TokenUsage{
		PromptTokens:     50000,
		CompletionTokens: 10000,
		TotalTokens:      60000,
	}

	msg := cliTokenRefreshMsg{
		channelName:     "cli",
		chatID:          "/session-A",
		tokenPrompt:     60000,
		tokenCompletion: 12000,
	}

	model.Update(msg)

	if model.lastTokenUsage.PromptTokens != 60000 {
		t.Errorf("higher DB value should be accepted, got PromptTokens=%d, want 60000",
			model.lastTokenUsage.PromptTokens)
	}
}

// TestTokenRefreshRejectsLowerValue verifies that cliTokenRefreshMsg is
// rejected when the DB value is lower than the current value (e.g., stale
// compressed count from a previous compression).
func TestTokenRefreshRejectsLowerValue(t *testing.T) {
	model := initTestModel()
	model.channelName = "cli"
	model.chatID = "/session-A"

	model.lastTokenUsage = &protocol.TokenUsage{
		PromptTokens:     50000,
		CompletionTokens: 10000,
		TotalTokens:      60000,
	}

	msg := cliTokenRefreshMsg{
		channelName:     "cli",
		chatID:          "/session-A",
		tokenPrompt:     20000, // stale compressed count
		tokenCompletion: 0,
	}

	model.Update(msg)

	if model.lastTokenUsage.PromptTokens != 50000 {
		t.Errorf("lower DB value should be rejected, got PromptTokens=%d, want 50000",
			model.lastTokenUsage.PromptTokens)
	}
}

// TestTokenRefreshAcceptsWhenNil verifies that cliTokenRefreshMsg is
// accepted when lastTokenUsage is nil (no prior data).
func TestTokenRefreshAcceptsWhenNil(t *testing.T) {
	model := initTestModel()
	model.channelName = "cli"
	model.chatID = "/session-A"

	model.lastTokenUsage = nil

	msg := cliTokenRefreshMsg{
		channelName:     "cli",
		chatID:          "/session-A",
		tokenPrompt:     30000,
		tokenCompletion: 5000,
	}

	model.Update(msg)

	if model.lastTokenUsage == nil {
		t.Fatal("nil lastTokenUsage should be filled by refresh")
	}
	if model.lastTokenUsage.PromptTokens != 30000 {
		t.Errorf("got PromptTokens=%d, want 30000", model.lastTokenUsage.PromptTokens)
	}
}
