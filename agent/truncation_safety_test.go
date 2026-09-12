package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// Rune-safety + retrieval-hint regression tests for agent-side truncation.
// All of these strings reach the model or the user, and the payload is
// routinely CJK: a raw s[:n] slices a 3-byte character in half (invalid UTF-8),
// and a silent cut leaves the model thinking the clipped text is complete.

// ── project/global context goes straight into the SYSTEM PROMPT ──

func TestFormatGlobalContext_CJKTruncationRuneSafe(t *testing.T) {
	// > maxProjectContextChars (10000) bytes of CJK.
	content := strings.Repeat("这是全局指令中的一段中文内容，用于验证截断不会切断多字节字符。", 200)

	got := formatGlobalContext(content, "AGENTS.md")

	if !utf8.ValidString(got) {
		t.Fatalf("formatGlobalContext produced invalid UTF-8 (raw byte slice?): %q", got[:200])
	}
	// The existing hint must survive: the model has to know how to read the rest.
	if !strings.Contains(got, "use Read tool") {
		t.Errorf("truncated global context must keep its 'use Read tool' hint, got: %q", tail(got, 300))
	}
	if !strings.Contains(got, "AGENTS.md") {
		t.Errorf("hint must name the file to read, got: %q", tail(got, 300))
	}
}

func TestFormatGlobalContext_ShortInputHasNoHint(t *testing.T) {
	got := formatGlobalContext("短指令。", "AGENTS.md")
	if strings.Contains(got, "truncated") {
		t.Errorf("short context must not be marked truncated, got: %q", got)
	}
}

// ── user-facing error text ──

func TestTruncateErrMsg_CJKTruncationRuneSafe(t *testing.T) {
	msg := strings.Repeat("上游返回的中文错误信息", 40) // > 120 bytes

	got := truncateErrMsg(msg)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateErrMsg produced invalid UTF-8: %q", got)
	}
	if len(got) > 120 {
		t.Errorf("result exceeds the budget: %d > 120", len(got))
	}
}

// ── SubAgent(action="inspect") output ──

func TestInspectInteractiveSession_TruncatedDumpCarriesRetrievalHint(t *testing.T) {
	const (
		role     = "explore"
		instance = "mem-1"
		channel  = "web"
		chatID   = "chat-1"
	)
	key := interactiveKey(channel, chatID, role, instance)

	ia := &interactiveAgent{
		roleName: role,
		instance: instance,
		cfg:      &RunConfig{},
		iterationHistory: []IterationSnapshot{{
			Iteration: 1,
			Content:   strings.Repeat("这一轮的中文内容很长需要被截断。", 60), // > 300 bytes
			Reasoning: strings.Repeat("中文推理过程也很长需要被截断。", 60),
			Tools: []IterationToolSnapshot{{
				Name:   "Read",
				Label:  strings.Repeat("很长的工具标签", 20),
				Status: "done",
			}},
		}},
	}
	a := &Agent{}
	a.interactiveSubAgents.Store(key, ia)
	defer a.interactiveSubAgents.Delete(key)

	out, err := a.InspectInteractiveSession(context.Background(), role, channel, chatID, instance, 5)
	if err != nil {
		t.Fatalf("InspectInteractiveSession: %v", err)
	}

	if !utf8.ValidString(out) {
		t.Fatalf("inspect dump produced invalid UTF-8 (raw byte slice?): %q", out[:200])
	}
	// A truncated dump must tell the model how to see more.
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected a truncation notice, got: %q", out)
	}
	if !strings.Contains(out, "offload_recall") {
		t.Errorf("truncation notice must mention offload_recall, got: %q", tail(out, 500))
	}
	if !strings.Contains(out, `role="explore"`) || !strings.Contains(out, `instance="mem-1"`) {
		t.Errorf("truncation notice must give actionable inspect params, got: %q", tail(out, 500))
	}
}

func TestInspectInteractiveSession_ShortDumpHasNoNotice(t *testing.T) {
	const (
		role     = "explore"
		instance = "short-1"
		channel  = "web"
		chatID   = "chat-2"
	)
	key := interactiveKey(channel, chatID, role, instance)

	ia := &interactiveAgent{
		roleName: role,
		instance: instance,
		cfg:      &RunConfig{},
		iterationHistory: []IterationSnapshot{{
			Iteration: 1,
			Content:   "short",
		}},
	}
	a := &Agent{}
	a.interactiveSubAgents.Store(key, ia)
	defer a.interactiveSubAgents.Delete(key)

	out, err := a.InspectInteractiveSession(context.Background(), role, channel, chatID, instance, 5)
	if err != nil {
		t.Fatalf("InspectInteractiveSession: %v", err)
	}
	if strings.Contains(out, "⚠️ This view is truncated") {
		t.Errorf("short dump must not claim truncation, got: %q", out)
	}
}

// tail returns the last n bytes of s (test helper; input is valid UTF-8 so
// slicing at a byte offset is fine for assertion messages).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
