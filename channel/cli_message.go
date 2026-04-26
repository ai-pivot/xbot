package channel

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"xbot/bus"
	"xbot/version"
)

// ---------------------------------------------------------------------------
// Helper Methods
// ---------------------------------------------------------------------------

// handleTabComplete: handle Tab completion (§8: /command completion, §8b: @file path completion)
func (m *cliModel) handleTabComplete() {
	input := m.textarea.Value()

	// Detect @ file reference completion (detect from end of input)
	atOk, atPrefix := detectAtPrefix(input)
	if atOk {
		m.handleFileTabComplete(input, atPrefix)
		return
	}

	// / Command completion
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return
	}

	if len(m.completions) == 0 {
		for _, cmd := range cliCommands {
			if strings.HasPrefix(cmd, trimmed) {
				m.completions = append(m.completions, cmd)
			}
		}
		if len(m.completions) == 0 {
			return
		}
		m.compIdx = 0
	} else {
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}

	m.textarea.SetValue(m.completions[m.compIdx] + " ")
}

// detectAtPrefix Detect if input text ends with @ to trigger file completion.
// ok=true means @ was detected (should trigger glob even if no characters follow).
// prefix is the part after @ to the end of text.
func detectAtPrefix(input string) (ok bool, prefix string) {
	if len(input) == 0 || input[len(input)-1] == ' ' {
		return false, ""
	}
	i := len(input) - 1
	for i >= 0 && input[i] != ' ' && input[i] != '@' {
		i--
	}
	if i < 0 || input[i] != '@' {
		return false, ""
	}
	if i > 0 && input[i-1] != ' ' {
		return false, ""
	}
	return true, input[i+1:]
}

// populateFileCompletions Execute glob search based on prefix and populate fileCompletions
func (m *cliModel) populateFileCompletions(prefix string) {
	pattern := prefix
	if !strings.Contains(pattern, "*") {
		pattern += "*"
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		m.fileCompletions = nil
		m.fileCompIdx = 0
		return
	}
	// Filter hidden files (starting with .)
	filtered := matches[:0]
	for _, f := range matches {
		base := filepath.Base(f)
		if len(base) > 0 && base[0] != '.' {
			filtered = append(filtered, f)
		}
	}
	matches = filtered
	sort.Slice(matches, func(i, j int) bool {
		di, dj := isDir(matches[i]), isDir(matches[j])
		if di != dj {
			return di
		}
		return matches[i] < matches[j]
	})
	if len(matches) > fileCompMaxNameRunes {
		matches = matches[:fileCompMaxNameRunes]
	}
	m.fileCompletions = matches
	m.fileCompIdx = 0
}

// handleFileTabComplete: handle @ file path Tab completion
func (m *cliModel) handleFileTabComplete(input string, prefix string) {
	if !m.fileCompActive || len(m.fileCompletions) == 0 {
		// First Tab or candidates cleared: glob and enter cycle mode
		m.populateFileCompletions(prefix)
		if len(m.fileCompletions) == 0 {
			return
		}
		m.fileCompActive = true
	} else {
		// Cycle mode: switch to next candidate
		m.fileCompIdx = (m.fileCompIdx + 1) % len(m.fileCompletions)
	}

	selected := m.fileCompletions[m.fileCompIdx]
	if isDir(selected) {
		selected += "/"
	}
	atStart := len(input) - len(prefix) - 1
	newInput := input[:atStart] + "@" + selected
	m.textarea.SetValue(newInput)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// newInbound creates a bus.InboundMessage with common fields pre-filled.
// metadata can be nil.
func (m *cliModel) newInbound(content string, metadata map[string]string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:    m.channelName,
		SenderID:   m.senderID,
		ChatID:     m.chatID,
		ChatType:   "p2p",
		Content:    content,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		Metadata:   metadata,
	}
}

// appendSystem adds a system message to the message history and marks it as dirty.
func (m *cliModel) appendSystem(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      roleSystem,
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
}

// appendSystemMarkdown adds a system message that will be rendered through
// the glamour markdown renderer (for tables, headers, etc.).
func (m *cliModel) appendSystemMarkdown(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      roleSystem,
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
		markdown:  true,
	})
}

// sendInbound sends a message to the agent's inbound channel.
// Uses non-blocking send to prevent the BubbleTea event loop from freezing
// if the channel is full (e.g., agent is busy with a long LLM call).
// Returns false if the message was dropped.
func (m *cliModel) sendInbound(msg bus.InboundMessage) bool {
	if m.sendInboundFn != nil {
		return m.sendInboundFn(msg)
	}
	if m.msgBus == nil {
		return false
	}
	select {
	case m.msgBus.Inbound <- msg:
		return true
	default:
		// Channel full — agent is backlogged. Drop to prevent TUI freeze.
		return false
	}
}

// sendInboundWait sends a message to the agent's inbound channel with a timeout.
// Use for critical messages (ask_user answers) that MUST be delivered.
// Returns false if the message couldn't be sent within the deadline.
func (m *cliModel) sendInboundWait(msg bus.InboundMessage, timeout time.Duration) bool {
	if m.sendInboundFn != nil {
		return m.sendInboundFn(msg)
	}
	if m.msgBus == nil {
		return false
	}
	select {
	case m.msgBus.Inbound <- msg:
		return true
	case <-time.After(timeout):
		return false
	}
}

// sendCancel sends a cancel request to the agent and adds a system notification.
func (m *cliModel) sendCancel() {
	if !m.sendInbound(m.newInbound("/cancel", nil)) {
		m.showSystemMsg("Cancel failed: agent channel busy, try again", feedbackError)
		return
	}
	m.showSystemMsg(m.locale.CancelSent, feedbackInfo)
}

// sendToAgent Send command to agent, and add user message to history (§3 command passthrough mechanism)
func (m *cliModel) sendToAgent(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      roleUser,
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
	if m.msgBus != nil {
		m.sendInbound(m.newInbound(content, map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}))
		m.startAgentTurn()
	}
}

// sendMessage Send user message, return possible tea.Cmd to execute (e.g. easter egg animation tick).
func (m *cliModel) sendMessage(content string) tea.Cmd {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "/") {
		return m.handleSlashCommand(content)
	}

	// 🥚 Easter egg #3: The Answer is 42 detection
	if isAnswer42(content) {
		_ = m.activateEasterEgg(easterEggAnswer42)
	}

	// Parse @ file references, extract file paths
	media := parseFileReferences(content)

	// Add user message to history
	m.messages = append(m.messages, cliMessage{
		role:      roleUser,
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})

	// Update display and force scroll to bottom (always visible when user sends new message)
	m.updateViewportContent()
	m.viewport.GotoBottom()
	m.newContentHint = false

	// Send to message bus
	if m.msgBus != nil {
		msg := m.newInbound(content, nil) // ReplyPolicyAuto (default)
		msg.Media = media
		m.sendInbound(msg)
		m.startAgentTurn()
	} else if m.sendInboundFn != nil {
		// Remote mode: msgBus is nil but sendInboundFn is set
		msg := m.newInbound(content, nil)
		msg.Media = media
		m.sendInbound(msg)
		m.startAgentTurn()
	}
	return nil
}

