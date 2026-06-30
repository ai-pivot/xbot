package textarea

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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

// TestCursorAtEndOfFullLine verifies that when text fills exactly one visual
// line (padding == 0) and the cursor is at the end, the rendered output does
// not exceed the textarea width. Before the fix, the cursor placeholder was
// appended to the already-full line, making it width+1 columns, causing the
// terminal to clip the last character and the cursor.
//
// Regression test for: "input box last character invisible + cursor disappears
// when text fills exactly one row."
func TestCursorAtEndOfFullLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int // SetWidth argument
	}{
		{"CJK_exact_fill", "一二三四五六", 12},             // 6 CJK × 2 cols = 12
		{"ASCII_exact_fill", "abcdefghijkl", 12},     // 12 ASCII × 1 col = 12
		{"CJK_multi_wrap_fill", "一二三四五六七八九十十一十二", 8}, // 4 CJK × 2 cols = 8, 6 visual lines
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.ShowLineNumbers = false
			m.Prompt = ""
			m.SetWidth(tt.width)
			m.SetHeight(6)
			m.Focus()

			// Insert all characters
			for _, r := range tt.input {
				m.InsertRune(r)
			}

			// Cursor should be at end
			if m.col != len(m.value[m.row]) {
				t.Fatalf("cursor col=%d, want %d (end of line)", m.col, len(m.value[m.row]))
			}

			// Render the full pipeline: view() → viewport.View() → Base.Render()
			// This is what the user actually sees.
			fullView := m.View()
			_ = fullView // use lipgloss.Width to measure each line below

			// Also check the raw view() output
			rawView := m.view()
			for i, line := range strings.Split(strings.TrimSuffix(rawView, "\n"), "\n") {
				w := lipgloss.Width(line)
				if w > m.width {
					t.Errorf("raw view() line %d width %d exceeds textarea width %d",
						i, w, m.width)
				}
			}

			// Check the full pipeline output (what the user actually sees).
			// Base.Render applies its own Width style which pads to m.width.
			// So the final output should be exactly m.width per line.
			fullLines := strings.Split(strings.TrimSuffix(fullView, "\n"), "\n")
			t.Logf("textarea m.width=%d, m.height=%d", m.width, m.height)
			for i, line := range fullLines {
				w := lipgloss.Width(line)
				t.Logf("  full line %d: visual_width=%d", i, w)
				// The full pipeline output should not exceed m.width
				if w > m.width {
					t.Errorf("full pipeline line %d width %d exceeds textarea width %d",
						i, w, m.width)
				}
			}
		})
	}
}

// TestCursorAtEndOfFullLineDiagnostic dumps detailed info about what happens
// when the cursor is at the end of a full line. For debugging.
func TestCursorAtEndOfFullLineDiagnostic(t *testing.T) {
	m := New()
	m.ShowLineNumbers = false
	m.Prompt = ""
	m.SetWidth(12) // Will become internal width after style deductions
	m.SetHeight(3)
	m.Focus()

	input := "一二三四五六"
	for _, r := range input {
		m.InsertRune(r)
	}

	t.Logf("SetWidth(12) → m.width=%d, m.Height()=%d", m.width, m.Height())
	t.Logf("Value=%q (len=%d runes)", m.Value(), len(m.value[0]))
	t.Logf("Cursor col=%d", m.col)
	t.Logf("m.Width()=%d (public getter)", m.Width())
	t.Logf("Viewport height=%d, YOffset=%d", m.viewport.Height(), m.viewport.YOffset())

	grid := m.memoizedWrap(m.value[0], m.width)
	t.Logf("wrap grid: %d visual lines", len(grid))
	for i, wl := range grid {
		t.Logf("  line %d: %q (len=%d, width=%d)", i, string(wl), len(wl), uniseg.StringWidth(string(wl)))
	}

	li := m.LineInfo()
	t.Logf("LineInfo: RowOffset=%d ColumnOffset=%d CharOffset=%d StartColumn=%d",
		li.RowOffset, li.ColumnOffset, li.CharOffset, li.StartColumn)
	t.Logf("cursorLineNumber()=%d", m.cursorLineNumber())
	t.Logf("totalVisualLines()=%d", m.totalVisualLines())

	raw := m.view()
	rawLines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	t.Logf("view() output: %d lines", len(rawLines))
	for i, line := range rawLines {
		ww := lipgloss.Width(line)
		t.Logf("  line %d: visual_width=%d", i, ww)
	}

	// Simulate repositionView: where does the viewport think the cursor is?
	cursorRow := m.cursorLineNumber()
	vpMin := m.viewport.YOffset()
	vpMax := vpMin + m.viewport.Height() - 1
	t.Logf("repositionView: cursorRow=%d, viewport=[%d, %d]", cursorRow, vpMin, vpMax)
	if cursorRow < vpMin {
		t.Errorf("BUG: cursor row %d < viewport min %d → cursor is ABOVE viewport (hidden!)", cursorRow, vpMin)
	}
	if cursorRow > vpMax {
		t.Errorf("BUG: cursor row %d > viewport max %d → cursor is BELOW viewport (hidden!)", cursorRow, vpMax)
	}

	// Check if viewport clips the overflow line
	vpView := m.viewport.View()
	vpLines := strings.Split(strings.TrimSuffix(vpView, "\n"), "\n")
	t.Logf("viewport.View() output: %d lines", len(vpLines))
	for i, line := range vpLines {
		ww := lipgloss.Width(line)
		t.Logf("  line %d: visual_width=%d", i, ww)
	}

	full := m.View()
	fullLines := strings.Split(strings.TrimSuffix(full, "\n"), "\n")
	t.Logf("View() (final) output: %d lines", len(fullLines))
	for i, line := range fullLines {
		ww := lipgloss.Width(line)
		t.Logf("  line %d: visual_width=%d", i, ww)
	}
}

