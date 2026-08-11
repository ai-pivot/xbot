package agent

import (
	"context"
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

	// v55: iteration_history is the single source of truth — progress_history
	// (Detail JSON via metadata) is no longer written. The [interrupted]
	// message is persisted to DB; iteration_history records (if any) were
	// written by snapshotCompletedIteration during the Run.
	// For early-exit (empty RunOutput), there are no iterations — just
	// verify the [interrupted] message was persisted.

	// Verify the [interrupted] message was persisted to the DB
	dbMsgs, err := sess.GetMessages()
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}

	foundInterrupted := false
	for _, m := range dbMsgs {
		if m.Role == "assistant" && m.Content == "[interrupted]" {
			foundInterrupted = true
			// v55: Detail is no longer written — iteration_history table is
			// the single source of truth. Don't check Detail.
		}
	}
	if !foundInterrupted {
		t.Error("expected [interrupted] message in DB")
	}
}
