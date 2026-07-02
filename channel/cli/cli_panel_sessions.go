package cli

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	log "xbot/logger"
	"xbot/session"
	"xbot/tools"
)

func (m *cliModel) openSessionsPanel() {
	m.panelState.mode = "sessions"
	m.relayoutViewport()

	// sessionsListFn now handles everything (main + local dir + subagents).
	// Only fall back to local dir sessions when there's no callback.
	if m.sessionsListFn != nil {
		m.panelState.sessItems = m.sessionsListFn()
	} else {
		m.panelState.sessItems = m.listLocalDirSessions()
	}
	// Position cursor on the currently active session
	m.panelState.sessCursor = 0
	for i, entry := range m.panelState.sessItems {
		if entry.Active {
			m.panelState.sessCursor = i
			break
		}
		// For agent sessions, match by chatID (Active is only set for main sessions)
		if entry.Type == "agent" {
			agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
			if entry.Instance != "" {
				agentChatID += ":" + entry.Instance
			}
			if agentChatID == m.chatID {
				m.panelState.sessCursor = i
				break
			}
		}
	}
	m.panelState.sessViewing = false
	m.panelState.scrollY = 0
	m.ensureSessionCursorVisible()
}

// updateSessionsPanel handles key events for the sessions panel.
// Returns (handled, model, cmd).
func (m *cliModel) updateSessionsPanel(msg tea.KeyPressMsg) (bool, *cliModel, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		if m.panelState.sessViewing {
			m.panelState.sessViewing = false
			m.panelState.scrollY = 0
			return true, m, nil
		}
		if !m.popPanel() {
			m.panelState.mode = ""
			m.panelState.sessItems = nil
			m.relayoutViewport()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if !m.panelState.sessViewing && m.panelState.sessCursor > 0 {
			m.panelState.sessCursor--
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyDown:
		if !m.panelState.sessViewing && m.panelState.sessCursor < len(m.panelState.sessItems)-1 {
			m.panelState.sessCursor++
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelState.sessViewing {
			// Viewing mode: Esc goes back, Enter does nothing
			return true, m, nil
		}
		if m.panelState.sessCursor >= 0 && m.panelState.sessCursor < len(m.panelState.sessItems) {
			entry := m.panelState.sessItems[m.panelState.sessCursor]
			switch entry.Type {
			case "main":
				// Switch to this chatroom + close panel
				if entry.ID != m.chatID {
					m.saveCurrentSession() // save current session state
					m.chatID = entry.ID
					SetLastActiveSession(m.defaultChatID, entry.ID)
					m.channelName = entry.Channel
					// Update workdir to match the session's workdir.
					workDir, _ := ParseChatID(entry.ID)
					if workDir != "" {
						m.workDir = workDir
						if m.channel != nil && m.channel.config.SetCWDFn != nil {
							_ = m.channel.config.SetCWDFn(entry.Channel, entry.ID, workDir)
						}
					}
					// Update background task session key for isolation
					if m.channel != nil {
						m.channel.bgSessionKey = "cli:" + entry.ID
						m.channel.updateBgTaskCountFn()
					}
					m.messages = nil
					m.lastTokenUsage = nil // clear stale token bar on session switch
					m.invalidateAllCache(false)
					m.todos = nil // clear stale todos from previous session
					m.todosDoneCleared = false
					m.restoreSession() // restore target session state (or reset to idle)
					cmds := m.postRestoreSessionSetup()
					m.panelState.mode = ""
					m.panelState.sessItems = nil
					if len(cmds) > 0 {
						return true, m, tea.Batch(cmds...)
					}
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到会话: %s", entry.Label), feedbackInfo)
				} else {
					// Already on this session, just close panel
					m.panelState.mode = ""
					m.panelState.sessItems = nil
					m.relayoutViewport()
				}
			case "agent":
				// Switch to agent session — same logic as normal session switch
				// Agent chatID uses interactiveKey format: "channel:chatID/roleName:instance"
				agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
				if entry.Instance != "" {
					agentChatID += ":" + entry.Instance
				}
				if agentChatID != m.chatID {
					m.saveCurrentSession() // save current session state
					m.chatID = agentChatID
					m.channelName = "agent"
					// Update workdir to match the parent session's workdir.
					workDir, _ := ParseChatID(entry.ParentID)
					if workDir != "" {
						m.workDir = workDir
						if m.channel != nil && m.channel.config.SetCWDFn != nil {
							_ = m.channel.config.SetCWDFn(entry.Channel, entry.ParentID, workDir)
						}
					}
					// Update background task session key for isolation
					if m.channel != nil {
						m.channel.bgSessionKey = "agent:" + agentChatID
						m.channel.updateBgTaskCountFn()
					}
					m.messages = nil
					m.lastTokenUsage = nil // clear stale token bar on session switch
					m.invalidateAllCache(false)
					m.todos = nil // clear stale todos from previous session
					m.todosDoneCleared = false
					m.restoreSession() // restore target session state (or reset to idle)
					cmds := m.postRestoreSessionSetup()
					m.panelState.mode = ""
					m.panelState.sessItems = nil
					if len(cmds) > 0 {
						return true, m, tea.Batch(cmds...)
					}
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到 agent 会话: %s/%s", entry.Role, entry.Instance), feedbackInfo)
				} else {
					// Already on this session, just close panel
					m.panelState.mode = ""
					m.panelState.sessItems = nil
					m.relayoutViewport()
				}
			}
		}
		return true, m, nil

	case msg.String() == "r":
		// Refresh sessions list
		if m.sessionsListFn != nil {
			m.panelState.sessItems = m.sessionsListFn()
		}
		return true, m, nil

	case msg.Code == tea.KeyHome:
		if !m.panelState.sessViewing && len(m.panelState.sessItems) > 0 {
			m.panelState.sessCursor = 0
			m.panelState.scrollY = 0
		}
		return true, m, nil

	case msg.Code == tea.KeyEnd:
		if !m.panelState.sessViewing && len(m.panelState.sessItems) > 0 {
			m.panelState.sessCursor = len(m.panelState.sessItems) - 1
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyPgUp:
		if !m.panelState.sessViewing {
			visibleH := m.panelVisibleHeight()
			m.panelState.sessCursor -= visibleH
			if m.panelState.sessCursor < 0 {
				m.panelState.sessCursor = 0
			}
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	// N: create new session in current directory
	case msg.String() == "n" || msg.String() == "N":
		if !m.panelState.sessViewing {
			return true, m, m.showSessionCreateDialog()
		}

	// D: delete selected session (except default) — with confirmation
	case msg.String() == "d" || msg.String() == "D":
		if !m.panelState.sessViewing && m.panelState.sessCursor >= 0 && m.panelState.sessCursor < len(m.panelState.sessItems) {
			entry := m.panelState.sessItems[m.panelState.sessCursor]
			if entry.Type == "main" && entry.Label != defaultSessionName {
				m.panelState.sessConfirmDelete = true
				m.panelState.sessConfirmEntry = entry
			}
		}
		return true, m, nil

	// Y: confirm delete (follows D)
	case (msg.String() == "y" || msg.String() == "Y") && m.panelState.sessConfirmDelete:
		m.panelState.sessConfirmDelete = false
		cmd := m.deleteLocalSession(m.panelState.sessConfirmEntry)
		return true, m, cmd

	// Any other key cancels delete confirmation
	case m.panelState.sessConfirmDelete:
		m.panelState.sessConfirmDelete = false
		return true, m, nil

	default:
		if msg.String() == "pgdown" && !m.panelState.sessViewing {
			visibleH := m.panelVisibleHeight()
			m.panelState.sessCursor += visibleH
			if m.panelState.sessCursor >= len(m.panelState.sessItems) {
				m.panelState.sessCursor = len(m.panelState.sessItems) - 1
			}
			m.ensureSessionCursorVisible()
			return true, m, nil
		}
	}
	return false, m, nil
}

// viewSessionsPanel renders the sessions management panel.
func (m *cliModel) viewSessionsPanel() string {
	if m.panelState.sessViewing {
		return m.viewSessionsDetail()
	}
	return m.viewSessionsList()
}

// viewSessionsList renders the session list.
func (m *cliModel) viewSessionsList() string {
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render("Sessions")
	help := s.PanelDesc.Render("↑↓ Navigate  Enter Switch/View  n New  d Delete  r Refresh  Esc Close")
	total := len(m.panelState.sessItems)
	scrollHint := ""
	if total > 1 {
		scrollHint = s.PanelDesc.Render(fmt.Sprintf(" [%d/%d]", m.panelState.sessCursor+1, total))
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString(scrollHint)
	sb.WriteString("\n")

	// Show delete confirmation prompt
	if m.panelState.sessConfirmDelete {
		sb.WriteString(s.ErrorMsg.Render(
			fmt.Sprintf("  ⚠ Delete session %q? [Y]es / [N]o", m.panelState.sessConfirmEntry.Label)))
		sb.WriteString("\n")
	}

	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	if len(m.panelState.sessItems) == 0 {
		sb.WriteString(s.PanelEmpty.Render("(no active sessions)"))
		return sb.String()
	}

	for i, entry := range m.panelState.sessItems {
		prefix := "  "
		if i == m.panelState.sessCursor {
			prefix = cursorStyle.Render("▸")
		}

		var icon, line string
		switch entry.Type {
		case "main":
			// Determine busy state with liveSessionStates override.
			mainBusy := false
			if entry.Active {
				mainBusy = m.typing
			} else {
				mainBusy = entry.Busy
				if ls, ok := m.progressState.liveStates[entry.ID]; ok {
					mainBusy = ls.busy
				}
			}
			iconChar := "●"
			iconColor := lipgloss.Color("#10b981")
			if entry.Active {
				// Active: ● — user sees it, no extra mark needed.
			} else if mainBusy {
				// Non-active busy: spinner.
				iconChar = m.ticker.viewFrames(sidebarSpinnerFrames, 3)
			} else if m.progressState.unread[entry.ID] {
				// Non-active idle, but has unread results.
				iconChar = "✦"
				iconColor = lipgloss.Color("#f59e0b")
			} else {
				iconChar = "○"
			}
			icon = lipgloss.NewStyle().Foreground(iconColor).Render(iconChar)
			label := entry.Label
			if label == "" {
				label = entry.ID
			}
			labelW := contentW - 6
			if labelW < 10 {
				labelW = 10
			}
			label = truncateToWidth(label, labelW)
			line = fmt.Sprintf("%s %s  %s", prefix, icon, label)
		case "agent":
			roleColor := lipgloss.Color(RoleColor(entry.Role))
			// Use liveSessionStates for running state if available.
			agentRunning := entry.Running
			if ls, ok := m.progressState.liveStates[entry.ID]; ok {
				agentRunning = ls.busy
			}
			statusIcon := "●"
			statusStyle := lipgloss.NewStyle().Foreground(roleColor)
			if !agentRunning {
				if m.progressState.unread[entry.ID] {
					statusIcon = "✦"
					statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
				} else {
					statusIcon = "◦"
					statusStyle = statusStyle.Faint(true)
				}
			}
			label := fmt.Sprintf("🤖 %s/%s", entry.Role, entry.Instance)
			if entry.MessageHint != "" {
				hint := entry.MessageHint
				if runes := []rune(hint); len(runes) > 35 {
					hint = string(runes[:32]) + "..."
				}
				hint = strings.ReplaceAll(hint, "\n", " ")
				label = fmt.Sprintf("🤖 %s: %s", entry.Role, hint)
			}
			labelW := contentW - 5
			if labelW < 10 {
				labelW = 10
			}
			label = truncateToWidth(label, labelW)
			labelStyle := lipgloss.NewStyle().Foreground(roleColor)
			if !agentRunning {
				labelStyle = labelStyle.Faint(true)
			}
			line = fmt.Sprintf("%s %s  %s", prefix, statusStyle.Render(statusIcon), labelStyle.Render(label))
			if agentRunning {
				line += " ⏳"
			}
		default:
			line = fmt.Sprintf("%s   %s", prefix, entry.Label)
		}
		sb.WriteString(truncateToWidth(line, contentW))
		sb.WriteString("\n")
	}

	return sb.String()
}

// viewSessionsDetail renders the detail/log view for a selected session.
func (m *cliModel) viewSessionsDetail() string {
	s := &m.styles

	var title string
	if m.panelState.sessCursor >= 0 && m.panelState.sessCursor < len(m.panelState.sessItems) {
		entry := m.panelState.sessItems[m.panelState.sessCursor]
		switch entry.Type {
		case "main":
			title = "👤 " + entry.Label
		case "agent":
			title = fmt.Sprintf("🤖 %s/%s", entry.Role, entry.Instance)
		case "group":
			title = "💬 " + entry.Label
		}
	}
	help := s.PanelDesc.Render("Esc Back")

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(title))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	for _, line := range m.panelState.bgLogLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// showSessionCreateDialog creates a new session with an auto-generated name.
func (m *cliModel) showSessionCreateDialog() tea.Cmd {
	m.panelState.mode = "" // close sessions panel
	ds, err := LoadDirSessions(m.workDir)
	if err != nil {
		m.showTempStatus(fmt.Sprintf("Failed: %v", err))
		return nil
	}
	name, chatID, err := ds.addSessionAuto()
	if err != nil {
		m.showTempStatus(fmt.Sprintf("Failed: %v", err))
		return nil
	}
	// --- Pre-creation cleanup: nuke ALL residual state for this chatID ---
	// Even with UUID-based IDs (guaranteed unique), we clean defensively to
	// handle edge cases like race conditions or manual chatID reuse.
	sessionKey := "cli:" + chatID
	// 1. Delete any residual DB tenant (from a previously failed deletion)
	if m.channel != nil && m.channel.config.SessionsDeleteFn != nil {
		_ = m.channel.config.SessionsDeleteFn("cli", chatID)
	}
	// 2. Clean up any leftover worktree registration + physical git worktree.
	// This handles the case where a previous session with the same chatID had
	// auto_worktree enabled and left a worktree behind.
	tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
	// 3. Nuke persisted CWD — prevent inheriting a stale worktree directory.
	session.DeletePersistedCWD("cli", chatID)
	// 4. Nuke saved session state from memory.
	delete(m.savedSessions, sessionKey)
	// 5. Nuke persisted todo data.
	if m.todoManager != nil {
		m.todoManager.SetTodos(sessionKey, nil)
		_ = m.todoManager.SaveToFile(sessionKey)
	}

	m.saveCurrentSession()
	// Bind the new session's model to DB immediately. In remote mode,
	// SaveSessionLLMState skips local JSON (skipSub=true) AND does NOT write
	// to the DB tenants table — the session would have no model binding in DB,
	// forcing refreshCachedModelName to fall back to the global default (which
	// may not match the parent session's model). Calling SelectModel writes
	// (subID, model) to the tenants table atomically.
	if m.activeSubID != "" && m.cachedModelName != "" {
		if m.llmSubscriber != nil {
			if err := m.llmSubscriber.SelectModel(m.senderID, m.activeSubID, m.cachedModelName, chatID); err != nil {
				log.WithError(err).Warn("showSessionCreateDialog: SelectModel failed for new session")
			}
		}
		// Also save to local JSON as cache (don't skip in remote mode — local
		// JSON is the fallback when GetSessionSubscription RPC fails).
		SaveSessionLLMState(m.workDir, chatID, SessionLLMState{
			SubscriptionID: m.activeSubID,
			Model:          m.cachedModelName,
		})
	}
	m.chatID = chatID
	SetLastActiveSession(m.defaultChatID, chatID)
	m.sessionName = name
	m.channelName = "cli"
	// Update workdir and persist CWD for the new session (same as switchToSession)
	workDir, _ := ParseChatID(chatID)
	if workDir != "" {
		m.workDir = workDir
		if m.channel != nil && m.channel.config.SetCWDFn != nil {
			_ = m.channel.config.SetCWDFn("cli", chatID, workDir)
		}
	}
	// Reset ALL session state to prevent inheritance from deleted sessions
	m.resetToIdleState()
	m.restoreSession()
	// Unified session setup — handles BindChatFn, suLoadHistoryCmd,
	// checkAndRestorePendingAskUser, inputReady, etc.
	cmds := m.postRestoreSessionSetup()
	// Refresh sessions list cache so sidebar/sessions panel shows the new session
	if m.sessionsListFn != nil {
		m.panelState.sessItems = m.sessionsListFn()
	}
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
	m.showTempStatus(fmt.Sprintf("Created session: %s", name))
	return tea.Batch(cmds...)
}

// deleteLocalSession deletes the selected session and switches to default if active.
func (m *cliModel) deleteLocalSession(entry SessionPanelEntry) tea.Cmd {
	// 1. Try to delete from backend DB. Ignore "not found" errors — the session
	// may be local-only (created in CLI JSON but never synced to server/runner).
	if m.channel != nil && m.channel.config.SessionsDeleteFn != nil {
		if err := m.channel.config.SessionsDeleteFn("cli", entry.ID); err != nil {
			errMsg := err.Error()
			if !strings.Contains(errMsg, "not found") {
				log.WithError(err).WithField("chatID", entry.ID).Warn("Backend session delete failed")
				m.showTempStatus(fmt.Sprintf("Delete failed: %v", err))
				return nil
			}
			// "not found" is fine — session is local-only, proceed to clean local JSON
		}
	}
	// 2. Remove from local JSON file (always, regardless of backend result).
	ds, err := LoadDirSessions(m.workDir)
	if err == nil {
		if err := ds.removeSessionByChatID(entry.ID); err != nil {
			log.WithError(err).WithField("chatID", entry.ID).Warn("Local session remove failed")
		}
	}
	// 3. Clean up worktree / peer registration for this session.
	sessionKey := "cli:" + entry.ID
	tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
	// 3b. Clean up persisted CWD so a future session with the same chatID
	// (e.g. the default workDir-based session) does not inherit a stale CWD.
	session.DeletePersistedCWD("cli", entry.ID)
	// 3c. Remove saved session state from memory to prevent stale state leaks.
	delete(m.savedSessions, sessionKey)
	if m.todoManager != nil {
		m.todoManager.SetTodos(sessionKey, nil)
		_ = m.todoManager.SaveToFile(sessionKey) // delete persisted todo file
	}
	// If we deleted the active session, switch to default
	if entry.Active {
		m.saveCurrentSession()
		m.chatID = m.defaultChatID
		SetLastActiveSession(m.defaultChatID, m.defaultChatID)
		m.sessionName = defaultSessionName
		m.workDir = m.defaultChatID
		if m.channel != nil && m.channel.config.SetCWDFn != nil {
			_ = m.channel.config.SetCWDFn("cli", m.defaultChatID, m.defaultChatID)
		}
		m.resetToIdleState()
		m.invalidateAllCache(false)
		m.restoreSession()
		cmds := m.postRestoreSessionSetup()
		// Refresh sessions list so sidebar/sessions panel reflects the deletion
		if m.sessionsListFn != nil {
			m.panelState.sessItems = m.sessionsListFn()
		}
		if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
			m.channel.config.SessionsListRefresh()
		}
		m.showTempStatus(fmt.Sprintf("Deleted session: %s", entry.Label))
		return tea.Batch(cmds...)
	}
	// Non-active session deleted: refresh sidebar so it disappears immediately.
	if m.sessionsListFn != nil {
		m.panelState.sessItems = m.sessionsListFn()
	}
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
	m.showTempStatus(fmt.Sprintf("Deleted session: %s", entry.Label))
	return nil
}

// switchToSession switches to the given session entry directly (used by sidebar click).
// Extracted from the sessions panel Enter key handler for reuse.
func (m *cliModel) switchToSession(entry SessionPanelEntry) (bool, tea.Cmd) {
	switch entry.Type {
	case "main":
		if entry.ID != m.chatID {
			// Clear unread flag — user is now viewing this session.
			delete(m.progressState.unread, entry.ID)
			// Close AskUser panel if it belongs to a different session
			if m.panelState.mode == "askuser" && m.askUserSession != entry.ID {
				m.panelState.mode = ""
				m.panelState.askItems = nil
				m.relayoutViewport()
			}
			m.saveCurrentSession()
			m.chatID = entry.ID
			m.senderID = "cli_user" // 重置为 CLI 默认身份，防止 /su 跨渠道残留
			SetLastActiveSession(m.defaultChatID, entry.ID)
			m.channelName = entry.Channel
			// Update workdir to match the session's workdir.
			workDir, _ := ParseChatID(entry.ID)
			if workDir != "" {
				m.workDir = workDir
				if m.channel != nil && m.channel.config.SetCWDFn != nil {
					_ = m.channel.config.SetCWDFn(entry.Channel, entry.ID, workDir)
				}
			}
			// Update background task session key for isolation
			if m.channel != nil {
				m.channel.bgSessionKey = "cli:" + entry.ID
				m.channel.updateBgTaskCountFn()
			}
			m.messages = nil
			m.lastTokenUsage = nil
			m.invalidateAllCache(false)
			m.todos = nil // clear stale todos from previous session
			m.todosDoneCleared = false
			m.restoreSession()
			cmds := m.postRestoreSessionSetup()
			if len(cmds) > 0 {
				return true, tea.Batch(cmds...)
			}
		}
	case "agent":
		agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
		if entry.Instance != "" {
			agentChatID += ":" + entry.Instance
		}
		if agentChatID != m.chatID {
			// Clear unread flag — user is now viewing this session.
			delete(m.progressState.unread, entry.ID)
			// Close AskUser panel if it doesn't belong to the new agent session
			if m.panelState.mode == "askuser" && m.askUserSession != agentChatID {
				m.panelState.mode = ""
				m.panelState.askItems = nil
				m.relayoutViewport()
			}
			m.saveCurrentSession()
			m.chatID = agentChatID
			m.channelName = "agent"
			// Update workdir to match the parent session's workdir.
			workDir, _ := ParseChatID(entry.ParentID)
			if workDir != "" {
				m.workDir = workDir
				if m.channel != nil && m.channel.config.SetCWDFn != nil {
					_ = m.channel.config.SetCWDFn(entry.Channel, entry.ParentID, workDir)
				}
			}
			// Update background task session key for isolation
			if m.channel != nil {
				m.channel.bgSessionKey = "agent:" + agentChatID
				m.channel.updateBgTaskCountFn()
			}
			m.messages = nil
			m.lastTokenUsage = nil
			m.invalidateAllCache(false)
			m.todos = nil // clear stale todos from previous session
			m.todosDoneCleared = false
			m.restoreSession()
			cmds := m.postRestoreSessionSetup()
			if len(cmds) > 0 {
				return true, tea.Batch(cmds...)
			}
		}
	}
	return true, nil
}
