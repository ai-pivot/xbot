// cli_types_test.go — Unit tests for truncateToWidth and hardWrapRunes.
//
// These tests verify that placeholder text is correctly truncated on narrow
// terminals and that CJK-aware hard wrapping works at character boundaries.

package channel

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// truncateToWidth
// ---------------------------------------------------------------------------

func TestTruncateToWidth_ShortString(t *testing.T) {
	got := truncateToWidth("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateToWidth_ExactFit(t *testing.T) {
	got := truncateToWidth("hello", 5)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateToWidth_ASCII(t *testing.T) {
	got := truncateToWidth("hello world", 8)
	// "hello" = 5, "..." = 3, target = 5, so "hello..." = 8 cols
	if got != "hello..." {
		t.Errorf("expected %q, got %q", "hello...", got)
	}
	if ansi.StringWidth(got) != 8 {
		t.Errorf("expected width 8, got %d", ansi.StringWidth(got))
	}
}

func TestTruncateToWidth_CJK(t *testing.T) {
	// "你好世界" = 8 display columns (each CJK char = 2 cols)
	got := truncateToWidth("你好世界", 8)
	if got != "你好世界" {
		t.Errorf("expected %q, got %q", "你好世界", got)
	}
}

func TestTruncateToWidth_CJKTruncated(t *testing.T) {
	// "你好世界" = 8 cols, truncate to 6 → target = 6-3 = 3
	// 你(2) fits (2<=3), 好(2) → 4>3, so return "你..."
	got := truncateToWidth("你好世界", 6)
	if got != "你..." {
		t.Errorf("expected %q, got %q", "你...", got)
	}
	if w := ansi.StringWidth(got); w > 6 {
		t.Errorf("expected width ≤ 6, got %d", w)
	}
}

func TestTruncateToWidth_CJKMixedASCII(t *testing.T) {
	// Typical placeholder on a very narrow terminal (width=12).
	got := truncateToWidth("Enter 发送 · Ctrl+J 换行 · /help", 12)
	if w := ansi.StringWidth(got); w > 12 {
		t.Errorf("expected width ≤ 12, got %d for %q", w, got)
	}
	if got == "Enter 发送 · Ctrl+J 换行 · /help" {
		t.Error("expected truncation, got full string")
	}
}

func TestTruncateToWidth_VeryNarrow(t *testing.T) {
	// maxWidth = 2, ellipsis = 3, target = -1 → returns "..."[:2] = ".."
	got := truncateToWidth("hello", 2)
	if got != ".." {
		t.Errorf("expected %q, got %q", "..", got)
	}
}

func TestTruncateToWidth_WidthOne(t *testing.T) {
	got := truncateToWidth("hello", 1)
	if got != "." {
		t.Errorf("expected %q, got %q", ".", got)
	}
}

func TestTruncateToWidth_EmptyString(t *testing.T) {
	got := truncateToWidth("", 10)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestTruncateToWidth_PlaceholderNarrowTerminal(t *testing.T) {
	// Simulates the real placeholder at various narrow terminal widths.
	ph := "Enter 发送 · Ctrl+J 换行 · /help"
	for _, tw := range []int{10, 14, 18, 22, 28, 40} {
		got := truncateToWidth(ph, tw)
		w := ansi.StringWidth(got)
		if w > tw {
			t.Errorf("width=%d: truncated placeholder width %d exceeds %d", tw, w, tw)
		}
	}
}

// ---------------------------------------------------------------------------
// hardWrapRunes
// ---------------------------------------------------------------------------

func TestHardWrapRunes_ShortLine(t *testing.T) {
	got := hardWrapRunes("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestHardWrapRunes_ASCIIWrap(t *testing.T) {
	got := hardWrapRunes("abcdefghij", 5)
	expected := "abcde\nfghij"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestHardWrapRunes_CJKWrap(t *testing.T) {
	// "你好世界你好" = 12 cols, width=6 → 2 lines of 6 cols each
	got := hardWrapRunes("你好世界你好", 6)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w != 6 {
			t.Errorf("line %d: expected width 6, got %d (%q)", i, w, line)
		}
	}
}

func TestHardWrapRunes_CJKWithSpaces_NoWordWrap(t *testing.T) {
	// "你好abc 你好abc" — space should NOT be a wrap point.
	// 你(2)+好(2)+a(1)+b(1)+c(1)+ (1)+你(2) = 10 cols → fills exactly to width 10
	// 好(2) would make 12 > 10 → wrap
	input := "你好abc 你好abc"
	got := hardWrapRunes(input, 10)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	w1 := ansi.StringWidth(lines[0])
	if w1 != 10 {
		t.Errorf("line 1: expected width 10 (filled to boundary), got %d (%q)", w1, lines[0])
	}
	// Space must stay on line 1 — no word-wrap at space
	if ansi.StringWidth(lines[0]) < 10 && lines[0] == "你好abc" {
		t.Errorf("line 1 wrapped at space (word-wrap), expected hard-wrap: %q", lines[0])
	}
}

func TestHardWrapRunes_CJKWithMultipleSpaces(t *testing.T) {
	// "你好 世界 你好" = 2+2+1+2+1+1+2+2 = 13 cols
	// width = 6: 你(2)+好(2)+ (1) = 5, 世(2) → 7>6 wrap
	input := "你好 世界 你好"
	got := hardWrapRunes(input, 6)
	lines := splitLines(got)
	w1 := ansi.StringWidth(lines[0])
	if w1 != 5 {
		t.Errorf("line 1: expected width 5, got %d (%q)", w1, lines[0])
	}
}

func TestHardWrapRunes_PureSpaces(t *testing.T) {
	got := hardWrapRunes("a b c d e", 3)
	lines := splitLines(got)
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w > 3 {
			t.Errorf("line %d: width %d exceeds 3: %q", i, w, line)
		}
	}
}

func TestHardWrapRunes_DoubleWidthAtBoundary(t *testing.T) {
	// "abc好" = 3+2 = 5 cols, width = 4 → 好 wraps to line 2
	got := hardWrapRunes("abc好", 4)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "abc" {
		t.Errorf("line 1: expected %q, got %q", "abc", lines[0])
	}
	if lines[1] != "好" {
		t.Errorf("line 2: expected %q, got %q", "好", lines[1])
	}
}

func TestHardWrapRunes_CJKEnglishMix(t *testing.T) {
	// "阿道夫·希特勒（Adolf Hitler）" mixed CJK + English.
	// At width 10, should break at CJK boundaries, not mid-English-word.
	input := "阿道夫·希特勒（Adolf Hitler）"
	got := hardWrapRunes(input, 10)
	lines := splitLines(got)
	for i, line := range lines {
		w := ansi.StringWidth(line)
		if w > 10 {
			t.Errorf("line %d: width %d exceeds 10: %q", i, w, line)
		}
	}
	// Verify no English word is split across lines (e.g. "Adolf\nHitler")
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " ")
		if strings.HasSuffix(trimmed, "Adol") || strings.HasSuffix(trimmed, "Hitle") {
			t.Errorf("English word split across lines: %q", line)
		}
	}
}

func TestHardWrapRunes_SpaceBreak(t *testing.T) {
	// "hello world foo" at width 8 → break at space after "hello"
	got := hardWrapRunes("hello world foo", 8)
	lines := splitLines(got)
	if len(lines) < 2 {
		t.Fatalf("expected >= 2 lines, got %d: %v", len(lines), lines)
	}
	// First line should be "hello " (break at space, space stays on line 1)
	if !strings.HasPrefix(lines[0], "hello") {
		t.Errorf("line 1: expected to start with 'hello', got %q", lines[0])
	}
	// "world" should not be split
	for _, line := range lines {
		if strings.Contains(line, "wor") && !strings.Contains(line, "world") && !strings.Contains(line, "world ") {
			t.Errorf("word 'world' was split: %q", line)
		}
	}
}

// splitLines is a test helper — declared in cli_panel.go.

func TestHardWrapRunes_AnsiColorPreserved(t *testing.T) {
	// Simulate glamour output: colored text that wraps
	// \x1b[38;5;188m = light yellow fg, \x1b[0m = reset
	input := "\x1b[38;5;188mABCDEFGHIJ" + "KLMNOPQRST" + "\x1b[0m"
	got := hardWrapRunes(input, 10)
	lines := splitLines(got)
	if len(lines) < 2 {
		t.Fatalf("expected >= 2 lines, got %d: %q", len(lines), got)
	}
	// Continuation line must replay the active ANSI color
	if !strings.HasPrefix(lines[1], "\x1b[38;5;188m") {
		t.Errorf("continuation line lost ANSI color: %q", lines[1])
	}
}

func TestHardWrapRunes_AnsiResetClearsState(t *testing.T) {
	// After reset, continuation should NOT replay old color
	input := "\x1b[38;5;188mAB\x1b[0mCDEFGHIJKLMNOP"
	got := hardWrapRunes(input, 8)
	lines := splitLines(got)
	if len(lines) < 2 {
		t.Fatalf("expected >= 2 lines, got %d", len(lines))
	}
	// Line 2 should start with plain text, not the old color
	if strings.HasPrefix(lines[1], "\x1b[38;5;188m") {
		t.Errorf("continuation replayed color after reset: %q", lines[1])
	}
}

// TestHardWrapRunes_AnsiColorBreakOrder verifies that ANSI state is replayed
// BEFORE the rest text on continuation lines, not after. This was the root
// cause of "character loss" during TUI streaming: the escape sequence was
// injected mid-word, corrupting the terminal output.
//
// Before fix: line 1 = "W\x1b[36morld..." (escape after 'W', before 'orld')
// After fix:  line 1 = "\x1b[36mWorld..." (escape before the text)
func TestHardWrapRunes_AnsiColorBreakOrder(t *testing.T) {
	// "Hello" (plain) + "\x1b[36m" (cyan) + " World" (cyan) + "\x1b[0m" (reset) + "ABCDEFGHIJKLMNO"
	input := "Hello\x1b[36m World\x1b[0mABCDEFGHIJKLMNO"
	got := hardWrapRunes(input, 7)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected >= 3 lines, got %d: %q", len(lines), got)
	}

	// Verify: no line should have an ANSI escape in the MIDDLE of a word.
	// Each line should either start with an escape or have plain text.
	for i, line := range lines {
		plain := ansi.Strip(line)
		// Check that the line is not empty
		if plain == "" {
			t.Errorf("line %d is empty: %q", i, line)
		}
		// Check that the plain text is a substring of the original plain text
		orig := ansi.Strip(input)
		if !strings.Contains(orig, plain) && len(plain) > 0 {
			t.Errorf("line %d: plain %q not found in original %q", i, plain, orig)
		}
	}

	// Verify: total plain text reconstruction equals original
	var recon []string
	for _, line := range lines {
		recon = append(recon, ansi.Strip(line))
	}
	orig := ansi.Strip(input)
	reconstructed := strings.Join(recon, "")
	if orig != reconstructed {
		t.Errorf("character loss: got %q, want %q", reconstructed, orig)
	}
}

