package plugin

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestSentinelErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"ErrPluginNotFound matches itself", ErrPluginNotFound, ErrPluginNotFound},
		{"ErrPluginAlreadyRegistered matches itself", ErrPluginAlreadyRegistered, ErrPluginAlreadyRegistered},
		{"ErrPluginNotActive matches itself", ErrPluginNotActive, ErrPluginNotActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.want) {
				t.Errorf("errors.Is(%v, %v) = false, want true", tt.err, tt.want)
			}
		})
	}
}

func TestErrPluginNotFound(t *testing.T) {
	// Wrapping with fmt.Errorf should still match via errors.Is
	wrapped := fmt.Errorf("context: %w", ErrPluginNotFound)
	if !errors.Is(wrapped, ErrPluginNotFound) {
		t.Error("wrapped ErrPluginNotFound should match errors.Is")
	}

	// Different sentinel should not match
	if errors.Is(wrapped, ErrPluginAlreadyRegistered) {
		t.Error("wrapped ErrPluginNotFound should NOT match ErrPluginAlreadyRegistered")
	}
}

func TestErrPluginActivationFailed(t *testing.T) {
	inner := fmt.Errorf("timeout after 5s")
	err := &ErrPluginActivationFailed{PluginID: "test-plugin", Err: inner}

	// Unwrap exposes inner error
	if !errors.Is(err, inner) {
		t.Error("errors.Is should reach inner error via Unwrap")
	}

	// errors.As extracts the structured type
	var typed *ErrPluginActivationFailed
	if !errors.As(err, &typed) {
		t.Fatal("errors.As should match *ErrPluginActivationFailed")
	}
	if typed.PluginID != "test-plugin" {
		t.Errorf("PluginID = %q, want %q", typed.PluginID, "test-plugin")
	}

	// Error message contains useful info
	msg := err.Error()
	if !contains(msg, "test-plugin") {
		t.Errorf("Error() = %q, want plugin ID in message", msg)
	}
	if !contains(msg, "timeout after 5s") {
		t.Errorf("Error() = %q, want inner error in message", msg)
	}
}

func TestErrRateLimitExceeded(t *testing.T) {
	err := &ErrRateLimitExceeded{PluginID: "rl-plugin", RetryAfter: 5 * time.Second}

	var typed *ErrRateLimitExceeded
	if !errors.As(err, &typed) {
		t.Fatal("errors.As should match *ErrRateLimitExceeded")
	}
	if typed.RetryAfter != 5*time.Second {
		t.Errorf("RetryAfter = %v, want 5s", typed.RetryAfter)
	}

	msg := err.Error()
	if !contains(msg, "rate limit exceeded") {
		t.Errorf("Error() = %q, want rate limit message", msg)
	}
	if !contains(msg, "rl-plugin") {
		t.Errorf("Error() = %q, want plugin ID", msg)
	}
}

func TestErrors_Is(t *testing.T) {
	// Wrapped sentinel errors from manager should match via errors.Is
	err := fmt.Errorf("register: %w", ErrPluginAlreadyRegistered)
	if !errors.Is(err, ErrPluginAlreadyRegistered) {
		t.Error("wrapped ErrPluginAlreadyRegistered should match")
	}

	err = fmt.Errorf("reload: %w", ErrPluginNotFound)
	if !errors.Is(err, ErrPluginNotFound) {
		t.Error("wrapped ErrPluginNotFound should match")
	}

	// ActivationFailed wraps inner error
	inner := fmt.Errorf("some activation error")
	actErr := &ErrPluginActivationFailed{PluginID: "p", Err: inner}
	if !errors.Is(actErr, inner) {
		t.Error("ErrPluginActivationFailed should unwrap to inner error")
	}
}

func TestPermissionErrorMigration(t *testing.T) {
	// Verify PermissionError still works after migration to errors.go
	var err error = &PermissionError{
		PluginID:   "test-plugin",
		Permission: "tools.register",
		Action:     "register tool",
	}

	msg := err.Error()
	if !contains(msg, "test-plugin") {
		t.Errorf("Error() = %q, want plugin ID", msg)
	}
	if !contains(msg, "tools.register") {
		t.Errorf("Error() = %q, want permission", msg)
	}
	if !contains(msg, "register tool") {
		t.Errorf("Error() = %q, want action", msg)
	}

	// Type assertion still works (existing tests use this pattern)
	pe, ok := err.(*PermissionError)
	if !ok {
		t.Fatal("type assertion to *PermissionError should still work")
	}
	if pe.PluginID != "test-plugin" {
		t.Errorf("PluginID = %q, want %q", pe.PluginID, "test-plugin")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
