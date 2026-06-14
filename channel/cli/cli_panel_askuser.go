package cli

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"xbot/internal/textarea"
)

type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

type askItem struct {
	Question string   // the question text
	Options  []string // choices (empty = free input only)
	Answer   string   // user's answer (set on submit)
	Other    string   // user's custom input when "Other" option selected
}

// openAskUserPanel activates the ask-user panel overlay.
func (m *cliModel) openAskUserPanel(items []askItem, onAnswer func(map[string]string), onCancel func()) {
	m.panelMode = "askuser"
	// Do NOT clear m.progress here — the viewport above the AskUser panel
	// still renders the progress block (iteration history, tool calls, etc).
	// Clearing it causes all iteration info from the current turn to disappear.
	// Progress will be cleaned up by endAgentTurn when the turn actually finishes.
	m.typing = false
	m.relayoutViewport() // viewport gets split-layout height
	m.panelItems = items
	m.panelTab = 0
	m.panelOptSel = make(map[int]map[int]bool)
	m.panelOptCursor = make(map[int]int)
	m.askPanelScrollY = 0
	m.askPanelTotalLines = 0
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.Prompt = "  "
	applyTAStyles(&ta, &m.styles)
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(50))
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()
	m.panelAnswerTA = ta
	// Initialize Other single-line input
	ti := textinput.New()
	ti.Placeholder = m.locale.PanelOtherPlaceholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	m.panelOtherTI = ti
	m.panelOnAnswer = onAnswer
	m.panelOnCancel = onCancel
}