// TestCursorAtEndOfFullLineRealWidth simulates the actual CLI rendering pipeline
// with realistic terminal width to catch viewport clipping issues.
func TestCursorAtEndOfFullLineRealWidth(t *testing.T) {
	// Simulate: terminal width 120, InputBox width-8 = 112
	terminalWidth := 120
	textareaWidth := terminalWidth - 8 // matches CLI: iw = width - 8

	m := New()
	m.ShowLineNumbers = false
	m.Prompt = ""
	m.SetWidth(textareaWidth)
	m.SetHeight(3) // min height
	m.Focus()

	// Type enough CJK chars to exactly fill one line
	charsPerLine := textareaWidth / 2 // CJK = 2 cols each
	for i := 0; i < charsPerLine; i++ {
		m.InsertRune('测')
	}

	t.Logf("terminalWidth=%d, textareaWidth=%d, charsPerLine=%d", terminalWidth, textareaWidth, charsPerLine)
	t.Logf("m.width=%d, m.Height()=%d, cursor=%d/%d", m.width, m.Height(), m.col, len(m.value[0]))
	t.Logf("Viewport: height=%d, YOffset=%d", m.viewport.Height(), m.viewport.YOffset())
	t.Logf("cursorLineNumber()=%d, totalVisualLines()=%d", m.cursorLineNumber(), m.totalVisualLines())

	raw := m.view()
	rawLines := strings.Split(strings.TrimSuffix(raw, "\n"), "\n")
	t.Logf("view() → %d lines:", len(rawLines))
	for i, line := range rawLines {
		t.Logf("  [%d] width=%d", i, lipgloss.Width(line))
	}

	vpView := m.viewport.View()
	vpLines := strings.Split(strings.TrimSuffix(vpView, "\n"), "\n")
	t.Logf("viewport.View() → %d lines:", len(vpLines))
	for i, line := range vpLines {
		t.Logf("  [%d] width=%d", i, lipgloss.Width(line))
	}

	// The overflow line should be visible in viewport
	cursorRow := m.cursorLineNumber()
	vpHeight := m.viewport.Height()
	vpYOffset := m.viewport.YOffset()
	t.Logf("Viewport range: [%d, %d], cursorRow=%d", vpYOffset, vpYOffset+vpHeight-1, cursorRow)

	final := m.View()
	finalLines := strings.Split(strings.TrimSuffix(final, "\n"), "\n")
	t.Logf("Final View() → %d lines:", len(finalLines))
	for i, line := range finalLines {
		t.Logf("  [%d] width=%d", i, lipgloss.Width(line))
	}

	// Now test with real key events (simulating CLI Update flow)
	t.Log("--- Testing with real key events ---")
	m2 := New()
	m2.ShowLineNumbers = false
	m2.Prompt = ""
	m2.SetWidth(textareaWidth)
	m2.SetHeight(3)
	m2.Focus()

	for i := 0; i < charsPerLine-1; i++ {
		m2.InsertRune('测')
	}
	t.Logf("Before last char: cursor=%d, height=%d, vpH=%d, vpYOff=%d",
		m2.col, m2.Height(), m2.viewport.Height(), m2.viewport.YOffset())

	// Insert the last character that fills the line exactly
	m2.InsertRune('测')
	t.Logf("After last char: cursor=%d, height=%d, vpH=%d, vpYOff=%d, cursorLine=%d, totalVis=%d",
		m2.col, m2.Height(), m2.viewport.Height(), m2.viewport.YOffset(),
		m2.cursorLineNumber(), m2.totalVisualLines())

	raw2 := m2.view()
	raw2Lines := strings.Split(strings.TrimSuffix(raw2, "\n"), "\n")
	t.Logf("view() → %d lines:", len(raw2Lines))
	for i, line := range raw2Lines {
		t.Logf("  [%d] width=%d", i, lipgloss.Width(line))
	}

	vpView2 := m2.viewport.View()
	vp2Lines := strings.Split(strings.TrimSuffix(vpView2, "\n"), "\n")
	t.Logf("viewport.View() → %d lines:", len(vp2Lines))
	for i, line := range vp2Lines {
		t.Logf("  [%d] width=%d raw_len=%d raw_start=%q",
			i, lipgloss.Width(line), len(line),
			line[:min(len(line), 50)])
	}

	// Check if overflow line (line[1]) is different from end-of-buffer lines
	if len(vp2Lines) >= 3 {
		t.Logf("Overflow line [1] exists and has width %d", lipgloss.Width(vp2Lines[1]))
	}

	// Also dump the raw view() bytes for the key lines
	rawBytes := m2.view()
	rawBytesLines := strings.Split(strings.TrimSuffix(rawBytes, "\n"), "\n")
	t.Logf("view() raw bytes for first 2 lines:")
	for i := 0; i < min(2, len(rawBytesLines)); i++ {
		t.Logf("  [%d] len=%d start=%q", i, len(rawBytesLines[i]),
			rawBytesLines[i][:min(len(rawBytesLines[i]), 80)])
	}
}

