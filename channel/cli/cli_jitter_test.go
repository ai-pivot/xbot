package cli

import (
	"strings"
	"testing"

	"xbot/protocol"
)

// TestPulseJitter_SequentialTools verifies that the pulse spinner does NOT
// appear/disappear as tools transition between pending→running→done states.
// This is the root cause of the H→H+1→H height jitter: the pulse adds 1 line
// when no tool is "running", then loses it when the next tool starts running.
func TestPulseJitter_SequentialTools(t *testing.T) {
	model := newCLIModel()

	// Step 1: Tool A running, Tool B pending
	// Before fix: hasSpinner=true (A running), no pulse → 2 lines
	// After fix: same (tools present suppresses pulse)
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: make build", Status: "running"},
			{Name: "Read", Label: "Read: main.go", Status: "pending"},
		},
	}
	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered := renderTurnBlocks(blocks)
	lineCount1 := strings.Count(rendered, "\n") + 1
	if hasPulseBlock(blocks) {
		t.Errorf("Step 1 (A running, B pending): pulse should NOT appear — "+
			"causes jitter when next tool starts. Rendered:\n%s", rendered)
	}

	// Step 2: Tool A done, Tool B pending (A just finished, B hasn't started)
	// Before fix: hasSpinner=false → pulse appears → 3 lines (JITTER +1!)
	// After fix: tools present → pulse suppressed → 2 lines (no jitter)
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: make build", Status: "done", Elapsed: 5000},
			{Name: "Read", Label: "Read: main.go", Status: "pending"},
		},
	}
	blocks = model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered = renderTurnBlocks(blocks)
	lineCount2 := strings.Count(rendered, "\n") + 1
	if hasPulseBlock(blocks) {
		t.Errorf("Step 2 (A done, B pending): pulse should NOT appear — "+
			"this is the +1 jitter. Rendered:\n%s", rendered)
	}
	if lineCount2 != lineCount1 {
		t.Errorf("Step 1→2 line count changed: %d→%d (should be same, no jitter)",
			lineCount1, lineCount2)
	}

	// Step 3: Tool A done, Tool B running (B starts)
	// Before fix: hasSpinner=true → pulse disappears → 2 lines (JITTER -1!)
	// After fix: same — tools present, pulse suppressed
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: make build", Status: "done", Elapsed: 5000},
			{Name: "Read", Label: "Read: main.go", Status: "running"},
		},
	}
	blocks = model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered = renderTurnBlocks(blocks)
	lineCount3 := strings.Count(rendered, "\n") + 1
	if hasPulseBlock(blocks) {
		t.Errorf("Step 3 (A done, B running): pulse should NOT appear. Rendered:\n%s", rendered)
	}
	if lineCount3 != lineCount1 {
		t.Errorf("Step 2→3 line count changed: %d→%d (should be same, no jitter)",
			lineCount2, lineCount3)
	}

	// Step 4: Both done
	// Before fix: hasSpinner=false → pulse appears → 3 lines (JITTER +1!)
	// After fix: tools present → pulse suppressed → 2 lines
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: make build", Status: "done", Elapsed: 5000},
			{Name: "Read", Label: "Read: main.go", Status: "done", Elapsed: 10},
		},
	}
	blocks = model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered = renderTurnBlocks(blocks)
	lineCount4 := strings.Count(rendered, "\n") + 1
	if hasPulseBlock(blocks) {
		t.Errorf("Step 4 (both done): pulse should NOT appear. Rendered:\n%s", rendered)
	}
	if lineCount4 != lineCount1 {
		t.Errorf("Step 3→4 line count changed: %d→%d (should be same, no jitter)",
			lineCount3, lineCount4)
	}
}

// TestPulseJitter_SingleToolDone verifies that a single done tool suppresses
// the pulse. Before the fix, a done tool + pulse caused +1 line compared to
// when the tool was running.
func TestPulseJitter_SingleToolDone(t *testing.T) {
	model := newCLIModel()

	// Tool running: no pulse (hasSpinner=true from running)
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: ls", Status: "running"},
		},
	}
	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered := renderTurnBlocks(blocks)
	lineCountRunning := strings.Count(rendered, "\n") + 1

	// Tool done: should also have no pulse
	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 1,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Label: "Shell: ls", Status: "done", Elapsed: 50},
		},
	}
	blocks = model.liveIterationBlocks(model.progressState.current, 80, "")
	rendered = renderTurnBlocks(blocks)
	lineCountDone := strings.Count(rendered, "\n") + 1

	if hasPulseBlock(blocks) {
		t.Errorf("Done tool should suppress pulse (causes +1 jitter vs running). "+
			"Rendered:\n%s", rendered)
	}
	if lineCountDone != lineCountRunning {
		t.Errorf("Line count changed running→done: %d→%d (should be same)",
			lineCountRunning, lineCountDone)
	}
}

// TestPulseJitter_EmptyIterationShowsPulse verifies that the pulse IS shown
// when the live iteration has no tools at all (thinking phase).
func TestPulseJitter_EmptyIterationShowsPulse(t *testing.T) {
	model := newCLIModel()

	model.progressState.current = &protocol.ProgressEvent{
		Phase:     "thinking",
		Iteration: 1,
	}
	blocks := model.liveIterationBlocks(model.progressState.current, 80, "")

	if !hasPulseBlock(blocks) {
		t.Error("Pulse should appear when iteration is empty (no tools, no content)")
	}
}

// hasPulseBlock returns true if any block in the slice is a pulse block.
func hasPulseBlock(blocks []turnBlock) bool {
	for _, b := range blocks {
		if b.kind == turnBlockPulse {
			return true
		}
	}
	return false
}
