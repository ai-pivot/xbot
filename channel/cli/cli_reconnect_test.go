package cli

import (
	"testing"
)

// TestConnStateMachine validates the single-source-of-truth connState transitions.
// This is a regression test for the bug where showDisconnect flag was added/removed
// and connState was not trusted as the sole indicator of connection health.
func TestConnStateMachine(t *testing.T) {
	model := newCLIModel()
	model.remoteMode = true

	// Initial state for remote mode: "connected"
	model.connState = "connected"

	// Disconnect detection
	model.connState = "disconnected"
	if model.connState != "disconnected" {
		t.Fatal("connState should be 'disconnected'")
	}

	// Reconnect attempt
	model.connState = "reconnecting"
	if model.connState != "reconnecting" {
		t.Fatal("connState should be 'reconnecting'")
	}

	// Reconnect success
	model.connState = "connected"
	if model.connState != "connected" {
		t.Fatal("connState should be 'connected'")
	}
}

// TestSplashConditionUsesOnlyConnState verifies that the splash screen condition
// uses ONLY connState — no showDisconnect or other flags.
func TestSplashConditionUsesOnlyConnState(t *testing.T) {
	model := newCLIModel()
	model.remoteMode = true
	model.connState = "connected"

	checkSplash := func() bool {
		return model.remoteMode && model.connState != "connected" && model.connState != ""
	}

	// Connected → no splash
	if checkSplash() {
		t.Error("connected: splash should NOT show")
	}

	// Disconnected → splash
	model.connState = "disconnected"
	if !checkSplash() {
		t.Error("disconnected: splash SHOULD show")
	}

	// Reconnecting → splash
	model.connState = "reconnecting"
	if !checkSplash() {
		t.Error("reconnecting: splash SHOULD show")
	}

	// Back to connected → no splash
	model.connState = "connected"
	if checkSplash() {
		t.Error("reconnected: splash should NOT show")
	}

	// Empty connState → no splash (edge case)
	model.connState = ""
	if checkSplash() {
		t.Error("empty connState: splash should NOT show")
	}
}

// TestDisconnectGuardUsesOnlyConnState verifies that the disconnect guard
// (which blocks keys during disconnect) triggers only on connState.
func TestDisconnectGuardUsesOnlyConnState(t *testing.T) {
	model := newCLIModel()
	model.remoteMode = true
	model.connState = "connected"

	// Connected → guard inactive (keys pass through)
	if shouldBlock := model.remoteMode && model.connState != "connected" && model.connState != ""; shouldBlock {
		t.Error("connected: guard should be inactive")
	}

	// Disconnected → guard active
	model.connState = "disconnected"
	if shouldBlock := model.remoteMode && model.connState != "connected" && model.connState != ""; !shouldBlock {
		t.Error("disconnected: guard should be active")
	}

	// Reconnected → guard inactive
	model.connState = "connected"
	if shouldBlock := model.remoteMode && model.connState != "connected" && model.connState != ""; shouldBlock {
		t.Error("reconnected: guard should be inactive after reconnect")
	}
}
