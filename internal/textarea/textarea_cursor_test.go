package textarea

import (
	"testing"

	"github.com/rivo/uniseg"
)

// TestWrapNoTrailingSpaces verifies that wrap() no longer appends phantom
// trailing spaces to each visual line. This was the root cause of cursor
// misalignment at CJK wrap boundaries.
func TestWrapNoTrailingSpaces(t *testing.T) {
	input := "一二三四五六七八"
	result := wrap([]rune(input), 6) // width=6, CJK=2 cols each → 3 chars/line
	// Expected: 3 visual lines, no trailing spaces
	if len(result) != 3 {
		t.Fatalf("expected 3 visual lines, got %d", len(result))
	}
	for i, line := range result {
		if len(line) > 0 && line[len(line)-1] == ' ' {
			t.Errorf("visual line %d ends with unexpected trailing space: %q", i, string(line))
		}
	}
	// Verify content: each line should have exactly the expected CJK chars
	expectedLines := []string{"一二三", "四五六", "七八"}
	for i, line := range result {
		if string(line) != expectedLines[i] {
			t.Errorf("visual line %d: got %q, want %q", i, string(line), expectedLines[i])
		}
	}
}

// TestCursorAtWrapBoundaryCJK verifies cursor navigation across CJK wrap
// boundaries: no phantom positions, consistent left/right movement.
func TestCursorAtWrapBoundaryCJK(t *testing.T) {
	// "一二三四五六七八" at width=6 → 3 visual lines of 3 chars each
	m := New()
	m.SetWidth(12) // internal width will be ~6 after style deductions

	// Type characters one by one
	input := "一二三四五六七八"
	for _, r := range input {
		m.InsertRune(r)
	}

	// After typing all chars, cursor should be at end (col=8).
	if m.col != 8 {
		t.Errorf("after typing 8 CJK chars, col=%d, want 8", m.col)
	}

	// grid should have 3 visual lines
	grid := wrap(m.value[0], m.width)
	if len(grid) != 3 {
		t.Errorf("grid has %d lines, want 3 (width=%d, text=%q)",
			len(grid), m.width, m.Value())
	}

	// Move left across all wrap boundaries. Cursor should visit each
	// character position precisely once.
	visited := make(map[int]bool)
	for m.col > 0 {
		visited[m.col] = true
		m.characterLeft(false)
	}
	visited[0] = true

	// All positions 0-8 should be visited exactly once.
	for i := 0; i <= 8; i++ {
		if !visited[i] {
			t.Errorf("cursor never visited col=%d", i)
		}
	}
	if len(visited) != 9 {
		t.Errorf("visited %d unique positions, want 9", len(visited))
	}

	// Move right across all wrap boundaries.
	visited = make(map[int]bool)
	for m.col < len(m.value[m.row]) {
		visited[m.col] = true
		m.characterRight()
	}
	visited[m.col] = true
	for i := 0; i <= 8; i++ {
		if !visited[i] {
			t.Errorf("cursor never visited col=%d on rightward move", i)
		}
	}
}

// TestCursorUpDownAtWrapBoundaryCJK verifies vertical cursor movement
// across soft-wrap boundaries preserves horizontal position.
func TestCursorUpDownAtWrapBoundaryCJK(t *testing.T) {
	// "一二三四五六七八" at width=6 → 3 visual lines
	m := New()
	m.SetWidth(12)
	m.SetValue("一二三四五六七八")

	// Start at end (col=8, visual line 2).
	m.SetCursorColumn(8)
	if m.col != 8 {
		t.Fatalf("start col=%d, want 8", m.col)
	}

	// CursorUp: from line 2 end → should go to line 1, preserving col
	m.CursorUp()
	li := m.LineInfo()
	t.Logf("After CursorUp from end: col=%d RowOffset=%d StartColumn=%d",
		m.col, li.RowOffset, li.StartColumn)
	// Should be on visual line 1 (RowOffset=1), near the end
	if li.RowOffset != 1 {
		t.Errorf("CursorUp from line 2: RowOffset=%d, want 1", li.RowOffset)
	}

	// CursorUp again: from line 1 → line 0
	m.CursorUp()
	li = m.LineInfo()
	t.Logf("After CursorUp x2: col=%d RowOffset=%d StartColumn=%d",
		m.col, li.RowOffset, li.StartColumn)
	if li.RowOffset != 0 {
		t.Errorf("CursorUp from line 1: RowOffset=%d, want 0", li.RowOffset)
	}

	// CursorDown: back to line 1
	m.CursorDown()
	li = m.LineInfo()
	if li.RowOffset != 1 {
		t.Errorf("CursorDown from line 0: RowOffset=%d, want 1", li.RowOffset)
	}

	// CursorDown: back to line 2
	m.CursorDown()
	li = m.LineInfo()
	if li.RowOffset != 2 {
		t.Errorf("CursorDown from line 1: RowOffset=%d, want 2", li.RowOffset)
	}
}