// parseFileReferences Extract @path file references from user message.
// Match paths after @ followed by non-space characters, verify file exists before returning.
func parseFileReferences(content string) []string {
	var files []string
	seen := make(map[string]bool)
	for i := 0; i < len(content); i++ {
		if content[i] == '@' {
			// @ must be at word start
			if i > 0 && content[i-1] != ' ' {
				continue
			}
			// Extract path after @
			j := i + 1
			for j < len(content) && content[j] != ' ' {
				j++
			}
			path := content[i+1 : j]
			// Remove trailing /
			path = strings.TrimRight(path, "/")
			if path != "" && !seen[path] {
				if _, err := os.Stat(path); err == nil {
					files = append(files, path)
					seen[path] = true
				}
			}
			i = j
		}
	}
	return files
}

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.lastReasoning = ""
	m.progress = nil
	m.iterationStartTime = time.Now() // wall-clock start for iteration 0
	m.typingStartTime = time.Now()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []CLIToolProgress {
	var all []CLIToolProgress
	for _, snap := range m.iterationHistory {
		all = append(all, snap.Tools...)
	}
	return all
}

// handleSlashCommand Handle slash commands
func (m *cliModel) handleSlashCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)
	// Extract command part (strip arguments)
	parts := strings.Fields(cmd)
	command := ""
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}

	// 🥚 Easter egg commands checked first (hidden commands not registered in cliCommands)
	if handled, cmd := m.handleEasterEggCommand(cmd); handled {
		return cmd
	}

	switch command {
	// --- Local commands ---
	case "/cancel":
		m.sendCancel()

	case "/clear":
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.cachedHistory = ""
		m.exitSearch()

	case "/rewind":
		m.openRewindPanel()

	case "/settings":
		m.handleSettingsSlashCommand()

	case "/setup":
		m.openSetupPanel()

	case "/update":
		m.handleUpdateSlashCommand()

	case "/quit", "/exit":
		m.shouldQuit = true

	case "/help":
		helpContent := m.renderHelpPanel()
		m.showSystemMsg(helpContent, feedbackInfo)

	case "/search":
		m.enterSearchMode()

	case "/compact":
		// Keep local handling (system message style), send to msgBus but not as user bubble
		if m.msgBus != nil {
			m.sendInbound(m.newInbound("/compact", nil))
		}

	// --- Passthrough commands (sent to agent) ---
	case "/context":
		m.sendToAgent(cmd) // Direct passthrough, agent layer will parse

	case "/new":
		m.sendToAgent("/new")

	case "/tasks":
		m.handleTasksSlashCommand()

	case "/su":
		if cmd := m.handleSuSlashCommand(parts); cmd != nil {
			return cmd
		}

	case "/ss", "/sessions":
		m.openSessionsPanel()

	case "/chat":
		if cmd := m.handleChatSlashCommand(parts); cmd != nil {
			return cmd
		}

	case "/usage":
		m.handleUsageCommand()

	case "/channel":
		m.openChannelPanel()

	case "/user":
		userArg := ""
		if len(parts) > 1 {
			userArg = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
		m.handleUserCommand(userArg)

	default:
		if cmd := m.handleDefaultSlashCommand(command, cmd); cmd != nil {
			return cmd
		}
	}

	m.updateViewportContent()
	return nil
}

// handleSettingsSlashCommand handles the /settings command to open the interactive settings panel.
func (m *cliModel) handleSettingsSlashCommand() {
	// Open interactive settings panel locally
	if m.channel == nil {
		return
	}
	schema := m.channel.SettingsSchema()
	if len(schema) == 0 {
		m.showSystemMsg(m.locale.NoSettings, feedbackWarning)
		return
	}
	// Get current values: config is the single source of truth for LLM settings.
	// Only overlay non-LLM settings from SettingsService (e.g. theme, language).
	currentValues := m.mergeCLISettingsValues()
	// Inject model list into combo options for tier model selectors.
	if m.channel.modelLister != nil {
		allModels := m.channel.modelLister.ListAllModels()
		for i, s := range schema {
			if (s.Key == "vanguard_model" || s.Key == "balance_model" || s.Key == "swift_model") && len(allModels) > 0 {
				opts := make([]SettingOption, len(allModels))
				for j, ml := range allModels {
					opts[j] = SettingOption{Label: ml, Value: ml}
				}
				schema[i].Options = opts
			}
		}
	}
	m.openSettingsPanel(schema, currentValues, func(values map[string]string) {
		// --- Subscription generation guard ---
		// If the active subscription changed since this panel was opened,
		// the per-subscription LLM fields (provider/key/model/base_url) are STALE
		// and must NOT be written back — they would overwrite the new subscription.
		// This is the structural guarantee against subscription data corruption.
		if m.panelSubGeneration != m.subGeneration {
			for k := range values {
				if isSubscriptionScopedSettingKey(k) {
					delete(values, k)
				}
			}
		}
		// Persist user-scoped settings to SettingsService, and apply global/runtime
		// settings through config.ApplySettings (single source of truth for global/LLM).
		m.persistCLISettingsValues(values)
		// NOTE: UI updates (theme/locale/model/viewport) are handled
		// by handleSettingsSavedMsg in Update() — do NOT call them here
		// since this callback runs in a background goroutine.
	})
}

// handleUpdateSlashCommand handles the /update command to check for updates.
func (m *cliModel) handleUpdateSlashCommand() {
	if m.checkingUpdate {
		m.showSystemMsg(m.locale.CheckingUpdate, feedbackInfo)
	} else {
		m.checkingUpdate = true
		m.updateNotice = nil
		if m.channel != nil {
			m.channel.CheckUpdateAsync()
		}
		m.showSystemMsg(m.locale.CheckingUpdate, feedbackInfo)
	}
}

// handleTasksSlashCommand handles the /tasks command to open the unified tasks & agents panel.
func (m *cliModel) handleTasksSlashCommand() {
	taskCount := 0
	if m.bgTaskCountFn != nil {
		taskCount = m.bgTaskCountFn()
	}
	agentCnt := 0
	if m.agentCountFn != nil {
		agentCnt = m.agentCountFn()
	}
	if taskCount+agentCnt == 0 {
		m.showSystemMsg(m.locale.BgTasksEmpty, feedbackInfo)
	} else {
		m.openBgTasksPanel()
	}
}

// handleSuSlashCommand handles the /su command to switch user identity.
//
//	/su          — switch back to default identity
//	/su <userID> — switch to specified user identity
//	/su web:<senderID>[:<token>] — switch to Web user
//
// Returns a non-nil tea.Cmd if an async operation was started (e.g. history loading),
// or nil to continue with the standard slash command tail.
func (m *cliModel) handleSuSlashCommand(parts []string) tea.Cmd {
	if len(parts) < 2 {
		if m.senderID == "cli_user" {
			m.showSystemMsg(m.locale.SuAlreadyDefault, feedbackInfo)
			return nil
		}
		m.senderID = "cli_user"
		m.chatID = m.defaultChatID
	} else {
		arg := strings.TrimSpace(parts[1])
		if strings.HasPrefix(arg, "web:") {
			webParts := strings.SplitN(strings.TrimPrefix(arg, "web:"), ":", 2)
			if len(webParts) == 0 || webParts[0] == "" {
				m.showSystemMsg("❌ 格式: /su web:<senderID>[:<token>]", feedbackInfo)
				return nil
			}
			m.channelName = "web"
			m.senderID = webParts[0]
			m.chatID = webParts[0]
			m.showSystemMsg(fmt.Sprintf("✅ 已切换到 Web 用户: %s", webParts[0]), feedbackInfo)
		} else {
			newID := arg
			if newID == "cli_user" || newID == "" {
				m.senderID = "cli_user"
				m.chatID = m.defaultChatID
			} else {
				m.senderID = newID
				m.chatID = newID
			}
		}
	}
	m.messages = nil
	m.invalidateAllCache(false)
	if m.channel != nil && m.channel.config.DynamicHistoryLoader != nil {
		m.suLoading = true
		m.splashFrame = 0
		return tea.Batch(m.splashTick(0), m.suLoadHistoryCmd())
	}
	m.showSystemMsg(fmt.Sprintf(m.locale.SuSwitched, m.chatID), feedbackInfo)
	return nil
}

