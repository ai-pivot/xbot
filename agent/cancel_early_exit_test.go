package agent

import (
	"context"
	"encoding/json"
	"testing"

	"xbot/bus"
	ch "xbot/channel"
	"xbot/session"
	"xbot/tools"
)

// TestHandleCancelledRun_EarlyExit_PreservesUserCancelled verifies that the
// cancel early-exit path in processMessage (ctx cancelled during setup) calls
// handleCancelledRun, which persists [interrupted] with user_cancelled and
// returns an OutboundMsg with progress_history.
//
// This test creates a minimal Agent with a real MultiTenantSession (SQLite)
// to exercise the full handleCancelledRun path.
func TestHandleCancelledRun_EarlyExit_PreservesUserCancelled(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := tmpDir + "/test.db"

	multiSession, err := session.NewMultiTenant(dbPath)
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	t.Cleanup(func() { multiSession.Close() })

	var sentMsgs []ch.OutboundMsg
	a := &Agent{
		multiSession: multiSession,
		todoManager:  tools.NewTodoManager(),
		directSend: func(msg ch.OutboundMsg) (string, error) {
			sentMsgs = append(sentMsgs, msg)
			return "", nil
		},
		channelFinder: func(name string) (ch.Channel, bool) { return nil, false },
	}

	msg := bus.InboundMessage{
		Channel:  "web",
		ChatID:   "test-chat",
		SenderID: "test-user",
		Content:  "do something",
	}

	// Create the tenant session (handleCancelledRun needs it to persist [interrupted])
	sess, err := multiSession.GetOrCreateSession("web", "test-chat")
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}

	// Call handleCancelledRun with an empty RunOutput (simulates early-exit
	// where Run() never started, so iterationSnapshots is empty).
	out, err := a.handleCancelledRun(context.Background(), msg, &RunOutput{}, sess)
	if err != nil {
		t.Fatalf("handleCancelledRun: %v", err)
	}

	// The OutboundMsg must have cancelled=true
	if out.Metadata == nil || out.Metadata["cancelled"] != "true" {
		t.Errorf("expected cancelled=true in metadata, got %+v", out.Metadata)
	}

	// progress_history must be present (contains user_cancelled)
	progressHistory := out.Metadata["progress_history"]
	if progressHistory == "" {
		t.Fatal("expected non-empty progress_history, got empty")
	}

	// Verify progress_history contains user_cancelled
	var iters []IterationSnapshot
	if err := json.Unmarshal([]byte(progressHistory), &iters); err != nil {
		t.Fatalf("failed to unmarshal progress_history: %v", err)
	}
	if len(iters) == 0 {
		t.Fatal("expected at least 1 iteration in progress_history")
	}

	// The last iteration should contain user_cancelled
	lastIter := iters[len(iters)-1]
	foundUserCancelled := false
	for _, tool := range lastIter.Tools {
		if tool.Name == "user_cancelled" {
			foundUserCancelled = true
		}
	}
	if !foundUserCancelled {
		t.Error("expected user_cancelled tool in the last iteration")
	}

	// Verify the [interrupted] message was persisted to the DB
	dbMsgs, err := sess.GetMessages()
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}

	foundInterrupted := false
	for _, m := range dbMsgs {
		if m.Role == "assistant" && m.Content == "[interrupted]" {
			foundInterrupted = true
			if m.Detail == "" {
				t.Error("[interrupted] message has empty Detail")
			}
		}
	}
	if !foundInterrupted {
		t.Error("expected [interrupted] message in DB")
	}
}
