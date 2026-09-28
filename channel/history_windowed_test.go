package channel

import (
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// TestConvertMessagesToHistoryWindowed_BasicWindow covers the windowed
// conversion: the message-row assembly is IDENTICAL to the full path (the user
// row + the single assistant row per turn), and the windowed payload carries
// the text-block Rows as Iterations + the run summaries + the bounds — NOT the
// full record list.
//
// Fixture (turn 1): iter 1 = text block, iter 2 = run head (content + 2
// tools), iters 3..12 = run members (1 tool each), iter 13 = text block.
// The window (the storage's IterationWindowResult): Rows = [1, 13] (the text
// blocks), Runs = [2..12] (the head + 10 members), Total = 13, LoadedTop = 1.
func TestConvertMessagesToHistoryWindowed_BasicWindow(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "do it", TurnID: 1},
		{Role: "assistant", TurnID: 1},
	}
	window := &sqlite.IterationWindowResult{
		Rows: []sqlite.IterationRecord{
			{TurnID: 1, Iteration: 1, Content: "text-1"},
			{TurnID: 1, Iteration: 13, Content: "text-13"},
		},
		Runs: []sqlite.RunSummaryRecord{{
			StartIter:     2, EndIter: 12,
			HeadContent:   "head-text",
			HeadToolsJSON: `[{"name":"t0"},{"name":"t1"},{"name":"t2"},{"name":"t3"},{"name":"t4"},{"name":"t5"},{"name":"t6"},{"name":"t7"}]`,
			ToolCount:     12,
		}},
		Total:     13,
		LoadedTop: 1,
	}
	got := ConvertMessagesToHistoryWindowed(msgs, map[uint64]*sqlite.IterationWindowResult{1: window})
	if len(got) != 2 {
		t.Fatalf("messages = %d, want 2 (user + assistant)", len(got))
	}
	var assistant *HistoryMessage
	for i := range got {
		if got[i].Role == "assistant" && got[i].TurnID == 1 {
			assistant = &got[i]
		}
	}
	if assistant == nil {
		t.Fatalf("no assistant row for turn 1: %+v", got)
	}
	// The windowed payload: the text blocks as Iterations (NOT the full list).
	if n := len(assistant.Iterations); n != 2 {
		t.Fatalf("iterations = %d, want 2 (the text-block Rows only — the run members are NOT transferred)", n)
	}
	if assistant.Iterations[0].Iteration != 1 || assistant.Iterations[1].Iteration != 13 {
		t.Fatalf("iterations = %d,%d, want 1,13", assistant.Iterations[0].Iteration, assistant.Iterations[1].Iteration)
	}
	// The run summary: the head tools (PILL_INLINE_MAX=8) + the true count + the extent.
	if len(assistant.RunSummaries) != 1 {
		t.Fatalf("run summaries = %d, want 1", len(assistant.RunSummaries))
	}
	rs := assistant.RunSummaries[0]
	if rs.StartIter != 2 || rs.EndIter != 12 {
		t.Fatalf("run extent = %d..%d, want 2..12", rs.StartIter, rs.EndIter)
	}
	if rs.HeadContent != "head-text" {
		t.Fatalf("run head content = %q, want head-text", rs.HeadContent)
	}
	if rs.ToolCount != 12 {
		t.Fatalf("run tool count = %d, want 12", rs.ToolCount)
	}
	if len(rs.HeadTools) != 8 {
		t.Fatalf("head tools = %d, want 8 (PILL_INLINE_MAX — ≤8 全显示)", len(rs.HeadTools))
	}
	if rs.HeadTools[0].Name != "t0" {
		t.Fatalf("head tools[0] = %q, want t0 (the run renders its front)", rs.HeadTools[0].Name)
	}
	// The bounds.
	if assistant.IterWindow == nil {
		t.Fatalf("iter window metadata missing")
	}
	if assistant.IterWindow.Total != 13 || assistant.IterWindow.LoadedTop != 1 {
		t.Fatalf("iter window = %+v, want Total=13 LoadedTop=1", assistant.IterWindow)
	}
}

