package agent

import (
	"context"
	"testing"

	"xbot/bus"
	"xbot/llm"
)

// ─────────────────────────────────────────────────────────────────────────────
// Restart resume turn identity — regression tests for the "two big DOMs" bug.
//
// A session interrupted mid-turn by a graceful-shutdown restart is resumed via
// InjectInboundResume (resumePendingTurns / /continue). The resume Run MUST
// continue the interrupted turn's identity:
//
//   1. admitToMsgCh does NOT pre-allocate a fresh turn id for resume_turn
//      messages (like AskUser answers — the turn id is resolved at dequeue
//      time and REUSES the last user message's turn).
//   2. resolveResumeTurnID returns the turn of the last non-display-only user
//      message — the interrupted turn's owner. Without this, every restart
//      split the logical turn into a new turn id (user=275, resume1=276,
//      resume2=277 in tenant 157491), rendering the interrupted work and the
//      resumed work as TWO separate assistant blocks instead of one.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdmitToMsgCh_ResumeSkipsTurnAllocation verifies that resume_turn
// messages are NOT assigned a fresh turn id at queue admission. The resume
// continues the interrupted turn — the id is resolved at dequeue time from the
// DB (last user message's turn). Pre-allocating here would force a NEW turn
// (nextTurnID), splitting the logical turn across restarts.
func TestAdmitToMsgCh_ResumeSkipsTurnAllocation(t *testing.T) {
	a := &Agent{}
	ss := &bgSessionState{}
	msgCh := make(chan bus.InboundMessage, 4)
	ctx := context.Background()

	// A normal message: turn id pre-allocated and stamped into metadata.
	normal := bus.InboundMessage{Channel: "web", ChatID: "c1", SenderID: "u1", Content: "hello"}
	a.admitToMsgCh(ctx, "web:c1", normal, ss, msgCh)
	queued := <-msgCh
	if queued.Metadata["turn_id"] != "1" {
		t.Fatalf("normal message turn_id = %q, want \"1\" (pre-allocated)", queued.Metadata["turn_id"])
	}

	// A resume message: NO pre-allocation — dequeue resolves the interrupted
	// turn's id instead. The queue-admission counter must stay untouched.
	resume := bus.InboundMessage{
		Channel:   "web",
		ChatID:    "c1",
		SenderID:  "u1",
		Content:   "",
		Metadata:  map[string]string{"resume_turn": "true"},
		RequestID: "req-resume",
	}
	a.admitToMsgCh(ctx, "web:c1", resume, ss, msgCh)
	queued = <-msgCh
	if queued.Metadata["turn_id"] != "" {
		t.Fatalf("resume message turn_id = %q, want empty (dequeue-time resolution reuses the interrupted turn)", queued.Metadata["turn_id"])
	}

	// The resume did NOT consume a turn id: the next normal message still
	// gets 2 (without the fix the resume would have consumed 2 and the next
	// normal message would get 3).
	normal2 := bus.InboundMessage{Channel: "web", ChatID: "c1", SenderID: "u1", Content: "again"}
	a.admitToMsgCh(ctx, "web:c1", normal2, ss, msgCh)
	queued = <-msgCh
	if queued.Metadata["turn_id"] != "2" {
		t.Fatalf("post-resume normal message turn_id = %q, want \"2\" (resume must not consume a turn id)", queued.Metadata["turn_id"])
	}
}

// TestResolveResumeTurnID verifies the dequeue-time turn resolution: the resume
// reuses the LAST non-display-only user message's turn id so the interrupted
// work and the resumed work share one turn (one assistant block in the
// frontend — the same rendering as an uninterrupted turn).
func TestResolveResumeTurnID(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	a := &Agent{multiSession: mt}

	// No user message yet: unresolvable → 0 (caller falls back to nextTurnID).
	if tid := a.resolveResumeTurnID("test", "chat"); tid != 0 {
		t.Fatalf("resolveResumeTurnID on empty session = %d, want 0", tid)
	}

	// User message (turn 5) interrupted mid-run by a restart: the resume must
	// reuse turn 5 — the turn that owns the user message.
	u := llm.NewUserMessage("interrupted question")
	u.TurnID = 5
	if _, err := sess.AppendMessage(u); err != nil {
		t.Fatal(err)
	}
	// Intermediate assistant rows of the interrupted Run carry the same turn.
	a1 := llm.NewAssistantMessage("")
	a1.TurnID = 5
	a1.ToolCalls = []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}
	if _, err := sess.AppendMessage(a1); err != nil {
		t.Fatal(err)
	}
	if tid := a.resolveResumeTurnID("test", "chat"); tid != 5 {
		t.Fatalf("resolveResumeTurnID = %d, want 5 (the interrupted turn's id — the last user message's turn)", tid)
	}

	// A later user message (turn 8) becomes the resume anchor.
	u2 := llm.NewUserMessage("later question")
	u2.TurnID = 8
	if _, err := sess.AppendMessage(u2); err != nil {
		t.Fatal(err)
	}
	if tid := a.resolveResumeTurnID("test", "chat"); tid != 8 {
		t.Fatalf("resolveResumeTurnID = %d, want 8 (the LAST user message's turn)", tid)
	}
}
