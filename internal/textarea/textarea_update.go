package textarea

import (
	"slices"

	"xbot/internal/textarea/memoization"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

/*** Update + key/mouse 处理 — 拆分自 textarea.go */

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	if !m.focus {
		m.virtualCursor.Blur()
		return m, nil
	}

	// Used to determine if the cursor should blink.
	oldRow, oldCol := m.cursorLineNumber(), m.col

	var cmds []tea.Cmd

	if m.value[m.row] == nil {
		m.value[m.row] = make([]rune, 0)
	}

	if m.cache == nil {
		m.cache = memoization.NewMemoCache[line, [][]rune](wrapCacheCap)
	}

	switch msg := msg.(type) {
	case tea.PasteMsg:
		m.insertRunesFromUserInput([]rune(msg.Content))
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, m.KeyMap.DeleteAfterCursor):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
			m.deleteAfterCursor()
		case key.Matches(msg, m.KeyMap.DeleteBeforeCursor):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			m.deleteBeforeCursor()
		case key.Matches(msg, m.KeyMap.DeleteCharacterBackward):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			if len(m.value[m.row]) > 0 {
				m.value[m.row] = append(m.value[m.row][:max(0, m.col-1)], m.value[m.row][m.col:]...)
				if m.col > 0 {
					m.SetCursorColumn(m.col - 1)
				}
			}
		case key.Matches(msg, m.KeyMap.DeleteCharacterForward):
			if len(m.value[m.row]) > 0 && m.col < len(m.value[m.row]) {
				m.value[m.row] = slices.Delete(m.value[m.row], m.col, m.col+1)
			}
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
		case key.Matches(msg, m.KeyMap.DeleteWordBackward):
			if m.col <= 0 {
				m.mergeLineAbove(m.row)
				break
			}
			m.deleteWordLeft()
		case key.Matches(msg, m.KeyMap.DeleteWordForward):
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			if m.col >= len(m.value[m.row]) {
				m.mergeLineBelow(m.row)
				break
			}
			m.deleteWordRight()
		case key.Matches(msg, m.KeyMap.InsertNewline):
			if m.atContentLimit() {
				return m, nil
			}
			m.col = clamp(m.col, 0, len(m.value[m.row]))
			m.splitLine(m.row, m.col)
		case key.Matches(msg, m.KeyMap.LineEnd):
			m.CursorEnd()
		case key.Matches(msg, m.KeyMap.LineStart):
			m.CursorStart()
		case key.Matches(msg, m.KeyMap.CharacterForward):
			m.characterRight()
		case key.Matches(msg, m.KeyMap.LineNext):
			m.CursorDown()
		case key.Matches(msg, m.KeyMap.WordForward):
			m.wordRight()
		case key.Matches(msg, m.KeyMap.Paste):
			return m, Paste
		case key.Matches(msg, m.KeyMap.CharacterBackward):
			m.characterLeft(false /* insideLine */)
		case key.Matches(msg, m.KeyMap.LinePrevious):
			m.CursorUp()
		case key.Matches(msg, m.KeyMap.WordBackward):
			m.wordLeft()
		case key.Matches(msg, m.KeyMap.InputBegin):
			m.MoveToBegin()
		case key.Matches(msg, m.KeyMap.InputEnd):
			m.MoveToEnd()
		case key.Matches(msg, m.KeyMap.PageUp):
			m.PageUp()
		case key.Matches(msg, m.KeyMap.PageDown):
			m.PageDown()
		case key.Matches(msg, m.KeyMap.LowercaseWordForward):
			m.lowercaseRight()
		case key.Matches(msg, m.KeyMap.UppercaseWordForward):
			m.uppercaseRight()
		case key.Matches(msg, m.KeyMap.CapitalizeWordForward):
			m.capitalizeRight()
		case key.Matches(msg, m.KeyMap.TransposeCharacterBackward):
			m.transposeLeft()

		default:
			m.insertRunesFromUserInput([]rune(msg.Text))
		}

	case pasteMsg:
		m.insertRunesFromUserInput([]rune(msg))

	case pasteErrMsg:
		m.Err = msg
	}

	m.recalculateHeight()

	// Make sure we set the content of the viewport before updating it.
	view := m.view()
	m.viewport.SetContent(view)
	vp, cmd := m.viewport.Update(msg)
	m.viewport = &vp
	cmds = append(cmds, cmd)

	if m.useVirtualCursor {
		m.virtualCursor, cmd = m.virtualCursor.Update(msg)

		// If the cursor has moved, reset the blink state. This is a small UX
		// nuance that makes cursor movement obvious and feel snappy.
		newRow, newCol := m.cursorLineNumber(), m.col
		if (newRow != oldRow || newCol != oldCol) && m.virtualCursor.Mode() == cursor.CursorBlink {
			m.virtualCursor.IsBlinked = false
			cmd = m.virtualCursor.Blink()
		}
		cmds = append(cmds, cmd)
	}

	m.repositionView()

	return m, tea.Batch(cmds...)
}