// handleChatSlashCommand handles the /chat command for chat room management.
//
//	/chat new [label] — create new session
//	/chat <id>        — switch to specified session
//	/chat ls          — list all sessions (text version)
//
// Returns a non-nil tea.Cmd if an async operation was started (e.g. history loading),
// or nil to continue with the standard slash command tail.
func (m *cliModel) handleChatSlashCommand(parts []string) tea.Cmd {
	if len(parts) < 2 {
		m.showSystemMsg("用法: /chat new [label] | /chat <id> | /chat ls", feedbackInfo)
		return nil
	}
	arg := strings.TrimSpace(parts[1])
	switch arg {
	case "new":
		if m.channel != nil && m.channel.config.ChatCreateFn != nil {
			label := ""
			if len(parts) > 2 {
				label = strings.Join(parts[2:], " ")
			}
			chatID, err := m.channel.config.ChatCreateFn(m.channelName, m.defaultChatID, label)
			if err != nil {
				m.showSystemMsg("创建失败: "+err.Error(), feedbackInfo)
				return nil
			}
			m.chatID = chatID
			m.messages = nil
			m.invalidateAllCache(false)
			m.showSystemMsg(fmt.Sprintf("✅ 新会话已创建: %s", chatID), feedbackInfo)
		} else {
			m.showSystemMsg("❌ 当前不支持创建新会话", feedbackInfo)
		}
	case "ls":
		if m.sessionsListFn != nil {
			entries := m.sessionsListFn()
			if len(entries) == 0 {
				m.showSystemMsg("(no active sessions)", feedbackInfo)
			} else {
				var lines []string
				for _, e := range entries {
					switch e.Type {
					case "main":
						active := ""
						if e.ID == m.chatID {
							active = " ←"
						}
						lines = append(lines, fmt.Sprintf("  ● %s%s", e.Label, active))
					case "agent":
						status := "●"
						if !e.Running {
							status = "◦"
						}
						lines = append(lines, fmt.Sprintf("  %s 🤖 %s/%s", status, e.Role, e.Instance))
					}
				}
				m.showSystemMsg("Sessions:\n"+strings.Join(lines, "\n"), feedbackInfo)
			}
		} else {
			m.showSystemMsg("❌ Sessions list not available", feedbackInfo)
		}
	default:
		// Switch to specific chatID
		m.chatID = arg
		m.messages = nil
		m.invalidateAllCache(false)
		if m.channel != nil && m.channel.config.DynamicHistoryLoader != nil {
			m.suLoading = true
			m.splashFrame = 0
			return tea.Batch(m.splashTick(0), m.suLoadHistoryCmd())
		}
		m.showSystemMsg(fmt.Sprintf("✅ 已切换到会话: %s", arg), feedbackInfo)
	}
	return nil
}

// handleDefaultSlashCommand handles unrecognized slash commands.
// It checks for easter eggs (e.g. /version triple-call detection), then passes
// unknown commands through to the agent layer.
// Returns a non-nil tea.Cmd for early-exit scenarios, or nil to continue
// with the standard slash command tail.
func (m *cliModel) handleDefaultSlashCommand(command string, cmd string) tea.Cmd {
	// 🥚 Easter egg #7: /version triple-call detection
	if command == "/version" {
		if m.recordVersionHit() {
			art := fmt.Sprintf(versionAchievementArt, version.Version)
			_ = m.activateEasterEgg(easterEggVersion)
			m.easterEggCustom = art
			m.updateViewportContent()
			return nil
		}
	}
	// Unknown command attempted to passthrough to agent (agent layer may recognize it)
	m.sendToAgent(cmd)
	return nil
}

