package agent

import (
	"testing"

	"xbot/protocol"
)

// ---------------------------------------------------------------------------
// G-5/G-6: the normalized tools[] array — only the CURRENT iteration, stable
// call_id, monotone status, complete on cancel.
// ---------------------------------------------------------------------------

// protocolTool builds a minimal wire tool entry for merge tests.
func protocolTool(status, callID string) protocol.ToolProgress {
	return protocol.ToolProgress{Name: "Shell", Status: status, CallID: callID}
}

// TestNormalizeIterationTools_OnlyCurrentIteration — a stale entry from a
// completed iteration (they linger in ActiveTools/CompletedTools until the next
// beginIteration) must never appear in the current iteration's array; that is
// exactly the filtering every client had to implement itself (and got wrong).
func TestNormalizeIterationTools_OnlyCurrentIteration(t *testing.T) {
	completed := []ToolProgress{
		{Name: "Read", Label: "Read a.go", Status: ToolDone, Iteration: 1, CallID: "call-1-1-0"},
		{Name: "Shell", Label: "go build", Status: ToolRunning, Iteration: 2, CallID: "call-1-2-0"},
	}
	active := []ToolProgress{
		{Name: "Grep", Label: "Grep foo", Status: ToolPending, Iteration: 2, CallID: "call-1-2-1"},
	}

	got := normalizeIterationTools(active, completed, 2, false)
	if len(got) != 2 {
		t.Fatalf("tools = %d, want 2 (iteration 1's Read must be filtered out): %+v", len(got), got)
	}
	for _, tool := range got {
		if tool.Iteration != 2 {
			t.Fatalf("tool %q carries iteration %d, want the current iteration 2", tool.Name, tool.Iteration)
		}
		if tool.CallID == "" {
			t.Fatalf("tool %q has no call_id — identity must always be present", tool.Name)
		}
	}
}

// TestNormalizeIterationTools_MergesDuplicateByCallID — the same call shows up
// as "active pending" and "completed done"; the union must be ONE entry whose
// status is the higher one, otherwise the tool renders twice.
func TestNormalizeIterationTools_MergesDuplicateByCallID(t *testing.T) {
	completed := []ToolProgress{
		{Name: "Shell", Label: "go test", Status: ToolDone, Iteration: 3, CallID: "call-1-3-0", Summary: "ok"},
	}
	active := []ToolProgress{
		{Name: "Shell", Label: "go test", Status: ToolPending, Iteration: 3, CallID: "call-1-3-0"},
	}
	got := normalizeIterationTools(active, completed, 3, false)
	if len(got) != 1 {
		t.Fatalf("tools = %d, want 1 (same call_id must merge): %+v", len(got), got)
	}
	if got[0].Status != "done" {
		t.Fatalf("status = %q, want done (a late pending frame must not regress it)", got[0].Status)
	}
	if got[0].Summary != "ok" {
		t.Fatalf("merge dropped the payload: %+v", got[0])
	}
}

// TestNormalizeIterationTools_StatusNeverRegresses — contract C-8: status may
// only move forward (generating → pending → running → done/failed). A later
// frame carrying an earlier status must not regress the entry — that is the
// "running turned back into queued" flicker users reported.
func TestNormalizeIterationTools_StatusNeverRegresses(t *testing.T) {
	cases := []struct{ prev, next, want string }{
		{"running", "pending", "running"},
		{"done", "running", "done"},
		{"failed", "running", "failed"},
		{"generating", "pending", "pending"},
		{"pending", "running", "running"},
	}
	for _, c := range cases {
		got := mergeToolProgress(
			protocolTool(c.prev, "call-1-1-0"),
			protocolTool(c.next, "call-1-1-0"),
		)
		if got.Status != c.want {
			t.Fatalf("merge(%s → %s) = %s, want %s", c.prev, c.next, got.Status, c.want)
		}
	}
}

// TestNormalizeIterationTools_CancelDowngradesToFailed — contract C-6: on
// cancellation every non-terminal tool becomes `failed`, so the committed block
// never contains a tool that looks like it is still running (and never drops it).
func TestNormalizeIterationTools_CancelDowngradesToFailed(t *testing.T) {
	completed := []ToolProgress{
		{Name: "Read", Status: ToolDone, Iteration: 2, CallID: "call-1-2-0"},
	}
	active := []ToolProgress{
		{Name: "Shell", Status: ToolRunning, Iteration: 2, CallID: "call-1-2-1"},
		{Name: "Grep", Status: ToolPending, Iteration: 2, CallID: "call-1-2-2"},
	}
	got := blockTools(active, completed, 2, true)
	if len(got) != 3 {
		t.Fatalf("tools = %d, want 3 (nothing disappears on cancel)", len(got))
	}
	for _, tool := range got {
		switch tool.Name {
		case "Shell", "Grep":
			if tool.Status != "failed" {
				t.Fatalf("%s = %q, want failed", tool.Name, tool.Status)
			}
		case "Read":
			if tool.Status != "done" {
				t.Fatalf("finished tool changed status to %q", tool.Status)
			}
		}
	}
}

// TestBlockToolsAlwaysCarriesArray — P5: an empty tool set is `[]`, never a
// missing field (missing vs empty must stay distinguishable on the wire).
func TestBlockToolsAlwaysCarriesArray(t *testing.T) {
	got := blockTools(nil, nil, 1, false)
	if got == nil {
		t.Fatal("block tools must be a non-nil empty slice, not nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tool set, got %+v", got)
	}
}

// TestIterationBlockFromSnapshot_DowngradesInterruptedTools — the snapshot path
// must downgrade too (a cancelled run snapshots mid-tool).
func TestIterationBlockFromSnapshot_DowngradesInterruptedTools(t *testing.T) {
	snap := IterationSnapshot{
		Iteration: 4,
		Content:   "partial text",
		Reasoning: "thinking",
		Tools: []IterationToolSnapshot{
			{Name: "Read", Status: string(ToolDone), CallID: "call-1-4-0"},
			{Name: "Shell", Status: string(ToolRunning), CallID: "call-1-4-1"},
		},
	}
	block := iterationBlockFromSnapshot(9, snap, true)
	if block.TurnID != 9 || block.Iteration != 4 || block.Content != "partial text" || block.Reasoning != "thinking" {
		t.Fatalf("unexpected block: %+v", block)
	}
	if !block.Cancelled {
		t.Fatal("block must be marked cancelled")
	}
	for _, tool := range block.Tools {
		if tool.Name == "Shell" && tool.Status != "failed" {
			t.Fatalf("interrupted Shell = %q, want failed", tool.Status)
		}
		if tool.CallID == "" {
			t.Fatalf("tool %q lost its call_id in the committed block", tool.Name)
		}
	}
}

// TestIterationBlockFromSnapshot_LegacyErrorBecomesFailed — the engine's legacy
// `error` status maps onto the protocol's terminal `failed` family so clients
// have one terminal name.
func TestIterationBlockFromSnapshot_LegacyErrorBecomesFailed(t *testing.T) {
	block := iterationBlockFromSnapshot(1, IterationSnapshot{
		Iteration: 1,
		Tools:     []IterationToolSnapshot{{Name: "Shell", Status: string(ToolError), CallID: "call-1-1-0"}},
	}, false)
	if block.Tools[0].Status != "failed" {
		t.Fatalf("status = %q, want failed", block.Tools[0].Status)
	}
}
