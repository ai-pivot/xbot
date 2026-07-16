package cli

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type rewindItem struct {
	HistoryID int64
	MsgIndex  int       // index in m.messages
	Preview   string    // first line of the message content (for display)
	Content   string    // full message content (for input box on select)
	Time      time.Time // message timestamp (for DB truncation cutoff)
}

// openRewindPanel collects user messages from history and opens the rewind overlay.
// Compacted source messages stay hidden in the transcript but remain valid
// rewind candidates because the append-only DB retains them.
func (m *cliModel) openRewindPanel() {
	var items []rewindItem
	for i, msg := range m.messages {
		if msg.role != "user" || msg.displayOnly || (msg.recordType != "" && msg.recordType != "message") || (msg.historyID == 0 && msg.timestamp.IsZero()) {
			continue
		}
		content := msg.content
		// Build preview: first line, truncated
		preview := content
		if idx := strings.Index(preview, "\n"); idx >= 0 {
			preview = preview[:idx]
		}
		if runes := []rune(preview); len(runes) > 60 {
			preview = string(runes[:57]) + "..."
		}
		items = append(items, rewindItem{
			HistoryID: msg.historyID,
			MsgIndex:  i,
			Preview:   preview,
			Content:   content,
			Time:      msg.timestamp,
		})
	}
	if len(items) == 0 {
		m.showTempStatus(m.locale.NoMessagesToDelete)
		return
	}
	m.rewindItems = items
	m.rewindCursor = len(items) - 1 // default to most recent
	m.rewindMode = true
	m.rc.valid = false
}

// closeRewindPanel deactivates the rewind overlay.
func (m *cliModel) closeRewindPanel() {
	m.rewindMode = false
	m.rewindItems = nil
	m.rewindCursor = 0
}

// viewRewindPanel renders the rewind selection overlay (centered panel).
func (m *cliModel) viewRewindPanel(width, height int) string {
	if !m.rewindMode || len(m.rewindItems) == 0 {
		return ""
	}

	var lines []string

	// Header
	lines = append(lines, m.styles.PanelHeader.Render(m.locale.RewindTitle))
	lines = append(lines, m.styles.PanelDesc.Render(m.locale.RewindHint))
	lines = append(lines, "") // spacer

	// Items (newest first for natural selection)
	total := len(m.rewindItems)
	maxVisible := height - 10 // leave room for header + hints + borders
	if maxVisible < 3 {
		maxVisible = 3
	}

	// Calculate scroll offset to keep cursor visible
	scrollStart := 0
	scrollEnd := total
	if total > maxVisible {
		scrollStart = m.rewindCursor - maxVisible/2
		if scrollStart < 0 {
			scrollStart = 0
		}
		scrollEnd = scrollStart + maxVisible
		if scrollEnd > total {
			scrollEnd = total
			scrollStart = scrollEnd - maxVisible
		}
	}

	for i := scrollStart; i < scrollEnd; i++ {
		item := m.rewindItems[i]
		cursor := " "
		style := m.styles.TextMutedSt
		if i == m.rewindCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		// Show turn number (newest = 1)
		turnNum := total - i
		line := style.Render(fmt.Sprintf(" %s #%d  %s", cursor, turnNum, item.Preview))
		lines = append(lines, line)
	}

	// Scroll indicator with position
	if total > maxVisible {
		scrollInfo := fmt.Sprintf("  [%d-%d/%d]", scrollStart+1, scrollEnd, total)
		lines = append(lines, m.styles.TextMutedSt.Render(scrollInfo))
	}

	// Build panel with border
	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint line
	hint := m.styles.PanelHint.Render(" ↑↓ Navigate  PgUp/PgDn Page  Home/End Jump  Enter Rewind  Esc Cancel")

	// Center vertically
	listH := len(lines) + 3
	blankLines := max(0, (height-listH)/2)
	var b strings.Builder
	for i := 0; i < blankLines; i++ {
		b.WriteString("\n")
	}
	b.WriteString(box)
	b.WriteString("\n")
	b.WriteString(hint)

	return b.String()
}

