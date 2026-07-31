package channel

import (
	"strings"
	"testing"
)

func TestFormatToolLabel_Short(t *testing.T) {
	tests := []struct {
		name     string
		argsJSON string
		want     string
	}{
		{"Shell", `{"command":"ls -la"}`, "Shell(ls -la)"},
		{"Read", `{"path":"/etc/hosts"}`, "Read(/etc/hosts)"},
		{"Grep", `{"pattern":"TODO"}`, "Grep(TODO)"},
		{"WebSearch", `{"query":"hello"}`, "WebSearch(hello)"},
		{"SubAgent", `{"role":"explore","task":"find bug"}`, "SubAgent(explore: find bug)"},
		{"Unknown", `{"key":"value"}`, "Unknown(value)"},
		{"NoArgs", `{}`, "NoArgs"},
		{"InvalidJSON", `not json`, "InvalidJSON"},
	}
	for _, tt := range tests {
		got := formatToolLabel(tt.name, tt.argsJSON)
		if got != tt.want {
			t.Errorf("formatToolLabel(%q, %q) = %q, want %q", tt.name, tt.argsJSON, got, tt.want)
		}
	}
}

func TestFormatToolLabel_LongCommand(t *testing.T) {
	longCmd := "this is a very long shell command that exceeds the max label length and should be truncated"
	got := formatToolLabel("Shell", `{"command":"`+longCmd+`"}`)
	if len(got) <= 0 {
		t.Fatal("expected non-empty result")
	}
	if len(got) <= len("Shell()") {
		t.Fatalf("result too short: %q", got)
	}
	// Should be truncated with "..."
	if len([]rune(got)) > 60 {
		t.Errorf("result too long (%d runes): %q", len([]rune(got)), got)
	}
}

func TestFormatToolLabel_LongToolName(t *testing.T) {
	// Tool name longer than maxLen (60) — must NOT panic.
	longName := "very_long_tool_name_that_exceeds_the_max_label_length_of_sixty_characters_xxxxxxxxxxxxxxxx"
	got := formatToolLabel(longName, `{"param":"some value"}`)
	if got == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestFormatToolLabel_VeryLongToolName(t *testing.T) {
	// Extremely long tool name — must NOT panic.
	extremeName := "a" + strings.Repeat("x", 500)
	got := formatToolLabel(extremeName, `{"param":"val"}`)
	if got == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestFormatToolLabel_CJKContent(t *testing.T) {
	// CJK characters — rune-safe truncation.
	cjk := "这是一个非常长的中文命令需要被截断处理测试用例"
	got := formatToolLabel("Shell", `{"command":"`+cjk+`"}`)
	if got == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestTruncateLabel(t *testing.T) {
	tests := []struct {
		s        string
		maxRunes int
		want     string
	}{
		{"hello", 10, "hello"},         // no truncation needed
		{"hello", 5, "hello"},          // exactly fits
		{"hello", 3, "hel"},            // maxRunes <= 3, no "..."
		{"hello world", 8, "hello..."}, // truncate with ellipsis
		{"hello", 0, "hello"},          // maxRunes <= 0, return original
		{"hello", -1, "hello"},         // negative, return original
		{"hi", 1, "h"},                 // very short maxRunes
	}
	for _, tt := range tests {
		got := truncateLabel(tt.s, tt.maxRunes)
		if got != tt.want {
			t.Errorf("truncateLabel(%q, %d) = %q, want %q", tt.s, tt.maxRunes, got, tt.want)
		}
	}
}

func TestFormatToolLabel_SubAgentLongTask(t *testing.T) {
	longTask := "this is a very long task description that should be truncated to fit within thirty runes"
	got := formatToolLabel("SubAgent", `{"role":"explore","task":"`+longTask+`"}`)
	// Should not panic and should contain truncation
	if len([]rune(got)) > 60 {
		t.Errorf("result too long: %q (%d runes)", got, len([]rune(got)))
	}
}