// handleAgentMessage Handle agent reply
func (m *cliModel) handleAgentMessage(msg bus.OutboundMessage) {
	// Filter by session: only process outbound for the currently viewed session.
	if msg.Channel != "" && msg.ChatID != "" {
		if msg.Channel != m.channelName || msg.ChatID != m.chatID {
			return
		}
	}

	turnID := m.agentTurnID // capture at entry for stale-signal guard
	content := msg.Content

	// Handle __FEISHU_CARD__ protocol (simplified display)
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ConvertFeishuCard(content)
	}

	// Empty content with no waiting user: end turn and flush queue,
	// but don't append a blank message.
	if content == "" && !msg.WaitingUser && len(msg.ToolsUsed) == 0 {
		m.streamingMsgIdx = -1
		m.progress = nil
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}
		return
	}

	if msg.IsPartial {
		// Streaming output: append to current message
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// Append to existing streaming message
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// Create new streaming message
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      roleAssistant,
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
			})
		}
	} else {
		// Complete message
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// Update streaming message to complete message
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// Add new complete assistant message
			m.messages = append(m.messages, cliMessage{
				role:      roleAssistant,
				content:   content,
				timestamp: time.Now(),
				isPartial: false,
				dirty:     true,
			})
		}
		// Reset streaming state
		m.streamingMsgIdx = -1
		// Capture reasoning from progress before it might be cleared.
		// Do NOT clear m.progress here — progress is only cleared by endAgentTurn.
		// Intermediate text messages (e.g. thinking content) arrive while the agent
		// is still running; clearing progress here would hide the progress panel
		// and make it look like the turn ended prematurely.
		if turnID == m.agentTurnID && m.progress != nil {
			reasoning := m.progress.Reasoning
			if reasoning == "" {
				reasoning = m.progress.ReasoningStreamContent
			}
			if reasoning != "" {
				m.lastReasoning = reasoning
			}
			if m.progress.Thinking != "" {
				m.lastThinking = m.progress.Thinking
			}
		}
		m.renderCacheValid = false
		m.updateViewportContent()

		// §12 AskUser panel: detect WaitingUser and open interactive panel
		if msg.WaitingUser {
			var items []askItem
			if msg.Metadata != nil {
				if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
					// Multi-question mode: parse questions array
					var qs []askQItem
					if json.Unmarshal([]byte(qJSON), &qs) == nil {
						for _, q := range qs {
							items = append(items, askItem{Question: q.Question, Options: q.Options})
						}
					}
				}
			}
			// Fallback: search message history for ❓ (legacy single-question format)
			if len(items) == 0 {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if strings.HasPrefix(m.messages[i].content, "❓") {
						question := strings.TrimSpace(strings.TrimPrefix(m.messages[i].content, "❓"))
						m.messages = append(m.messages[:i], m.messages[i+1:]...)
						if question != "" {
							items = append(items, askItem{Question: question})
						}
						break
					}
				}
			}
			if len(items) > 0 {
				m.updateViewportContent()
				m.openAskUserPanel(items, func(answers map[string]string) {
					// Format answers as tool-call style message
					var parts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", item.Question, ans))
					}
					content := strings.Join(parts, "\n\n")
					// Send to agent as tool result replacement (not a new user message).
					// Use blocking send with timeout — ask_user answers are critical:
					// if dropped, the agent hangs indefinitely waiting for a response.
					if m.msgBus != nil {
						if !m.sendInboundWait(m.newInbound(content, map[string]string{"ask_user_answered": "true"}), 5*time.Second) {
							m.showSystemMsg("Failed to deliver answer to agent, please try again", feedbackError)
						}
					}
					// Render as tool call style (not user message)
					m.messages = append(m.messages, cliMessage{
						role:       roleToolSummary,
						content:    "AskUser",
						timestamp:  time.Now(),
						dirty:      true,
						iterations: nil,
						tools: []CLIToolProgress{
							{
								Name:    "AskUser",
								Label:   fmt.Sprintf("asked %d question(s)", len(items)),
								Status:  "completed",
								Elapsed: 0,
							},
						},
					})
					// Show answers as system message
					var answerParts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						answerParts = append(answerParts, fmt.Sprintf("  %s → %s", item.Question, ans))
					}
					m.showSystemMsg(strings.Join(answerParts, "\n"), feedbackInfo)
					m.startAgentTurn()
					m.updateViewportContent()
				}, func() {
					m.showSystemMsg(m.locale.AskCancelled, feedbackInfo)
					m.typing = false
					m.updatePlaceholder()
					m.inputReady = true
					m.resetProgressState()
					m.updateViewportContent()
				})
				return
			}
		}

		// Snapshot the final iteration before clearing
		if m.lastSeenIteration >= 0 && (len(m.lastCompletedTools) > 0 || m.lastReasoning != "" || m.lastThinking != "") {
			alreadySnapped := false
			for _, s := range m.iterationHistory {
				if s.Iteration == m.lastSeenIteration {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				// Filter tools by Iteration field to ensure correct attribution
				var finalTools []CLIToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.lastSeenIteration {
						finalTools = append(finalTools, t)
					}
				}
				snap := cliIterationSnapshot{
					Iteration:   m.lastSeenIteration,
					Reasoning:   m.lastReasoning,
					Thinking:    m.lastThinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
				}
				if len(finalTools) > 0 || m.lastReasoning != "" || m.lastThinking != "" {
					m.iterationHistory = append(m.iterationHistory, snap)
				}
			}
		}

		// §2 Tool visualization: insert tool_summary before assistant message
		// Build iterations from pendingToolSummary (PhaseDone) + local iterationHistory.
		// Deduplicate: if an iteration exists in both, prefer the PhaseDone version
		// (which has complete reasoning from the server) over the local snapshot.
		var toolSummaryIterations []cliIterationSnapshot
		pendingIters := make(map[int]bool)
		if m.pendingToolSummary != nil {
			for _, it := range m.pendingToolSummary.iterations {
				pendingIters[it.Iteration] = true
			}
			toolSummaryIterations = append(toolSummaryIterations, m.pendingToolSummary.iterations...)
			// Remove the last tool_summary placeholder that PhaseDone appended.
			// We track by index from end because append copies the value,
			// making pointer comparison unreliable.
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].role == roleToolSummary {
					m.messages = append(m.messages[:i], m.messages[i+1:]...)
					break
				}
			}
			m.pendingToolSummary = nil
		}
		if len(m.iterationHistory) > 0 {
			for _, it := range m.iterationHistory {
				if !pendingIters[it.Iteration] {
					toolSummaryIterations = append(toolSummaryIterations, it)
				}
			}
		}
		if len(toolSummaryIterations) > 0 {
			toolMsg := cliMessage{
				role:       roleToolSummary,
				content:    "",
				timestamp:  time.Now(),
				iterations: toolSummaryIterations,
				dirty:      true,
			}
			// Find the assistant message we just added and insert before it
			assistantIdx := len(m.messages) - 1
			if assistantIdx >= 0 && m.messages[assistantIdx].role == roleAssistant {
				m.messages = append(m.messages[:assistantIdx], append([]cliMessage{toolMsg}, m.messages[assistantIdx:]...)...)
			} else {
				// Fallback: append at end
				m.messages = append(m.messages, toolMsg)
			}
			m.renderCacheValid = false
		}

		// Reset iteration tracking state
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			// §Q Mark that message queue needs refresh (checked by Update loop)
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}

	}

	m.updateViewportContent()
}