// handleRewindKey handles key events for the rewind overlay.
// Returns (handled, cmd). Called from Update() at same priority as quickSwitch.
func (m *cliModel) handleRewindKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.rewindMode {
		return false, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		m.closeRewindPanel()
		return true, nil
	case tea.KeyUp:
		if m.rewindCursor > 0 {
			m.rewindCursor--
		}
		return true, nil
	case tea.KeyDown:
		if m.rewindCursor < len(m.rewindItems)-1 {
			m.rewindCursor++
		}
		return true, nil
	case tea.KeyPgUp:
		maxVisible := m.height - 10
		if maxVisible < 3 {
			maxVisible = 3
		}
		if m.rewindCursor > 0 {
			m.rewindCursor -= min(maxVisible, m.rewindCursor)
		}
		return true, nil
	case tea.KeyPgDown:
		maxVisible := m.height - 10
		if maxVisible < 3 {
			maxVisible = 3
		}
		maxIdx := len(m.rewindItems) - 1
		if m.rewindCursor < maxIdx {
			m.rewindCursor += min(maxVisible, maxIdx-m.rewindCursor)
		}
		return true, nil
	case tea.KeyHome:
		m.rewindCursor = 0
		return true, nil
	case tea.KeyEnd:
		m.rewindCursor = len(m.rewindItems) - 1
		return true, nil
	case tea.KeyEnter:
		m.applyRewind()
		return true, nil
	}
	return true, nil // block all other keys
}

// applyRewind executes the rewind: truncate history at selected message,
// run file checkpoint rollback, and place selected message content in input box.
func (m *cliModel) applyRewind() {
	if m.rewindCursor < 0 || m.rewindCursor >= len(m.rewindItems) {
		m.closeRewindPanel()
		return
	}
	item := m.rewindItems[m.rewindCursor]
	selectedContent := item.Content
	cutoff := item.Time
	cutIdx := item.MsgIndex

	// Commit DB history before changing transcript, draft, or files.
	var checkpointHandled bool
	if m.channel != nil && m.channel.config.RewindHistoryFn != nil {
		result, err := m.channel.config.RewindHistoryFn(m.channelName, m.chatID, item.HistoryID, cutoff)
		if err != nil {
			m.showSystemMsg(fmt.Sprintf("Rewind failed: %v", err), feedbackError)
			m.closeRewindPanel()
			return
		}
		checkpointHandled = true
		m.rewindResult = result.Checkpoint
		if result.CheckpointError != "" {
			m.showSystemMsg("History rewound; files were not fully restored: "+result.CheckpointError, feedbackWarning)
		}
	} else if m.channel != nil && m.channel.config.TrimHistoryFn != nil {
		if err := m.channel.config.TrimHistoryFn(m.channelName, m.chatID, cutoff); err != nil {
			m.showSystemMsg(fmt.Sprintf("Rewind failed: %v", err), feedbackError)
			m.closeRewindPanel()
			return
		}
	} else if m.trimHistoryFn != nil {
		if err := m.trimHistoryFn(cutoff); err != nil {
			m.showSystemMsg(fmt.Sprintf("Rewind failed: %v", err), feedbackError)
			m.closeRewindPanel()
			return
		}
	} else {
		m.showSystemMsg("Rewind failed: history service unavailable", feedbackError)
		m.closeRewindPanel()
		return
	}
	m.messages = m.messages[:cutIdx]
	for i := range m.messages {
		if m.messages[i].compactedBy >= item.HistoryID {
			m.messages[i].compactedBy = 0
			m.messages[i].hidden = false
			m.messages[i].dirty = true
		}
	}

	// Reset cached token counts so maybeCompress doesn't use stale values
	// from before the rewind and trigger an immediate (incorrect) compression.
	if !checkpointHandled && m.resetTokenStateFn != nil {
		m.resetTokenStateFn()
	}

	// File rollback via checkpoint state
	if !checkpointHandled && m.checkpointState != nil && m.checkpointState.Store() != nil {
		// Compute the absolute turn index for the selected user message.
		// m.agentTurnID is the turn index of the most recent user message.
		// Each rewindItem corresponds to one user turn (startAgentTurn increments
		// agentTurnID by 1). The selected item at rewindCursor has turn index:
		//   agentTurnID - (totalItems - 1 - rewindCursor)
		// This correctly handles multiple rewind-send-cancel cycles where
		// agentTurnID has grown beyond the number of visible user messages.
		totalItems := len(m.rewindItems)
		absTurnIdx := int(m.agentTurnID) - (totalItems - 1 - m.rewindCursor)
		if absTurnIdx < 1 {
			absTurnIdx = 1
		}
		result, _ := m.checkpointState.Store().Rewind(absTurnIdx)
		m.rewindResult = &result
	}

	// Put selected message content into input box
	m.textarea.SetValue(selectedContent)
	m.textarea.CursorEnd()
	m.textarea.Focus()

	// Reset state
	m.rewindMode = false
	m.rewindItems = nil
	m.rewindCursor = 0
	m.rc.valid = false
	m.rc.history = ""
	m.rc.histLines = nil
	m.rc.bumpHistGen()
	m.rc.allLines = nil
	m.rc.allLinesHistLen = 0
	m.updateViewportContent()
}