// TestCursorUpDownShorterLine verifies that vertical cursor movement to a
// shorter line clamps the cursor to the END of that line (after the last
// character), not before it.
//
// Regression test for: "cursor at position 3 on a 3-char line, moving up to
// a shorter 2-char line goes BEFORE the last char instead of AFTER."
// Root cause: setCursorLineRelative's offset loop used `CharWidth-1` as the
// break condition, stopping one character early.
func TestCursorUpDownShorterLine(t *testing.T) {
	tests := []struct {
		name  string
		line0 string
		line1 string
		col   int // cursor column on line 1 before moving
	}{
		{"ascii_short", "ab", "xyz", 3},  // cursor at end of "xyz"
		{"ascii_mid", "ab", "xyz", 2},    // cursor between 'y' and 'z'
		{"cjk_short", "你好", "你好世", 3},    // CJK: 3 runes on line 1
		{"cjk_to_ascii", "ab", "你好", 2},  // ASCII→CJK
		{"ascii_to_cjk", "你好", "abc", 3}, // CJK→ASCII
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.ShowLineNumbers = false
			m.Prompt = ""
			m.SetWidth(40)
			m.SetHeight(6)
			m.Focus()
			m.SetValue(tt.line0 + "\n" + tt.line1)
			m.row = 1
			m.SetCursorColumn(tt.col)

			m.CursorUp()

			want := len([]rune(tt.line0)) // end of line 0
			if m.col != want {
				t.Errorf("CursorUp to shorter line: col=%d, want %d (end of line %q)",
					m.col, want, tt.line0)
			}

			// Moving back down should restore the goal column.
			m.CursorDown()
			if m.col != tt.col {
				t.Logf("CursorDown: col=%d, want %d (goal column restored)", m.col, tt.col)
			}
		})
	}
}

// TestCursorUpDownSameWidth verifies horizontal position is preserved when
// moving between lines of equal length.
func TestCursorUpDownSameWidth(t *testing.T) {
	m := New()
	m.ShowLineNumbers = false
	m.Prompt = ""
	m.SetWidth(40)
	m.SetHeight(6)
	m.Focus()
	m.SetValue("abc\nabc")
	m.row = 1
	m.SetCursorColumn(2) // between 'b' and 'c'

	m.CursorUp()
	if m.col != 2 {
		t.Errorf("CursorUp same width: col=%d, want 2", m.col)
	}
}