// TestCursorWrapDiagnostic traces cursor behavior at CJK wrap boundaries.
func TestCursorWrapDiagnostic(t *testing.T) {
	// Simulate user typing "一二三四五六七八" (8 CJK chars) on width=12
	// Each CJK char = 2 columns → 6 chars fill 12 columns exactly, wrapping after 6
	m := New()
	m.SetWidth(12)
	m.SetHeight(6)

	t.Logf("After SetWidth(12): m.width=%d", m.width)

	// Type characters one by one, tracing cursor position after each.
	input := "一二三四五六七八"
	for i, r := range input {
		m.InsertRune(r)
		li := m.LineInfo()
		t.Logf("After typing %q (pos %d): col=%d row=%d RowOffset=%d ColumnOffset=%d StartColumn=%d Width=%d",
			string(r), i+1, m.col, m.row, li.RowOffset, li.ColumnOffset, li.StartColumn, li.Width)
	}

	// Now test character navigation across wrap boundaries
	t.Log("=== Moving cursor left across wrap boundary ===")
	for m.col > 0 {
		m.characterLeft(false)
		li := m.LineInfo()
		t.Logf("  Left → col=%d RowOffset=%d ColumnOffset=%d",
			m.col, li.RowOffset, li.ColumnOffset)
	}

	t.Log("=== Moving cursor right across wrap boundary ===")
	for m.col < len(m.value[m.row]) {
		m.characterRight()
		li := m.LineInfo()
		t.Logf("  Right → col=%d RowOffset=%d ColumnOffset=%d",
			m.col, li.RowOffset, li.ColumnOffset)
	}

	// Also test View rendering
	t.Logf("\n=== View output ===\n%s", m.View())

	// Test CursorUp/CursorDown at wrap boundaries
	m.SetCursorColumn(8) // end
	t.Log("\n=== CursorUp at end ===")
	m.CursorUp()
	li := m.LineInfo()
	t.Logf("  Up → col=%d RowOffset=%d ColumnOffset=%d StartColumn=%d",
		m.col, li.RowOffset, li.ColumnOffset, li.StartColumn)

	t.Log("\n=== CursorDown ===")
	m.CursorDown()
	li = m.LineInfo()
	t.Logf("  Down → col=%d RowOffset=%d ColumnOffset=%d StartColumn=%d",
		m.col, li.RowOffset, li.ColumnOffset, li.StartColumn)

	// Test cursor at wrap boundary
	t.Log("\n=== Cursor at wrap boundary (col=6) ===")
	m.SetCursorColumn(6)
	li = m.LineInfo()
	t.Logf("  col=%d RowOffset=%d ColumnOffset=%d StartColumn=%d Width=%d",
		m.col, li.RowOffset, li.ColumnOffset, li.StartColumn, li.Width)

	// Test cursor at col=7 (start of next visual line)
	t.Log("\n=== Cursor at col=7 ===")
	m.SetCursorColumn(7)
	li = m.LineInfo()
	t.Logf("  col=%d RowOffset=%d ColumnOffset=%d StartColumn=%d Width=%d",
		m.col, li.RowOffset, li.ColumnOffset, li.StartColumn, li.Width)

	// Check the grid directly
	t.Log("\n=== Grid contents ===")
	grid := wrap(m.value[0], m.width)
	for i, line := range grid {
		t.Logf("  grid[%d]: %q (len=%d, strwidth=%d)",
			i, string(line), len(line), uniseg.StringWidth(string(line)))
	}
}