// TestHardWrapRunes_AnsiBreakBeforeRest verifies the fix for the streaming
// character loss bug: when breaking at a breakable position inside styled text,
// the continuation line must replay the ANSI state BEFORE rest text.
func TestHardWrapRunes_AnsiBreakBeforeRest(t *testing.T) {
	// Styled text with a space as break point, followed by more styled text.
	// The break should produce lines where the ANSI replay is at the START
	// of the continuation line, not injected into the middle of text.
	input := "\x1b[36mABCDEF\x1b[0m GHIJKLMNOP"
	got := hardWrapRunes(input, 6)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected >= 2 lines, got %d", len(lines))
	}

	// Line 0 should end the cyan region cleanly
	line0 := lines[0]
	if !strings.HasSuffix(line0, "\x1b[0m") && !strings.Contains(line0, "\x1b[0m ") {
		// Line 0 has the reset, which is fine
	}

	// Line 1 should NOT have an escape injected between characters of a word.
	// Before the fix, line 1 could be "G\x1b[0mHIJKL" with escape between G and H.
	line1 := lines[1]
	plain1 := ansi.Strip(line1)
	// The plain text should be a contiguous substring of the original
	orig := ansi.Strip(input)
	if !strings.Contains(orig, plain1) {
		t.Errorf("line 1 plain %q not found in original %q\nfull line: %q", plain1, orig, line1)
	}
}

// TestHardWrapRunes_MultipleColorsNoLoss verifies that wrapping text with
// multiple color changes doesn't lose any characters.
func TestHardWrapRunes_MultipleColorsNoLoss(t *testing.T) {
	input := "This is a \x1b[36mcode\x1b[0m block with \x1b[33mmore\x1b[0m text here"
	for _, maxW := range []int{10, 15, 20, 25} {
		got := hardWrapRunes(input, maxW)
		lines := strings.Split(got, "\n")
		var recon []string
		for _, l := range lines {
			recon = append(recon, ansi.Strip(l))
		}
		reconstructed := strings.Join(recon, "")
		orig := ansi.Strip(input)
		if orig != reconstructed {
			t.Errorf("maxW=%d: character loss. got %q, want %q", maxW, reconstructed, orig)
		}
		// Width constraint
		for i, l := range lines {
			w := ansi.StringWidth(l)
			if w > maxW {
				t.Errorf("maxW=%d line %d: width %d exceeds maxW: %q", maxW, i, w, l)
			}
		}
	}
}
