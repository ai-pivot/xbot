package tools

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
)

// ==================== task_id array schema (anti-regression) ====================
//
// The task_* tools take task_id as an ARRAY of ID strings — schema declares a
// pure "array" type (NOT a string/array union: unions get shaky support on
// strict providers, and a bare "string" schema made models pass single
// strings). The Execute side (parseTaskIDs) still tolerates a bare string for
// backward compatibility with older tool-call history, but the schema is the
// single source of truth — always an array. These assertions prevent a
// regression back to Type: "string" or a union type.

func findTaskIDParam(t *testing.T, tool Tool) *llm.ToolParam {
	t.Helper()
	for _, p := range tool.Parameters() {
		if p.Name == "task_id" {
			return &p
		}
	}
	t.Fatalf("tool %s has no task_id parameter", tool.Name())
	return nil
}

func TestTaskTools_TaskIDSchemaPureArray(t *testing.T) {
	for _, tool := range []Tool{&TaskWaitTool{}, &TaskStatusTool{}, &TaskKillTool{}} {
		p := findTaskIDParam(t, tool)
		if p.Type != "array" {
			t.Errorf("%s task_id schema type must be a PURE array (\"array\", no union), got %#v — unions get shaky provider support; a bare string schema made models pass single strings", tool.Name(), p.Type)
		}
		if p.Items == nil || p.Items.Type != "string" {
			t.Errorf("%s task_id must declare Items.Type=string (array element type), got %+v", tool.Name(), p.Items)
		}
		if p.TypeDisplay() != "array" {
			t.Errorf("%s task_id TypeDisplay = %q, want %q", tool.Name(), p.TypeDisplay(), "array")
		}
	}
}

func TestTaskRead_TaskIDStaysSingleString(t *testing.T) {
	// task_read is deliberately single-ID: outputs can be tens of thousands of
	// chars; batch reading would blow the context. The schema must NOT become
	// a union here.
	p := findTaskIDParam(t, &TaskReadTool{})
	if p.Type != "string" {
		t.Errorf("task_read task_id must stay a single string type, got %#v", p.Type)
	}
}

// ==================== task_status multi-ID ====================

func TestTaskStatus_MultiID_TolerantAggregation(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-ms1")
	defer mgr.UnregisterSubAgentTask("sub-ms2")

	// One done sub-agent, one running sub-agent, plus a nonexistent ID in the
	// array — the unknown ID must be reported in the output WITHOUT aborting
	// the rest (per-ID tolerant aggregation).
	done := mgr.RegisterSubAgentTask("sub-ms1", "web:chat", "user1", "explore", "inst-1", func() {})
	mgr.CloseSubAgentTask(done.ID, BgTaskDone, "finished ok")
	mgr.RegisterSubAgentTask("sub-ms2", "web:chat", "user1", "explore", "inst-2", func() {})

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	result, err := (&TaskStatusTool{}).Execute(toolCtx, `{"task_id":["sub-ms1","sub-ms2","nope-x"]}`)
	if err != nil {
		t.Fatalf("multi-ID task_status returned error: %v", err)
	}
	for _, want := range []string{
		"Status of 3 tasks",
		"Sub-Agent Task: sub-ms1",
		"Sub-Agent Task: sub-ms2",
		"nope-x",
		"not found",
	} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("multi-ID task_status output missing %q, got:\n%s", want, result.Summary)
		}
	}
}

func TestTaskStatus_SingleID_BackwardCompatible(t *testing.T) {
	mgr := NewBackgroundTaskManager()

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	// Single (nonexistent) ID keeps the exact old error format.
	_, err := (&TaskStatusTool{}).Execute(toolCtx, `{"task_id":"ghost-id"}`)
	if err == nil || !strings.Contains(err.Error(), "task ghost-id not found") {
		t.Errorf("single-ID not-found error must keep the old format, got: %v", err)
	}
}

// ==================== task_kill multi-ID ====================

func TestTaskKill_MultiID_PerIDResults(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	defer mgr.UnregisterSubAgentTask("sub-mk1")

	mgr.RegisterSubAgentTask("sub-mk1", "web:chat", "user1", "explore", "inst-1", func() {})

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	// Kill one real sub-agent + one nonexistent ID: the failure must be
	// reported per-ID without aborting the rest.
	result, err := (&TaskKillTool{}).Execute(toolCtx, `{"task_id":["sub-mk1","nope-y"]}`)
	if err != nil {
		t.Fatalf("multi-ID task_kill returned error: %v", err)
	}
	for _, want := range []string{
		"Kill results for 2 tasks",
		"- Sub-agent task sub-mk1: cancelled",
		"- Task nope-y: FAILED",
	} {
		if !strings.Contains(result.Summary, want) {
			t.Errorf("multi-ID task_kill output missing %q, got:\n%s", want, result.Summary)
		}
	}
}

func TestTaskKill_SingleID_BackwardCompatible(t *testing.T) {
	mgr := NewBackgroundTaskManager()

	toolCtx := &ToolContext{BgTaskManager: mgr, Ctx: context.Background()}
	result, err := (&TaskKillTool{}).Execute(toolCtx, `{"task_id":"ghost-id"}`)
	if err != nil {
		t.Fatalf("single-ID task_kill returned error: %v", err)
	}
	// Old behavior: single nonexistent ID → error result (not a Go error).
	if !result.IsError {
		t.Errorf("single nonexistent kill should be an error result, got: %+v", result)
	}
	if !strings.Contains(result.Summary, "Failed to kill task ghost-id") {
		t.Errorf("unexpected single-ID kill result: %q", result.Summary)
	}
}

// ==================== parseTaskIDs boundaries ====================

func TestParseTaskIDs(t *testing.T) {
	// Single string.
	ids, err := parseTaskIDs([]byte(`"abc"`))
	if err != nil || len(ids) != 1 || ids[0] != "abc" {
		t.Errorf("string form: got %v, %v", ids, err)
	}
	// Array of strings.
	ids, err = parseTaskIDs([]byte(`["a","b"]`))
	if err != nil || len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("array form: got %v, %v", ids, err)
	}
	// Empty array → empty slice, no error (Execute rejects with "required").
	ids, err = parseTaskIDs([]byte(`[]`))
	if err != nil || len(ids) != 0 {
		t.Errorf("empty array: got %v, %v", ids, err)
	}
	// Non-string/non-array JSON → error.
	if _, err = parseTaskIDs([]byte(`42`)); err == nil {
		t.Error("number should be rejected")
	}
	if _, err = parseTaskIDs([]byte(`[1,2]`)); err == nil {
		t.Error("array of numbers should be rejected")
	}
}

// ==================== TypeDisplay (llm.ToolParam union rendering) ====================

func TestToolParamTypeDisplay(t *testing.T) {
	single := llm.ToolParam{Name: "x", Type: "string"}
	if got := single.TypeDisplay(); got != "string" {
		t.Errorf("single type display = %q, want %q", got, "string")
	}
	union := llm.ToolParam{Name: "x", Type: []string{"string", "array"}}
	if got := union.TypeDisplay(); got != "string|array" {
		t.Errorf("union type display = %q, want %q", got, "string|array")
	}
}
