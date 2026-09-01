package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// TestIsSyntheticToolName verifies the prefix list covers every synthetic
// tool name the system injects as notification tool-call pairs. If a new
// injectXxxNotification helper adds a name, it MUST be added here — the LLM
// mimics names from its conversation history, and an unhandled mimic call
// produces the user-visible "unknown tool" error.
func TestIsSyntheticToolName(t *testing.T) {
	known := []string{
		"background_task_result",
		"bg_subagent_completed",
		"bg_subagent_failed",
		"cron_fired",
		"delivered_message",
		"pre_turn_end",
		"user_cancelled",
		"loop_detected",
		"ask_user",
	}
	for _, name := range known {
		if !isSyntheticToolName(name) {
			t.Errorf("isSyntheticToolName(%q) = false, want true (known synthetic name)", name)
		}
	}
	notSynthetic := []string{
		"Shell", "Read", "SubAgent", "WebSearch", "random_tool", "background_task",
	}
	for _, name := range notSynthetic {
		if isSyntheticToolName(name) {
			t.Errorf("isSyntheticToolName(%q) = true, want false (real/unknown tool)", name)
		}
	}
}

// TestSyntheticToolResultIsFriendly verifies the mimic-call result: not an
// error (the model cannot act on "unknown tool"), but a clear instruction
// that this is a notification channel and it should continue with real tools.
func TestSyntheticToolResultIsFriendly(t *testing.T) {
	res, err := syntheticToolResult(llm.ToolCall{Name: "background_task_result", ID: "bg_test"})
	if err != nil {
		t.Fatalf("syntheticToolResult returned error: %v", err)
	}
	if res.IsError {
		t.Error("syntheticToolResult should not be an error result (model cannot act on it)")
	}
	if res.Summary == "" {
		t.Error("syntheticToolResult should return non-empty summary")
	}
	if !strings.Contains(res.Summary, "not a callable tool") || !strings.Contains(res.Summary, "Do NOT call") {
		t.Errorf("syntheticToolResult should explain the situation and instruct continuation, got: %s", res.Summary)
	}
}
