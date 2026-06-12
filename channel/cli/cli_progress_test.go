package cli

import (
	"strings"
	"testing"
	"time"
	"xbot/protocol"
)

// initTestModel creates a model with channelName/chatID set for progress tests.
func initTestModel() *cliModel {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"
	return model
}

func sendProgress(model *cliModel, payload *protocol.ProgressEvent) {
	if payload.ChatID == "" {
		payload.ChatID = model.channelName + ":" + model.chatID
	}
	model.Update(cliProgressMsg{payload: payload})
}

func sendDone(model *cliModel, content string) {
	model.typing = false
	model.Update(cliOutboundMsg{
		msg: OutboundMsg{
			Content:   content,
			IsPartial: false,
		},
	})
}

func countToolsInSummary(model *cliModel) int {
	// Check assistant messages for iterations (unified model).
	// Also check any remaining tool_summary messages (AskUser).
	for _, msg := range model.messages {
		if len(msg.iterations) > 0 {
			count := 0
			for _, it := range msg.iterations {
				count += len(it.Tools)
			}
			return count
		}
		if len(msg.tools) > 0 {
			return len(msg.tools)
		}
	}
	return 0
}

// Basic: 2 iterations, no final empty iteration
func TestProgressNoDuplication(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Thinking: "A"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: "B"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 500, Iteration: 2},
		},
	})

	// Verify iterationHistory has entries and tools
	if len(model.iterationHistory) == 0 {
		t.Error("Expected iterationHistory to have entries after progress events")
	}

	sendDone(model, "Final answer")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}
}

// Realistic: 2 iterations with 2+1 tools, then empty thinking iteration before done
func TestProgressRealisticSequence(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 1, Thinking: "Let me look"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "Let me look",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read config", Status: "done", Elapsed: 500, Iteration: 1},
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300, Iteration: 1},
		},
	})
	// Iter 1
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: "Based on results"})
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "Based on results",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Fix bug", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})
	// Iter 2: empty thinking (no tools)
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 3, Thinking: ""})

	if len(model.iterationHistory) == 0 {
		t.Error("Expected iterationHistory to have entries")
	}

	sendDone(model, "Here is the fix.")

	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("Expected 3 tools in summary, got %d", tools)
	}
}

// Bug scenario: lastCompletedTools leaking across iterations
func TestLastCompletedToolsLeak(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.typingStartTime = time.Now()

	// Iter 0: 1 tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 0, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 0},
		},
	})
	// Iter 1: 1 tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 1},
		},
	})
	// Iter 2: empty thinking (triggers iter 1 snapshot, should clear lastCompletedTools)
	sendProgress(model, &protocol.ProgressEvent{Phase: "thinking", Iteration: 2, Thinking: ""})

	// Verify lastCompletedTools was cleared after iter 1 snapshot
	if len(model.lastCompletedTools) != 0 {
		t.Errorf("lastCompletedTools should be empty after iter switch, got %d entries", len(model.lastCompletedTools))
	}

	sendDone(model, "Done")

	// Should have exactly 2 tools (Read + Edit), not 3 (no duplicate Edit)
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools (no leak), got %d", tools)
	}
}

// Error tool Iteration: verify error tools have correct Iteration and don't
// appear under the wrong iteration.
func TestErrorToolIterationAttribution(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: a tool that errors
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "Trying A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "error", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1: a tool that succeeds
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "Trying B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

	sendDone(model, "Done")

	// Verify both tools are in summary, each in their own iteration
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}

	// Check iteration attribution in the summary (in assistant messages)
	var foundIter0, foundIter1 bool
	for _, msg := range model.messages {
		for _, it := range msg.iterations {
			if it.Iteration == 1 && len(it.Tools) == 1 && it.Tools[0].Name == "read" && it.Tools[0].Status == "error" {
				foundIter0 = true
			}
			if it.Iteration == 2 && len(it.Tools) == 1 && it.Tools[0].Name == "edit" && it.Tools[0].Status == "done" {
				foundIter1 = true
			}
		}
	}
	if !foundIter0 {
		t.Error("Expected error tool 'read' in iteration 1 of summary")
	}
	if !foundIter1 {
		t.Error("Expected success tool 'edit' in iteration 2 of summary")
	}
}