// renderWrappedBlock writes text lines to sb with a guide prefix and content style.
// Empty lines and trailing whitespace are skipped. Lines exceeding maxWidth are
// hard-wrapped using CJK-aware rune counting.
func renderWrappedBlock(sb *strings.Builder, text string, maxWidth int, guide, contentStyle lipgloss.Style) {
	guideStr := guide.Render("  │ ")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		for _, wl := range strings.Split(hardWrapRunes(line, maxWidth), "\n") {
			sb.WriteString(guideStr + contentStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
}

// renderWrappedBlockDimmed is like renderWrappedBlock but wraps the entire line
// in outerStyle (typically dimStyle for completed iterations).
func renderWrappedBlockDimmed(sb *strings.Builder, text string, maxWidth int, guide, contentStyle, outerStyle lipgloss.Style) {
	guideStr := guide.Render("  │ ")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		for _, wl := range strings.Split(hardWrapRunes(line, maxWidth), "\n") {
			sb.WriteString(outerStyle.Render(guideStr + contentStyle.Render(wl)))
			sb.WriteString("\n")
		}
	}
}

// renderProgressBlock renders the iteration progress panel for the viewport.
func (m *cliModel) renderProgressBlock() string {
	if !m.typing && m.progress == nil {
		return ""
	}

	bubbleWidth := m.contentWidth()
	innerWidth := bubbleWidth - 4 // border(2) + padding(2)

	// §20 Use cached styles
	s := &m.styles
	iterStyle := s.ProgressIter
	thinkingStyle := s.ProgressThinking
	reasoningStyle := s.TextMutedSt // dimmed style for reasoning chain
	toolDoneStyle := s.ProgressDone
	toolRunningStyle := s.ProgressRunning
	toolErrorStyle := s.ProgressError
	elapsedStyle := s.ProgressElapsed
	indentGuide := s.ProgressIndent
	reasoningGuide := s.ProgressDim // dimmer │ for reasoning
	thinkingGuide := indentGuide    // normal │ for thinking
	reasoningW := lipgloss.Width(reasoningGuide.Render("  │ "))
	thinkingW := lipgloss.Width(thinkingGuide.Render("  │ "))
	dimStyle := s.ProgressDim

	var sb strings.Builder

	// Render completed iterations (dimmed)
	for _, snap := range m.iterationHistory {
		sb.WriteString(dimStyle.Render(iterStyle.Render(fmt.Sprintf("#%d", snap.Iteration))))
		sb.WriteString("\n")
		renderWrappedBlockDimmed(&sb, snap.Reasoning, innerWidth-reasoningW, reasoningGuide, reasoningStyle, dimStyle)
		renderWrappedBlockDimmed(&sb, snap.Thinking, innerWidth-thinkingW, thinkingGuide, thinkingStyle, dimStyle)
		for _, tool := range snap.Tools {
			label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			line := fmt.Sprintf("  │ %s %s", icon, label)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(dimStyle.Render(sty.Render(line)))
			sb.WriteString("\n")
		}
	}

	// Render current iteration
	if m.progress != nil {
		sb.WriteString(iterStyle.Render(fmt.Sprintf("#%d", m.progress.Iteration)))
		sb.WriteString("\n")

		// Reasoning: prefer streaming content (real-time) over static snapshot
		reasoningText := m.progress.ReasoningStreamContent
		if reasoningText == "" {
			reasoningText = m.progress.Reasoning
		}
		isReasoningStreaming := m.progress.ReasoningStreamContent != "" && m.progress.StreamContent == ""
		if reasoningText != "" {
			// Typewriter effect for reasoning streaming content
			totalReasoningRunes := len([]rune(m.progress.ReasoningStreamContent))
			if isReasoningStreaming && totalReasoningRunes > 0 {
				runes := []rune(m.progress.ReasoningStreamContent)
				if m.rwVisible > 0 && m.rwVisible < totalReasoningRunes {
					runes = runes[:m.rwVisible]
				}
				reasoningText = string(runes)
			}
			lines := strings.Split(reasoningText, "\n")
			// Solid cursor while actively typing; blink only when waiting for next chunk.
			reasoningTyping := isReasoningStreaming && m.rwVisible < totalReasoningRunes
			cursorVisible := reasoningTyping || (m.ticker.ticks/5)%2 == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					if isLast && isReasoningStreaming && cursorVisible {
						sb.WriteString(reasoningGuide.Render("  │ ") + reasoningStyle.Render(wl) + s.StreamCursor.Render("▋"))
					} else {
						sb.WriteString(reasoningGuide.Render("  │ ") + reasoningStyle.Render(wl))
					}
					sb.WriteString("\n")
				}
			}
		}

		renderWrappedBlock(&sb, m.progress.Thinking, innerWidth-thinkingW, thinkingGuide, thinkingStyle)

		// Completed tools in current iteration — filter by Iteration field
		for _, tool := range m.progress.CompletedTools {
			if tool.Iteration != m.progress.Iteration {
				continue
			}
			label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			if tool.Elapsed > 0 {
				elapsedStr := formatElapsed(tool.Elapsed)
				// Prefix: "  │ "(4) + icon(2) + " "(1) = 7, elapsed adds len(elapsedStr) more
				overhead := 7 + len(elapsedStr)
				label = truncateToWidth(label, innerWidth-overhead)
				line := fmt.Sprintf("  │ %s %s", icon, label)
				pad := innerWidth - lipgloss.Width(line) - len(elapsedStr)
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(elapsedStr)
				sb.WriteString(sty.Render(line))
			} else {
				line := fmt.Sprintf("  │ %s %s", icon, truncateToWidth(label, innerWidth-7))
				sb.WriteString(sty.Render(line))
			}
			sb.WriteString("\n")
		}

		// Active tools — label + live elapsed timer
		for _, tool := range m.progress.ActiveTools {
			if tool.Status == "done" || tool.Status == "error" {
				continue
			}
			label, _, _ := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			pulseIcon := m.ticker.viewFrames(pulseFrames)
			// Calculate live elapsed time
			var elapsedMs int64
			if !tool.StartedAt.IsZero() {
				elapsedMs = time.Since(tool.StartedAt).Milliseconds()
			} else {
				elapsedMs = tool.Elapsed
			}
			elapsedStr := formatElapsed(elapsedMs)
			// Prefix: "  │ "(4) + icon(2) + " "(1) = 7, elapsed adds ~8 more
			overhead := 7 + 2 + len(elapsedStr)
			label = truncateToWidth(label, innerWidth-overhead)
			line := fmt.Sprintf("  │ %s %s", pulseIcon, label)
			pad := innerWidth - lipgloss.Width(line) - len(elapsedStr)
			if pad < 1 {
				pad = 1
			}
			line += strings.Repeat(" ", pad) + elapsedStyle.Render(elapsedStr)
			sb.WriteString(toolRunningStyle.Render(line))
			sb.WriteString("\n")
		}

		// Phase-specific fallback when no tools are shown
		hasTools := len(m.progress.ActiveTools) > 0 || len(m.progress.CompletedTools) > 0

		// Stream content: render LLM output in progress block when streaming
		if m.progress.StreamContent != "" {
			// Typewriter effect: gradually reveal characters
			totalRunes := len([]rune(m.progress.StreamContent))
			runes := []rune(m.progress.StreamContent)
			if m.twVisible > 0 && m.twVisible < totalRunes {
				runes = runes[:m.twVisible]
			}
			streamText := string(runes)
			lines := strings.Split(streamText, "\n")
			// Blinking cursor: only blink when waiting for next stream chunk.
			// While actively typing (behind buffer), cursor stays solid.
			typing := m.twVisible < totalRunes
			cursorVisible := typing || (m.ticker.ticks/5)%2 == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					if isLast && cursorVisible {
						sb.WriteString(thinkingGuide.Render("  │ ") + thinkingStyle.Render(wl) + s.StreamCursor.Render("▋"))
					} else {
						sb.WriteString(thinkingGuide.Render("  │ ") + thinkingStyle.Render(wl))
					}
					sb.WriteString("\n")
				}
			}
		} else if !hasTools {
			switch m.progress.Phase {
			case "thinking":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.view())
				sb.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
				sb.WriteString("\n")
			case "compressing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" compressing..."))
				sb.WriteString("\n")
			case "retrying":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" retrying..."))
				sb.WriteString("\n")
			}
		}

		// SubAgent tree
		if len(m.progress.SubAgents) > 0 {
			var treeSB strings.Builder
			m.renderSubAgentTree(&treeSB, m.progress.SubAgents, "", innerWidth)
			if treeSB.Len() > 0 {
				sb.WriteString("\n")
				sb.WriteString(treeSB.String())
			}
		}
	} else if m.typing {
		sb.WriteString("  ")
		sb.WriteString(m.ticker.viewFrames(orbitFrames))
		sb.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
		sb.WriteString("\n")
	}

	content := strings.TrimRight(sb.String(), "\n")
	if content == "" {
		return ""
	}

	// Total elapsed
	elapsed := ""
	if !m.typingStartTime.IsZero() {
		elapsed = " " + elapsedStyle.Render(formatElapsed(time.Since(m.typingStartTime).Milliseconds()))
	}

	// Header
	headerStyle := s.ProgressHeader
	header := headerStyle.Render("Progress") + elapsed

	// Wrap in border
	blockStyle := s.ProgressBlock.Width(bubbleWidth)

	return blockStyle.Render(header+"\n"+content) + "\n\n"
}

// renderSubAgentTree renders nested sub-agents with indentation.
// Only renders running/pending agents — completed or errored ones are already
// captured in the tool summary and shouldn't linger in the progress panel.
//
// Uses a prefix-based approach instead of depth-based: each level appends
// "│   " or "    " to the prefix depending on whether the parent was the last
// sibling. This avoids spurious vertical lines after a └── branch.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []CLISubAgent, prefix string, maxWidth int) {
	for i, sa := range agents {
		if sa.Status == "done" || sa.Status == "error" {
			continue
		}
		isLast := i == len(agents)-1
		connector := "└── "
		if !isLast {
			connector = "├── "
		}
		icon := m.ticker.viewFrames(waveFrames)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(RoleColor(sa.Role)))
		switch sa.Status {
		case "error":
			icon = "✗"
			style = m.styles.ProgressError
		}
		line := fmt.Sprintf("%s%s%s %s", prefix, connector, icon, sa.Role)
		if sa.Desc != "" {
			// Truncate Desc separately to account for prefix+icon+role overhead.
			overhead := lipgloss.Width(line) + 2 // +2 for ": "
			descW := maxWidth - overhead
			if descW < 10 {
				descW = 10
			}
			line += ": " + truncateToWidth(sa.Desc, descW)
		}
		sb.WriteString(style.Render(line))
		sb.WriteString("\n")
		if len(sa.Children) > 0 {
			childPrefix := prefix
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
			m.renderSubAgentTree(sb, sa.Children, childPrefix, maxWidth)
		}
	}
}

