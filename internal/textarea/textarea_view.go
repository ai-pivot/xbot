package textarea

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

/*** 渲染/视图方法 — 拆分自 textarea.go */

func (m *Model) repositionView() {
	minimum := m.viewport.YOffset()
	maximum := minimum + m.viewport.Height() - 1
	if row := m.cursorLineNumber(); row < minimum {
		m.viewport.ScrollUp(minimum - row)
	} else if row > maximum {
		m.viewport.ScrollDown(row - maximum)
	}
}

func (m *Model) view() string {
	if len(m.Value()) == 0 && m.row == 0 && m.col == 0 && m.Placeholder != "" {
		return m.placeholderView()
	}
	m.virtualCursor.TextStyle = m.activeStyle().computedCursorLine()

	var (
		s        strings.Builder
		style    lipgloss.Style
		newLines int

		lineInfo = m.LineInfo()
		styles   = m.activeStyle()
	)

	displayLine := 0
	for l, line := range m.value {
		wrappedLines := m.memoizedWrap(line, m.width)

		if m.row == l {
			style = styles.computedCursorLine()
		} else {
			style = styles.computedText()
		}

		for wl, wrappedLine := range wrappedLines {
			prompt := m.promptView(displayLine)
			prompt = styles.computedPrompt().Render(prompt)
			s.WriteString(style.Render(prompt))
			displayLine++

			if m.ShowLineNumbers {
				if wl == 0 { // logical line
					s.WriteString(m.lineNumberView(l+1, m.row == l))
				} else { // soft-wrapped continuation
					s.WriteString(m.lineNumberView(-1, m.row == l))
				}
			}

			strwidth := uniseg.StringWidth(string(wrappedLine))
			padding := m.width - strwidth
			// When the line is exactly full and the cursor is at the end,
			// there's no room for the cursor placeholder. Render the full
			// text on this line and put the cursor alone on the next line.
			cursorOverflow := padding == 0 && m.row == l &&
				lineInfo.RowOffset == wl &&
				lineInfo.ColumnOffset >= len(wrappedLine) &&
				(wl == len(wrappedLines)-1 || m.col == len(line))
			if m.row == l && lineInfo.RowOffset == wl && !cursorOverflow {
				co := min(lineInfo.ColumnOffset, len(wrappedLine))
				s.WriteString(style.Render(string(wrappedLine[:co])))
				if co >= len(wrappedLine) {
					m.virtualCursor.SetChar(" ")
					s.WriteString(m.virtualCursor.View())
					padding = max(0, padding-1)
				} else {
					m.virtualCursor.SetChar(string(wrappedLine[co]))
					s.WriteString(style.Render(m.virtualCursor.View()))
					s.WriteString(style.Render(string(wrappedLine[co+1:])))
				}
			} else {
				s.WriteString(style.Render(string(wrappedLine)))
			}
			s.WriteString(style.Render(strings.Repeat(" ", max(0, padding))))
			s.WriteRune('\n')
			newLines++

			// Cursor overflow: line was exactly full, put cursor on next line.
			if cursorOverflow {
				prompt := m.promptView(displayLine)
				prompt = styles.computedPrompt().Render(prompt)
				s.WriteString(style.Render(prompt))
				displayLine++
				if m.ShowLineNumbers {
					s.WriteString(m.lineNumberView(-1, false))
				}
				m.virtualCursor.SetChar(" ")
				s.WriteString(m.virtualCursor.View())
				s.WriteString(style.Render(strings.Repeat(" ", max(0, m.width-1))))
				s.WriteRune('\n')
				newLines++
			}
		}
	}

	// Always show at least `m.Height` lines at all times.
	// To do this we can simply pad out a few extra new lines in the view.
	for range m.height {
		s.WriteString(m.promptView(displayLine))
		displayLine++

		// Write end of buffer content
		leftGutter := string(m.EndOfBufferCharacter)
		rightGapWidth := m.Width() - uniseg.StringWidth(leftGutter)
		rightGap := strings.Repeat(" ", max(0, rightGapWidth))
		s.WriteString(styles.computedEndOfBuffer().Render(leftGutter + rightGap))
		s.WriteRune('\n')
	}

	return s.String()
}

