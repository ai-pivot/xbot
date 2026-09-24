package agent

import (
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
)

// The v55+ data model stores the reply text in iteration_history (the final
// iteration) and leaves session_messages.content of the final assistant row
// EMPTY (handleRunOutput strips it deliberately). At LLM-context build time
// fillAssistantContentFromIterations restores that text.
//
// The bug (2026-09-24, user report "上一个迭代结束的 Content 在下一个 turn 的
// 某一个迭代中会莫名其妙重复一次"): the restore filled **every** assistant
// message of the turn that had empty content — including the per-iteration
// tool-only/reasoning-only messages whose empty content is FAITHFUL (the model
// really produced no text there) — from the turn's LAST iteration content, i.e.
// the turn's final answer. A turn with 88 text-less iterations therefore
// prepended 88 copies of the previous final answer right before the new user
// message (~110k extra tokens on tenant 229007 turn 49, measured:
// prompt_chars 432,095 → 801,870) and the model reproduced that text as its own
// answer in the new turn.
//
// Discriminating assertion: the final answer must appear EXACTLY ONCE in the
// built prompt — once is the legitimate final-reply restore, more than once is
// the bug (the pre-fix code yields 1 + number of text-less iterations).
func TestFillAssistantContentFromIterations_OnlyFinalReply(t *testing.T) {
	_, sess := newAgentHistorySession(t)

	const (
		turnID      = uint64(7)
		finalAnswer = "FINAL-ANSWER-MARKER：全部完成并在生产验证过。"
		iterations  = 5 // tool-only iterations the model produced no text for
	)

	// Turn 7: user → (assistant tool_call + tool result) × N → final reply.
	if err := sess.AddMessage(llm.ChatMessage{Role: "user", Content: "回答我", TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= iterations; i++ {
		callID := fmt.Sprintf("call_%d", i)
		assistant := llm.ChatMessage{
			Role:    "assistant",
			Content: "", // model emitted tool calls only — no text
			TurnID:  turnID,
			ToolCalls: []llm.ToolCall{{
				ID:        callID,
				Name:      "Shell",
				Arguments: fmt.Sprintf(`{"command":"echo %d"}`, i),
			}},
		}
		if err := sess.AddMessage(assistant); err != nil {
			t.Fatal(err)
		}
		tool := llm.NewToolMessage("Shell", callID, fmt.Sprintf(`{"command":"echo %d"}`, i), fmt.Sprintf("out %d", i))
		tool.TurnID = turnID
		if err := sess.AddMessage(tool); err != nil {
			t.Fatal(err)
		}
		// iteration_history: this iteration's own content is empty (faithful).
		if err := sess.AppendIterationHistory(0, turnID, sqlite.IterationRecord{
			TurnID:    turnID,
			Iteration: i,
			Content:   "",
			Tools:     "[]",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The final text reply: persisted as an EMPTY placeholder (content stripped),
	// its text lives only in iteration_history's final iteration.
	placeholder := llm.ChatMessage{Role: "assistant", Content: "", TurnID: turnID}
	if err := sess.AddMessage(placeholder); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendIterationHistory(0, turnID, sqlite.IterationRecord{
		TurnID:    turnID,
		Iteration: iterations + 1,
		Content:   finalAnswer,
		Tools:     "[]",
	}); err != nil {
		t.Fatal(err)
	}

	// Next turn's user message.
	if err := sess.AddMessage(llm.ChatMessage{Role: "user", Content: "下一个问题", TurnID: turnID + 1}); err != nil {
		t.Fatal(err)
	}

	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	a.fillAssistantContentFromIterations(msgs, sess)
	msgs = llm.SanitizeMessages(msgs)

	// Count how many prompt messages carry the final answer text.
	copies := 0
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, "FINAL-ANSWER-MARKER") {
			copies++
		}
	}
	if copies != 1 {
		var dump []string
		for i, m := range msgs {
			dump = append(dump, fmt.Sprintf("  [%d] %s tool_calls=%d content=%q", i, m.Role, len(m.ToolCalls), truncateForTest(m.Content, 40)))
		}
		t.Fatalf("final reply text appears %d times in the LLM prompt, want exactly 1 "+
			"(the turn's final-reply placeholder). Copies of a previous turn's answer injected into "+
			"per-iteration tool messages make the model repeat it in the next turn.\nprompt:\n%s",
			copies, strings.Join(dump, "\n"))
	}
}

// The tool iterations must keep their faithful empty content: filling them with
// a later iteration's text makes the model's own history self-contradictory.
func TestFillAssistantContentFromIterations_KeepsToolIterationContentEmpty(t *testing.T) {
	_, sess := newAgentHistorySession(t)

	const turnID = uint64(3)

	if err := sess.AddMessage(llm.ChatMessage{Role: "user", Content: "go", TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	callID := "call_1"
	if err := sess.AddMessage(llm.ChatMessage{
		Role:      "assistant",
		Content:   "",
		TurnID:    turnID,
		ToolCalls: []llm.ToolCall{{ID: callID, Name: "Read", Arguments: `{"path":"/tmp/x"}`}},
	}); err != nil {
		t.Fatal(err)
	}
	tool := llm.NewToolMessage("Read", callID, `{"path":"/tmp/x"}`, "file body")
	tool.TurnID = turnID
	if err := sess.AddMessage(tool); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendIterationHistory(0, turnID, sqlite.IterationRecord{TurnID: turnID, Iteration: 1, Content: "", Tools: "[]"}); err != nil {
		t.Fatal(err)
	}
	// final text reply (stripped placeholder) + its iteration record
	if err := sess.AddMessage(llm.ChatMessage{Role: "assistant", Content: "", TurnID: turnID}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendIterationHistory(0, turnID, sqlite.IterationRecord{TurnID: turnID, Iteration: 2, Content: "the reply", Tools: "[]"}); err != nil {
		t.Fatal(err)
	}

	msgs, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{}
	a.fillAssistantContentFromIterations(msgs, sess)

	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 && m.Content != "" {
			t.Fatalf("tool iteration assistant message was filled with %q — its empty content is the model's real output and must stay empty", m.Content)
		}
	}
	reply := 0
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == "the reply" {
			reply++
		}
	}
	if reply != 1 {
		t.Fatalf("final reply restored %d times, want 1", reply)
	}
}

func truncateForTest(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var _ = session.TenantSession{}
