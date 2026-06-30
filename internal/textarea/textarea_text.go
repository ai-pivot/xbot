package textarea

import (
	"charm.land/lipgloss/v2"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

/*** 文本操作方法 — 拆分自 textarea.go */

func (s StyleState) computedCursorLine() lipgloss.Style {
	return s.CursorLine.Inherit(s.Base).Inline(true)
}

func (m *Model) SetValue(s string) {
	m.Reset()
	m.InsertString(s)
	m.recalculateHeight()
}

func (m *Model) InsertString(s string) {
	m.insertRunesFromUserInput([]rune(s))
	m.recalculateHeight()
}

func (m *Model) InsertRune(r rune) {
	m.insertRunesFromUserInput([]rune{r})
	m.recalculateHeight()
}

func (m *Model) insertRunesFromUserInput(runes []rune) {
	// Clean up any special characters in the input provided by the
	// clipboard. This avoids bugs due to e.g. tab characters and
	// whatnot.
	runes = m.san().Sanitize(runes)

	if m.CharLimit > 0 {
		availSpace := m.CharLimit - m.Length()
		// If the char limit's been reached, cancel.
		if availSpace <= 0 {
			return
		}
		// If there's not enough space to paste the whole thing cut the pasted
		// runes down so they'll fit.
		if availSpace < len(runes) {
			runes = runes[:availSpace]
		}
	}

	// Split the input into lines.
	var lines [][]rune
	lstart := 0
	for i := range runes {
		if runes[i] == '\n' {
			// Queue a line to become a new row in the text area below.
			// Beware to clamp the max capacity of the slice, to ensure no
			// data from different rows get overwritten when later edits
			// will modify this line.
			lines = append(lines, runes[lstart:i:i])
			lstart = i + 1
		}
	}
	if lstart <= len(runes) {
		// The last line did not end with a newline character.
		// Take it now.
		lines = append(lines, runes[lstart:])
	}

	// Obey the maximum line limit.
	if maxLines > 0 && len(m.value)+len(lines)-1 > maxLines {
		allowedHeight := max(0, maxLines-len(m.value)+1)
		lines = lines[:allowedHeight]
	}

	// Obey MaxContentHeight in visual rows when set.
	if m.MaxContentHeight > 0 {
		budget := m.MaxContentHeight - m.totalVisualLines()
		// Trim lines from the end until we fit within the budget.
		for len(lines) > 1 && m.visualLinesForInsert(lines) > budget {
			lines = lines[:len(lines)-1]
		}
		if m.visualLinesForInsert(lines) > budget {
			return
		}
	}

	if len(lines) == 0 {
		// Nothing left to insert.
		return
	}

	// Save the remainder of the original line at the current
	// cursor position.
	tail := make([]rune, len(m.value[m.row][m.col:]))
	copy(tail, m.value[m.row][m.col:])

	// Paste the first line at the current cursor position.
	m.value[m.row] = append(m.value[m.row][:m.col], lines[0]...)
	m.col += len(lines[0])

	if numExtraLines := len(lines) - 1; numExtraLines > 0 {
		// Add the new lines.
		// We try to reuse the slice if there's already space.
		var newGrid [][]rune
		if cap(m.value) >= len(m.value)+numExtraLines {
			// Can reuse the extra space.
			newGrid = m.value[:len(m.value)+numExtraLines]
		} else {
			// No space left; need a new slice.
			newGrid = make([][]rune, len(m.value)+numExtraLines)
			copy(newGrid, m.value[:m.row+1])
		}
		// Add all the rows that were after the cursor in the original
		// grid at the end of the new grid.
		copy(newGrid[m.row+1+numExtraLines:], m.value[m.row+1:])
		m.value = newGrid
		// Insert all the new lines in the middle.
		for _, l := range lines[1:] {
			m.row++
			m.value[m.row] = l
			m.col = len(l)
		}
	}

	// Finally add the tail at the end of the last line inserted.
	m.value[m.row] = append(m.value[m.row], tail...)

	m.SetCursorColumn(m.col)
}

func (m Model) Line() int {
	return m.row
}

func (m Model) Column() int {
	return m.col
}

// ScrollYOffset returns the Y offset (top row) index of the current view, which
func (m *Model) setCursorLineRelative(delta int) {
	if delta == 0 {
		return
	}

	li := m.LineInfo()
	charOffset := max(m.lastCharOffset, li.CharOffset)
	m.lastCharOffset = charOffset

	// Without trailing spaces in the grid, StartColumn+Width gives the first
	// character of the next visual line, and StartColumn-1 steps back across
	// a wrap boundary.
	const trailingStep = 1

	if delta > 0 { //nolint:nestif
		// Moving down.
		for range delta {
			if li.RowOffset+1 >= li.Height && m.row < len(m.value)-1 {
				m.row++
				m.col = 0
			} else {
				// Move the cursor to the start of the next virtual line.
				m.col = min(li.StartColumn+li.Width, len(m.value[m.row])-1)
			}
			li = m.LineInfo()
		}
	} else {
		// Moving up.
		for range -delta {
			if li.RowOffset <= 0 && m.row > 0 {
				m.row--
				m.col = len(m.value[m.row])
			} else {
				// Move the cursor to the end of the previous line.
				m.col = li.StartColumn - trailingStep
			}
			li = m.LineInfo()
		}
	}

	nli := m.LineInfo()
	m.col = nli.StartColumn

	if nli.Width <= 0 {
		m.repositionView()
		return
	}

	offset := 0
	for offset < charOffset {
		if m.row >= len(m.value) || m.col >= len(m.value[m.row]) || offset >= nli.CharWidth {
			break
		}
		offset += rw.RuneWidth(m.value[m.row][m.col])
		m.col++
	}
	m.repositionView()
}

func (m *Model) CursorDown() {
	m.setCursorLineRelative(1)
}

func (m *Model) CursorUp() {
	m.setCursorLineRelative(-1)
}

// SetCursorColumn moves the cursor to the given position. If the position is
func (m *Model) SetCursorColumn(col int) {
	m.col = clamp(col, 0, len(m.value[m.row]))
	// Any time that we move the cursor horizontally we need to reset the last
	// offset so that the horizontal position when navigating is adjusted.
	m.lastCharOffset = 0
}

func (m *Model) Click(x int) {
	if len(m.value) == 0 {
		return
	}

	// Map x to rune offset in the current line
	line := m.value[m.row]

	// Accumulate visual width to find which rune corresponds to x
	col := 0
	visualW := 0
	for _, r := range line {
		rw := uniseg.StringWidth(string(r))
		if visualW+rw > x {
			break
		}
		visualW += rw
		col++
	}
	m.col = clamp(col, 0, len(line))
	m.lastCharOffset = 0
	m.repositionView()
}

// ClickAt positions the cursor based on a mouse click at the given
// terminal coordinates. x is the column, y is the visual line index
func (m *Model) ClickAt(x, y int) {
	if len(m.value) == 0 {
		return
	}

	// Map visual line y to (logical row, rune offset)
	visualLine := 0
	for row, line := range m.value {
		wrappedLines := m.memoizedWrap(line, m.width)
		for wl, wrappedRunes := range wrappedLines {
			if visualLine == y {
				// Found the visual line — position cursor here
				m.row = row

				// Calculate base offset (runes before this wrapped segment)
				baseOffset := 0
				for i := range wl {
					baseOffset += len(wrappedLines[i])
				}

				// Map x to rune offset within this wrapped segment
				col := baseOffset
				visualW := 0
				for _, r := range wrappedRunes {
					rw := uniseg.StringWidth(string(r))
					if visualW+rw > x {
						break
					}
					visualW += rw
					col++
				}
				m.col = clamp(col, 0, len(line))
				m.lastCharOffset = 0
				m.repositionView()
				return
			}
			visualLine++
		}
	}
}

// tokenRight returns the column after skipping one word/token to the right
// from the given column. It skips spaces first, then advances through
// non-boundary characters using isWordBoundary (whitespace or CJK chars).
func (m *Model) deleteBeforeCursor() {
	m.value[m.row] = m.value[m.row][m.col:]
	m.SetCursorColumn(0)
}

// deleteAfterCursor deletes all text after the cursor. Returns whether or not
// the cursor blink should be reset. If input is masked delete everything after
func (m *Model) deleteAfterCursor() {
	m.value[m.row] = m.value[m.row][:m.col]
	m.SetCursorColumn(len(m.value[m.row]))
}

// transposeLeft exchanges the runes at the cursor and immediately
// before. No-op if the cursor is at the beginning of the line.  If
// the cursor is not at the end of the line yet, moves the cursor to
func (m *Model) deleteWordLeft() {
	if m.col == 0 || len(m.value[m.row]) == 0 {
		return
	}

	oldCol := m.col
	m.SetCursorColumn(m.tokenLeft(m.col))

	if oldCol > len(m.value[m.row]) {
		m.value[m.row] = m.value[m.row][:m.col]
	} else {
		m.value[m.row] = append(m.value[m.row][:m.col], m.value[m.row][oldCol:]...)
	}
}

func (m *Model) deleteWordRight() {
	if m.col >= len(m.value[m.row]) || len(m.value[m.row]) == 0 {
		return
	}

	oldCol := m.col
	endCol := m.tokenRight(m.col)

	if endCol > len(m.value[m.row]) {
		m.value[m.row] = m.value[m.row][:oldCol]
	} else {
		m.value[m.row] = append(m.value[m.row][:oldCol], m.value[m.row][endCol:]...)
	}

	m.SetCursorColumn(oldCol)
}

func (m *Model) characterRight() {
	if m.col < len(m.value[m.row]) {
		m.SetCursorColumn(m.col + 1)
	} else {
		if m.row < len(m.value)-1 {
			m.row++
			m.CursorStart()
		}
	}
}

// characterLeft moves the cursor one character to the left.
// If insideLine is set, the cursor is moved to the last
func (m *Model) characterLeft(insideLine bool) {
	if m.col == 0 && m.row != 0 {
		m.row--
		m.CursorEnd()
		if !insideLine {
			return
		}
	}
	if m.col > 0 {
		m.SetCursorColumn(m.col - 1)
	}
}

func (m *Model) wordLeft() {
	m.SetCursorColumn(m.tokenLeft(m.col))
}

func (m *Model) wordRight() {
	m.doWordRight(func(int, int) { /* nothing */ })
}

// doWordRight moves the cursor one word to the right, invoking fn for each
// character traversed. The fn callback receives the character index within the
// word and the absolute column position. This enables word-transform operations
// (uppercase, lowercase, capitalize) to modify characters as the cursor moves.
//
// Uses isWordBoundary (whitespace or CJK characters) for word-edge detection.
// On the first character (charIdx==0), always advance at least one rune
// even if it is a word boundary (e.g. CJK char), so that CJK text
func (m *Model) MoveToBegin() {
	m.row = 0
	m.SetCursorColumn(0)
	m.repositionView()
}

func (m *Model) MoveToEnd() {
	m.row = len(m.value) - 1
	m.SetCursorColumn(len(m.value[m.row]))
	m.repositionView()
}

// PageUp moves the cursor up by one page. First call snaps to the first visible
func (m *Model) PageUp() {
	// If not on the first visible line, snap to it.
	if offset := m.viewport.YOffset() - m.cursorLineNumber(); offset < 0 {
		m.setCursorLineRelative(offset)
		return
	}

	// Already on first visible line, move up by a full page.
	m.setCursorLineRelative(-m.height)
}

// PageDown moves the cursor down by one page. First call snaps to the last
func (m *Model) PageDown() {
	// If not on the last visible line, snap to it.
	if offset := m.cursorLineNumber() - m.viewport.YOffset(); offset < m.height-1 {
		m.setCursorLineRelative(m.height - 1 - offset)
		return
	}

	// Already on last visible line, move down by a full page.
	m.setCursorLineRelative(m.height)
}

// SetWidth sets the width of the textarea to fit exactly within the given width.
// This means that the textarea will account for the width of the prompt and
// whether or not line numbers are being shown.
//
// Ensure that SetWidth is called after setting the Prompt and ShowLineNumbers,
// It is important that the width of the textarea be exactly the given width
func (m *Model) splitLine(row, col int) {
	// To perform a split, take the current line and keep the content before
	// the cursor, take the content after the cursor and make it the content of
	// the line underneath, and shift the remaining lines down by one
	head, tailSrc := m.value[row][:col], m.value[row][col:]
	tail := make([]rune, len(tailSrc))
	copy(tail, tailSrc)

	m.value = append(m.value[:row+1], m.value[row:]...)

	m.value[row] = head
	m.value[row+1] = tail

	m.col = 0
	m.row++
}