// renderHelpPanel Render formatted help panel (round 4).
// Use lipgloss borders + grouped layout + status icons, replacing plain text.
func (m *cliModel) renderHelpPanel() string {
	contentWidth := m.contentWidth()
	if contentWidth < 40 {
		contentWidth = 40
	}

	// §20 Use cached styles
	s := &m.styles
	titleStyle := s.HelpTitle
	cmdStyle := s.HelpCmd
	descStyle := s.HelpDesc
	groupStyle := s.HelpGroup
	keyStyle := s.HelpKey
	panelStyle := s.HelpPanel.Width(contentWidth)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(m.locale.HelpTitle))
	sb.WriteString("\n")

	sb.WriteString(groupStyle.Render(m.locale.HelpCommandsTitle))
	sb.WriteString("\n")
	for _, c := range m.locale.HelpCmds {
		sb.WriteString("  " + cmdStyle.Render(c.Cmd) + " " + descStyle.Render(c.Desc))
		sb.WriteString("\n")
	}

	sb.WriteString(groupStyle.Render(m.locale.HelpShortcutsTitle))
	sb.WriteString("\n")
	for _, k := range m.locale.HelpKeys {
		sb.WriteString("  " + keyStyle.Render(k.Key) + " " + descStyle.Render(k.Desc))
		sb.WriteString("\n")
	}

	return panelStyle.Render(sb.String())
}

// renderMessage: render a single message as ANSI string (§1 Incremental rendering: self-contained method)
// toolDisplayInfo extracts display label, status icon, and style from tool progress entry.
func toolDisplayInfo(tool CLIToolProgress, okStyle, errStyle lipgloss.Style) (label, icon string, sty lipgloss.Style) {
	if tool.Label == "" {
		label = tool.Name
	} else {
		label = tool.Label
	}
	icon = "✓"
	sty = okStyle
	if tool.Status == "error" {
		icon = "✗"
		sty = errStyle
	}
	return
}

func (m *cliModel) renderMessage(msg *cliMessage) string {
	// §20 Use cached styles
	s := &m.styles
	var sb strings.Builder
	contentWidth := m.contentWidth() // Leave margin
	timeStyle := s.Time
	userLabelStyle := s.UserLabel
	assistantLabelStyle := s.AssistLabel
	streamingLabelStyle := s.StreamingLabel
	systemMsgStyle := s.SystemMsg
	errorMsgStyle := s.ErrorMsg

	// Render Markdown (assistant messages + system messages with markdown markers)
	var rendered string
	if msg.role == roleAssistant || (msg.role == roleSystem && msg.markdown) {
		// Pre-process: render mermaid code blocks to ASCII art
		// Truncate to glamour wrap width to prevent wrapping.
		preprocessed := msg.content
		if msg.role == roleAssistant {
			preprocessed = renderMermaidBlocks(msg.content, m.contentWidth())
		}
		var err error
		rendered, err = m.renderer.Render(preprocessed)
		if err != nil {
			rendered = msg.content
		}
		rendered = strings.TrimSpace(rendered)
	} else {
		rendered = msg.content
	}

	timeStr := timeStyle.Render(msg.timestamp.Format("15:04:05"))

	switch msg.role {
	case roleToolSummary:
		// §20 Use cached styles
		toolSummaryStyle := s.ToolSummary
		toolHeaderStyle := s.ToolHeader
		toolItemStyle := s.ToolItem
		toolErrorItemStyle := s.ToolErrorItem
		thinkingStyle := s.ProgressThinking
		reasoningStyle := s.TextMutedSt
		reasoningGuide := s.ProgressDim
		thinkingGuide := s.ProgressIndent
		hintStyle := s.ToolHint

		// Count total tools and total duration
		allTools, iterCount := msg.iterToolsFlat()
		totalTools := len(allTools)
		totalMs := int64(0)
		for _, it := range msg.iterations {
			totalMs += it.ElapsedWall
		}

		var toolSb strings.Builder

		if m.toolSummaryExpanded {
			// Expanded mode: full render
			if iterCount > 0 {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d iterations, %d calls)", iterCount, totalTools)))
				toolSb.WriteString("\n")
				// Box internal width: ToolSummary has Border(2) + Padding(0,1 → 2) = 4 cols overhead
				boxInnerW := contentWidth - 4
				guideW := lipgloss.Width(s.ProgressIndent.Render("  │ "))
				textW := boxInnerW - guideW
				for _, it := range msg.iterations {
					// Render #iter header with wall-clock time
					iterLabel := fmt.Sprintf("#%d", it.Iteration)
					if it.ElapsedWall > 0 {
						iterLabel += " " + reasoningStyle.Render(formatElapsed(it.ElapsedWall))
					}
					toolSb.WriteString(s.ProgressIter.Render(iterLabel))
					toolSb.WriteString("\n")
					renderWrappedBlock(&toolSb, it.Reasoning, textW, reasoningGuide, reasoningStyle)
					renderWrappedBlock(&toolSb, it.Thinking, textW, thinkingGuide, thinkingStyle)
					for _, tool := range it.Tools {
						label, icon, sty := toolDisplayInfo(tool, toolItemStyle, toolErrorItemStyle)
						elapsed := ""
						if tool.Elapsed > 0 {
							elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
						}
						toolSb.WriteString(sty.Render(fmt.Sprintf("    %s %s%s", icon, label, elapsed)))
						toolSb.WriteString("\n")
					}
				}
			} else {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d)", totalTools)))
				toolSb.WriteString("\n")
				for _, tool := range msg.tools {
					label, icon, sty := toolDisplayInfo(tool, toolItemStyle, toolErrorItemStyle)
					elapsed := ""
					if tool.Elapsed > 0 {
						elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
					}
					toolSb.WriteString(sty.Render(fmt.Sprintf("  %s %s%s", icon, label, elapsed)))
					toolSb.WriteString("\n")
				}
			}
		} else {
			// Fold mode upgrade (round 4): stats summary + success/failure status icons
			elapsedStr := formatElapsed(totalMs)
			// Count successful/failed tool count
			successCount, errorCount := 0, 0
			for _, tool := range allTools {
				if tool.Status == "error" {
					errorCount++
				} else {
					successCount++
				}
			}
			var statusIcons string
			if errorCount > 0 {
				statusIcons = s.ProgressError.Render("✗") +
					s.TextMutedSt.Render(strconv.Itoa(errorCount))
			}
			if successCount > 0 && errorCount > 0 {
				statusIcons += " "
			}
			if successCount > 0 {
				statusIcons += s.ProgressDone.Render("✓") +
					s.TextMutedSt.Render(strconv.Itoa(successCount))
			}
			toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools %d calls · %s", totalTools, elapsedStr)))
			if statusIcons != "" {
				toolSb.WriteString("  ")
				toolSb.WriteString(statusIcons)
			}
			toolSb.WriteString("  ")
			toolSb.WriteString(hintStyle.Render("[Ctrl+O]"))
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
	case roleSystem:
		if msg.markdown {
			// Markdown system messages (e.g. /usage tables): use glamour-rendered output directly
			sb.WriteString(rendered)
		} else if isErrorContent(msg.content) {
			sb.WriteString(errorMsgStyle.Render("⚠ " + msg.content))
		} else {
			sb.WriteString(systemMsgStyle.Render(msg.content))
		}
	case roleUser:
		// Above user messages: soft dot separator on right, symmetrical with assistant's left vertical line
		dotSep := s.UserDotSep.Width(contentWidth).Align(lipgloss.Right).Render("···")
		sb.WriteString(dotSep)
		sb.WriteString("\n")
		label := userLabelStyle.Render("You")
		header := s.UserHeader.Width(contentWidth).Align(lipgloss.Right).Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		// User messages: right-aligned bubble effect
		// Calculate content max line width, right-align entire block rather than stretching each line
		lines := strings.Split(rendered, "\n")
		maxWidth := 0
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
		}
		maxBubble := contentWidth * 3 / 4
		userStyle := s.UserContent
		if maxWidth <= maxBubble {
			// Content narrow enough, left-pad to right-align bubble
			userStyle = s.UserContent.PaddingLeft(contentWidth - maxWidth)
		}
		// Fallback to left-align when content is too wide, prevent terminal wrapping to leftmost
		sb.WriteString(userStyle.Render(rendered))
	default:
		// Assistant messages: left vertical line guide + label
		guide := s.AssistantGuide.Render("│")
		if msg.isPartial {
			guide = s.WarningSt.Render("│")
			label := streamingLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s ...", guide, timeStr, label)
		} else {
			label := assistantLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s", guide, timeStr, label)
		}
		sb.WriteString("\n")
		// §19 Long message folding: truncate preview for completed assistant messages
		if msg.folded && !msg.isPartial {
			origLines := msg.originalRenderedLines
			if origLines == 0 {
				origLines = msg.renderedLines
			}
			if origLines > msgFoldThresholdLines {
				renderedLines := strings.Split(rendered, "\n")
				if len(renderedLines) > msgFoldPreviewLines {
					rendered = strings.Join(renderedLines[:msgFoldPreviewLines], "\n")
					foldHint := m.styles.TextMutedSt.Render(
						fmt.Sprintf("  ... %s (%d lines) ...",
							m.locale.MsgCollapsed, origLines))
					rendered += "\n" + foldHint
				}
			}
		}
		// Agent messages rendered directly (glamour already handles markdown)
		// Trim trailing newlines so cursor appears inline at end of content
		trimmedRendered := strings.TrimRight(rendered, "\n")
		sb.WriteString(trimmedRendered)
		// Append blinking cursor during streaming output so user perceives "generating"
		if msg.isPartial && trimmedRendered != "" {
			cursorVisible := (m.ticker.ticks/5)%2 == 0
			if cursorVisible {
				sb.WriteString(s.StreamCursor.Render("▋"))
			}
		}
	}

	sb.WriteString("\n\n")

	// §19 Calculate rendered line count (recalculated on each dirty)
	msg.renderedLines = countLines(sb.String())

	return sb.String()
}