func (m *cliModel) updateAskUserPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return true, m, nil
	}

	// Panel-internal scroll for long content.
	// Two separate scroll targets:
	//   Shift+↑/↓ — scroll the conversation viewport (history above)
	//   Ctrl+↑/↓  — scroll the ask panel content (question/options)
	//   PgUp/PgDn — scroll the ask panel content (page at a time)
	switch {
	case msg.String() == "ctrl+o":
		// §11 Ctrl+O toggles tool summary expand/collapse — must work in askuser mode too
		m.toggleToolSummary()
		return true, m, nil
	case msg.Code == tea.KeyHome:
		// Home/End jump to top/bottom of viewport (iteration history above the panel)
		m.viewport.GotoTop()
		m.userScrolledUp = true
		m.newContentHint = true
		return true, m, nil
	case msg.Code == tea.KeyEnd:
		m.viewport.GotoBottom()
		m.newContentHint = false
		m.userScrolledUp = false
		return true, m, nil
	case msg.String() == "shift+up":
		m.viewport.ScrollUp(1)
		return true, m, nil
	case msg.String() == "shift+down":
		m.viewport.ScrollDown(1)
		return true, m, nil
	case msg.String() == "ctrl+up":
		m.askPanelScrollY -= 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
		return true, m, nil
	case msg.String() == "ctrl+down":
		m.askPanelScrollY += 1
		// clamp happens in View via clampAskUserPanelScroll
		return true, m, nil
	case msg.String() == "pgup":
		m.askPanelScrollY -= 5
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
		return true, m, nil
	case msg.String() == "pgdown":
		m.askPanelScrollY += 5
		// clamp happens in View via clampAskUserPanelScroll
		return true, m, nil
	}

	item := &m.panelItems[m.panelTab]
	numOpts := len(item.Options)
	hasOpts := numOpts > 0
	isLastTab := m.panelTab == len(m.panelItems)-1
	// Cursor: 0..numOpts-1 (checkbox), numOpts (Other input), numOpts+1 (Submit, last tab only)
	cursor := m.panelOptCursor[m.panelTab]
	onOther := hasOpts && cursor == numOpts
	onSubmit := hasOpts && isLastTab && cursor == numOpts+1

	switch {
	case msg.String() == "ctrl+s":
		return m.submitAskAnswers()
	case msg.Code == tea.KeyEsc:
		if m.panelOnCancel != nil {
			m.panelOnCancel()
		}
		m.closePanel()
		return true, m, nil
	case msg.Code == tea.KeyRight || msg.Code == tea.KeyTab:
		if len(m.panelItems) > 1 && m.panelTab < len(m.panelItems)-1 {
			m.saveCurrentFreeInput()
			m.panelTab++
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.String() == "shift+tab" || msg.Code == tea.KeyLeft:
		if len(m.panelItems) > 1 && m.panelTab > 0 {
			m.saveCurrentFreeInput()
			m.panelTab--
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.Code == tea.KeyUp:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts - 1
				m.ensureAskUserCursorVisible()
				return true, m, nil
			}
			if onSubmit {
				m.panelOptCursor[m.panelTab] = numOpts
				m.ensureAskUserCursorVisible()
				return true, m, nil
			}
			if cursor > 0 {
				m.panelOptCursor[m.panelTab] = cursor - 1
				// Auto-scroll panel up when cursor moves above visible area
				m.ensureAskUserCursorVisible()
			} else if cursor == 0 && m.askPanelScrollY > 0 {
				// At top option and panel is scrolled — scroll content up
				m.askPanelScrollY -= 1
				if m.askPanelScrollY < 0 {
					m.askPanelScrollY = 0
				}
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyDown:
		if hasOpts {
			maxCursor := numOpts // Other input is the last item
			if isLastTab {
				maxCursor = numOpts + 1 // Submit button only on last tab
			}
			if onOther {
				if isLastTab {
					m.panelOptCursor[m.panelTab] = numOpts + 1
					m.ensureAskUserCursorVisible()
				}
				return true, m, nil
			}
			if cursor < maxCursor {
				m.panelOptCursor[m.panelTab] = cursor + 1
				// Auto-scroll panel down when cursor moves below visible area
				m.ensureAskUserCursorVisible()
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyEnter:
		if hasOpts {
			if onSubmit {
				return m.submitAskAnswers()
			}
			// On checkbox: toggle; on Other: do nothing (let user type)
			if !onOther {
				m.toggleOptAtCursor()
			}
			return true, m, nil
		}
		// No options (textarea): submit only on last tab, otherwise advance
		if isLastTab {
			return m.submitAskAnswers()
		}
		m.saveCurrentFreeInput()
		m.panelTab++
		m.restoreFreeInput()
		return true, m, nil
	case msg.Code == tea.KeySpace:
		if hasOpts && !onOther {
			if cursor < numOpts {
				m.toggleOptAtCursor()
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		if onOther {
			// Other 输入框：空格传给 textinput
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		// No options: fall through to textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case len(msg.Text) > 0:
		if hasOpts && !onOther {
			m.panelOptCursor[m.panelTab] = numOpts
			m.restoreOtherInput()
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			// With options, all input goes through Other textinput
			return true, m, nil
		}
		// No options: textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	default:
		if isCtrlJ(msg) {
			if !hasOpts {
				m.panelAnswerTA.InsertString("\n")
				m.autoExpandAskTA()
			}
			return true, m, nil
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	}

}

// toggleOptAtCursor toggles the checkbox at the current cursor position.
func (m *cliModel) toggleOptAtCursor() {
	tab := m.panelTab
	if m.panelOptSel[tab] == nil {
		m.panelOptSel[tab] = make(map[int]bool)
	}
	cursor := m.panelOptCursor[tab]
	m.panelOptSel[tab][cursor] = !m.panelOptSel[tab][cursor]
}

// collectAskAnswers gathers answers from all questions.
func (m *cliModel) collectAskAnswers() map[string]string {
	answers := make(map[string]string)
	for i, item := range m.panelItems {
		key := fmt.Sprintf("q%d", i)
		hasOpts := len(item.Options) > 0
		var parts []string
		if hasOpts {
			if sel, ok := m.panelOptSel[i]; ok && len(sel) > 0 {
				// Iterate by index order (maps are unordered in Go)
				for idx := 0; idx < len(item.Options); idx++ {
					if sel[idx] {
						parts = append(parts, item.Options[idx])
					}
				}
			}
			var otherText string
			if i == m.panelTab {
				otherText = strings.TrimSpace(m.panelOtherTI.Value())
			} else {
				otherText = strings.TrimSpace(item.Other)
			}
			if otherText != "" {
				parts = append(parts, otherText)
			}
			answers[key] = strings.Join(parts, ", ")
		} else {
			if i == m.panelTab {
				answers[key] = strings.TrimSpace(m.panelAnswerTA.Value())
			} else {
				answers[key] = strings.TrimSpace(item.Other)
			}
		}
	}
	return answers
}

// saveCurrentFreeInput saves textarea/textinput content for the current tab.
func (m *cliModel) saveCurrentFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		item.Other = m.panelOtherTI.Value()
	} else {
		item.Other = m.panelAnswerTA.Value()
	}
}

// restoreFreeInput restores textarea/textinput content for the current tab.
func (m *cliModel) restoreFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		m.panelOtherTI.SetValue(item.Other)
		m.panelOtherTI.CursorEnd()
		m.panelOtherTI.Focus()
	} else {
		m.panelAnswerTA.SetValue(item.Other)
		m.panelAnswerTA.CursorEnd()
		m.panelAnswerTA.Focus()
		m.autoExpandAskTA()
	}
}

// restoreOtherInput restores the Other textinput for the current tab (options mode).
func (m *cliModel) restoreOtherInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	m.panelOtherTI.SetValue(m.panelItems[m.panelTab].Other)
	m.panelOtherTI.CursorEnd()
}

// autoExpandAskTA dynamically grows the textarea height based on content.
func (m *cliModel) autoExpandAskTA() {
	lines := strings.Count(m.panelAnswerTA.Value(), "\n") + 1
	if lines < 2 {
		lines = 2
	}
	if lines > 6 {
		lines = 6
	}
	if m.panelAnswerTA.Height() != lines {
		m.panelAnswerTA.SetHeight(lines)
	}
}

// ensureAskUserCursorVisible adjusts askPanelScrollY so the current option
// cursor stays within the visible panel area. This provides automatic
// edge-scrolling when navigating options with ↑/↓ keys.
func (m *cliModel) ensureAskUserCursorVisible() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) == 0 {
		return
	}
	cursor := m.panelOptCursor[m.panelTab]
	// Calculate exact line offset of the cursor by counting actual header lines.
	// Tab bar: 2 lines (tabs + blank) if multiple questions, 0 otherwise.
	headerLines := 0
	if len(m.panelItems) > 1 {
		headerLines = 2 // tab bar + blank line
	}
	// Question: may be multiple lines after hardWrap.
	qWrapWidth := m.askUserQuestionWrapWidth()
	wrapped := hardWrapRunes("❓ "+item.Question, qWrapWidth)
	headerLines += strings.Count(wrapped, "\n") + 1 // question lines
	headerLines++                                   // blank line between question and options

	// Each option may span multiple lines after hardWrap. Count lines for
	// options before the cursor to compute its true Y position.
	// prefixW matches viewAskUserPanel: "▸ ☑ " = 4 visible columns.
	cursorLine := headerLines
	prefixW := ansi.StringWidth("▸ ☑ ")
	optWrapW := qWrapWidth - prefixW
	if optWrapW < 10 {
		optWrapW = 10
	}
	for i := 0; i < cursor && i < len(item.Options); i++ {
		optWrapped := hardWrapRunes(item.Options[i], optWrapW)
		cursorLine += strings.Count(optWrapped, "\n") + 1
	}
	// cursor itself: add at least 1 line
	if cursor < len(item.Options) {
		optWrapped := hardWrapRunes(item.Options[cursor], optWrapW)
		cursorLine += strings.Count(optWrapped, "\n")
	}
	// cursor at "Other" or "Submit" row: 1 line each (no wrapping needed)

	// Visible height — use askUserPanelVisibleHeight for the askuser split layout.
	askVisibleH := m.askUserPanelVisibleHeight()
	if askVisibleH <= 0 {
		return
	}
	// Scroll up if cursor is above visible area
	if cursorLine < m.askPanelScrollY+1 {
		m.askPanelScrollY = cursorLine - 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
	}
	// Scroll down if cursor is below visible area
	if cursorLine > m.askPanelScrollY+askVisibleH-1 {
		m.askPanelScrollY = cursorLine - askVisibleH + 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
	}
}

