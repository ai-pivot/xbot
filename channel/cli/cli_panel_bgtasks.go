package cli

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// openBgTasksPanel opens the background tasks management panel.
func (m *cliModel) openBgTasksPanel() {
	m.panelState.mode = "bgtasks"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间

	// Fetch tasks — use callback (works for both local and remote mode)
	m.panelState.bgTasks = m.listBgTasks()

	// Fetch agents and filter by current session
	m.panelState.bgAgents = nil
	if m.agentListFn != nil {
		allAgents := m.agentListFn()
		for _, ag := range allAgents {
			if ag.ParentChatID == "" || ag.ParentChatID == m.chatID {
				m.panelState.bgAgents = append(m.panelState.bgAgents, ag)
			}
		}
	}

	m.panelState.bgCursor = 0
	m.panelState.bgViewing = false
	m.panelState.scrollY = 0
	m.panelState.bgLogLines = nil
	m.panelState.bgLogFollow = false
	// Clamp cursor
	totalItems := len(m.panelState.bgTasks) + len(m.panelState.bgAgents)
	if totalItems == 0 {
		m.panelState.bgCursor = -1
	} else if m.panelState.bgCursor >= totalItems {
		m.panelState.bgCursor = totalItems - 1
	}
}

// listBgTasks returns running background tasks via callback or direct access.
func (m *cliModel) listBgTasks() []*BgTask {
	if m.bgTaskListFn != nil {
		return m.bgTaskListFn()
	}
	return nil
}

// cleanupCompletedBgTasks removes completed/errored tasks from the task manager
// so they don't accumulate indefinitely. Running tasks are preserved.
func (m *cliModel) cleanupCompletedBgTasks() {
	if m.bgTaskCleanupFn != nil {
		m.bgTaskCleanupFn()
	}
}

// killBgTask kills a background task via callback or direct access.
func (m *cliModel) killBgTask(taskID string) error {
	if m.bgTaskKillFn != nil {
		return m.bgTaskKillFn(taskID)
	}
	return fmt.Errorf("background tasks not available")
}