// trimAndWrapLines splits content by newlines, trims trailing whitespace from
// each line, and hard-wraps each line to the given width. Returns the list of
// wrapped display lines.
func trimAndWrapLines(content string, width int) []string {
	var result []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, " \t")
		result = append(result, strings.Split(hardWrapRunes(line, width), "\n")...)
	}
	return result
}

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.
// Lines wider than the viewport are truncated to prevent layout breakage.
func (m *cliModel) setViewportContent(content string) {
	// Deduplicate: skip if content and width haven't changed.
	// During resize storms or high-frequency ticks (busy state), this prevents
	// O(N*W) hardWrapRunes from running every 100ms on the same content.
	if content == m.lastViewportContent && m.width == m.lastViewportWidth && m.ready {
		return
	}
	m.lastViewportContent = content
	m.lastViewportWidth = m.width

	if m.width > 0 {
		// Strip trailing whitespace and hard-wrap — mermaid-ascii and wide tables
		// pad lines with spaces that inflate lipgloss.Width() far beyond
		// the actual visible content, causing premature wrapping.
		wrapped := trimAndWrapLines(content, m.width)
		content = strings.Join(wrapped, "\n")
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		m.newContentHint = true
	}
}

// wrappedLineCount returns the number of viewport display lines after hard-wrapping.
// The logic mirrors setViewportContent exactly so that msgLineOffsets (computed via
// this function) are always in sync with the viewport's internal line numbering.
func wrappedLineCount(content string, width int) int {
	if content == "" {
		return 0
	}
	if width <= 0 {
		return strings.Count(content, "\n")
	}
	return len(trimAndWrapLines(content, width))
}

// visibleTurnIndices Return the start slice index for each "conversation turn".
// Each turn starts with a user message, including all subsequent assistant/tool_summary messages
// Until the next user message. tool_summary automatically belongs to the turn of its nearest preceding user.
//
// e.g.: [user(0), assistant(1), tool_summary(2), user(3), assistant(4)]
// turns: [0, 3] — delete last 1 round by pressing "1", i.e. cutIdx=3, keep [user(0), assistant(1), tool_summary(2)]
func visibleTurnIndices(messages []cliMessage) []int {
	var turns []int
	for i, msg := range messages {
		if msg.role == roleUser {
			turns = append(turns, i)
		}
	}
	// If there are no user messages but other messages exist, fall back to old logic (for compatibility)
	if len(turns) == 0 && len(messages) > 0 {
		turns = append(turns, 0)
	}
	return turns
}

// visibleMsgGroupIndices is an alias for visibleTurnIndices, kept for backward compatibility.
func visibleMsgGroupIndices(messages []cliMessage) []int {
	return visibleTurnIndices(messages)
}

// updateViewportContent: update viewport display content (§1 Incremental rendering)
func (m *cliModel) updateViewportContent() {
	// Fast path: streaming message + cache valid
	if m.streamingMsgIdx >= 0 && m.renderCacheValid {
		m.updateStreamingOnly()
		return
	}

	// Fast path: cache valid + no streaming + message count unchanged, only refresh progress block (tick scenario)
	if m.renderCacheValid && m.streamingMsgIdx < 0 && m.cachedMsgCount == len(m.messages) {
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(m.renderProgressBlock())
		sb.WriteString(m.renderRewindResultBlock())
		m.setViewportContent(sb.String())
		return
	}

	// Fast path: cache valid + only new messages appended (no streaming, no search)
	// Only render new dirty messages and append to cachedHistory, skip full rebuild.
	if m.renderCacheValid && m.streamingMsgIdx < 0 && !m.searchMode &&
		len(m.messages) > m.cachedMsgCount {
		m.appendNewMessagesToCache()
		return
	}

	// Slow path: full rebuild
	m.fullRebuild()
}

// updateStreamingOnly Only re-render current streaming message (fast path)
func (m *cliModel) updateStreamingOnly() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// Only render current streaming message
	msg := &m.messages[m.streamingMsgIdx]
	msg.dirty = true
	sb.WriteString(m.renderMessage(msg))

	// Append progress block
	sb.WriteString(m.renderProgressBlock())

	// Append rewind result block
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// since cachedMsgCount, updating cachedHistory and msgLineOffsets without rebuilding
// old messages. This is O(new_messages) instead of O(all_messages).
func (m *cliModel) appendNewMessagesToCache() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// Calculate starting line offset for new messages
	runningLines := 0
	if len(m.msgLineOffsets) > 0 {
		// Approximate: use the line count of cachedHistory at current width.
		// This is an estimate but sufficient for msgLineOffsets (used for Ctrl+E folding).
		runningLines = wrappedLineCount(m.cachedHistory, m.width)
	}

	startIdx := m.cachedMsgCount
	for i := startIdx; i < len(m.messages); i++ {
		msg := &m.messages[i]
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		rendered := m.renderMessage(msg)
		msg.rendered = rendered
		msg.dirty = false
		msg.renderWidth = m.width
		sb.WriteString(rendered)
		runningLines += wrappedLineCount(rendered, m.width)
	}

	m.cachedHistory = sb.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// Set viewport with new content + progress block
	var vp strings.Builder
	vp.WriteString(m.cachedHistory)
	vp.WriteString(m.renderProgressBlock())
	vp.WriteString(m.renderRewindResultBlock())
	m.setViewportContent(vp.String())
}