func (m *cliModel) askUserQuestionWrapWidth() int {
	// layoutAskUser uses PanelBox.Width(cw-2) with effective text_area = cw-6.
	// applyScrollbar uses contentWidth = cw-7. Lines at exactly contentWidth get
	// truncated (>= check), so qWrapWidth must be strictly less: cw-8.
	// This ensures hardWrapRunes output never triggers applyScrollbar truncation.
	w := m.chatWidth() - 8
	if w < 1 {
		return 1
	}
	return w
}

func (m *cliModel) viewAskUserPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	questionStyle := s.WarningSt.Bold(true)
	activeTabStyle := s.PanelHeader
	inactiveTabStyle := s.PanelDesc
	checkStyle := s.ToolItem
	cursorStyle := s.PanelCursor
	submitStyle := s.TodoDone

	var sb strings.Builder

	// Tab bar (if multiple questions)
	if len(m.panelItems) > 1 {
		for i := range m.panelItems {
			label := fmt.Sprintf(" %d ", i+1)
			if i == m.panelTab {
				sb.WriteString(activeTabStyle.Render(label))
			} else {
				sb.WriteString(inactiveTabStyle.Render(label))
			}
			if i < len(m.panelItems)-1 {
				sb.WriteString(inactiveTabStyle.Render("│"))
			}
		}
		sb.WriteString("\n\n")
	}

	// Current question
	if m.panelTab >= 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		isLastTab := m.panelTab == len(m.panelItems)-1
		// Wrap question text to fit inside PanelBox and its optional scrollbar.
		qWrapWidth := m.askUserQuestionWrapWidth()
		// Wrap first, then render style per-line to avoid lipgloss.Render
		// re-wrapping multi-line styled content (causes width miscalculation).
		wrapped := hardWrapRunes("❓ "+item.Question, qWrapWidth)
		for _, wl := range strings.Split(wrapped, "\n") {
			sb.WriteString(questionStyle.Render(wl))
			sb.WriteString("\n")
		}

		hasOpts := len(item.Options) > 0

		if hasOpts {
			sb.WriteString("\n")
			sel := m.panelOptSel[m.panelTab]
			cursor := m.panelOptCursor[m.panelTab]
			numOpts := len(item.Options)

			// renderAskUserOption renders a single option with proper wrapping.
			// It avoids nested Render() calls which corrupt ANSI sequences,
			// and wraps option text to fit within the panel content width.
			renderAskUserOption := func(isCursor, isChecked bool, optText string) {
				var boxStr string
				if isChecked {
					boxStr = "☑"
				} else {
					boxStr = "☐"
				}
				// Compute the visible prefix width for this option line.
				// First line format: "▸ ☑ " or "  ☐ " (cursor prefix + box + space)
				var plainPrefix string
				if isCursor {
					plainPrefix = "▸ " + boxStr + " "
				} else {
					plainPrefix = "  " + boxStr + " "
				}
				prefixW := ansi.StringWidth(plainPrefix)
				optWrapW := qWrapWidth - prefixW
				if optWrapW < 10 {
					optWrapW = 10
				}
				wrappedOpt := hardWrapRunes(optText, optWrapW)
				optLines := strings.Split(wrappedOpt, "\n")
				for j, ol := range optLines {
					if j == 0 {
						// First line: render prefix + box + wrapped option fragment
						var plainPrefix string
						if isCursor {
							plainPrefix = "▸ " + boxStr + " "
						} else {
							plainPrefix = "  " + boxStr + " "
						}
						rendered := plainPrefix + ol
						if isChecked {
							sb.WriteString(checkStyle.Render(rendered))
						} else if isCursor {
							// Cursor prefix styled separately, rest is plain
							sb.WriteString(cursorStyle.Render("▸ ") + boxStr + " " + ol)
						} else {
							sb.WriteString(rendered)
						}
					} else {
						// Continuation lines: indent to align with option text
						indent := strings.Repeat(" ", prefixW)
						if isChecked {
							sb.WriteString(checkStyle.Render(indent + ol))
						} else {
							sb.WriteString(indent + ol)
						}
					}
					sb.WriteString("\n")
				}
			}

			for i, opt := range item.Options {
				checked := sel != nil && sel[i]
				isCur := i == cursor
				renderAskUserOption(isCur, checked, opt)
			}

			// Other input (single-line)
			otherLabel := m.locale.PanelOther
			var prefix string
			if cursor == numOpts {
				prefix = cursorStyle.Render("▸ ")
			} else {
				prefix = "  "
			}
			sb.WriteString(prefix)
			sb.WriteString(otherLabel)
			// Resize textinput to fit within panel content width (qWrapWidth)
			// minus label width and scrollbar column.  The textinput View()
			// (specifically placeholderView) always outputs Width()+1 chars
			// (cursor+placeholder+padding), so we need -2 instead of -1.
			tiWidth := qWrapWidth - lipgloss.Width(prefix+otherLabel) - 2
			if tiWidth < 10 {
				tiWidth = 10
			}
			m.panelOtherTI.SetWidth(tiWidth)
			// Strip NUL bytes from textinput View(). When the input is empty,
			// placeholderView() copies the placeholder string into a rune slice
			// sized to Width()+1 and renders the unwritten slots as \x00.
			// lipgloss.Width() counts these as 0-width, but lipgloss.Render()
			// (used by PanelBox) treats them as 1-column during word-wrap,
			// causing the scrollbar "▐" to wrap to the next line.
			tiView := strings.Map(func(r rune) rune {
				if r == 0 {
					return -1
				}
				return r
			}, m.panelOtherTI.View())
			sb.WriteString(tiView)
			sb.WriteString("\n")

			// Submit button (only on last tab)
			if isLastTab {
				submitLabel := m.locale.PanelSubmit
				if cursor == numOpts+1 {
					sb.WriteString(cursorStyle.Render("▸ "))
					sb.WriteString(submitStyle.Render(submitLabel))
				} else {
					sb.WriteString("  ")
					sb.WriteString(submitStyle.Render(submitLabel))
				}
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("\n")
			sb.WriteString(m.panelAnswerTA.View())
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func (m *cliModel) ensureAskUserVisible() {
	if m.panelMode != "askuser" || m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	visible := m.askUserPanelVisibleHeight()
	if visible <= 0 {
		return
	}
	total := m.askPanelTotalLines
	if total == 0 {
		return
	}
	if total <= visible {
		m.askPanelScrollY = 0
		return
	}
	if m.askPanelScrollY < 0 {
		m.askPanelScrollY = 0
	}
	maxScroll := total - visible
	if m.askPanelScrollY > maxScroll {
		m.askPanelScrollY = maxScroll
	}
}