// updateBgTasksPanel handles key events in the bg tasks panel.
// Returns (handled, newModel, cmd).
func (m *cliModel) updateBgTasksPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// Refresh task list
	m.panelState.bgTasks = m.listBgTasks()
	totalItems := len(m.panelState.bgTasks)

	// Log viewing sub-mode
	if m.panelState.bgViewing {
		switch {
		case msg.Code == tea.KeyEsc || msg.String() == "ctrl+c":
			// If navigator stack has a parent (e.g. sidebar direct-click),
			// pop back to it (which closes the panel to main view).
			// Otherwise, just exit log view back to task list.
			if !m.popPanel() {
				m.panelState.bgViewing = false
				m.panelState.scrollY = 0
				m.panelState.bgLogLines = nil
			}
			return true, m, nil
		case msg.Code == tea.KeyUp:
			m.panelState.scrollY -= 5
			if m.panelState.scrollY < 0 {
				m.panelState.scrollY = 0
			}
			m.panelState.bgLogFollow = false
			return true, m, nil
		case msg.Code == tea.KeyDown:
			m.panelState.scrollY += 5
			m.panelState.bgLogFollow = false
			return true, m, nil
		case msg.Code == tea.KeyPgUp:
			m.panelState.scrollY -= m.panelVisibleHeight()
			if m.panelState.scrollY < 0 {
				m.panelState.scrollY = 0
			}
			m.panelState.bgLogFollow = false
			return true, m, nil
		default:
			// PgDn: bubbletea doesn't have a constant, match by string
			if msg.String() == "pgdown" {
				m.panelState.scrollY += m.panelVisibleHeight()
				m.panelState.bgLogFollow = false
				return true, m, nil
			}
		}
		return true, m, nil
	}

	// Task list mode
	switch {
	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()
	case msg.Code == tea.KeyEsc:
		if !m.popPanel() {
			return m.closePanelAndResume()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if m.panelState.bgCursor > 0 {
			m.panelState.bgCursor--
			m.ensureBgCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyDown || msg.String() == "ctrl+j":
		if m.panelState.bgCursor < totalItems-1 {
			m.panelState.bgCursor++
			m.ensureBgCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelState.bgCursor >= 0 && m.panelState.bgCursor < len(m.panelState.bgTasks) {
			// Task entry: view output log
			task := m.panelState.bgTasks[m.panelState.bgCursor]
			m.panelState.bgLogLines = sanitizeOutputLines(task.Output)
			if len(m.panelState.bgLogLines) == 0 {
				m.panelState.bgLogLines = []string{"(no output)"}
			}
			m.panelState.bgViewing = true
			m.panelState.scrollY = 0
			m.panelState.bgLogFollow = true
		}
		return true, m, nil

	case msg.Code == tea.KeyDelete || msg.String() == "ctrl+d":
		// Kill selected running task
		if m.panelState.bgCursor >= 0 && m.panelState.bgCursor < len(m.panelState.bgTasks) {
			task := m.panelState.bgTasks[m.panelState.bgCursor]
			if task.Status == BgTaskRunning {
				if err := m.killBgTask(task.ID); err != nil {
					m.showTempStatus(fmt.Sprintf(m.locale.KillFailed, err))
					return true, m, m.clearTempStatusCmd()
				}
				// Refresh list after kill, filter out killed tasks
				m.panelState.bgTasks = m.listBgTasks()
				var running []*BgTask
				for _, t := range m.panelState.bgTasks {
					if t.Status == BgTaskRunning {
						running = append(running, t)
					}
				}
				m.panelState.bgTasks = running
				if len(m.panelState.bgTasks) == 0 {
					handled, m2, cmd := m.closePanelAndResume()
					return handled, m2, cmd
				}
				if m.panelState.bgCursor >= len(m.panelState.bgTasks) {
					m.panelState.bgCursor = len(m.panelState.bgTasks) - 1
				}
				return true, m, nil
			}
		}
		return true, m, nil
	}

	return true, m, nil
}

// viewBgTasksPanel renders the bg tasks panel.
func (m *cliModel) viewBgTasksPanel() string {
	if m.panelState.bgViewing {
		return m.viewBgTaskLog()
	}
	return m.viewBgTaskList()
}

// viewBgTaskList renders the background task list view.
func (m *cliModel) viewBgTaskList() string {
	// §20 使用缓存样式
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render(m.locale.BgTasksTitle)
	help := s.PanelDesc.Render(m.locale.BgTasksHelp)

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	// Calculate dynamic truncation width.
	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	totalItems := len(m.panelState.bgTasks)

	if totalItems == 0 {
		sb.WriteString(s.PanelEmpty.Render(m.locale.BgTasksEmpty))
	} else {
		idx := 0
		// Render tasks
		for _, task := range m.panelState.bgTasks {
			elapsed := time.Since(task.StartedAt).Round(time.Second)
			if task.FinishedAt != nil {
				elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
			}
			statusIcon := "●"
			statusStyle := s.ProgressRunning
			switch task.Status {
			case BgTaskDone:
				if task.Error != "" || task.ExitCode != 0 {
					statusIcon = "✗"
					statusStyle = s.ProgressError
				} else {
					statusIcon = "✓"
					statusStyle = s.ProgressDone
				}
			case BgTaskKilled:
				statusIcon = "✗"
				statusStyle = s.ProgressError
			}

			prefix := "  "
			if idx == m.panelState.bgCursor {
				prefix = cursorStyle.Render("▸")
			}

			cmd := task.Command
			cmdW := contentW - 23
			if cmdW < 10 {
				cmdW = 10
			}
			cmd = truncateToWidth(cmd, cmdW)

			line := fmt.Sprintf("%s %s  %-8s %s  %s",
				prefix,
				statusStyle.Render(statusIcon),
				task.ID,
				formatElapsed(int64(elapsed.Milliseconds())),
				cmd,
			)
			sb.WriteString(truncateToWidth(line, contentW))
			sb.WriteString("\n")
			idx++
		}
	}

	return sb.String()
}

// viewBgTaskLog renders the log viewer for a selected task.
// Returns the FULL content — scrolling is handled by the outer clampPanelScroll + cli_view.go slicing.
// Refreshes task data on each render so the log updates in real-time while the task runs.
func (m *cliModel) viewBgTaskLog() string {
	// §20 使用缓存样式
	s := &m.styles

	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	// Refresh task list to get latest output for running tasks
	latestTasks := m.listBgTasks()

	var title string
	if m.panelState.bgCursor >= 0 && m.panelState.bgCursor < len(latestTasks) {
		task := latestTasks[m.panelState.bgCursor]
		cmd := truncateToWidth(task.Command, contentW-12)
		title = fmt.Sprintf(m.locale.BgTaskLogTitle, task.ID, cmd)
		// Update log lines from latest task output
		oldCount := len(m.panelState.bgLogLines)
		m.panelState.bgLogLines = sanitizeOutputLines(task.Output)
		newCount := len(m.panelState.bgLogLines)
		// Follow-tail: auto-scroll when new lines appear
		if m.panelState.bgLogFollow && newCount > oldCount {
			visibleH := m.panelVisibleHeight() - 1 // -1 for header line
			m.panelState.scrollY = max(0, newCount-visibleH)
		}
	} else if m.panelState.bgCursor >= 0 && m.panelState.bgCursor < len(m.panelState.bgTasks) {
		// Fallback to cached task list if refresh returned empty
		task := m.panelState.bgTasks[m.panelState.bgCursor]
		cmd := truncateToWidth(task.Command, contentW-12)
		title = fmt.Sprintf(m.locale.BgTaskLogTitle, task.ID, cmd)
	}
	help := s.PanelDesc.Render(m.locale.BgTaskLogHelp)

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(truncateToWidth(title, contentW)))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	lines := m.panelState.bgLogLines
	if len(lines) == 0 {
		lines = []string{"(no output yet)"}
	}
	for _, line := range lines {
		sb.WriteString(truncateToWidth(line, contentW))
		sb.WriteString("\n")
	}

	return sb.String()
}
