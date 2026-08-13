package logger

import (
	"context"
	"testing"
)

func TestContextObservabilityFields(t *testing.T) {
	ctx := context.Background()
	ctx = WithRequestID(ctx, "rq-1")
	ctx = WithSessionID(ctx, "cli:/tmp/repo")
	ctx = WithTurnID(ctx, 42)
	ctx = WithUserID(ctx, "admin")

	if got := RequestID(ctx); got != "rq-1" {
		t.Errorf("RequestID = %q, want rq-1", got)
	}
	if got := SessionID(ctx); got != "cli:/tmp/repo" {
		t.Errorf("SessionID = %q, want cli:/tmp/repo", got)
	}
	if got := TurnID(ctx); got != 42 {
		t.Errorf("TurnID = %d, want 42", got)
	}
	if got := UserID(ctx); got != "admin" {
		t.Errorf("UserID = %q, want admin", got)
	}

	// Ctx(ctx) must surface all four as log fields — the agent loop's
	// log.Ctx(ctx) lines (LLM calls, turn lifecycle) become greppable by
	// session/request/turn, matching the LLM HTTP headers.
	entry := Ctx(ctx)
	if entry.Data["session_id"] != "cli:/tmp/repo" {
		t.Errorf("log field session_id = %v, want cli:/tmp/repo", entry.Data["session_id"])
	}
	if entry.Data["request_id"] != "rq-1" {
		t.Errorf("log field request_id = %v, want rq-1", entry.Data["request_id"])
	}
	if entry.Data["user_id"] != "admin" {
		t.Errorf("log field user_id = %v, want admin", entry.Data["user_id"])
	}
	if entry.Data["turn_id"] != int64(42) {
		t.Errorf("log field turn_id = %v (%T), want int64(42)", entry.Data["turn_id"], entry.Data["turn_id"])
	}
}

func TestCtxEmptyContext(t *testing.T) {
	entry := Ctx(context.Background())
	if len(entry.Data) != 0 {
		t.Fatalf("Ctx(background) should have no fields, got %v", entry.Data)
	}
}

func TestContextExtractEmpty(t *testing.T) {
	ctx := context.Background()
	if RequestID(ctx) != "" || SessionID(ctx) != "" || UserID(ctx) != "" || TurnID(ctx) != 0 {
		t.Fatal("empty context should yield zero values")
	}
}
