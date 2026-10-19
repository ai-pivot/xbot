package storage

import (
	"strings"
	"testing"
	"unicode/utf8"

	"xbot/llm"
)

// TestPGTextRepairsTornRun es pins the write-boundary defence. PostgreSQL
// validates encoding on INSERT and fails the WHOLE statement on a bad byte
// (SQLSTATE 22021), while SQLite stored the same bytes silently — so a single
// torn character upstream used to cost an entire conversation turn.
func TestPGTextRepairsTornRunes(t *testing.T) {
	valid := "全仓 CREATE TABLE（非 test）"
	if got := pgText(valid); got != valid {
		t.Fatalf("valid input changed: %q", got)
	}

	// The production shape: a 3-byte rune cut after 2 bytes, then the ".."
	// from the truncation marker (0xe6 0x2e 0x2e in the reported error).
	for _, bad := range []string{
		"abc\xe6...",
		"全仓 CREATE TABLE\xef\xbc...)",
		"\xe5...",
		strings.Repeat("中", 3) + "\xe9\x9d",
	} {
		if utf8.ValidString(bad) {
			t.Fatalf("fixture must be invalid: %q", bad)
		}
		if got := pgText(bad); !utf8.ValidString(got) {
			t.Fatalf("still invalid after pgText: %q", got)
		}
	}
}

// TestMessageArgsAreValidUTF8 asserts the arg vector handed to pgx can never
// carry an invalid byte sequence, whatever the caller passes in.
func TestMessageArgsAreValidUTF8(t *testing.T) {
	s := &pgSessionStore{}
	msg := llm.ChatMessage{
		Role:             "tool",
		Content:          "输出\xe6...",
		ToolName:         "Shell",
		ToolCallID:       "call\xe6",
		ToolArguments:    `{"command":"cd /x && echo 中文` + "\xe6" + `"}`,
		Detail:           "detail\xef\xbc",
		ReasoningContent: "思考\xe5",
	}

	for i, arg := range s.messageArgs(7, msg) {
		str, ok := arg.(string)
		if !ok {
			continue
		}
		if !utf8.ValidString(str) {
			t.Errorf("arg %d is invalid UTF-8: %q", i, str)
		}
	}
}