func (m Model) View() string {
	// XXX: This is a workaround for the case where the viewport hasn't
	// been initialized yet like during the initial render. In that case,
	// we need to render the view again because Update hasn't been called
	// yet to set the content of the viewport.
	m.viewport.SetContent(m.view())
	view := m.viewport.View()
	styles := m.activeStyle()
	return styles.Base.Render(view)
}

func (m Model) promptView(displayLine int) (prompt string) {
	prompt = m.Prompt
	if m.promptFunc == nil {
		return prompt
	}
	prompt = m.promptFunc(PromptInfo{
		LineNumber: displayLine,
		Focused:    m.focus,
	})
	width := lipgloss.Width(prompt)
	if width < m.promptWidth {
		prompt = fmt.Sprintf("%*s%s", m.promptWidth-width, "", prompt)
	}

	return m.activeStyle().computedPrompt().Render(prompt)
}

// lineNumberView renders the line number.
//
// If the argument is less than 0, a space styled as a line number is returned
// instead. Such cases are used for soft-wrapped lines.
//
// The second argument indicates whether this line number is for a 'cursorline'
func (m Model) lineNumberView(n int, isCursorLine bool) (str string) {
	if !m.ShowLineNumbers {
		return ""
	}

	if n <= 0 {
		str = " "
	} else {
		str = strconv.Itoa(n)
	}

	// XXX: is textStyle really necessary here?
	textStyle := m.activeStyle().computedText()
	lineNumberStyle := m.activeStyle().computedLineNumber()
	if isCursorLine {
		textStyle = m.activeStyle().computedCursorLine()
		lineNumberStyle = m.activeStyle().computedCursorLineNumber()
	}

	// Format line number dynamically based on the maximum number of lines.
	digits := len(strconv.Itoa(m.MaxHeight))
	str = fmt.Sprintf(" %*v ", digits, str)

	return textStyle.Render(lineNumberStyle.Render(str))
}

func (m Model) placeholderView() string {
	var (
		s      strings.Builder
		p      = m.Placeholder
		styles = m.activeStyle()
	)
	// word wrap lines
	pwordwrap := ansi.Wordwrap(p, m.width, "")
	// hard wrap lines (handles lines that could not be word wrapped)
	pwrap := ansi.Hardwrap(pwordwrap, m.width, true)
	// split string by new lines
	plines := strings.Split(strings.TrimSpace(pwrap), "\n")

	for i := range m.height {
		isLineNumber := len(plines) > i

		lineStyle := styles.computedPlaceholder()
		if len(plines) > i {
			lineStyle = styles.computedCursorLine()
		}

		// render prompt
		prompt := m.promptView(i)
		prompt = styles.computedPrompt().Render(prompt)
		s.WriteString(lineStyle.Render(prompt))

		// when show line numbers enabled:
		// - render line number for only the cursor line
		// - indent other placeholder lines
		// this is consistent with vim with line numbers enabled
		if m.ShowLineNumbers {
			var ln int

			switch {
			case i == 0:
				ln = i + 1
				fallthrough
			case len(plines) > i:
				s.WriteString(m.lineNumberView(ln, isLineNumber))
			default:
			}
		}

		switch {
		// first line
		case i == 0:
			// first character of first line as cursor with character
			m.virtualCursor.TextStyle = styles.computedPlaceholder()

			ch, rest, _, _ := uniseg.FirstGraphemeClusterInString(plines[0], 0)
			m.virtualCursor.SetChar(ch)
			s.WriteString(lineStyle.Render(m.virtualCursor.View()))

			// the rest of the first line
			s.WriteString(lineStyle.Render(styles.computedPlaceholder().Render(rest)))

			// extend the first line with spaces to fill the width, so that
			// the entire line is filled when cursorline is enabled.
			gap := strings.Repeat(" ", max(0, m.width-lipgloss.Width(plines[0])))
			s.WriteString(lineStyle.Render(gap))
		// remaining lines
		case len(plines) > i:
			// current line placeholder text
			if len(plines) > i {
				placeholderLine := plines[i]
				gap := strings.Repeat(" ", max(0, m.width-uniseg.StringWidth(plines[i])))
				s.WriteString(lineStyle.Render(placeholderLine + gap))
			}
		default:
			// end of line buffer character
			eob := styles.computedEndOfBuffer().Render(string(m.EndOfBufferCharacter))
			s.WriteString(eob)
		}

		// terminate with new line
		s.WriteRune('\n')
	}

	m.viewport.SetContent(s.String())
	return styles.Base.Render(m.viewport.View())
}
