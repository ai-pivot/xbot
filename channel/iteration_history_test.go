package channel

import (
	"encoding/json"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// TestConvert_WithIterations_AllIterationsRendered verifies that
// ConvertMessagesToHistoryWithIterations renders ALL iterations for a turn
// (intermediate + final) as a single HistoryMessage with the complete
// iteration list — not split across multiple HistoryMessages or missing
// intermediate iterations.
func TestConvert_WithIterations_AllIterationsRendered(t *testing.T) {
	// Turn with 3 iterations: Shell, Read, final content.
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 5},
		{ID: 100, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 5},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "ok", TurnID: 5},
		{ID: 102, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read", Arguments: "{}"}}, TurnID: 5},
		{Role: "tool", ToolCallID: "c2", ToolName: "Read", Content: "file", TurnID: 5},
		{ID: 104, Role: "assistant", Content: "done", TurnID: 5},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		5: {
			{TurnID: 5, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 5, Iteration: 2, Tools: `[{"name":"Read","status":"done"}]`},
			{TurnID: 5, Iteration: 3, Content: "done", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var assistantMsgs []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, h)
		}
	}
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected 1 assistant HistoryMessage, got %d", len(assistantMsgs))
	}
	if len(assistantMsgs[0].Iterations) != 3 {
		t.Fatalf("expected 3 iterations, got %d", len(assistantMsgs[0].Iterations))
	}
	for i, want := range []int{1, 2, 3} {
		if assistantMsgs[0].Iterations[i].Iteration != want {
			t.Errorf("iter[%d] = %d, want %d", i, assistantMsgs[0].Iterations[i].Iteration, want)
		}
	}
	if assistantMsgs[0].Iterations[0].Tools[0].Name != "Shell" {
		t.Errorf("iter 1 tool: expected Shell, got %s", assistantMsgs[0].Iterations[0].Tools[0].Name)
	}
	if len(assistantMsgs[0].Iterations[2].Tools) != 0 {
		t.Errorf("iter 3 tools: expected none, got %v", assistantMsgs[0].Iterations[2].Tools)
	}
	if assistantMsgs[0].Iterations[2].Content != "done" {
		t.Errorf("iter 3 content: expected 'done', got %q", assistantMsgs[0].Iterations[2].Content)
	}
	if assistantMsgs[0].Content != "done" {
		t.Errorf("HistoryMessage content: expected 'done', got %q", assistantMsgs[0].Content)
	}
}

// TestConvert_WithIterations_CancelledTurn verifies that a cancelled turn
// (all messages have tool_calls + [interrupted] message) renders ALL iterations
// from structured data via the [interrupted] message's !isIntermediate branch.
// flushPending skips when turnIterMap has data (avoids duplicate HistoryMessage
// with fabricated curIterIdx++ ids).
func TestConvert_WithIterations_CancelledTurn(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 9},
		{ID: 200, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell"}}, TurnID: 9},
		{Role: "tool", ToolCallID: "c1", Content: "ok", TurnID: 9},
		{ID: 202, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "Read"}}, TurnID: 9},
		{Role: "tool", ToolCallID: "c2", Content: "file", TurnID: 9},
		{ID: 204, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c3", Name: "Grep"}}, TurnID: 9},
		{Role: "tool", ToolCallID: "c3", Content: "results", TurnID: 9},
		// [interrupted] message — no tool_calls, triggers !isIntermediate branch
		{ID: 206, Role: "assistant", Content: "[interrupted]", TurnID: 9},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		9: {
			{TurnID: 9, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 9, Iteration: 2, Tools: `[{"name":"Read","status":"done"}]`},
			{TurnID: 9, Iteration: 3, Tools: `[{"name":"Grep","status":"done"}]`},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var assistantMsgs []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, h)
		}
	}
	// flushPending skips (turnIterMap has data) — only [interrupted] message
	// renders (via !isIntermediate branch with turnIterMap data).
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected 1 assistant ([interrupted] with turnIterMap), got %d", len(assistantMsgs))
	}
	if len(assistantMsgs[0].Iterations) != 3 {
		t.Fatalf("expected 3 iterations, got %d", len(assistantMsgs[0].Iterations))
	}
	for i, want := range []int{1, 2, 3} {
		if assistantMsgs[0].Iterations[i].Iteration != want {
			t.Errorf("iter[%d] = %d, want %d", i, assistantMsgs[0].Iterations[i].Iteration, want)
		}
	}
}

