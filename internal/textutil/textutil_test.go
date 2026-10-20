package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSanitizeUTF8 pins the repair that keeps a torn CJK sequence from
// reaching PostgreSQL, which rejects it with SQLSTATE 22021 and aborts the
// whole INSERT (not just the damaged value).
func TestSanitizeUTF8(t *testing.T) {
	valid := "全仓 CREATE TABLE（非 test）"
	if got := SanitizeUTF8(valid); got != valid {
		t.Fatalf("valid input changed: %q", got)
	}

	// The real production shape: a 3-byte rune cut after 2 bytes.
	torn := "全仓 CREATE TABLE\xef\xbc" // "（" without its third byte
	if utf8.ValidString(torn) {
		t.Fatal("fixture must be invalid")
	}
	got := SanitizeUTF8(torn)
	if !utf8.ValidString(got) {
		t.Fatalf("still invalid after repair: %q", got)
	}
	if !strings.HasPrefix(got, "全仓 CREATE TABLE") {
		t.Fatalf("prefix lost: %q", got)
	}
	if !strings.Contains(got, string(utf8.RuneError)) {
		t.Fatalf("expected U+FFFD marker, got %q", got)
	}

	// A lone lead byte followed by ASCII dots — exactly the bytes in the
	// reported error (0xe6 0x2e 0x2e).
	lone := "abc\xe6..."
	if got := SanitizeUTF8(lone); !utf8.ValidString(got) {
		t.Fatalf("lone lead byte not repaired: %q", got)
	}

	if got := SanitizeUTF8(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
}

func TestTruncateBytesRuneSafe(t *testing.T) {
	s := strings.Repeat("中", 10) // 30 bytes, every rune 3 bytes

	for _, max := range []int{1, 2, 3, 4, 5, 29, 30, 31} {
		got := TruncateBytes(s, max)
		if len(got) > max {
			t.Fatalf("max=%d: exceeded budget, len=%d", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d: invalid UTF-8 %q", max, got)
		}
	}
	// 3 bytes is exactly one rune; 4 must not yield 4 bytes of torn rune.
	if got := TruncateBytes(s, 4); got != "中" {
		t.Fatalf("max=4 got %q want 中", got)
	}
	if got := TruncateBytes(s, 0); got != "" {
		t.Fatalf("max=0 got %q", got)
	}
	if got := TruncateBytes(s, 100); got != s {
		t.Fatalf("under budget should be unchanged")
	}
}

// TestTruncateBytesWithSuffixNoTornRune reproduces the offload-args case that
// produced the production error: budget 80, suffix "...", CJK payload.
func TestTruncateBytesWithSuffixNoTornRune(t *testing.T) {
	s := `{"command": "cd /home/cjw/xbot && echo \"=== 1. 全仓 CREATE TABLE（非 test）...`
	got := TruncateBytesWithSuffix(s, 80, "...")
	if len(got) > 80 {
		t.Fatalf("exceeded budget: %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 produced: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("missing marker: %q", got)
	}

	// Budget smaller than the suffix must not panic (negative index) and must
	// still return valid UTF-8.
	for _, max := range []int{1, 2, 3} {
		got := TruncateBytesWithSuffix(s, max, "...")
		if len(got) > max || !utf8.ValidString(got) {
			t.Fatalf("max=%d: len=%d valid=%v", max, len(got), utf8.ValidString(got))
		}
	}
	// No truncation → no marker.
	if got := TruncateBytesWithSuffix("short", 80, "..."); got != "short" {
		t.Fatalf("marker added on untruncated input: %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	s := strings.Repeat("中", 10)
	if got := TruncateRunes(s, 3); got != "中中中" {
		t.Fatalf("got %q", got)
	}
	if got := TruncateRunes(s, 100); got != s {
		t.Fatalf("over budget changed")
	}
	if got := TruncateRunes(s, 0); got != "" {
		t.Fatalf("zero got %q", got)
	}
}
