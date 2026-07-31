package textarea

import (
	"github.com/rivo/uniseg"
)

/*** 滚动/软换行方法 — 拆分自 textarea.go */

func (m Model) LineInfo() LineInfo {
	grid := m.memoizedWrap(m.value[m.row], m.width)

	// Find out which line we are currently on. This can be determined by the
	// m.col and counting the number of runes that we need to skip.
	var counter int
	for i, line := range grid {
		// We've found the line that we are on
		if counter+len(line) == m.col && i+1 < len(grid) {
			// We wrap around to the next line if we are at the end of the
			// previous line so that we can be at the very beginning of the row
			return LineInfo{
				CharOffset:   0,
				ColumnOffset: 0,
				Height:       len(grid),
				RowOffset:    i + 1,
				StartColumn:  m.col,
				Width:        len(grid[i+1]),
				CharWidth:    uniseg.StringWidth(string(line)),
			}
		}

		if counter+len(line) >= m.col {
			return LineInfo{
				CharOffset:   uniseg.StringWidth(string(line[:max(0, m.col-counter)])),
				ColumnOffset: m.col - counter,
				Height:       len(grid),
				RowOffset:    i,
				StartColumn:  counter,
				Width:        len(line),
				CharWidth:    uniseg.StringWidth(string(line)),
			}
		}

		counter += len(line)
	}
	return LineInfo{}
}

// repositionView repositions the view of the viewport based on the defined
func (m Model) memoizedWrap(runes []rune, width int) [][]rune {
	input := line{runes: runes, width: width}
	if v, ok := m.cache.Get(input); ok {
		return v
	}
	v := wrap(runes, width)
	m.cache.Set(input, v)
	return v
}

// cursorLineNumber returns the line number that the cursor is on.
func (m Model) cursorLineNumber() int {
	line := 0
	for i := range m.row {
		// Calculate the number of lines that the current line will be split
		// into.
		line += len(m.memoizedWrap(m.value[i], m.width))
	}
	line += m.LineInfo().RowOffset
	return line
}

// hasCursorOverflow reports whether the current line will produce an extra
// cursor-overflow visual line during rendering. This happens when the cursor
// is at the end of the logical line AND the last visual line is exactly full
// (strwidth == m.width), so the cursor placeholder cannot fit on that line
// and is rendered alone on the next line.
func (m *Model) hasCursorOverflow() bool {
	if m.width <= 0 {
		return false
	}
	line := m.value[m.row]
	if m.col != len(line) {
		return false
	}
	wrappedLines := m.memoizedWrap(line, m.width)
	if len(wrappedLines) == 0 {
		return false
	}
	last := wrappedLines[len(wrappedLines)-1]
	return uniseg.StringWidth(string(last)) == m.width
}

// totalVisualLines returns the total number of display lines across all
func (m *Model) totalVisualLines() int {
	n := 0
	for _, line := range m.value {
		n += len(m.memoizedWrap(line, m.width))
	}
	if m.hasCursorOverflow() {
		n++ // view() creates an extra line for the cursor
	}
	return n
}

// recalculateHeight recomputes and applies the textarea height based on
func (m *Model) recalculateHeight() {
	if !m.DynamicHeight {
		return
	}
	minH := max(m.MinHeight, minHeight)
	total := m.totalVisualLines()
	h := max(total, minH)
	if m.MaxHeight > 0 {
		h = min(h, m.MaxHeight)
	}
	if maxOffset := total - h; m.viewport.YOffset() > maxOffset {
		m.viewport.SetYOffset(max(0, maxOffset))
	}
	m.SetHeight(h)
}

// atContentLimit reports whether the textarea has reached its content limit.
// When MaxContentHeight is set (> 0), it checks total visual lines.
// Otherwise it falls back to the legacy MaxHeight logical-line check for
func (m *Model) atContentLimit() bool {
	if m.MaxContentHeight > 0 {
		return m.totalVisualLines() >= m.MaxContentHeight
	}
	return m.MaxHeight > 0 && len(m.value) >= m.MaxHeight
}

// visualLinesForInsert estimates how many additional visual lines would result
// from inserting the given lines at the current cursor position. The first
func (m *Model) visualLinesForInsert(lines [][]rune) int {
	if len(lines) == 0 {
		return 0
	}

	// The current row's visual line count before insertion.
	currentRowVisual := len(m.memoizedWrap(m.value[m.row], m.width))

	// Simulate merging the first paste line into the current row.
	merged := make([]rune, m.col+len(lines[0]))
	copy(merged, m.value[m.row][:m.col])
	copy(merged[m.col:], lines[0])
	if len(lines) == 1 {
		merged = append(merged, m.value[m.row][m.col:]...)
	}
	delta := len(m.memoizedWrap(merged, m.width)) - currentRowVisual

	// Each additional line (beyond the first) is a new logical line.
	// lines[0] is already accounted for in the merged calculation above.
	for i, content := range lines {
		if i == 0 {
			// Skip: already counted in merged delta above.
			// If there's only one line, merged already includes the tail.
			continue
		}
		if i == len(lines)-1 {
			content = append(content, m.value[m.row][m.col:]...)
		}
		delta += len(m.memoizedWrap(content, m.width))
	}

	return delta
}

func (m *Model) mergeLineBelow(row int) {
	if row >= len(m.value)-1 {
		return
	}

	// To perform a merge, we will need to combine the two lines and then
	m.value[row] = append(m.value[row], m.value[row+1]...)

	// Shift all lines up by one
	for i := row + 1; i < len(m.value)-1; i++ {
		m.value[i] = m.value[i+1]
	}

	// And, remove the last line
	if len(m.value) > 0 {
		m.value = m.value[:len(m.value)-1]
	}
}

func (m *Model) mergeLineAbove(row int) {
	if row <= 0 {
		return
	}

	m.col = len(m.value[row-1])
	m.row = m.row - 1

	// To perform a merge, we will need to combine the two lines and then
	m.value[row-1] = append(m.value[row-1], m.value[row]...)

	// Shift all lines up by one
	for i := row; i < len(m.value)-1; i++ {
		m.value[i] = m.value[i+1]
	}

	// And, remove the last line
	if len(m.value) > 0 {
		m.value = m.value[:len(m.value)-1]
	}
}