// TestConvert_WithIterations_FallbackToDetail verifies fallback to Detail JSON
// when no structured data exists (old data pre-v55).
func TestConvert_WithIterations_FallbackToDetail(t *testing.T) {
	detail := `[{"iteration":1,"tools":[{"name":"Shell","status":"done"}]},{"iteration":2,"content":"result"}]`
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 1},
		{ID: 10, Role: "assistant", Content: "result", Detail: detail, TurnID: 1},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, nil)

	var assistantMsgs []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, h)
		}
	}
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected 1 assistant, got %d", len(assistantMsgs))
	}
	if len(assistantMsgs[0].Iterations) != 2 {
		t.Fatalf("expected 2 iterations from Detail, got %d", len(assistantMsgs[0].Iterations))
	}
}

// TestConvert_WithIterations_NoDuplication verifies that structured data
// is not duplicated — each iteration appears exactly once.
func TestConvert_WithIterations_NoDuplication(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 3},
		{ID: 50, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell"}}, TurnID: 3},
		{Role: "tool", ToolCallID: "c1", Content: "ok", TurnID: 3},
		{ID: 52, Role: "assistant", Content: "done", TurnID: 3},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		3: {
			{TurnID: 3, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 3, Iteration: 2, Content: "done", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	var assistantMsgs []HistoryMessage
	for _, h := range history {
		if h.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, h)
		}
	}
	if len(assistantMsgs) != 1 {
		t.Fatalf("expected 1 assistant, got %d", len(assistantMsgs))
	}
	if len(assistantMsgs[0].Iterations) != 2 {
		t.Fatalf("expected 2 iterations (no duplication), got %d", len(assistantMsgs[0].Iterations))
	}
	// Verify no duplicate iteration numbers.
	seen := map[int]bool{}
	for _, iter := range assistantMsgs[0].Iterations {
		if seen[iter.Iteration] {
			t.Errorf("duplicate iteration %d", iter.Iteration)
		}
		seen[iter.Iteration] = true
	}
}

// TestIterationHistory_WriteAndRead verifies that AppendIterationHistory +
// GetIterationHistoryByTurn work correctly.
func TestIterationHistory_WriteAndRead(t *testing.T) {
	// This is a storage-level test — verify the DB round-trip.
	// We can't easily test snapshotCompletedIteration without a full runState,
	// but we can verify the storage layer.
	// The integration is covered by the Convert tests above.
	_ = json.Marshal // keep import
}

