package cli

import (
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// TestPendingToolSummary_NoLeakBetweenTurns verifies that pendingToolSummary
// from Turn 1 does NOT leak into Turn 2. This is the root cause of
// "U1 A1 U2 A1" bug: Turn 1's iterations (stored in pendingToolSummary)
// survive into Turn 2 because startAgentTurn doesn't clear it. Turn 2's
// PhaseDone dedup by iteration number skips Turn 2's iterations (same numbers
// 0,1,2 as Turn 1), so handleAgentMessage bakes Turn 1's iterations into A2.
func TestPendingToolSummary_NoLeakBetweenTurns(t *testing.T) {
	model := initTestModel()

	// ── Turn 1 ──
	model.startAgentTurn()
	turn1 := model.agentTurnID

	// Simulate Turn 1 iterations
	model.iterationHistory = []cliIterationSnapshot{
		{Iteration: 0, Thinking: "turn1-iter0", Tools: []protocol.ToolProgress{
			{Name: "Read", Label: "file1.go", Status: "done", Elapsed: 100, Iteration: 0},
		}},
		{Iteration: 1, Thinking: "turn1-iter1", Tools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build", Status: "done", Elapsed: 200, Iteration: 1},
		}},
	}
	model.lastSeenIteration = 1

	// PhaseDone for Turn 1 (populates pendingToolSummary)
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 1,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go build", Status: "done", Elapsed: 200, Iteration: 1},
		},
	})

	// Verify pendingToolSummary has Turn 1's data
	if model.pendingToolSummary == nil || len(model.pendingToolSummary.iterations) != 2 {
		t.Fatalf("Turn 1: pendingToolSummary should have 2 iterations, got %v", model.pendingToolSummary)
	}

	// Agent reply for Turn 1
	sendDone(model, "A1 response text")

	// After consuming, pendingToolSummary should be cleared
	if model.pendingToolSummary != nil {
		t.Error("pendingToolSummary should be cleared after handleAgentMessage consumes it")
	}

	// Verify A1 has correct iterations
	var a1 *cliMessage
	for i := range model.messages {
		if model.messages[i].role == "assistant" && model.messages[i].turnID == turn1 {
			a1 = &model.messages[i]
			break
		}
	}
	if a1 == nil {
		t.Fatal("A1 message not found")
	}
	if len(a1.iterations) != 2 {
		t.Errorf("A1 should have 2 iterations, got %d", len(a1.iterations))
	}

	// ── Turn 2 ──
	model.startAgentTurn()
	turn2 := model.agentTurnID

	if turn2 == turn1 {
		t.Fatal("agentTurnID should increment between turns")
	}

	// pendingToolSummary MUST be cleared by startAgentTurn
	if model.pendingToolSummary != nil {
		t.Fatal("pendingToolSummary must be cleared by startAgentTurn — stale Turn 1 data will leak into Turn 2")
	}

	// Simulate Turn 2 iterations
	model.iterationHistory = []cliIterationSnapshot{
		{Iteration: 0, Thinking: "turn2-iter0", Tools: []protocol.ToolProgress{
			{Name: "Edit", Label: "fix.go", Status: "done", Elapsed: 50, Iteration: 0},
		}},
		{Iteration: 1, Thinking: "turn2-iter1", Tools: []protocol.ToolProgress{
			{Name: "Grep", Label: "search", Status: "done", Elapsed: 30, Iteration: 1},
		}},
		{Iteration: 2, Thinking: "turn2-iter2", Tools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "done", Elapsed: 150, Iteration: 2},
		}},
	}
	model.lastSeenIteration = 2

	// PhaseDone for Turn 2
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "done",
		Iteration: 2,
		CompletedTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "go test", Status: "done", Elapsed: 150, Iteration: 2},
		},
	})

	// Verify pendingToolSummary has ONLY Turn 2's data (3 iterations)
	if model.pendingToolSummary == nil {
		t.Fatal("Turn 2 PhaseDone should populate pendingToolSummary")
	}
	if len(model.pendingToolSummary.iterations) != 3 {
		t.Errorf("Turn 2 pendingToolSummary should have 3 iterations, got %d", len(model.pendingToolSummary.iterations))
	}

	// Verify Turn 2's iterations are NOT Turn 1's
	for _, it := range model.pendingToolSummary.iterations {
		if it.Thinking == "turn1-iter0" || it.Thinking == "turn1-iter1" {
			t.Errorf("Turn 1 iteration leaked into Turn 2: %s", it.Thinking)
		}
	}

	// Agent reply for Turn 2
	sendDone(model, "A2 response text")

	// ── Verify final state: U1 A1 U2 A2 ──
	var assistants []cliMessage
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistants = append(assistants, msg)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("expected 2 assistant messages, got %d", len(assistants))
	}

	// A1 should have Turn 1's iterations
	if len(assistants[0].iterations) != 2 {
		t.Errorf("A1 iterations = %d, want 2", len(assistants[0].iterations))
	}
	for _, it := range assistants[0].iterations {
		if it.Thinking != "turn1-iter0" && it.Thinking != "turn1-iter1" {
			t.Errorf("A1 has wrong iteration: %s", it.Thinking)
		}
	}

	// A2 should have Turn 2's iterations (NOT Turn 1's!)
	if len(assistants[1].iterations) != 3 {
		t.Errorf("A2 iterations = %d, want 3", len(assistants[1].iterations))
	}
	for _, it := range assistants[1].iterations {
		if it.Thinking == "turn1-iter0" || it.Thinking == "turn1-iter1" {
			t.Errorf("A2 has Turn 1 iteration leaked: %s — this is the 'U1 A1 U2 A1' bug",
				it.Thinking)
		}
	}

	// Verify content is correct (not swapped)
	if assistants[0].content != "A1 response text" {
		t.Errorf("A1 content = %q, want 'A1 response text'", assistants[0].content)
	}
	if assistants[1].content != "A2 response text" {
		t.Errorf("A2 content = %q, want 'A2 response text'", assistants[1].content)
	}
}

// TestPendingToolSummary_ClearedOnCancelAck verifies that cancel ack also
// clears pendingToolSummary, preventing stale data from surviving into the
// next turn after a cancelled turn.
func TestPendingToolSummary_ClearedOnCancelAck(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	turn1 := model.agentTurnID
	model.cancelTargetTurnID = turn1

	// Populate iterationHistory + pendingToolSummary
	model.iterationHistory = []cliIterationSnapshot{
		{Iteration: 0, Thinking: "cancel-iter0", Tools: []protocol.ToolProgress{
			{Name: "Read", Label: "file.go", Status: "done", Elapsed: 100, Iteration: 0},
		}},
	}
	model.lastSeenIteration = 0
	model.pendingToolSummary = &cliMessage{iterations: model.iterationHistory}

	// Cancel ack
	model.Update(cliOutboundMsg{
		msg: channel.OutboundMsg{
			Content:  "",
			Metadata: map[string]string{"cancelled": "true"},
		},
	})

	// pendingToolSummary must be cleared
	if model.pendingToolSummary != nil {
		t.Error("pendingToolSummary should be cleared after cancel ack")
	}
}