// fullRebuild Full rebuild of render cache (slow path)
func (m *cliModel) fullRebuild() {
	var historyBuf strings.Builder

	// splitIdx ensures current streaming message doesn't enter cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 {
		splitIdx = m.streamingMsgIdx
	}

	// §19 Reset message line number offsets (based on viewport line numbers after wrapping)
	m.msgLineOffsets = m.msgLineOffsets[:0]
	runningLines := 0
	for i := range m.messages[:splitIdx] {
		// §19 Record start line number of message in viewport-wrapped content
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != m.width
		if needsRender {
			rendered := m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = m.width
		}
		// Build per-message chunk for line counting (avoids calling
		// historyBuf.String() on every iteration — the O(N²) full
		// buffer copy caused 100% CPU during resize with many messages).
		chunk := m.messages[i].rendered
		// §21 Search highlight: insert indicator bar before matching messages
		if m.searchMode && m.isSearchMatch(i) {
			indicator := m.styles.SearchIndicator.Render("▸ ")
			historyBuf.WriteString(indicator)
			chunk = indicator + chunk
		}
		historyBuf.WriteString(m.messages[i].rendered)
		// Accumulate line count occupied by this message (including search indicator) after wrapping
		runningLines += wrappedLineCount(chunk, m.width)
	}

	m.cachedHistory = historyBuf.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// Concatenate final content: history + current streaming message (if any) + progress block + rewind result
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderProgressBlock())
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// isSearchMatch Check if message matches current search (§21)
func (m *cliModel) isSearchMatch(idx int) bool {
	for _, si := range m.searchResults {
		if si == idx {
			return true
		}
	}
	return false
}

// toggleMessageFold Batch toggle fold state of all assistant messages (§19)
// If any long message is currently unfolded → fold all; otherwise → unfold all.
func (m *cliModel) toggleMessageFold() {
	if len(m.messages) == 0 {
		return
	}
	// Determine target state: if any unfolded long message exists, fold all
	anyUnfolded := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role == roleAssistant && !msg.isPartial && !msg.folded {
			lines := msg.originalRenderedLines
			if lines == 0 {
				lines = msg.renderedLines
			}
			if lines > msgFoldThresholdLines {
				anyUnfolded = true
				break
			}
		}
	}
	targetFold := anyUnfolded

	changed := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role != "assistant" || msg.isPartial {
			continue
		}
		if !targetFold {
			// Unfolding: skip threshold — renderedLines reflects folded preview,
			// not original length. Only unfold messages that are currently folded.
			if !msg.folded {
				continue
			}
			msg.folded = false
			msg.dirty = true
			changed = true
			continue
		}
		// Folding: check threshold using original line count
		lines := msg.originalRenderedLines
		if lines == 0 {
			lines = msg.renderedLines
		}
		if lines <= msgFoldThresholdLines {
			continue
		}
		if !msg.folded {
			msg.folded = true
			msg.originalRenderedLines = msg.renderedLines
			msg.dirty = true
			changed = true
		}
	}
	if changed {
		m.renderCacheValid = false
		m.updateViewportContent()
	}
}

// enterSearchMode Enter search mode (§21)
func (m *cliModel) enterSearchMode() {
	ti := textinput.New()
	ti.Placeholder = m.locale.SearchPlaceholder
	ti.Prompt = "/"
	ti.CharLimit = 100
	ti.Focus()
	w := m.width - 20
	if w < 20 {
		w = 20
	}
	ti.SetWidth(w)
	m.searchTI = ti
	m.searchMode = true
	m.searchEditing = true
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.renderCacheValid = false
	m.updateViewportContent()
}

// executeSearch Execute search (§21)
func (m *cliModel) executeSearch() {
	query := strings.TrimSpace(m.searchTI.Value())
	if query == "" {
		m.exitSearch()
		return
	}
	m.searchQuery = query
	lower := strings.ToLower(query)
	m.searchResults = nil
	for i, msg := range m.messages {
		if msg.role == roleSystem {
			continue
		}
		if strings.Contains(strings.ToLower(msg.content), lower) {
			m.searchResults = append(m.searchResults, i)
		}
	}
	m.searchIdx = -1
	m.searchEditing = false
	if len(m.searchResults) == 0 {
		m.showSystemMsg(m.locale.SearchNoResults, feedbackInfo)
	} else {
		m.showSystemMsg(fmt.Sprintf(m.locale.SearchResults, len(m.searchResults)), feedbackInfo)
		m.jumpToSearchResult(0)
	}
	m.renderCacheValid = false
	m.updateViewportContent()
}

// exitSearch Exit search mode (§21)
func (m *cliModel) exitSearch() {
	m.searchMode = false
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.searchEditing = false
	m.renderCacheValid = false
	m.updateViewportContent()
}

// jumpToSearchResult Jump to specified search result (§21)
func (m *cliModel) jumpToSearchResult(idx int) {
	if idx < 0 || idx >= len(m.searchResults) {
		return
	}
	m.searchIdx = idx
	msgIdx := m.searchResults[idx]
	if msgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[msgIdx])
	}
}

// // tickCmd returns a command that periodically refreshes viewport during agent processing.
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return cliTickMsg{}
	})
}

// typewriterTickCmd returns a command that advances the typewriter by 1 rune every 50ms.
// Runs independently from the main tick to give the typewriter its own update frequency.
func typewriterTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return typewriterTickMsg{}
	})
}

// idleTickCmd returns a low-frequency tick (3s) for placeholder rotation in idle state.
func idleTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return idleTickMsg{}
	})
}

// renderRewindResultBlock renders the rewind result summary after a /rewind operation.
// NOTE: This is a pure render function — it does NOT modify m.rewindResult.
// The result is cleared when a new agent turn starts (in startAgentTurn).
func (m *cliModel) renderRewindResultBlock() string {
	if m.rewindResult == nil {
		return ""
	}
	r := m.rewindResult

	var sb strings.Builder
	sb.WriteString("\n")
	sb.WriteString(m.styles.ProgressDone.Bold(true).Render("  Rewind complete"))
	sb.WriteString("\n")

	if len(r.Restored) > 0 {
		fmt.Fprintf(&sb, "  Files restored: %d\n", len(r.Restored))
		for _, f := range r.Restored {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.CreatedDel) > 0 {
		fmt.Fprintf(&sb, "  Files deleted: %d\n", len(r.CreatedDel))
		for _, f := range r.CreatedDel {
			sb.WriteString(m.styles.TextMutedSt.Render(fmt.Sprintf("    %s\n", f)))
		}
	}
	if len(r.Errors) > 0 {
		for _, e := range r.Errors {
			sb.WriteString(m.styles.ProgressError.Render(fmt.Sprintf("  Error: %s\n", e)))
		}
	}

	return sb.String()
}

// tickerCmd is deprecated — ticker is now driven by cliTickMsg.
// Kept for reference only.
// func tickerCmd() tea.Cmd {
// 	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
// 		return tickerTickMsg{}
// 	})
// }

// historyMessageToCLI converts a HistoryMessage (DB/persistence layer) to a
// cliMessage (TUI layer). This is the single conversion point — all callers
// (cli.go, handleSuHistoryLoad, handleHistoryReload) use this function.
func historyMessageToCLI(hm HistoryMessage) cliMessage {
	cm := cliMessage{
		role:      hm.Role,
		content:   hm.Content,
		timestamp: hm.Timestamp,
		isPartial: false,
		dirty:     true,
	}
	if len(hm.Iterations) > 0 {
		cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
		for i, hi := range hm.Iterations {
			cm.iterations[i] = cliIterationSnapshot(hi)
		}
	}
	return cm
}