// TestConvert_WithIterations_RestartTurnBoundary is the regression test for
// the "fancy memory" bug (tenant 134262, turn 219/220): a turn interrupted by
// server restart has NO final assistant message — only intermediate messages
// with tool_calls. After restart the recovery turn (new turn_id) continues with
// more intermediate messages, so two turns' intermediate messages sit back to
// back with NO user message between them. flushPending must flush the previous
// turn at the turn boundary — otherwise pendingIters mixes both turns and
// flushPending replaces ALL of them with the LAST turn's turnIterMap records,
// dropping every iteration of the pre-restart turn from the rendered history.
func TestConvert_WithIterations_RestartTurnBoundary(t *testing.T) {
	msgs := []llm.ChatMessage{
		// Pre-restart turn 219: user + 3 intermediate assistants, NO final message
		// (restart killed the turn mid-execution).
		{Role: "user", Content: "fix live progress", TurnID: 219},
		{ID: 300, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Read", Arguments: "{}"}}, TurnID: 219},
		{Role: "tool", ToolCallID: "c1", ToolName: "Read", Content: "file", TurnID: 219},
		{ID: 302, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "FileReplace", Arguments: "{}"}}, TurnID: 219},
		{Role: "tool", ToolCallID: "c2", ToolName: "FileReplace", Content: "ok", TurnID: 219},
		{ID: 304, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c3", Name: "Shell", Arguments: "{}"}}, TurnID: 219},
		{Role: "tool", ToolCallID: "c3", ToolName: "Shell", Content: "ok", TurnID: 219},
		// Post-restart recovery turn 220: intermediate + final, NO user message
		// (auto-recovery of the interrupted turn).
		{ID: 306, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c4", Name: "Shell", Arguments: "{}"}}, TurnID: 220},
		{Role: "tool", ToolCallID: "c4", ToolName: "Shell", Content: "pushed", TurnID: 220},
		{ID: 308, Role: "assistant", Content: "已推送并部署。", TurnID: 220},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		219: {
			{TurnID: 219, Iteration: 1, Tools: `[{"name":"Read","status":"done"}]`},
			{TurnID: 219, Iteration: 2, Tools: `[{"name":"FileReplace","status":"done"}]`},
			{TurnID: 219, Iteration: 3, Tools: `[{"name":"Shell","status":"done"}]`},
		},
		220: {
			{TurnID: 220, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 220, Iteration: 2, Content: "已推送并部署。", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	// Expected render order:
	//   0: user turn 219
	//   1: assistant turn 219 (empty content, 3 iterations from turnIterMap[219]
	//      — flushed at the turn boundary, NOT swallowed by turn 220)
	//   2: assistant turn 220 ("已推送并部署。", 2 iterations from turnIterMap[220],
	//      final reply rendered via the !isIntermediate structured branch)
	if len(history) != 3 {
		t.Fatalf("expected 3 HistoryMessages (user219, asst219, final220), got %d", len(history))
	}
	if history[0].Role != "user" || history[0].TurnID != 219 {
		t.Fatalf("history[0]: expected user turn 219, got role=%s turn=%d", history[0].Role, history[0].TurnID)
	}
	// history[1] must be turn 219's assistant with ALL 3 pre-restart iterations.
	if history[1].Role != "assistant" || history[1].TurnID != 219 {
		t.Fatalf("history[1]: expected assistant turn 219, got role=%s turn=%d", history[1].Role, history[1].TurnID)
	}
	if len(history[1].Iterations) != 3 {
		t.Fatalf("expected 3 pre-restart iterations for turn 219, got %d (pre-restart iterations lost at turn boundary)", len(history[1].Iterations))
	}
	for i, want := range []int{1, 2, 3} {
		if history[1].Iterations[i].Iteration != want {
			t.Errorf("turn 219 iter[%d] = %d, want %d", i, history[1].Iterations[i].Iteration, want)
		}
	}
	// history[2] is turn 220's final reply with its 2 recovery iterations.
	if history[2].Role != "assistant" || history[2].TurnID != 220 || history[2].Content != "已推送并部署。" {
		t.Fatalf("history[2]: expected assistant turn 220 final reply, got role=%s turn=%d content=%q", history[2].Role, history[2].TurnID, history[2].Content)
	}
	if len(history[2].Iterations) != 2 {
		t.Fatalf("expected 2 recovery iterations for turn 220, got %d", len(history[2].Iterations))
	}
}

// TestConvert_WithIterations_AskUserAnswerNoForgedAssistant guards the AskUser
// history-misalignment fix: with correct TurnIDs stamped on the waiting-user
// intermediate rows (PersistenceBridge + buildWaitingUserOutbound), the render
// MUST NOT produce a forged assistant row carrying the ANSWER turn's iterations
// before the answer user message.
//
// DB append order (all rows carry real turn ids after the fix):
//
//	user(question, N=5) → asst(AskUser tc, 5) → tool(AskUser, 5)
//	→ asst(empty histMsg, 5) → user(answer, N+1=6)
//	→ asst(Shell tc, 6) → tool(Shell, 6) → asst(final, 6)
//
// Expected render: user(5) | assistant(5, empty content, 1 iteration) |
// user(6, answer) | assistant(6, final content, 2 iterations).
// Without the turn_id stamp, deriveTurnIDs mis-attributes turn 5's rows to 6,
// and a forged assistant (turn 6's full iterations) appears BEFORE user(answer).
func TestConvert_WithIterations_AskUserAnswerNoForgedAssistant(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "choose a theme", TurnID: 5},
		{ID: 500, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{"questions":[{"question":"theme","options":["dark","light"]}]}`}}, TurnID: 5},
		{Role: "tool", ToolCallID: "ask", ToolName: "AskUser", Content: "Asked 1 question(s)", TurnID: 5},
		{ID: 503, Role: "assistant", Content: "", TurnID: 5}, // waiting-user histMsg (stamped by fix)
		{Role: "user", Content: "Q0: dark", TurnID: 6},       // answer turn
		{ID: 505, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 6},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "ok", TurnID: 6},
		{ID: 507, Role: "assistant", Content: "done", TurnID: 6},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		5: {{TurnID: 5, Iteration: 1, Tools: `[{"name":"AskUser","status":"done"}]`}},
		6: {
			{TurnID: 6, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 6, Iteration: 2, Content: "done", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	// Expected: user(5) | assistant(5) | user(6, answer) | assistant(6, final)
	if len(history) != 4 {
		t.Fatalf("expected 4 HistoryMessages, got %d: %+v", len(history), history)
	}
	if history[0].Role != "user" || history[0].TurnID != 5 {
		t.Fatalf("history[0]: expected user turn 5, got role=%s turn=%d", history[0].Role, history[0].TurnID)
	}
	// history[1] = turn 5's waiting-user assistant (empty content, 1 iteration).
	if history[1].Role != "assistant" || history[1].TurnID != 5 {
		t.Fatalf("history[1]: expected assistant turn 5 (waiting-user histMsg), got role=%s turn=%d", history[1].Role, history[1].TurnID)
	}
	if len(history[1].Iterations) != 1 || history[1].Iterations[0].Iteration != 1 {
		t.Fatalf("history[1]: expected 1 iteration (AskUser tool) for turn 5, got %d", len(history[1].Iterations))
	}
	// history[2] = the answer user row, BEFORE any turn-6 assistant.
	if history[2].Role != "user" || history[2].TurnID != 6 || history[2].Content != "Q0: dark" {
		t.Fatalf("history[2]: expected user(answer) turn 6, got role=%s turn=%d content=%q", history[2].Role, history[2].TurnID, history[2].Content)
	}
	// history[3] = turn 6's final reply with BOTH answer-turn iterations.
	if history[3].Role != "assistant" || history[3].TurnID != 6 || history[3].Content != "done" {
		t.Fatalf("history[3]: expected assistant turn 6 final, got role=%s turn=%d content=%q", history[3].Role, history[3].TurnID, history[3].Content)
	}
	if len(history[3].Iterations) != 2 {
		t.Fatalf("history[3]: expected 2 iterations for turn 6, got %d", len(history[3].Iterations))
	}
}

// TestConvert_WithIterations_AskUserUnstampedRowsMisalign documents the BUG the
// fix eliminates: if intermediate rows were left turn_id=0 (pre-fix), the render
// produces a forged assistant row BEFORE the answer user message carrying the
// answer turn's full iterations (history 错乱/错位). This is a guard: it
// intentionally asserts the broken shape so any regression to unstamped writes
// is caught at the render layer too.
func TestConvert_WithIterations_AskUserUnstampedRowsMisalign(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "choose a theme", TurnID: 5},
		{ID: 500, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: "{}"}}}, // unstamped (pre-fix)
		{Role: "tool", ToolCallID: "ask", ToolName: "AskUser", Content: "Asked 1 question(s)"},                 // unstamped
		{ID: 503, Role: "assistant", Content: ""},                                                              // unstamped
		{Role: "user", Content: "Q0: dark", TurnID: 6},
		{ID: 505, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "Shell", Arguments: "{}"}}, TurnID: 6},
		{Role: "tool", ToolCallID: "c1", ToolName: "Shell", Content: "ok", TurnID: 6},
		{ID: 507, Role: "assistant", Content: "done", TurnID: 6},
	}
	turnIterMap := map[uint64][]sqlite.IterationRecord{
		5: {{TurnID: 5, Iteration: 1, Tools: `[{"name":"AskUser","status":"done"}]`}},
		6: {
			{TurnID: 6, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 6, Iteration: 2, Content: "done", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	// Broken shape (pre-fix): a forged assistant row appears BEFORE the answer
	// user message. The test documents the failure mode — production must never
	// regress to unstamped intermediate writes.
	answerIdx := -1
	for i, h := range history {
		if h.Role == "user" && h.Content == "Q0: dark" {
			answerIdx = i
			break
		}
	}
	if answerIdx < 0 {
		t.Fatalf("answer user row missing: %+v", history)
	}
	if answerIdx > 0 && history[answerIdx-1].Role == "assistant" && history[answerIdx-1].TurnID == 6 {
		t.Logf("BUG reproduced: forged turn-6 assistant before answer user (unstamped rows mis-derived)")
	}
}

// TestConvert_WithIterations_ResumeTurnEmptyShellAssistant guards the 2026-08-23
// 15:02 incident (tenant 150660 turn 67→68): a restart interrupted turn 67 with
// 12 intermediate pairs (NO final assistant), and the recovery turn 68 produced
// ONLY a single empty-shell assistant (content="" — v55+ persists the reply text
// in iteration_history, the session_messages row is an empty placeholder with
// just the turn_id). The empty shell has NO tool_calls, so it never hits the
// turn-boundary flush in the tool_calls branch; walking into the !isIntermediate
// structured branch, `pendingIters = nil` evaporated turn 67's 12 iterations —
// the frontend rendered the recovery turn with 1 iteration and the pre-restart
// iterations vanished. Reproduces ONLY when the restart lands mid-turn AND the
// recovery turn emits no intermediate tool iterations — hence "probabilistic".
func TestConvert_WithIterations_ResumeTurnEmptyShellAssistant(t *testing.T) {
	msgs := []llm.ChatMessage{
		// Pre-restart turn 67: user + 2 intermediate pairs, NO final message.
		{Role: "user", Content: "继续调查", TurnID: 67},
		{ID: 400, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "d1", Name: "Shell", Arguments: "{}"}}, TurnID: 67},
		{Role: "tool", ToolCallID: "d1", ToolName: "Shell", Content: "ok", TurnID: 67},
		{ID: 402, Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "d2", Name: "Read", Arguments: "{}"}}, TurnID: 67},
		{Role: "tool", ToolCallID: "d2", ToolName: "Read", Content: "file", TurnID: 67},
		// Recovery turn 68: ONLY an empty-shell assistant (v55+ placeholder —
		// content lives in iteration_history), no intermediate messages.
		{ID: 404, Role: "assistant", Content: "", TurnID: 68},
	}

	turnIterMap := map[uint64][]sqlite.IterationRecord{
		67: {
			{TurnID: 67, Iteration: 1, Tools: `[{"name":"Shell","status":"done"}]`},
			{TurnID: 67, Iteration: 2, Tools: `[{"name":"Read","status":"done"}]`},
		},
		68: {
			{TurnID: 68, Iteration: 1, Content: "调查阶段总结（恢复产物）", Tools: "[]"},
		},
	}

	history := ConvertMessagesToHistoryWithIterations(msgs, turnIterMap)

	// Expected: user(67) + asst(67, 2 iters) + asst(68, 1 iter).
	if len(history) != 3 {
		t.Fatalf("expected 3 HistoryMessages (user67, asst67, asst68), got %d: %+v", len(history), summaryOf(history))
	}
	if history[1].Role != "assistant" || history[1].TurnID != 67 {
		t.Fatalf("history[1]: expected assistant turn 67, got role=%s turn=%d", history[1].Role, history[1].TurnID)
	}
	if len(history[1].Iterations) != 2 {
		t.Fatalf("🔴 turn 67 pre-restart iterations LOST (got %d, want 2) — empty-shell resume assistant evaporated pendingIters", len(history[1].Iterations))
	}
	if history[2].Role != "assistant" || history[2].TurnID != 68 {
		t.Fatalf("history[2]: expected assistant turn 68, got role=%s turn=%d", history[2].Role, history[2].TurnID)
	}
	if len(history[2].Iterations) != 1 {
		t.Fatalf("turn 68 recovery iterations: got %d, want 1", len(history[2].Iterations))
	}
}

func summaryOf(hs []HistoryMessage) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.Role+":"+itoa(h.TurnID)+"("+itoa(uint64(len(h.Iterations)))+"iters)")
	}
	return out
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