// TestConvertMessagesToHistoryWindowed_GiantRunTurn covers the headline
// scenario: a turn that is ONE giant tool-only run (no text blocks at all).
// The windowed Rows are EMPTY but the run summary is the turn's body — the
// assistant row MUST still be created (with the run summary + the bounds),
// never dropped (the mutation guard: reverting the hasData/attach condition
// to require non-empty Rows drops the turn entirely).
func TestConvertMessagesToHistoryWindowed_GiantRunTurn(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "run everything", TurnID: 7},
		{Role: "assistant", TurnID: 7},
	}
	window := &sqlite.IterationWindowResult{
		Rows: []sqlite.IterationRecord{}, // NO text blocks — the whole turn is one run
		Runs: []sqlite.RunSummaryRecord{{
			StartIter: 1, EndIter: 10001,
			HeadToolsJSON: `[{"name":"t0"},{"name":"t1"},{"name":"t2"},{"name":"t3"},{"name":"t4"},{"name":"t5"},{"name":"t6"},{"name":"t7"}]`,
			ToolCount:     10001,
		}},
		Total:     10001,
		LoadedTop: 1,
	}
	got := ConvertMessagesToHistoryWindowed(msgs, map[uint64]*sqlite.IterationWindowResult{7: window})
	var assistant *HistoryMessage
	for i := range got {
		if got[i].Role == "assistant" && got[i].TurnID == 7 {
			assistant = &got[i]
		}
	}
	if assistant == nil {
		t.Fatalf("the giant-run turn's assistant row was DROPPED (empty Rows must not drop the turn — the run summary IS the body): %+v", got)
	}
	if len(assistant.Iterations) != 0 {
		t.Fatalf("iterations = %d, want 0 (no text blocks)", len(assistant.Iterations))
	}
	if len(assistant.RunSummaries) != 1 {
		t.Fatalf("run summaries = %d, want 1", len(assistant.RunSummaries))
	}
	if assistant.RunSummaries[0].ToolCount != 10001 {
		t.Fatalf("tool count = %d, want 10001 (the +N badge's true count)", assistant.RunSummaries[0].ToolCount)
	}
	if assistant.IterWindow == nil || assistant.IterWindow.Total != 10001 {
		t.Fatalf("iter window = %+v, want Total=10001", assistant.IterWindow)
	}
}

// TestConvertMessagesToHistoryWindowed_FullPathUnchanged covers the backward
// compat: the full path (ConvertMessagesToHistoryWithIterations) is UNCHANGED
// by the refactor — no RunSummaries/IterWindow on the full-path messages (the
// legacy shape), the full iteration list attached as before.
func TestConvertMessagesToHistoryWindowed_FullPathUnchanged(t *testing.T) {
	recs := []sqlite.IterationRecord{
		{TurnID: 1, Iteration: 1, Content: "text"},
		{TurnID: 1, Iteration: 2, Tools: `[{"name":"a"}]`},
	}
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "go", TurnID: 1},
		{Role: "assistant", TurnID: 1},
	}
	got := ConvertMessagesToHistoryWithIterations(msgs, map[uint64][]sqlite.IterationRecord{1: recs})
	for i := range got {
		if got[i].Role != "assistant" {
			continue
		}
		if len(got[i].Iterations) != 2 {
			t.Fatalf("full-path iterations = %d, want 2 (the full list — unchanged)", len(got[i].Iterations))
		}
		if got[i].RunSummaries != nil {
			t.Fatalf("full-path message must NOT carry RunSummaries (the legacy shape): %+v", got[i].RunSummaries)
		}
		if got[i].IterWindow != nil {
			t.Fatalf("full-path message must NOT carry IterWindow (the legacy shape): %+v", got[i].IterWindow)
		}
		return
	}
	t.Fatalf("no assistant row: %+v", got)
}
