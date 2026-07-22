package cli

import (
	"fmt"
	"strings"
	"time"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
)

type rewindItem struct {
	HistoryID int64
	MsgIndex  int       // index in m.messages
	Preview   string    // first line of the message content (for display)
	Content   string    // full message content (for input box on select)
	Time      time.Time // message timestamp for the panel label
}

type rewindWarningSync struct {
	generation      uint64
	targetHistoryID int64
	awaitResult     bool
	resetSeen       bool
	resultSeen      bool
	reloadSeen      bool
	selectedContent string
	checkpoint      *protocol.RewindResult
	warning         string
}

const rewindFilesWarningPrefix = "History rewound; files were not fully restored: "

// openRewindPanel collects user messages from history and opens the rewind overlay.
// Compacted source messages remain visible and rewindable because append-only
// history keeps every original message as a stable node.
func (m *cliModel) openRewindPanel() {
	var items []rewindItem
	for i, msg := range m.messages {
		if msg.role != "user" || msg.displayOnly || (msg.recordType != "" && msg.recordType != "message") || msg.historyID == 0 {
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
		return true, m.applyRewind()
	}
	return true, nil // block all other keys
}

type cliRewindDoneMsg struct {
	channelName     string
	chatID          string
	generation      uint64
	targetHistoryID int64
	selectedContent string
	cutIdx          int
	result          protocol.HistoryRewindResult
	err             error
}

// applyRewind starts the remote rewind as a Bubble Tea command. No RPC runs on
// the Update goroutine.
func (m *cliModel) applyRewind() tea.Cmd {
	if m.rewindCursor < 0 || m.rewindCursor >= len(m.rewindItems) {
		m.closeRewindPanel()
		return nil
	}
	item := m.rewindItems[m.rewindCursor]
	// The server owns DB truncation, token reset, and file checkpoint rollback.
	if m.channel == nil || m.channel.config == nil || m.channel.config.RewindHistoryFn == nil {
		m.rewindSync = rewindWarningSync{}
		m.showSystemMsg("Rewind failed: history service unavailable", feedbackError)
		m.closeRewindPanel()
		return nil
	}
	m.rewindGeneration++
	generation := m.rewindGeneration
	m.rewindPending = true
	m.rewindPendingGen = generation
	m.rewindSync = rewindWarningSync{
		generation:      generation,
		targetHistoryID: item.HistoryID,
		awaitResult:     true,
		selectedContent: item.Content,
	}

	rewindFn := m.channel.config.RewindHistoryFn
	channelName, chatID := m.channelName, m.chatID
	m.closeRewindPanel()
	return func() tea.Msg {
		result, err := rewindFn(channelName, chatID, item.HistoryID)
		return cliRewindDoneMsg{
			channelName: channelName, chatID: chatID, generation: generation,
			targetHistoryID: item.HistoryID, selectedContent: item.Content,
			cutIdx: item.MsgIndex, result: result, err: err,
		}
	}
}

func (m *cliModel) handleRewindDoneMsg(done cliRewindDoneMsg) {
	if done.channelName != m.channelName || done.chatID != m.chatID || m.rewindSync.generation != done.generation {
		m.unlockRewind(done.generation)
		return
	}
	if done.err != nil {
		m.rewindSync.resultSeen = true
		m.rewindSync.warning = fmt.Sprintf("History rewound, but file restore status is unknown: %v", done.err)
		if m.rewindSync.resetSeen {
			m.finishRewindAfterReload()
			return
		}
		m.unlockRewind(done.generation)
		m.showSystemMsg(fmt.Sprintf("Rewind failed: %v", done.err), feedbackError)
		return
	}
	if !done.result.HistoryRewound {
		if m.rewindSync.resetSeen {
			m.rewindSync.resultSeen = true
			m.rewindSync.warning = "History rewind was confirmed, but the RPC result was unavailable"
			m.finishRewindAfterReload()
			return
		}
		m.unlockRewind(done.generation)
		m.rewindSync = rewindWarningSync{}
		m.showSystemMsg("Rewind failed: history was not changed", feedbackError)
		return
	}
	warning := ""
	if !done.result.FilesRewound || done.result.CheckpointError != "" {
		detail := done.result.CheckpointError
		if detail == "" {
			detail = "file checkpoint restore reported errors"
		}
		warning = rewindFilesWarningPrefix + detail
	}

	m.rewindSync.resultSeen = true
	m.rewindSync.selectedContent = done.selectedContent
	m.rewindSync.checkpoint = done.result.Checkpoint
	m.rewindSync.warning = warning
	m.finishRewindAfterReload()
}

func (m *cliModel) unlockRewind(generation uint64) {
	if generation != 0 && m.rewindPendingGen == generation {
		m.rewindPending = false
		m.rewindPendingGen = 0
	}
}

func (m *cliModel) finishRewindAfterReload() {
	sync := m.rewindSync
	if sync.generation == 0 || !sync.resetSeen || !sync.reloadSeen || (sync.awaitResult && !sync.resultSeen) {
		return
	}
	m.rewindResult = sync.checkpoint
	if sync.warning != "" {
		m.showSystemMsg(sync.warning, feedbackWarning)
	}
	if sync.selectedContent != "" {
		m.textarea.SetValue(sync.selectedContent)
		m.textarea.CursorEnd()
		m.textarea.Focus()
	}
	m.unlockRewind(sync.generation)
	m.rewindSync = rewindWarningSync{}
	m.rc.valid = false
	m.rc.history = ""
	m.rc.histLines = nil
	m.rc.bumpHistGen()
	m.rc.allLines = nil
	m.rc.allLinesHistLen = 0
	m.updateViewportContent()
}
