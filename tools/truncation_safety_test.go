package tools

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Rune-safety regression tests for the shared truncation helpers' callers.
// These strings reach the model (SendMessage delivery receipts, card
// descriptions), and the content is routinely CJK — a raw s[:n] slices a
// 3-byte character in half and hands the LLM invalid UTF-8.

// ── degenerate byte budgets must not panic ──
// s[:maxBytes-4] / s[len-maxBytes+4:] blow up for maxBytes < 4 (negative index /
// out-of-range slice). Found in review; the helpers are exported, so any caller
// passing a small budget would take the process down.

func TestTruncateHeadPreview_TinyBudgetDoesNotPanic(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 3, 4, 5, 8} {
		got := TruncateHeadPreview("中文字符串内容", n)
		if !utf8.ValidString(got) {
			t.Errorf("maxBytes=%d produced invalid UTF-8: %q", n, got)
		}
		if n >= 0 && len(got) > n {
			t.Errorf("maxBytes=%d exceeded the budget: %d bytes (%q)", n, len(got), got)
		}
	}
}

func TestTruncateTailPreview_TinyBudgetDoesNotPanic(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2, 3, 4, 5, 8} {
		got := TruncateTailPreview("中文字符串内容", n)
		if !utf8.ValidString(got) {
			t.Errorf("maxBytes=%d produced invalid UTF-8: %q", n, got)
		}
		if n >= 0 && len(got) > n {
			t.Errorf("maxBytes=%d exceeded the budget: %d bytes (%q)", n, len(got), got)
		}
	}
}

func TestTruncateMsg_CJKNeverSlicedMidRune(t *testing.T) {
	msg := strings.Repeat("这是一个很长的中文消息内容。", 40) // > 200 bytes
	got := truncateMsg(msg, 200)

	if !utf8.ValidString(got) {
		t.Fatalf("truncateMsg produced invalid UTF-8: %q", got)
	}
	if len(got) > 200 {
		t.Errorf("result exceeds the byte budget: %d > 200", len(got))
	}
	if !strings.HasSuffix(got, " ...") {
		t.Errorf("expected the ' ...' suffix, got %q", got)
	}
}

func TestTruncateMsg_ShortInputUnchanged(t *testing.T) {
	if got := truncateMsg("短消息", 200); got != "短消息" {
		t.Errorf("short input must be returned unchanged, got %q", got)
	}
}

func TestDescribeElement_CJKMarkdownNeverSlicedMidRune(t *testing.T) {
	e := &CardElement{
		Tag:        "markdown",
		Properties: map[string]any{"content": strings.Repeat("卡片里的中文文案", 30)},
	}
	var sb strings.Builder
	describeElement(&sb, e, 0)

	if !utf8.ValidString(sb.String()) {
		t.Fatalf("describeElement produced invalid UTF-8: %q", sb.String())
	}
}

func TestDescribeElement_CJKDivTextNeverSlicedMidRune(t *testing.T) {
	e := &CardElement{
		Tag: "div",
		Properties: map[string]any{
			"text": map[string]any{"content": strings.Repeat("分组内中文文本", 30)},
		},
	}
	var sb strings.Builder
	describeElement(&sb, e, 0)

	if !utf8.ValidString(sb.String()) {
		t.Fatalf("describeElement (div) produced invalid UTF-8: %q", sb.String())
	}
}