// Out-of-order CompletedTools: even if the payload contains tools from
// multiple iterations (simulating event timing anomalies), tools should
// be correctly grouped by their Iteration field.
func TestCrossIterationToolsFiltered(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0 with tool from iter 0
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "A",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1},
		},
	})
	// Iter 1 payload that accidentally includes a tool from iter 0 (stale)
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "B",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100, Iteration: 1}, // stale
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200, Iteration: 2},
		},
	})

	sendDone(model, "Done")

	// Summary should have exactly 2 tools (Read in iter 1, Edit in iter 2)
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}

	// Verify iteration attribution (in assistant messages, not tool_summary)
	for _, msg := range model.messages {
		if msg.role == "assistant" && len(msg.iterations) > 0 {
			for _, it := range msg.iterations {
				if it.Iteration == 1 {
					if len(it.Tools) != 1 || it.Tools[0].Name != "read" {
						t.Errorf("Iter 1 should have 1 'read' tool, got %+v", it.Tools)
					}
				}
				if it.Iteration == 2 {
					if len(it.Tools) != 1 || it.Tools[0].Name != "edit" {
						t.Errorf("Iter 2 should have 1 'edit' tool, got %+v", it.Tools)
					}
				}
			}
		}
	}
}

// ==================== Background Task Injection ====================

func TestBgTaskInjectedUserMessage_ShowsAsUserMessage(t *testing.T) {
	model := initTestModel()

	content := "[System Notification] Background task abc123 completed.\nCommand: sleep 30\nStatus: done | Elapsed: 30s\nExit Code: 0\n\nOutput:\nok"

	// Simulate InjectUserMessage
	model.Update(cliInjectedUserMsg{content: content})

	// Should have exactly 1 message with role "user"
	userMsgCount := 0
	for _, msg := range model.messages {
		if msg.role == "user" {
			userMsgCount++
			if !strings.Contains(msg.content, "abc123") {
				t.Error("user message should contain task ID")
			}
		}
	}
	if userMsgCount != 1 {
		t.Errorf("expected 1 user message, got %d", userMsgCount)
	}
}

func TestBgTaskInjectedUserMessage_StartsSpinner(t *testing.T) {
	model := initTestModel()

	// Before injection, not typing
	if model.typing {
		t.Error("should not be typing initially")
	}

	_, cmd := model.Update(cliInjectedUserMsg{content: "bg task done"})

	// After injection, should be typing and re-arm fast tick chain.
	// This prevents spinner/elapsed timers from freezing when a bg task
	// completion arrives while the UI was idle.
	if cmd == nil {
		t.Fatal("expected injected bg-task message to schedule follow-up commands (tick/toast)")
	}
	if !model.typing {
		t.Error("should be typing after bg injection")
	}
	if model.inputReady {
		t.Error("input should not be ready during processing")
	}
}

func TestBgTaskInjectedUserMessage_RefreshesBgCount(t *testing.T) {
	model := initTestModel()

	callCount := 0
	model.bgTaskCountFn = func() int {
		callCount++
		return 2
	}

	model.Update(cliInjectedUserMsg{content: "bg task done"})

	// Should have called bgTaskCountFn
	if callCount != 1 {
		t.Errorf("bgTaskCountFn should be called once, got %d", callCount)
	}
	if model.bgTaskCount != 2 {
		t.Errorf("bgTaskCount should be 2, got %d", model.bgTaskCount)
	}
}

func TestBgDrainCompletedTool_AppearsInIteration(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: normal tool + bg drain tool in same iteration
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 100, Iteration: 1},
			{Name: "background_task_result", Label: "bg:abc123", Status: "done", Elapsed: 30000, Iteration: 1},
		},
	})

	// Final done — snapshot into summary
	sendDone(model, "all done")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("expected 2 tools in summary, got %d", tools)
	}
}

func TestBgDrainCrossIterationDoesNotLeak(t *testing.T) {
	model := initTestModel()
	model.startAgentTurn()
	model.typingStartTime = time.Now()

	// Iter 0: bg tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 1, Thinking: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:old", Status: "done", Elapsed: 1000, Iteration: 1},
		},
	})

	// Iter 1: bg tool
	sendProgress(model, &protocol.ProgressEvent{
		Phase: "tool_exec", Iteration: 2, Thinking: "working",
		CompletedTools: []protocol.ToolProgress{
			{Name: "background_task_result", Label: "bg:new", Status: "done", Elapsed: 2000, Iteration: 2},
		},
	})

	// Final done — snapshot both iterations into summary
	sendDone(model, "done")

	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("expected 2 tools in summary (one per iteration), got %d", tools)
	}
}
