package agent

import (
	"context"
	"testing"

	"xbot/bus"
	"xbot/llm"
)

// TestContinueCmd_SeesSessionDBRows guards the v71+ per-session DB split for
// /continue: user/assistant rows live in the SESSION DB (main DB
// session_messages is empty after the split), so the "last user message" /
// "assistant reply after last user" lookups MUST go through the tenant
// session (TenantSession), never the main DB. Before the fix both queries ran
// against the main DB, always returned empty, and /continue answered
// "没有找到可继续的对话" even though the interrupted turn was fully persisted.
func TestContinueCmd_SeesSessionDBRows(t *testing.T) {
	mt, sess := newAgentHistorySession(t)

	// Interrupted turn: user message only, no assistant reply yet.
	u := llm.NewUserMessage("interrupted question")
	u.TurnID = 1
	if _, err := sess.AppendMessage(u); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt, bus: bus.NewMessageBus(), agentCtx: context.Background()}
	cmd := &continueCmd{}
	out, err := cmd.Execute(context.Background(), a, bus.InboundMessage{Channel: "test", ChatID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Fatalf("/continue returned %q, want no reply — the resumed turn produces one", out.Content)
	}

	// The resume turn must have been injected with resume_turn metadata.
	select {
	case injected := <-a.bus.Inbound:
		if injected.Metadata["resume_turn"] != "true" {
			t.Fatalf("injected message metadata=%v, want resume_turn=true", injected.Metadata)
		}
		if injected.Content != "" {
			t.Fatalf("resume message content=%q, want empty (user row already in DB)", injected.Content)
		}
	default:
		t.Fatal("expected resume turn injection into bus.Inbound")
	}
}

// TestContinueCmd_DetectsCompletedTurnInSessionDB guards the hasReply half of
// the same bug: a turn that completed naturally (final assistant reply after
// the user message, both in the session DB) must be detected as "already
// answered" — the pre-fix main-DB lookup always saw zero rows and offered to
// continue completed turns.
func TestContinueCmd_DetectsCompletedTurnInSessionDB(t *testing.T) {
	mt, sess := newAgentHistorySession(t)

	u := llm.NewUserMessage("answered question")
	u.TurnID = 1
	if _, err := sess.AppendMessage(u); err != nil {
		t.Fatal(err)
	}
	// Intermediate tool-call row (same turn) — not a final reply.
	a1 := llm.NewAssistantMessage("")
	a1.TurnID = 1
	a1.ToolCalls = []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}
	if _, err := sess.AppendMessage(a1); err != nil {
		t.Fatal(err)
	}
	// Final reply row (no tool calls) — the turn completed.
	a2 := llm.NewAssistantMessage("final answer")
	a2.TurnID = 1
	if _, err := sess.AppendMessage(a2); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt, bus: bus.NewMessageBus(), agentCtx: context.Background()}
	cmd := &continueCmd{}
	out, err := cmd.Execute(context.Background(), a, bus.InboundMessage{Channel: "test", ChatID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("/continue injected a resume turn for an already-completed turn, want the 已完成 reply")
	}
	if out.Content != "💡 上一轮对话已完成，无需继续。" {
		t.Fatalf("/continue reply=%q, want the completed-turn notice", out.Content)
	}
	// No resume injection may happen.
	select {
	case injected := <-a.bus.Inbound:
		t.Fatalf("unexpected injection for completed turn: %+v", injected)
	default:
	}
}
