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

// TestPGTextRepairsNUL pins the other half of the write-boundary defence.
// NUL (0x00) IS valid UTF-8, so a UTF-8 validator lets it through — but
// PostgreSQL text cannot store it and rejects the whole statement with
//
//	ERROR: invalid byte sequence for encoding "UTF8": 0x00 (SQLSTATE 22021)
//
// Real production failure: Read on a binary file killed the turn's persistence
// (8 occurrences in the server log).
func TestPGTextRepairsNUL(t *testing.T) {
	for _, bad := range []string{
		"before\x00after",
		"ELF\x7f\x02\x01\x00\x00\x00",
		"\x00",
		"中文\x00中文",
	} {
		got := pgText(bad)
		if strings.ContainsRune(got, 0) {
			t.Fatalf("NUL survived pgText: %q", got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("pgText produced invalid UTF-8: %q", got)
		}
		if !strings.Contains(got, "before") && strings.HasPrefix(bad, "before") {
			t.Fatalf("payload lost: %q", got)
		}
	}
}

// TestMessageArgsAreValidUTF8 asserts the arg vector handed to pgx can never
// carry an invalid byte sequence, whatever the caller passes in.
func TestMessageArgsAreValidUTF8(t *testing.T) {
	s := &pgSessionStore{}
	msg := llm.ChatMessage{
		Role:             "tool",
		Content:          "输出\xe6...\x00binary",
		ToolName:         "Shell",
		ToolCallID:       "call\xe6",
		ToolArguments:    `{"command":"cd /x && echo 中文` + "\xe6" + `\x00"}`,
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
		if strings.ContainsRune(str, 0) {
			t.Errorf("arg %d still contains NUL: %q", i, str)
		}
	}
}
