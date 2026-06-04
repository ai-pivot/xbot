package channel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
	"xbot/version"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/google/uuid"
	"github.com/rivo/uniseg"
)

// ---------------------------------------------------------------------------
// Package-level compiled regexps (compiled once, not per-call)
// ---------------------------------------------------------------------------

var (
	readLineNumRe = regexp.MustCompile(`^\s*(\d+)\t(.*)$`)
	diffHunkRe    = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)
)

// ---------------------------------------------------------------------------
// Helper Methods
// ---------------------------------------------------------------------------

// handleTabComplete 处理 Tab 补全（§8：/ 命令补全，§8b：@ 文件路径补全）
func (m *cliModel) handleTabComplete() {
	input := m.textarea.Value()

	// 检测 @ 文件引用补全（从输入末尾检测）
	atOk, atPrefix := detectAtPrefix(input)
	if atOk {
		m.handleFileTabComplete(input, atPrefix)
		return
	}

	// / 命令补全
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

// detectAtPrefix 检测输入文本末尾是否有 @ 触发文件补全。
// ok=true 表示检测到 @（即使后面无字符也应触发 glob）。
// prefix 是 @ 之后到文本末尾的部分。
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

// populateFileCompletions 根据 prefix 执行 glob 搜索并填充 fileCompletions
func (m *cliModel) populateFileCompletions(prefix string) {
	pattern := prefix
	if !strings.Contains(pattern, "*") {
		if strings.HasSuffix(pattern, "/") {
			pattern += "*"
		} else {
			pattern += "*"
		}
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		m.fileCompletions = nil
		m.fileCompIdx = 0
		return
	}
	// 过滤隐藏文件（以 . 开头）
	matches = slices.DeleteFunc(matches, func(f string) bool {
		base := filepath.Base(f)
		return len(base) > 0 && base[0] == '.'
	})
	sort.Slice(matches, func(i, j int) bool {
		di, dj := isDir(matches[i]), isDir(matches[j])
		if di != dj {
			return di
		}
		return matches[i] < matches[j]
	})
	if len(matches) > 20 {
		matches = matches[:20]
	}
	m.fileCompletions = matches
	m.fileCompIdx = 0
}

// handleFileTabComplete 处理 @ 文件路径 Tab 补全
func (m *cliModel) handleFileTabComplete(input string, prefix string) {
	if !m.fileCompActive || len(m.fileCompletions) == 0 {
		// 首次 Tab 或候选被清空：glob 并进入循环模式
		m.populateFileCompletions(prefix)
		if len(m.fileCompletions) == 0 {
			return
		}
		m.fileCompActive = true
	} else {
		// 循环模式：切换到下一个候选
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

// newInbound creates an InboundMsg with common fields pre-filled.
// metadata can be nil.
func (m *cliModel) newInbound(content string, metadata map[string]string) InboundMsg {
	return InboundMsg{
		Channel:    m.channelName,
		SenderID:   m.senderID,
		ChatID:     m.chatID,
		ChatType:   "p2p",
		Content:    content,
		SenderName: "CLI User",
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		Metadata:   metadata,
	}
}

// appendSystem adds a system message to the message history and marks it as dirty.
func (m *cliModel) appendSystem(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
}

// appendSystemMarkdown adds a system message that will be rendered through
// the glamour markdown renderer (for tables, headers, etc.).
func (m *cliModel) appendSystemMarkdown(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
		markdown:  true,
	})
}

// appendSystemStyled adds a pre-styled system message (content already contains ANSI codes).
// The message bypasses both glamour rendering and systemMsgStyle wrapping.
func (m *cliModel) appendSystemStyled(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
		styled:    true,
	})
}

// sendInbound sends a message to the agent's inbound channel.
// Uses non-blocking send to prevent the BubbleTea event loop from freezing
// if the channel is full (e.g., agent is busy with a long LLM call).
// Returns false if the message was dropped.
func (m *cliModel) sendInbound(msg InboundMsg) bool {
	if m.sendInboundFn != nil {
		return m.sendInboundFn(msg)
	}
	return false
}

// sendInboundWait sends a message to the agent's inbound channel with a timeout.
// Use for critical messages (ask_user answers) that MUST be delivered.
// Returns false if the message couldn't be sent within the deadline.
func (m *cliModel) sendInboundWait(msg InboundMsg, timeout time.Duration) bool {
	if m.sendInboundFn != nil {
		return m.sendInboundFn(msg)
	}
	return false
}

// sendCancel sends a cancel request to the agent and adds a system notification.
func (m *cliModel) sendCancel() {
	if !m.sendInbound(m.newInbound("/cancel", nil)) {
		m.showSystemMsg("Cancel failed: agent channel busy, try again", feedbackError)
		return
	}
	m.showSystemMsg(m.locale.CancelSent, feedbackInfo)
}

// sendToAgent 发送命令到 agent，并添加用户消息到历史（§3 命令透传机制）
func (m *cliModel) sendToAgent(content string) {
	// Check if LLM is configured before sending (same check as sendMessage).
	if m.cachedModelName == "" && m.hasNoSubscription() {
		m.showSystemMsg(m.locale.SetupNoLLM, feedbackWarning)
		return
	}
	userCliMsg := cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	}
	m.messages = append(m.messages, userCliMsg)
	m.pendingUserMsg = &userCliMsg
	m.savePendingToSessionState()
	m.sendInbound(m.newInbound(content, map[string]string{MetadataReplyPolicy: ReplyPolicyOptional}))
	m.startAgentTurn()
}

// sendMessage 发送用户消息，返回可能需要执行的 tea.Cmd（如彩蛋动画 tick）。
func (m *cliModel) sendMessage(content string) tea.Cmd {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "/") {
		return m.handleSlashCommand(content)
	}

	// Check if LLM is configured before sending.
	// When there's no API key and model name is empty, the LLM call will fail anyway.
	// Show a friendly setup prompt instead of a cryptic error.
	// Skip this check when sendInboundFn is set (local mode or tests) since
	// the agent handles missing config gracefully in those cases.
	if m.sendInboundFn == nil && m.cachedModelName == "" && m.hasNoSubscription() {
		m.showSystemMsg(m.locale.SetupNoLLM, feedbackWarning)
		return nil
	}

	// 🥚 彩蛋 #3: The Answer is 42 检测
	if isAnswer42(content) {
		_ = m.activateEasterEgg(easterEggAnswer42)
	}

	// 解析 @ 文件引用，提取文件路径
	media := parseFileReferences(content)

	// 添加用户消息到历史
	userCliMsg := cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	}
	m.messages = append(m.messages, userCliMsg)

	// User explicitly sent a message — cancel any pending suLoading.
	// Background history loads are stale once the user initiates a new turn.
	// If we don't clear suLoading, handleProgressMsg drops ALL progress events
	// (line 391) and the session never enters typing state.
	m.suLoading = false
	m.suPhaseDoneConfirmed = false

	// Save as pending user message so it survives session switches before
	// the agent's eager-save to DB completes. Restored in handleSuHistoryLoad.
	m.pendingUserMsg = &userCliMsg
	m.savePendingToSessionState()

	// 更新显示并强制滚动到底部（用户发送新消息时始终可见）
	m.updateViewportContent()
	m.viewport.GotoBottom()
	m.newContentHint = false
	m.userScrolledUp = false

	// 发送到消息总线
	msg := m.newInbound(content, nil) // ReplyPolicyAuto (default)
	msg.Media = media
	m.sendInbound(msg)
	m.startAgentTurn()
	return nil
}

// parseFileReferences 从用户消息中提取 @path 文件引用。
// 匹配 @ 后跟非空格字符的路径，验证文件存在后返回。
func parseFileReferences(content string) []string {
	var files []string
	seen := make(map[string]bool)
	for i := 0; i < len(content); i++ {
		if content[i] == '@' {
			// @ 必须在词首
			if i > 0 && content[i-1] != ' ' {
				continue
			}
			// 提取 @ 后的路径
			j := i + 1
			for j < len(content) && content[j] != ' ' {
				j++
			}
			path := content[i+1 : j]
			// 去掉末尾的 /
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

// mergeMessagesPreservingCache replaces m.messages with newMessages while
// preserving the rendered cache from existing messages. This avoids O(N)
// glamour re-rendering when a history reload returns the same messages
// (e.g. after context compression replaced only the oldest messages).
//
// Returns true if ALL messages were matched (no truly new/dirty messages).
// When true, the caller can skip fullRebuild entirely.
func (m *cliModel) mergeMessagesPreservingCache(newMessages []cliMessage) bool {
	cw := m.chatWidth()
	// Build a fast lookup from existing messages: content-based key → index.
	// Only use the first occurrence of each key to handle dedup.
	existing := make(map[string]int, len(m.messages))
	for i := range m.messages {
		key := m.messages[i].role + ":" + m.messages[i].content
		if _, exists := existing[key]; !exists {
			existing[key] = i
		}
	}

	allMatched := true
	for i := range newMessages {
		key := newMessages[i].role + ":" + newMessages[i].content
		if oldIdx, found := existing[key]; found {
			old := &m.messages[oldIdx]
			nw := &newMessages[i]
			// Inherit cache if the old message was rendered at the same width
			if old.rendered != "" && old.renderWidth == cw && !old.dirty {
				nw.rendered = old.rendered
				nw.renderWidth = old.renderWidth
				nw.dirty = false
				nw.renderedLines = old.renderedLines
				nw.wrappedLines = old.wrappedLines
				nw.wrappedMaxWidth = old.wrappedMaxWidth
				nw.wrappedWidth = old.wrappedWidth
				if len(old.iterations) > 0 && len(nw.iterations) == 0 {
					nw.iterations = old.iterations
				}
				// Compute renderedLines from cached rendered output if missing
				if nw.renderedLines == 0 && nw.rendered != "" {
					nw.renderedLines = strings.Count(nw.rendered, "\n") + 1
				}
			} else {
				allMatched = false
			}
			// Remove from lookup to avoid double-matching
			delete(existing, key)
		} else {
			allMatched = false
		}
	}
	m.messages = newMessages
	return allMatched
}

// invalidateProgressHistoryCache clears the cached rendered output of completed
// iterations so it is rebuilt on the next renderProgressBlock call.
func (m *cliModel) invalidateProgressHistoryCache() {
	m.cachedProgressHistory = ""
	m.cachedProgressHistoryLines = nil
	m.cachedProgressHistoryLen = 0
	m.cachedProgressHistoryWidth = 0
	m.cachedCurrentStatic = ""
	m.cachedCurrentStaticFP = 0
	m.cachedCurrentStaticWidth = 0
	m.cachedCurrentIter = 0
	// Invalidate reasoning/stream/thinking block caches
	m.cachedReasoningBlock = ""
	m.cachedReasoningBlockFP = 0
	m.cachedReasoningBlockWidth = 0
	m.cachedStreamBlock = ""
	m.cachedStreamBlockFP = 0
	m.cachedStreamBlockWidth = 0
	m.cachedThinkingBlock = ""
	m.cachedThinkingBlockFP = 0
	m.cachedThinkingBlockWidth = 0
	// Full progress block output cache
	m.cachedProgressBlockOut = ""
	m.cachedProgressBlockFP = 0
	m.cachedProgressBlockWidth = 0
	m.cachedProgressBlockLines = nil
	m.cachedProgressHistoryFP = 0
}

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.lastProgressSeq = 0
	m.lastReasoning = ""
	m.reasoningByIter = nil
	m.progress = nil
	m.iterationStartTime = time.Now() // wall-clock start for iteration 0
	m.typingStartTime = time.Now()
	m.invalidateProgressHistoryCache()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []protocol.ToolProgress {
	var all []protocol.ToolProgress
	for _, snap := range m.iterationHistory {
		all = append(all, snap.Tools...)
	}
	return all
}

// handleSlashCommand 处理斜杠命令
func (m *cliModel) handleSlashCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)
	// 提取命令部分（去掉参数）
	parts := strings.Fields(cmd)
	command := ""
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}

	// 🥚 彩蛋命令优先检测（隐藏命令不注册到 cliCommands）
	if handled, cmd := m.handleEasterEggCommand(cmd); handled {
		return cmd
	}

	switch command {
	// --- 本地命令 ---
	case "/cancel":
		m.sendCancel()

	case "/commands", "/palette":
		m.openCommandPalette()

	case "/clear":
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.cachedHistory = ""
		m.exitSearch()

	case "/rewind":
		m.openRewindPanel()

	case "/settings":
		// Open interactive settings panel locally
		if m.channel != nil {
			schema := m.channel.SettingsSchema()
			if len(schema) == 0 {
				m.showSystemMsg(m.locale.NoSettings, feedbackWarning)
			} else {
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
				m.panelIsSetup = false // regular settings, not setup wizard
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
		}

	case "/setup":
		m.openSetupPanel()

	case "/update":
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

	case "/quit", "/exit":
		m.shouldQuit = true

	case "/help":
		helpContent := m.renderHelpPanel()
		m.showSystemMsg(helpContent, feedbackInfo)

	case "/search":
		m.enterSearchMode()

	case "/compress":
		// 保留本地处理（system 消息样式），发送但不作为用户气泡
		m.sendInbound(m.newInbound("/compress", nil))
		m.startAgentTurn()

	// --- 透传命令（发送到 agent） ---
	case "/context":
		m.sendToAgent(cmd) // 直接透传，agent 层会解析

	case "/new":
		m.lastTokenUsage = nil       // clear context bar immediately
		m.cachedMaxContextTokens = 0 // reset context budget — solid line until next progress
		m.sendToAgent("/new")

	case "/tasks":
		// /tasks — open unified tasks & agents panel
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

	case "/su":
		// /su — Switch user identity:
		//   /su          — 切回默认身份
		//   /su <userID> — 切换到指定用户身份
		//   /su web:<senderID>[:<token>] — 切换到 Web 端用户
		m.saveCurrentSession()
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
		// Reset critical state after identity switch, then apply unified setup.
		m.messages = nil
		m.invalidateAllCache(false)
		m.restoreSession()
		cmds := m.postRestoreSessionSetup()
		return tea.Batch(cmds...)

	case "/ss", "/sessions":
		// /ss — Open Sessions panel
		m.openSessionsPanel()

	case "/chat":
		// /chat — Chat room management:
		//   /chat new [label] — 创建新会话
		//   /chat <id>        — 切换到指定会话
		//   /chat ls          — 列出所有会话（文字版）
		if len(parts) < 2 {
			m.showSystemMsg("用法: /chat new [label] | /chat <id> | /chat ls", feedbackInfo)
			return nil
		}
		arg := strings.TrimSpace(parts[1])
		switch arg {
		case "new":
			if m.channel != nil && m.channel.config.ChatCreateFn != nil {
				m.saveCurrentSession()
				label := ""
				if len(parts) > 2 {
					label = strings.Join(parts[2:], " ")
				}
				chatID, err := m.channel.config.ChatCreateFn(m.channelName, m.defaultChatID, label)
				if err != nil {
					m.showSystemMsg("创建失败: "+err.Error(), feedbackInfo)
					return nil
				}
				// Pre-creation cleanup: nuke ALL residual state for this chatID.
				newSessionKey := "cli:" + chatID
				tools.GlobalWorktreeRegistry.CleanupSession(newSessionKey)
				session.DeletePersistedCWD("cli", chatID)
				delete(m.savedSessions, newSessionKey)
				if m.todoManager != nil {
					m.todoManager.SetTodos(newSessionKey, nil)
					_ = m.todoManager.SaveToFile(newSessionKey)
				}
				m.chatID = chatID
				SetLastActiveSession(m.defaultChatID, chatID)
				// Reset critical state for new session, then apply unified setup.
				m.messages = nil
				m.invalidateAllCache(false)
				m.restoreSession()
				cmds := m.postRestoreSessionSetup()
				m.showSystemMsg(fmt.Sprintf("✅ 新会话已创建: %s", chatID), feedbackInfo)
				return tea.Batch(cmds...)
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
			m.saveCurrentSession()
			m.chatID = arg
			SetLastActiveSession(m.defaultChatID, arg)
			// Reset critical state after session switch, then apply unified setup.
			m.messages = nil
			m.invalidateAllCache(false)
			m.restoreSession()
			cmds := m.postRestoreSessionSetup()
			m.showSystemMsg(fmt.Sprintf("✅ 已切换到会话: %s", arg), feedbackInfo)
			return tea.Batch(cmds...)
		}

	case "/usage":
		m.sendToAgent(cmd) // passthrough to agent-level /usage handler

	case "/channel":
		m.openChannelPanel()

	case "/user":
		userArg := ""
		if len(parts) > 1 {
			userArg = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
		m.handleUserCommand(userArg)

	case "/plugin":
		return m.handlePluginCommand(parts)

	default:
		// /debug subcommands for runtime diagnostics
		if strings.HasPrefix(command, "/debug") {
			m.handleDebugCommand(cmd) // pass full input including subcommand
			return nil
		}
		// 🥚 彩蛋 #7: /version 三连检测
		if command == "/version" {
			if m.recordVersionHit() {
				art := fmt.Sprintf(versionAchievementArt, version.Version)
				_ = m.activateEasterEgg(easterEggVersion)
				m.easterEggCustom = art
				m.updateViewportContent()
				return nil
			}
		}
		// 未知命令尝试透传到 agent（agent 层可能认识）
		m.sendToAgent(cmd)
	}

	m.updateViewportContent()
	return nil
}

// handleAgentMessage 处理 agent 回复
func (m *cliModel) handleAgentMessage(msg OutboundMsg) {
	// Persist pending AskUser questions BEFORE session filter, so they survive
	// session switches and restarts. Only persist if metadata has ask_questions.
	if msg.WaitingUser && msg.Metadata != nil && msg.Metadata["ask_questions"] != "" && msg.ChatID != "" {
		m.savePendingAskUser(msg.ChatID, msg.Metadata)
	}

	// Deduplication in handleSuHistoryLoad prevents message duplication,
	// so we accept all messages immediately even during suLoading.
	// This ensures progress/app responses are visible without delay
	// when the user sends a message right after session switch.
	// Filter by session: only process outbound for the currently viewed session.
	if msg.Channel != "" && msg.ChatID != "" {
		if msg.Channel != m.channelName || msg.ChatID != m.chatID {
			log.WithFields(log.Fields{
				"msg_channel":    msg.Channel,
				"msg_chatid":     msg.ChatID,
				"my_channelName": m.channelName,
				"my_chatid":      m.chatID,
				"waiting_user":   msg.WaitingUser,
			}).Warn("handleAgentMessage: session filter rejected outbound message")
			return
		}
	} else {
		log.WithFields(log.Fields{
			"msg_channel":    msg.Channel,
			"msg_chatid":     msg.ChatID,
			"my_channelName": m.channelName,
			"my_chatid":      m.chatID,
			"waiting_user":   msg.WaitingUser,
			"content_len":    len(msg.Content),
		}).Warn("handleAgentMessage: ChatID empty — filter bypassed, applying to current session")
	}

	turnID := m.agentTurnID // capture at entry for stale-signal guard
	content := msg.Content

	// Cancel ack handling: when a Run is cancelled, the agent sends outbound
	// messages with metadata cancelled=true. These belong to the cancelled turn,
	// not the current turn. If a new turn has already started (bg notification
	// injection arrived first via cliInjectedUserMsg), these cancel acks would
	// match the new turn's ID and incorrectly endAgentTurn. Skip turn-ending
	// logic for cancel acks to preserve the new turn's state.
	isCancelledAck := msg.Metadata != nil && msg.Metadata["cancelled"] == "true"
	if isCancelledAck {
		// Still clean up progress/streaming state for the cancelled turn.
		// Do NOT endAgentTurn — the current turn (if any) must remain active.
		if m.progress != nil {
			m.cacheTokenUsage(m.progress.TokenUsage)
		}
		m.streamingMsgIdx = -1
		m.progress = nil
		m.typing = false // clear typing indicator immediately after cancel
		m.renderCacheValid = false
		m.updateViewportContent()
		return
	}

	// 处理 __FEISHU_CARD__ 协议（简化显示）
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ConvertFeishuCard(content)
	}

	// Empty content with no waiting user: end turn and flush queue,
	// but don't append a blank message.
	// Guard: when AskUser panel is open, the turn is paused (not ended).
	// A late-arriving empty-content outbound (e.g. from engine cleanup) must
	// not trigger endAgentTurn, which clears iterationHistory and makes all
	// previous iterations disappear from the viewport.
	if content == "" && !msg.WaitingUser && len(msg.ToolsUsed) == 0 && m.panelMode != "askuser" {
		// Persist token usage before clearing progress
		if m.progress != nil {
			m.cacheTokenUsage(m.progress.TokenUsage)
		}
		m.streamingMsgIdx = -1
		m.progress = nil
		m.setTurnReplyReceived(turnID)
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
		// 流式输出：追加到当前消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 追加到现有流式消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// 创建新的流式消息
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
				turnID:    turnID,
			})
		}
	} else {
		// 完整消息 — save the message index for later thinking capture
		var completedMsgIdx int
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 更新流式消息为完整消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
			m.messages[m.streamingMsgIdx].turnID = turnID
			completedMsgIdx = m.streamingMsgIdx
		} else {
			// 新增完整的 assistant 消息 — use upsert to prevent duplicates
			assistantMsg := cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: false,
				dirty:     true,
				turnID:    turnID,
			}
			completedMsgIdx = m.upsertMessageByTurn(turnID, "assistant", assistantMsg)
		}
		// 重置流式状态
		m.streamingMsgIdx = -1
		// Capture reasoning from progress before it might be cleared.
		// Do NOT clear m.progress here — progress is only cleared by endAgentTurn.
		// Intermediate text messages (e.g. thinking content) arrive while the agent
		// is still running; clearing progress here would hide the progress panel
		// and make it look like the turn ended prematurely.
		// IMPORTANT: Do NOT fallback to m.progress.ReasoningStreamContent.
		// ReasoningStreamContent is a streaming accumulator with no per-iteration
		// boundary. When handleAgentMessage arrives after the next structured
		// progress has advanced m.progress.Iteration, ReasoningStreamContent may
		// still contain the previous iteration's content — causing the previous
		// iteration's reasoning to be misattributed to m.reasoningByIter[newIter].
		if turnID == m.agentTurnID && m.progress != nil {
			reasoning := m.progress.Reasoning
			if reasoning != "" {
				m.lastReasoning = reasoning
				if m.reasoningByIter == nil {
					m.reasoningByIter = make(map[int]string)
				}
				iter := m.progress.Iteration
				if iter >= 0 {
					m.reasoningByIter[iter] = reasoning
				}
			}
			if m.progress.Thinking != "" {
				m.lastThinking = m.progress.Thinking
			}
		}
		// Store captured thinking on the completed message for Thinking Box rendering.
		if completedMsgIdx >= 0 && completedMsgIdx < len(m.messages) {
			thinking := m.lastReasoning
			if thinking == "" {
				thinking = m.lastThinking
			}
			if thinking != "" {
				m.messages[completedMsgIdx].thinking = thinking
			}
		}
		m.renderCacheValid = false
		m.updateViewportContent()

		// §11.5 Session reset: clear messages and token usage bar after /new
		if msg.Metadata != nil && msg.Metadata["session_reset"] == "true" {
			m.lastTokenUsage = nil
			m.cachedMaxContextTokens = 0 // reset context budget — solid line until next progress
			m.messages = make([]cliMessage, 0, cliMsgBufSize)
			m.streamingMsgIdx = -1
			m.cachedHistory = ""
			m.cachedWrappedHistory = ""
			m.cachedWrappedHistoryRaw = ""
			m.cachedWrappedHistoryWidth = 0
			m.cachedHistoryMaxWidth = 0
			m.cachedHistoryLines = nil
			// PhaseDone from emitBuiltinProgressDone should arrive before this outbound,
			// so endAgentTurn is usually a no-op (turn already ended). Kept as safety net.
			m.endAgentTurn(m.agentTurnID)
			m.invalidateAllCache(true)
			m.viewport.GotoBottom()
		}

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
				m.askUserSession = m.chatID // bind AskUser to current session
				m.openAskUserPanel(items, func(answers map[string]string) {
					// Clean up persisted pending question now that user answered.
					m.deletePendingAskUser(m.askUserSession)
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
					if !m.sendInboundWait(m.newInbound(content, map[string]string{"ask_user_answered": "true"}), 5*time.Second) {
						m.showSystemMsg("Failed to deliver answer to agent, please try again", feedbackError)
					}
					// Render as tool call style (not user message)
					m.messages = append(m.messages, cliMessage{
						role:       "tool_summary",
						content:    "AskUser",
						timestamp:  time.Now(),
						dirty:      true,
						iterations: nil,
						tools: []protocol.ToolProgress{
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
					// Persist pre-AskUser iteration history before startAgentTurn clears it.
					// Without this, iterations 1..N from the first run disappear when
					// resetProgressState sets m.iterationHistory = nil.
					if len(m.iterationHistory) > 0 {
						m.upsertMessageByTurn(turnID, "tool_summary", cliMessage{
							role:       "tool_summary",
							content:    "",
							timestamp:  time.Now(),
							iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
							dirty:      true,
							turnID:     turnID,
						})
					}
					m.startAgentTurn()
					m.updateViewportContent()
				}, func() {
					// Clean up persisted pending question on cancel.
					m.deletePendingAskUser(m.askUserSession)
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
				var finalTools []protocol.ToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.lastSeenIteration {
						finalTools = append(finalTools, t)
					}
				}
				reasoning := m.lastReasoning
				if reasoning == "" && m.reasoningByIter != nil {
					reasoning = m.reasoningByIter[m.lastSeenIteration]
				}
				snap := cliIterationSnapshot{
					Iteration:   m.lastSeenIteration,
					Reasoning:   reasoning,
					Thinking:    m.lastThinking,
					Tools:       finalTools,
					ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
				}
				if len(finalTools) > 0 || reasoning != "" || m.lastThinking != "" {
					m.iterationHistory = append(m.iterationHistory, snap)
				}
			}
		}

		// Tool summary handling with deterministic rendering.
		// If handleProgressDone already processed this turn (doneProcessed=true),
		// the tool_summary is already in m.messages — skip to avoid duplicates.
		// If not, we need to create/update it from local iteration history.
		if !m.isTurnDoneProcessed(turnID) && len(m.iterationHistory) > 0 {
			// PhaseDone hasn't run yet (or arrived after the reply).
			// Create tool_summary from local iteration history.
			toolSummaryMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
				dirty:      true,
			}
			// Insert before the assistant message using upsert.
			// If a tool_summary for this turn already exists (shouldn't happen
			// since doneProcessed is false, but defensive), update in-place.
			tsIdx := m.upsertMessageByTurn(turnID, "tool_summary", toolSummaryMsg)

			// Ensure tool_summary is positioned BEFORE the assistant message.
			// The assistant was just added/updated above; find its index.
			asstIdx := m.findMessageByTurn(turnID, "assistant")
			if asstIdx >= 0 && tsIdx > asstIdx {
				// Swap positions: move tool_summary before assistant
				toolSummary := m.messages[tsIdx]
				m.messages = append(m.messages[:tsIdx], m.messages[tsIdx+1:]...)
				// Recalculate asstIdx after removal (shifted by 1 if asstIdx > tsIdx)
				asstIdx = m.findMessageByTurn(turnID, "assistant")
				if asstIdx >= 0 {
					m.messages = append(m.messages[:asstIdx], append([]cliMessage{toolSummary}, m.messages[asstIdx:]...)...)
				} else {
					m.messages = append(m.messages, toolSummary)
				}
			}
			m.renderCacheValid = false
		} else if m.isTurnDoneProcessed(turnID) {
			// PhaseDone already created the tool_summary. If we have richer
			// local iteration data (e.g. more complete reasoning), update it.
			if len(m.iterationHistory) > 0 {
				tsIdx := m.findMessageByTurn(turnID, "tool_summary")
				if tsIdx >= 0 {
					// Merge: prefer PhaseDone iterations (server-authoritative reasoning)
					// but add any local-only iterations not in PhaseDone's snapshot.
					existing := m.messages[tsIdx]
					pendingIters := make(map[int]bool)
					for _, it := range existing.iterations {
						pendingIters[it.Iteration] = true
					}
					for _, it := range m.iterationHistory {
						if !pendingIters[it.Iteration] {
							existing.iterations = append(existing.iterations, it)
						}
					}
					existing.dirty = true
					m.messages[tsIdx] = existing
					m.renderCacheValid = false
				}
			}
		}

		// Mark reply as received and reset iteration tracking state.
		// When WaitingUser is true (AskUser), the turn is paused not ended —
		// endAgentTurn would clear iterationHistory and progress, causing all
		// previous iterations to disappear. The turn will be ended later when
		// the agent completes after receiving the user's answer.
		if !msg.WaitingUser {
			m.setTurnReplyReceived(turnID)
			m.endAgentTurn(turnID)
			if turnID == m.agentTurnID {
				m.inputReady = true
				// §Q 标记需要刷新消息队列（由 Update 循环检查）
				if len(m.messageQueue) > 0 {
					m.needFlushQueue = true
				}
			}
		}

	}

	m.updateViewportContent()
}

// renderProgressBlock renders the iteration progress panel for the viewport.
// The output is cached via fingerprint — the expensive blockStyle.Render()
// (lipgloss border wrapping with ANSI width calculation on every character)
// renderHistoryRange renders a slice of iteration snapshots into buf.
// Extracted from renderProgressBlock to enable incremental rebuild — when only
// the last iteration is new, we render just that one instead of all N.
func (m *cliModel) renderHistoryRange(
	buf *strings.Builder,
	snaps []cliIterationSnapshot,
	innerWidth, reasoningW, thinkingW int,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle lipgloss.Style,
	s *cliStyles,
) {
	// Pre-render guide prefixes once (they're the same for all lines).
	// Without this, each iteration re-renders "  ┊ " with Style.Render,
	// which internally walks the string with ANSI parser — O(chars) per call.
	reasoningGuidePrefix := reasoningGuide.Render("  ┊ ")
	thinkingGuidePrefix := thinkingGuide.Render("  ┊ ")

	for j := range snaps {
		snap := &snaps[j]
		buf.WriteString(dimStyle.Render(iterStyle.Render(fmt.Sprintf("#%d", snap.Iteration))))
		buf.WriteString("\n")
		if snap.Reasoning != "" {
			for _, line := range strings.Split(snap.Reasoning, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n") {
					buf.WriteString(dimStyle.Render(reasoningGuidePrefix + reasoningStyle.Render(wl)))
					buf.WriteString("\n")
				}
			}
		}
		if snap.Thinking != "" {
			for _, line := range strings.Split(snap.Thinking, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					buf.WriteString(dimStyle.Render(thinkingGuidePrefix + thinkingStyle.Render(wl)))
					buf.WriteString("\n")
				}
			}
		}
		for k := range snap.Tools {
			tool := &snap.Tools[k]
			label, icon, sty := toolDisplayInfo(*tool, toolDoneStyle, toolErrorStyle)
			var elapsedStyled string
			if tool.Elapsed > 0 {
				elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			buf.WriteString(dimStyle.Render(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth))))
			buf.WriteString("\n")
			if content := m.renderToolContentBelow(tool, reasoningGuidePrefix, innerWidth, true, 0); content != "" {
				buf.WriteString(content)
				buf.WriteString("\n")
			}
		}
	}
}

// renderProgressBlock renders the full progress panel showing completed
// iterations (dimmed) and the current iteration. An internal cache ensures it
// only runs when the content actually changes. Elapsed is quantized to whole
// seconds so the cache stays stable across 100ms ticks.
func (m *cliModel) renderProgressBlock() string {
	if !m.typing && m.progress == nil {
		m.cachedProgressBlockOut = ""
		m.cachedProgressBlockFP = 0
		m.cachedProgressBlockLines = nil
		return ""
	}
	// Cross-session guard: if progress payload carries a ChatID that doesn't match
	// the currently viewed session, it's stale — discard it entirely. This prevents
	// phantom progress blocks when switching sessions while another session's agent
	// is still processing.
	if m.progress != nil && m.progress.ChatID != "" {
		currentKey := qualifyChatID(m.channelName, m.chatID)
		if m.progress.ChatID != currentKey {
			m.progress = nil
			m.typing = false
			m.cachedProgressBlockOut = ""
			m.cachedProgressBlockFP = 0
			m.cachedProgressBlockLines = nil
			return ""
		}
	}

	bubbleWidth := m.chatWidth() - 4
	if bubbleWidth < 10 {
		bubbleWidth = 10
	}
	innerWidth := bubbleWidth - 2 // padding(2)
	if innerWidth < 1 {
		innerWidth = 1
	}

	// §20 使用缓存样式
	s := &m.styles
	iterStyle := s.ProgressIter
	thinkingStyle := s.ProgressThinking
	reasoningStyle := s.TextMutedSt // dimmed style for reasoning chain
	toolDoneStyle := s.ProgressDone
	toolRunningStyle := s.ProgressRunning
	toolErrorStyle := s.ProgressError
	elapsedStyle := s.ProgressElapsed
	indentGuide := s.ProgressIndent
	reasoningGuide := s.ProgressDim // dimmer ┊ for reasoning
	thinkingGuide := indentGuide    // normal ┊ for thinking
	reasoningW := lipgloss.Width(reasoningGuide.Render("  ┊ "))
	thinkingW := lipgloss.Width(thinkingGuide.Render("  ┊ "))
	dimStyle := s.ProgressDim

	// Render completed iterations (dimmed) — use cache to avoid re-running
	// chroma/lipgloss on every 100ms tick (major CPU saver for long sessions).
	// Incremental rebuild: only render newly added iterations and append to cache.
	// Uses padded lines cache (cachedProgressHistoryLines) so that padProgressLines
	// only needs to process the newly added lines instead of the entire history.
	var historyLines []string
	if m.cachedProgressHistoryLen == len(m.iterationHistory) && m.cachedProgressHistoryWidth == bubbleWidth && m.cachedProgressHistory != "" {
		historyLines = m.cachedProgressHistoryLines
	} else if m.cachedProgressHistoryLen > 0 && m.cachedProgressHistoryWidth == bubbleWidth && m.cachedProgressHistoryLen < len(m.iterationHistory) {
		// Incremental: only render new iterations [cachedProgressHistoryLen:]
		var histBuf strings.Builder
		m.renderHistoryRange(&histBuf, m.iterationHistory[m.cachedProgressHistoryLen:], innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, s)
		newHistory := histBuf.String()
		m.cachedProgressHistory += newHistory
		m.cachedProgressHistoryLen = len(m.iterationHistory)
		m.cachedProgressHistoryFP = fnvHash64(m.cachedProgressHistory)
		// Incremental padded lines: only pad the new portion
		newLines := padLinesFromContent(newHistory)
		m.cachedProgressHistoryLines = append(m.cachedProgressHistoryLines, newLines...)
		historyLines = m.cachedProgressHistoryLines
	} else {
		// Full rebuild (width changed or cache invalidation)
		var histBuf strings.Builder
		m.renderHistoryRange(&histBuf, m.iterationHistory, innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, iterStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, s)
		m.cachedProgressHistory = histBuf.String()
		m.cachedProgressHistoryLen = len(m.iterationHistory)
		m.cachedProgressHistoryWidth = bubbleWidth
		m.cachedProgressHistoryFP = fnvHash64(m.cachedProgressHistory)
		// Full padded lines rebuild
		m.cachedProgressHistoryLines = padLinesFromContent(m.cachedProgressHistory)
		historyLines = m.cachedProgressHistoryLines
	}

	// Render current iteration into a separate buffer so we can
	// pad only the dynamic parts while reusing the pre-padded
	// history lines cache.
	var currentBuf strings.Builder

	if m.progress != nil {
		currentBuf.WriteString(iterStyle.Render(fmt.Sprintf("#%d", m.progress.Iteration)))
		currentBuf.WriteString("\n")

		// Render all current-iteration content with correct linear order.
		// Static cache (completed tools + content) is inserted mid-stream.
		m.renderCurrentIteration(&currentBuf, s, innerWidth, reasoningW, thinkingW, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, toolDoneStyle, toolErrorStyle, toolRunningStyle, elapsedStyle, dimStyle, iterStyle)
	} else if m.typing {
		currentBuf.WriteString("  ")
		currentBuf.WriteString(m.ticker.viewFrames(orbitFrames))
		currentBuf.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
		currentBuf.WriteString("\n")
	}

	currentContent := strings.TrimRight(currentBuf.String(), "\n")
	if currentContent == "" && len(historyLines) == 0 {
		return ""
	}

	// Total elapsed — quantize to whole seconds so the fingerprint stays stable
	// across 100ms ticks. Without this, formatElapsed changes every 100ms
	// (e.g. "1.2s" → "1.3s"), preventing the cache from ever hitting.
	var elapsedSec int64
	if !m.typingStartTime.IsZero() {
		elapsedSec = time.Since(m.typingStartTime).Milliseconds() / 1000
	}

	// --- Full progress block output cache ---
	// Uses O(1) composite fingerprint (sub-block FPs + active tool state)
	// instead of O(N) hash of the entire pre-render string.
	fp := m.progressBlockCompositeFP(elapsedSec, bubbleWidth)
	if fp == m.cachedProgressBlockFP && bubbleWidth == m.cachedProgressBlockWidth && m.cachedProgressBlockOut != "" {
		return m.cachedProgressBlockOut
	}

	// Header
	headerStyle := s.ProgressHeader
	elapsed := ""
	if !m.typingStartTime.IsZero() {
		elapsed = " " + elapsedStyle.Render(formatElapsed(elapsedSec*1000))
	}
	header := headerStyle.Render("Progress") + elapsed

	// Assemble padded lines incrementally:
	// 1. Header line (padded)
	// 2. History lines (already padded from cache — O(1) reuse)
	// 3. Current iteration lines (padded — always short, O(1))
	// This avoids O(N) padProgressLines on the entire content.
	totalCap := 1 + 1 + len(historyLines) + 20 // header + divider + history + ~20 lines for current
	allPaddedLines := make([]string, 0, totalCap)
	allPaddedLines = append(allPaddedLines, " "+header+" ")

	// Divider at the very start of the progress block
	allPaddedLines = append(allPaddedLines, " "+s.DimGuideSt.Render(strings.Repeat("─", innerWidth))+" ")

	// History lines are already padded — directly append
	allPaddedLines = append(allPaddedLines, historyLines...)

	// Pad only the current iteration content (divider + current iter — always short)
	if currentContent != "" {
		currentLines := padLinesFromContent(currentContent)
		allPaddedLines = append(allPaddedLines, currentLines...)
	}

	// Build the final string from padded lines
	var resultBuf strings.Builder
	resultBuf.Grow(len(allPaddedLines) * (bubbleWidth + 2))
	for _, line := range allPaddedLines {
		resultBuf.WriteString(line)
		resultBuf.WriteString("\n")
	}
	result := resultBuf.String()
	// Add trailing empty line for the bottom border
	allPaddedLines = append(allPaddedLines, "")

	m.cachedProgressBlockOut = result
	m.cachedProgressBlockFP = fp
	m.cachedProgressBlockWidth = bubbleWidth
	m.cachedProgressBlockLines = allPaddedLines

	return result
}

// padLinesFromContent splits content by newlines and pads each line with
// " " prefix and " " suffix (equivalent to Padding(0,1)). Returns only
// the []string of padded lines — used for incremental line assembly
// where the caller doesn't need the monolithic string.
func padLinesFromContent(content string) []string {
	if content == "" {
		return nil
	}
	nlCount := strings.Count(content, "\n")
	lines := make([]string, 0, nlCount+1)
	for {
		idx := strings.IndexByte(content, '\n')
		if idx < 0 {
			if content != "" {
				lines = append(lines, " "+content+" ")
			}
			break
		}
		if idx > 0 { // skip empty lines
			lines = append(lines, " "+content[:idx]+" ")
		}
		content = content[idx+1:]
	}
	return lines
}

// progressBlockCompositeFP computes an O(1) composite fingerprint by combining
// pre-computed sub-block FPs with active tool state. This replaces the old
// progressBlockFullFP which hashed the entire pre-render string at O(N) cost.
func (m *cliModel) progressBlockCompositeFP(elapsedSec int64, bubbleWidth int) uint64 {
	h := fnv.New64a()
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(elapsedSec))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(bubbleWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedProgressHistoryFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedCurrentStaticFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedReasoningBlockFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedThinkingBlockFP)
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], m.cachedStreamBlockFP)
	h.Write(eb[:])
	// Spinner tick count — ensures pulse/phase animations update every 100ms.
	// Without this, the cache stays stable for a whole second (elapsedSec is
	// quantized) and the spinner freezes.
	binary.LittleEndian.PutUint64(eb[:], uint64(m.ticker.ticks))
	h.Write(eb[:])
	// Active tools (dynamic — changes every tick during tool execution)
	if m.progress != nil {
		for _, t := range m.progress.ActiveTools {
			if t.Status != "done" && t.Status != "error" {
				h.Write([]byte(t.Name))
				h.Write([]byte(t.Status))
				binary.LittleEndian.PutUint64(eb[:], uint64(t.Elapsed))
				h.Write(eb[:])
			}
		}
	}
	return h.Sum64()
}

// fnvHash64 returns a fast FNV-1a hash of s for O(1) dirty detection.
func fnvHash64(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// progressStaticFP computes a fingerprint for the static parts of the current
// iteration's progress. When the fingerprint doesn't change between ticks,
// we can skip re-rendering reasoning/thinking/completed-tools/SubAgent tree.
func (m *cliModel) progressStaticFP() uint64 {
	if m.progress == nil {
		return 0
	}
	p := m.progress
	h := fnv.New64a()
	h.Write([]byte(p.Reasoning))
	h.Write([]byte(p.ReasoningStreamContent))
	h.Write([]byte(p.Thinking))
	h.Write([]byte(p.Phase))
	// Completed tools: count + status + label (elapsed is static after completion)
	for _, t := range p.CompletedTools {
		if t.Iteration == p.Iteration {
			h.Write([]byte(t.Name))
			h.Write([]byte(t.Status))
			var eb [8]byte
			binary.LittleEndian.PutUint64(eb[:], uint64(t.Elapsed))
			h.Write(eb[:])
		}
	}
	// Done/error active tools (also static once finished)
	for _, t := range p.ActiveTools {
		if t.Status == "done" || t.Status == "error" {
			h.Write([]byte(t.Name))
			h.Write([]byte(t.Status))
		}
	}
	// SubAgent tree structure
	for _, sa := range p.SubAgents {
		h.Write([]byte(sa.Role))
		h.Write([]byte(sa.Instance))
		h.Write([]byte(sa.Status))
	}
	return h.Sum64()
}

// reasoningBlockFP computes a fingerprint for the reasoning block cache.
// Includes cursor blink state since it affects the rendered output.
func (m *cliModel) reasoningBlockFP(innerWidth, reasoningW, cursorState int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	reasoningText := m.progress.Reasoning
	if reasoningText == "" {
		reasoningText = m.progress.ReasoningStreamContent
	}
	h.Write([]byte(reasoningText))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(m.rwVisible))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(reasoningW))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(cursorState))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(len([]rune(m.progress.ReasoningStreamContent))))
	h.Write(eb[:])
	return h.Sum64()
}

// streamBlockFP computes a fingerprint for the stream block cache.
// Includes cursor blink state since it affects the rendered output.
func (m *cliModel) streamBlockFP(innerWidth, thinkingW, cursorState int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(m.progress.StreamContent))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(m.twVisible))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(thinkingW))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(cursorState))
	h.Write(eb[:])
	return h.Sum64()
}

// thinkingBlockFP computes a fingerprint for the thinking block cache.
func (m *cliModel) thinkingBlockFP(innerWidth, thinkingW int) uint64 {
	if m.progress == nil {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(m.progress.Thinking))
	var eb [8]byte
	binary.LittleEndian.PutUint64(eb[:], uint64(innerWidth))
	h.Write(eb[:])
	binary.LittleEndian.PutUint64(eb[:], uint64(thinkingW))
	h.Write(eb[:])
	return h.Sum64()
}

// getCurrentStaticCache returns the cached rendering of static parts for the
// current iteration (completed tools, tool content). These are truly static
// once a tool finishes — no typewriter, no cursor, no elapsed timer.
// Reasoning, thinking, stream content, active tools, and SubAgent tree are
// always rendered dynamically to maintain correct linear order.
func (m *cliModel) getCurrentStaticCache(
	bubbleWidth, innerWidth int,
	s *cliStyles,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, iterStyle lipgloss.Style,
) string {
	if m.progress == nil {
		m.cachedCurrentStatic = ""
		return ""
	}

	fp := m.progressStaticFP()
	if m.cachedCurrentStatic != "" &&
		m.cachedCurrentStaticWidth == bubbleWidth &&
		m.cachedCurrentIter == m.progress.Iteration &&
		m.cachedCurrentStaticFP == fp {
		return m.cachedCurrentStatic
	}

	var sb strings.Builder

	// Completed tools in current iteration
	for _, tool := range m.progress.CompletedTools {
		if tool.Iteration != m.progress.Iteration {
			continue
		}
		label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
		var elapsedStyled string
		if tool.Elapsed > 0 {
			elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
		}
		sb.WriteString(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// Done/error active tools (static once finished)
	for _, tool := range m.progress.ActiveTools {
		if tool.Status != "done" && tool.Status != "error" {
			continue
		}
		label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
		var elapsedStyled string
		if tool.Elapsed > 0 {
			elapsedStyled = elapsedStyle.Render(formatElapsed(tool.Elapsed))
		}
		sb.WriteString(sty.Render(toolLine(icon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// Tool content (completed + done/error active)
	guide := reasoningGuide.Render("  ┊ ")
	for i := range m.progress.CompletedTools {
		tool := &m.progress.CompletedTools[i]
		if content := m.renderToolContentBelow(tool, guide, innerWidth, false, 0); content != "" {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}
	for i := range m.progress.ActiveTools {
		tool := &m.progress.ActiveTools[i]
		if tool.Status != "done" && tool.Status != "error" {
			continue
		}
		if content := m.renderToolContentBelow(tool, guide, innerWidth, false, 0); content != "" {
			sb.WriteString(content)
			sb.WriteString("\n")
		}
	}

	m.cachedCurrentStatic = sb.String()
	m.cachedCurrentStaticWidth = bubbleWidth
	m.cachedCurrentIter = m.progress.Iteration
	m.cachedCurrentStaticFP = fp
	return m.cachedCurrentStatic
}

// renderCurrentIteration renders the current iteration with correct linear order:
//
//  1. Reasoning (with typewriter when streaming) — cached per cursorState
//  2. Thinking — cached
//  3. Completed tools + content (from static cache)
//  4. Stream content (assistant text output) — cached per cursorState
//  5. Active tools (live elapsed)
//  6. Phase spinner (only when no content at all)
//  7. SubAgent tree
//
// Reasoning, thinking, and stream blocks are cached to avoid per-line Style.Render,
// lipgloss.Width, and hardWrapRunes on every tick when content is static.
func (m *cliModel) renderCurrentIteration(
	sb *strings.Builder,
	s *cliStyles,
	innerWidth, reasoningW, thinkingW int,
	reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle,
	toolDoneStyle, toolErrorStyle, toolRunningStyle, elapsedStyle, dimStyle, iterStyle lipgloss.Style,
) {
	if m.progress == nil {
		return
	}

	cursorState := int((m.ticker.ticks / 5) % 2) // 0 or 1, changes every ~500ms

	// --- 1. Reasoning (cached) ---
	isReasoningStreaming := m.progress.ReasoningStreamContent != "" && m.progress.StreamContent == ""
	reasoningText := m.progress.Reasoning
	if reasoningText == "" {
		reasoningText = m.progress.ReasoningStreamContent
	}
	if reasoningText != "" {
		fp := m.reasoningBlockFP(innerWidth, reasoningW, cursorState)
		if m.cachedReasoningBlock != "" && m.cachedReasoningBlockFP == fp && m.cachedReasoningBlockWidth == innerWidth {
			sb.WriteString(m.cachedReasoningBlock)
		} else {
			var blockBuf strings.Builder
			// Typewriter effect for reasoning streaming
			if isReasoningStreaming {
				totalRunes := len([]rune(m.progress.ReasoningStreamContent))
				runes := []rune(m.progress.ReasoningStreamContent)
				if m.rwVisible > 0 && m.rwVisible < totalRunes {
					runes = runes[:m.rwVisible]
				}
				reasoningText = string(runes)
			}
			lines := strings.Split(reasoningText, "\n")
			reasoningTyping := isReasoningStreaming && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
			cursorVisible := reasoningTyping || cursorState == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					guide := reasoningGuide.Render("  ┊ ")
					if isLast && isReasoningStreaming {
						cursorStr := s.StreamCursor.Render("▋")
						cursorOverflow := reasoningW+lipgloss.Width(wl)+lipgloss.Width("▋") > innerWidth
						if cursorOverflow {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl))
							blockBuf.WriteString("\n")
							if cursorVisible {
								blockBuf.WriteString(guide + cursorStr)
							} else {
								blockBuf.WriteString(guide)
							}
						} else if cursorVisible {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl) + cursorStr)
						} else {
							blockBuf.WriteString(guide + reasoningStyle.Render(wl))
						}
					} else {
						blockBuf.WriteString(guide + reasoningStyle.Render(wl))
					}
					blockBuf.WriteString("\n")
				}
			}
			m.cachedReasoningBlock = blockBuf.String()
			m.cachedReasoningBlockFP = fp
			m.cachedReasoningBlockWidth = innerWidth
			sb.WriteString(m.cachedReasoningBlock)
		}
	}

	// --- 2. Thinking (cached) ---
	if m.progress.Thinking != "" {
		fp := m.thinkingBlockFP(innerWidth, thinkingW)
		if m.cachedThinkingBlock != "" && m.cachedThinkingBlockFP == fp && m.cachedThinkingBlockWidth == innerWidth {
			sb.WriteString(m.cachedThinkingBlock)
		} else {
			var blockBuf strings.Builder
			for _, line := range strings.Split(m.progress.Thinking, "\n") {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					blockBuf.WriteString(thinkingGuide.Render("  ┊ ") + thinkingStyle.Render(wl))
					blockBuf.WriteString("\n")
				}
			}
			m.cachedThinkingBlock = blockBuf.String()
			m.cachedThinkingBlockFP = fp
			m.cachedThinkingBlockWidth = innerWidth
			sb.WriteString(m.cachedThinkingBlock)
		}
	}

	// --- 3. Completed tools + tool content (static cache) ---
	bubbleWidth := innerWidth + 4 // match the padding used elsewhere
	static := m.getCurrentStaticCache(bubbleWidth, innerWidth, s, reasoningGuide, reasoningStyle, thinkingGuide, thinkingStyle, toolDoneStyle, toolErrorStyle, elapsedStyle, dimStyle, iterStyle)
	if static != "" {
		sb.WriteString(static)
	}

	// --- 4. Stream content (assistant text output, cached) ---
	hasTools := len(m.progress.ActiveTools) > 0 || len(m.progress.CompletedTools) > 0

	if m.progress.StreamContent != "" {
		fp := m.streamBlockFP(innerWidth, thinkingW, cursorState)
		if m.cachedStreamBlock != "" && m.cachedStreamBlockFP == fp && m.cachedStreamBlockWidth == innerWidth {
			sb.WriteString(m.cachedStreamBlock)
		} else {
			var blockBuf strings.Builder
			totalRunes := len([]rune(m.progress.StreamContent))
			runes := []rune(m.progress.StreamContent)
			if m.twVisible > 0 && m.twVisible < totalRunes {
				runes = runes[:m.twVisible]
			}
			streamText := string(runes)
			lines := strings.Split(streamText, "\n")
			typing := m.twVisible < totalRunes
			cursorVisible := typing || cursorState == 0
			for i, line := range lines {
				line = strings.TrimRight(line, " \t\r")
				if line == "" {
					continue
				}
				isLastLine := i == len(lines)-1
				wrappedLines := strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n")
				for j, wl := range wrappedLines {
					isLast := isLastLine && j == len(wrappedLines)-1
					guide := thinkingGuide.Render("  ┊ ")
					if isLast {
						cursorStr := s.StreamCursor.Render("▋")
						cursorOverflow := thinkingW+lipgloss.Width(wl)+lipgloss.Width("▋") > innerWidth
						if cursorOverflow {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl))
							blockBuf.WriteString("\n")
							if cursorVisible {
								blockBuf.WriteString(guide + cursorStr)
							} else {
								blockBuf.WriteString(guide)
							}
						} else if cursorVisible {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl) + cursorStr)
						} else {
							blockBuf.WriteString(guide + thinkingStyle.Render(wl))
						}
					} else {
						blockBuf.WriteString(guide + thinkingStyle.Render(wl))
					}
					blockBuf.WriteString("\n")
				}
			}
			m.cachedStreamBlock = blockBuf.String()
			m.cachedStreamBlockFP = fp
			m.cachedStreamBlockWidth = innerWidth
			sb.WriteString(m.cachedStreamBlock)
		}
	} else if !hasTools {
		// Phase spinner only when no content at all
		hasReasoning := m.progress.Reasoning != "" || m.progress.ReasoningStreamContent != ""
		hasThinking := m.progress.Thinking != ""
		if !hasReasoning && !hasThinking {
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
			case "newing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" resetting session..."))
				sb.WriteString("\n")
			case "retrying":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" retrying..."))
				sb.WriteString("\n")
			}
		}
	}

	// --- 5. Active tools (live elapsed + pulse) ---
	for _, tool := range m.progress.ActiveTools {
		if tool.Status == "done" || tool.Status == "error" {
			continue
		}
		label, _, _ := toolDisplayInfo(tool, toolRunningStyle, lipgloss.Style{})
		pulseIcon := m.ticker.viewFrames(pulseFrames)
		var elapsedMs int64
		if !tool.StartedAt.IsZero() {
			elapsedMs = time.Since(tool.StartedAt).Milliseconds()
		} else {
			elapsedMs = tool.Elapsed
		}
		elapsedStyled := elapsedStyle.Render(formatElapsed(elapsedMs))
		sb.WriteString(toolRunningStyle.Render(toolLine(pulseIcon, label, elapsedStyled, innerWidth)))
		sb.WriteString("\n")
	}

	// --- 6. SubAgent tree ---
	if len(m.progress.SubAgents) > 0 {
		var treeSB strings.Builder
		m.renderSubAgentTree(&treeSB, m.progress.SubAgents, "", innerWidth)
		if treeSB.Len() > 0 {
			sb.WriteString("\n")
			sb.WriteString(treeSB.String())
		}
	}
}

// renderSubAgentTree renders nested sub-agents with indentation.
// Only renders running/pending agents — completed or errored ones are already
// captured in the tool summary and shouldn't linger in the progress panel.
//
// Uses a prefix-based approach instead of depth-based: each level appends
// "┊   " or "    " to the prefix depending on whether the parent was the last
// sibling. This avoids spurious vertical lines after a └── branch.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []protocol.SubAgentInfo, prefix string, maxWidth int) {
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
		roleText := sa.Role
		if sa.Instance != "" {
			roleText = sa.Role + " [" + sa.Instance + "]"
		}
		line := fmt.Sprintf("%s%s%s %s", prefix, connector, icon, roleText)
		if sa.Desc != "" {
			// Only add description if there's room — never exceed maxWidth.
			overhead := lipgloss.Width(line) + 2 // +2 for ": "
			descW := maxWidth - overhead
			if descW > 0 {
				line += ": " + truncateToWidth(strings.ReplaceAll(strings.ReplaceAll(sa.Desc, "\n", " "), "\r", ""), descW)
			}
		}
		sb.WriteString(style.Render(line))
		sb.WriteString("\n")
		if len(sa.Children) > 0 {
			childPrefix := prefix
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "┊   "
			}
			m.renderSubAgentTree(sb, sa.Children, childPrefix, maxWidth)
		}
	}
}

// renderHelpPanel 渲染格式化的帮助面板（第 4 轮）。
// 使用 lipgloss 边框 + 分组布局 + 状态图标，替代纯文本。
func (m *cliModel) renderHelpPanel() string {
	contentWidth := m.chatWidth() - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// §20 使用缓存样式
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

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
// toolDisplayInfo 从工具进度条目中提取显示用的 label、状态图标和样式。

// renderToolContentBelow renders tool body content below a tool line.
// Shows ToolHints (diff) first, then per-tool body (Read/Shell/Grep/Glob output).
// guide is the prefix for each line (e.g. "  ┊ ").
// dimmed controls whether the content is dimmed (for history iterations).
// maxLines caps diff rendering (0 = unlimited). Passed through to renderToolHint.
// Caches the result on the tool struct to avoid re-running chroma/lipgloss on every tick.
func (m *cliModel) renderToolContentBelow(tool *protocol.ToolProgress, guide string, bodyW int, dimmed bool, maxLines int) string {
	var sb strings.Builder
	guideFn := func(s string) string { return s }
	if dimmed {
		dimSt := m.styles.ProgressDim
		guideFn = func(s string) string { return dimSt.Render(s) }
	}

	// 1. ToolHints (diff from plugin or built-in) — render with guide prefix,
	// same as per-tool body, so diff appears inside the guide tree.
	if tool.ToolHints != "" {
		g := guideFn(guide)
		hintW := bodyW - lipgloss.Width(g)
		if hintW < 1 {
			hintW = 1
		}
		if r, err := m.renderToolHint(tool.ToolHints, hintW, maxLines); err == nil && r != "" {
			for _, line := range strings.Split(r, "\n") {
				sb.WriteString(g)
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}

	// 2. Per-tool body (Read code, Shell output, Grep matches, etc.)
	if tool.ToolHints == "" { // Don't show body if diff hints are already displayed
		g := guideFn(guide)
		bodyContentW := bodyW - lipgloss.Width(g)
		if bodyContentW < 1 {
			bodyContentW = 1
		}
		if body := m.renderToolBody(*tool, bodyContentW); body != "" {
			guideW := lipgloss.Width(g)
			for _, line := range strings.Split(body, "\n") {
				// Final safety net: ensure guide + rendered line fits within bodyW.
				// Tool body renderers (renderShellBody etc.) wrap to bodyContentW,
				// but lipgloss.Style.Render() may change effective width. Use hardWrapRunes
				// so overflow lines wrap (preserving content) instead of truncating.
				if visW := lipgloss.Width(line); guideW+visW > bodyW {
					wrapped := hardWrapRunes(line, bodyW-guideW)
					for _, wl := range strings.Split(wrapped, "\n") {
						sb.WriteString(g)
						sb.WriteString(wl)
						sb.WriteString("\n")
					}
					continue
				}
				sb.WriteString(g)
				sb.WriteString(line)
				sb.WriteString("\n")
			}
		}
	}

	result := strings.TrimRight(sb.String(), "\n")
	return result
}

func toolDisplayInfo(tool protocol.ToolProgress, okStyle, errStyle lipgloss.Style) (label, icon string, sty lipgloss.Style) {
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

// toolLine formats a tool progress line guaranteed to fit within maxWidth cells.
// icon and label are plain text; elapsed may be pre-styled with ANSI codes.
// Returns the formatted string — caller wraps with style.Render().
func toolLine(icon, label string, elapsedStyled string, maxWidth int) string {
	prefix := fmt.Sprintf("  ┊ %s ", icon)
	prefixW := lipgloss.Width(prefix)

	elapsedW := lipgloss.Width(elapsedStyled) // strips ANSI, measures visual width

	minPad := 0
	if elapsedW > 0 {
		minPad = 1
	}

	maxLabelW := maxWidth - prefixW - elapsedW - minPad
	if maxLabelW < 0 {
		maxLabelW = 0
	}
	label = truncateToWidth(label, maxLabelW)
	labelW := lipgloss.Width(label)

	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteString(label)
	if elapsedW > 0 {
		pad := maxWidth - prefixW - labelW - elapsedW
		if pad < minPad {
			pad = minPad
		}
		sb.WriteString(strings.Repeat(" ", pad))
		sb.WriteString(elapsedStyled)
	}
	return sb.String()
}

func (m *cliModel) renderMessage(msg *cliMessage) string {
	// §20 使用缓存样式
	s := &m.styles
	var sb strings.Builder
	contentWidth := m.chatWidth() - 4
	// chatUsableWidth: unified right boundary for user msg content.
	// Aligns with assistant body right edge (guide(2) + content(cw-4) = cw-2),
	// leaving 2 cols right padding so content doesn't touch the viewport edge.
	chatUsableWidth := m.chatWidth() - 2
	cw := m.chatWidth()
	timeStyle := s.Time
	userLabelStyle := s.UserLabel
	streamingLabelStyle := s.StreamingLabel
	// Override style widths to chatWidth (sidebar may be open, reducing available space)
	systemMsgStyle := s.SystemMsg.Width(cw)
	errorMsgStyle := s.ErrorMsg.Width(cw - 4)

	// 渲染 Markdown（assistant 消息 + 带 markdown 标记的 system 消息）
	var rendered string
	if msg.role == "assistant" || (msg.role == "system" && msg.markdown && !msg.styled) {
		// Pre-process: render mermaid code blocks to ASCII art
		// Truncate to glamour wrap width to prevent wrapping.
		preprocessed := msg.content
		if msg.role == "assistant" {
			preprocessed = renderMermaidBlocks(msg.content, m.chatWidth()-4)
			preprocessed = renderMathBlocks(preprocessed, m.chatWidth()-4)
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
	case "tool_summary":
		// §20 使用缓存样式（override width to chatWidth for sidebar compat）
		toolSummaryStyle := s.ToolSummary.Width(cw - 4)
		toolHeaderStyle := s.ToolHeader
		toolItemStyle := s.ToolItem
		toolErrorItemStyle := s.ToolErrorItem
		thinkingStyle := s.ProgressThinking
		reasoningStyle := s.TextMutedSt
		reasoningGuide := s.ProgressDim
		thinkingGuide := s.ProgressIndent
		hintStyle := s.ToolHint

		// 统计总工具数和总耗时
		allTools, iterCount := msg.iterToolsFlat()
		totalTools := len(allTools)
		totalMs := int64(0)
		for _, it := range msg.iterations {
			totalMs += it.ElapsedWall
		}

		var toolSb strings.Builder

		// Box internal width: ToolSummary has Border(2) + Padding(0,1 → 2) = 4 cols overhead
		boxInnerW := contentWidth - 4

		if m.toolSummaryExpanded {
			// 展开模式：完整渲染
			if iterCount > 0 {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d iterations, %d calls)", iterCount, totalTools)))
				toolSb.WriteString("\n")
				guideW := lipgloss.Width(s.ProgressIndent.Render("  ┊ "))
				textW := boxInnerW - guideW
				for ii := range msg.iterations {
					it := &msg.iterations[ii]
					iterLabel := fmt.Sprintf("#%d", it.Iteration)
					if it.ElapsedWall > 0 {
						iterLabel += " " + reasoningStyle.Render(formatElapsed(it.ElapsedWall))
					}
					toolSb.WriteString(s.ProgressIter.Render(iterLabel))
					toolSb.WriteString("\n")
					if it.Reasoning != "" {
						for _, line := range strings.Split(it.Reasoning, "\n") {
							line = strings.TrimRight(line, " \t\r")
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(reasoningGuide.Render("  ┊ ") + reasoningStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					if it.Thinking != "" {
						for _, line := range strings.Split(it.Thinking, "\n") {
							line = strings.TrimRight(line, " \t\r")
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(thinkingGuide.Render("  ┊ ") + thinkingStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					for k := range it.Tools {
						tool := &it.Tools[k]
						label, icon, sty := toolDisplayInfo(*tool, toolItemStyle, toolErrorItemStyle)
						elapsed := ""
						if tool.Elapsed > 0 {
							elapsed = fmt.Sprintf(" (%s)", formatElapsed(tool.Elapsed))
						}
						toolSb.WriteString(sty.Render(fmt.Sprintf("    %s %s%s", icon, label, elapsed)))
						toolSb.WriteString("\n")
						// Render tool body (diff hints or per-tool output)
						if content := m.renderToolContentBelow(tool, reasoningGuide.Render("  ┊ "), textW, false, 0); content != "" {
							toolSb.WriteString(content)
							toolSb.WriteString("\n")
						}
					}
				}
			} else {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d)", totalTools)))
				toolSb.WriteString("\n")
				for i := range msg.tools {
					tool := &msg.tools[i]
					label, icon, sty := toolDisplayInfo(*tool, toolItemStyle, toolErrorItemStyle)
					elapsed := ""
					if tool.Elapsed > 0 {
						elapsed = fmt.Sprintf(" (%s)", formatElapsed(tool.Elapsed))
					}
					toolSb.WriteString(sty.Render(fmt.Sprintf("  %s %s%s", icon, label, elapsed)))
					toolSb.WriteString("\n")
					// Render tool body for flat tool list too
					if content := m.renderToolContentBelow(tool, reasoningGuide.Render("  ┊ "), boxInnerW, false, 0); content != "" {
						toolSb.WriteString(content)
						toolSb.WriteString("\n")
					}
				}
			}
		} else {
			// 折叠模式升级（第 4 轮）：统计摘要 + 成功/失败状态图标
			elapsedStr := formatElapsed(totalMs)
			// 统计成功/失败工具数
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
					s.TextMutedSt.Render(fmt.Sprintf("%d", errorCount))
			}
			if successCount > 0 && errorCount > 0 {
				statusIcons += " "
			}
			if successCount > 0 {
				statusIcons += s.ProgressDone.Render("✓") +
					s.TextMutedSt.Render(fmt.Sprintf("%d", successCount))
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
	case "system":
		if msg.styled {
			// Pre-styled content: output as-is, no wrapping
			sb.WriteString(msg.content)
		} else if msg.markdown {
			// Markdown system messages (e.g. /usage tables): use glamour-rendered output directly
			sb.WriteString(rendered)
		} else if isErrorContent(msg.content) {
			sb.WriteString(errorMsgStyle.Render("⚠ " + msg.content))
		} else {
			sb.WriteString(systemMsgStyle.Render(msg.content))
		}
	case "user":
		// 用户消息上方：右侧柔和光点分隔，与 assistant 的左侧竖线形成对称
		dotSep := s.UserDotSep.Width(chatUsableWidth).Align(lipgloss.Right).Render("···")
		sb.WriteString(dotSep)
		sb.WriteString("\n")
		label := userLabelStyle.Render("You")
		header := s.UserHeader.Width(chatUsableWidth).Align(lipgloss.Right).Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		// 用户消息：右对齐气泡效果
		// 计算内容最大行宽，整块右对齐而非每行拉伸
		lines := strings.Split(rendered, "\n")
		maxWidth := 0
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
		}
		maxBubble := chatUsableWidth * 3 / 4
		userStyle := s.UserContent
		if maxWidth <= maxBubble {
			// 内容够窄，左填充实现气泡靠右
			userStyle = s.UserContent.PaddingLeft(chatUsableWidth - maxWidth)
		}
		// CRITICAL: lipgloss Render pads ALL lines to maxWidth including trailing
		// spaces. When contentWidth is close to terminal width, the padded lines
		// can overflow after being processed by hardWrapRunes. Fix: Render first,
		// then right-trim each line — the padding is visual-only (left-aligned by
		// PaddingLeft) and trailing spaces serve no purpose.
		renderedUser := userStyle.Render(rendered)
		userLines := strings.Split(renderedUser, "\n")
		for i, rl := range userLines {
			// Strip trailing whitespace (lipgloss padding) from ALL lines.
			// The left-padding is preserved because TrimRight only removes right side.
			userLines[i] = strings.TrimRight(rl, " \t")
		}
		sb.WriteString(strings.Join(userLines, "\n"))
	default:
		// assistant 消息 — crush 风格：先构建内容体，再逐行加 guide 前缀
		// Streaming: bright guide; Completed: dim guide
		var guideSt lipgloss.Style
		guideSym := "┊ "
		if msg.isPartial {
			guideSt = s.GuideSt
		} else {
			guideSt = s.DimGuideSt
		}

		// Build header line
		label := streamingLabelStyle.Render("Assistant")
		if !msg.isPartial {
			label = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary)).Render("Assistant")
		}
		headerLine := fmt.Sprintf("%s %s", guideSt.Render(guideSym)+timeStr, label)
		if msg.isPartial {
			headerLine += " ..."
		}
		sb.WriteString(headerLine)
		sb.WriteString("\n")

		// Build body lines (thinking box + content + cursor)
		var bodyLines []string

		// Thinking Box
		if !msg.isPartial && msg.thinking != "" {
			thinkingLines := strings.Split(strings.TrimSpace(msg.thinking), "\n")
			const maxTL = 10
			if len(thinkingLines) > 0 {
				var display []string
				truncated := len(thinkingLines) > maxTL
				if truncated {
					display = thinkingLines[len(thinkingLines)-maxTL:]
				} else {
					display = thinkingLines
				}
				body := strings.Join(display, "\n")
				if truncated {
					body = s.TextMutedSt.Render(fmt.Sprintf("… (%d lines hidden)", len(thinkingLines)-maxTL)) + "\n" + body
				}
				boxW := contentWidth - 4
				if boxW < 20 {
					boxW = 20
				}
				thinkingBox := s.ThinkingBox
				for _, l := range strings.Split(thinkingBox.Width(boxW).Render(body), "\n") {
					bodyLines = append(bodyLines, "  "+l)
				}
				bodyLines = append(bodyLines, "") // blank after box
			}
		}

		// §19 长消息折叠
		displayContent := rendered
		if msg.folded && !msg.isPartial {
			origLines := msg.originalRenderedLines
			if origLines == 0 {
				origLines = msg.renderedLines
			}
			if origLines > msgFoldThresholdLines {
				renderedLinesList := strings.Split(rendered, "\n")
				if len(renderedLinesList) > msgFoldPreviewLines {
					displayContent = strings.Join(renderedLinesList[:msgFoldPreviewLines], "\n")
					displayContent += "\n" + m.styles.TextMutedSt.Render(
						fmt.Sprintf("  ... %s (%d lines) ...", m.locale.MsgCollapsed, origLines))
				}
			}
		}

		// Main content — trim trailing newlines so cursor stays inline.
		// glamour already handles word-wrap via WithWordWrap(wrapWidth),
		// including CJK-aware line breaking (via forked muesli/reflow).
		// No additional hard-wrap needed — it would double-wrap and
		// break table structure.
		trimmed := strings.TrimRight(displayContent, "\n")
		if trimmed != "" {
			bodyLines = append(bodyLines, strings.Split(trimmed, "\n")...)
		}

		// Streaming cursor
		if msg.isPartial && trimmed != "" {
			cursorVisible := (m.ticker.ticks/5)%2 == 0
			if cursorVisible {
				bodyLines = append(bodyLines, s.StreamCursor.Render("▋"))
			}
		}

		// Render all body lines with guide prefix.
		// ANSI reset before guide: clears inline code background color inherited
		// from the previous body line (hardWrapRunes doesn't add trailing reset
		// when breaking inside an inline code span, so bg color leaks forward).
		// ANSI reset after guide: prevents guide foreground from mixing with
		// body line ANSI styles.
		// Right-align: body content width = contentWidth (same as user msg "You"),
		// so guide(2) + body(cw-4) leaves 2 cols right padding.
		const ansiReset = "\x1b[0m"
		for _, l := range bodyLines {
			sb.WriteString(ansiReset + guideSt.Render(guideSym) + ansiReset)
			sb.WriteString(l)
			sb.WriteString("\n")
		}
	}

	sb.WriteString("\n\n")

	// §19 计算渲染后行数（每次 dirty 重算）
	// Sanitize rendered output: strip \r carriage-return overwrites per line.
	// This is the final rendering-layer safety net — ensures progress bar
	// output (tqdm, curl etc.) from any source (old offload, history, network)
	// never corrupts the TUI layout.
	raw := sb.String()
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		if idx := strings.LastIndex(line, "\r"); idx >= 0 {
			lines[i] = line[idx+1:]
		}
	}
	cleaned := strings.Join(lines, "\n")
	msg.renderedLines = strings.Count(cleaned, "\n") + 1

	return cleaned
}

// wrapPreservingGuide handles viewport-level line wrapping.
// Guide lines (starting with "┊ ") were already word-wrapped by glamour (with CJK-aware
// reflow), so we skip hardWrapRunes to avoid breaking CJK text at character boundaries.
// Non-guide lines may need hardWrapRunes as a safety net for long ANSI-styled content.
func wrapPreservingGuide(line string, cw int) []string {
	prefix, rest, pw := splitGuidePrefix(line)
	if pw == 0 || rest == "" {
		// Non-guide line: apply hard wrap as safety net
		return strings.Split(hardWrapRunes(line, cw), "\n")
	}
	// Guide line: glamour already wrapped content to (cw - 4) width.
	// Guide prefix is 2 cols, so total = 2 + (cw-4) = cw-2, which fits.
	// Return as-is to preserve glamour's CJK-aware wrapping.
	// Only truncate if a line is somehow still too wide (defensive).
	if lipgloss.Width(line) <= cw {
		return []string{line}
	}
	// Defensive: if still too wide, truncate (don't hard-wrap — that breaks CJK)
	contentW := cw - pw
	if contentW <= 0 {
		return []string{line}
	}
	truncated := truncateToWidth(rest, contentW)
	return []string{prefix + truncated}
}

// splitGuidePrefix splits a rendered line into its guide prefix and the rest.
// Guide lines start with optional spaces then "┊ " (possibly ANSI-colored).
// Returns (prefix, rest, prefixDisplayWidth). If no guide, returns ("", line, 0).
func splitGuidePrefix(line string) (prefix, rest string, prefixW int) {
	i := 0
	n := len(line)
	inEscape := false
	foundPipe := false

	for i < n {
		b := line[i]
		if b == '\x1b' {
			inEscape = true
			i++
			continue
		}
		if inEscape {
			i++
			if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') {
				inEscape = false
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if !foundPipe {
			if r == '┊' {
				foundPipe = true
			} else if r != ' ' {
				// Not a guide line
				return "", line, 0
			}
		} else {
			// After ┊, expect a space
			if r == ' ' {
				end := i + size
				prefix = line[:end]
				rest = line[end:]
				prefixW = lipgloss.Width(prefix)
				return
			}
			// ┊ not followed by space — not a guide prefix
			return "", line, 0
		}
		i += size
	}
	return "", line, 0
}

// wrapDynamicPart wraps a dynamic content string (e.g. rewind block) into
// pre-wrapped lines for direct viewport assembly. This is the same logic as
// setViewportContent's dynamic-part wrapping, but returns []string directly.
func wrapDynamicPart(content string, cw int) []string {
	if content == "" {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != line {
			visualW := lipgloss.Width(line)
			trimmedW := lipgloss.Width(trimmed)
			if visualW == trimmedW {
				line = trimmed
			}
		}
		wrapped := wrapPreservingGuide(line, cw)
		lines = append(lines, wrapped...)
	}
	return lines
}

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.
// Lines wider than the viewport are truncated to prevent layout breakage.
func (m *cliModel) setViewportContent(content string) {
	// Deduplicate: skip if content and width haven't changed.
	// During resize storms or high-frequency ticks (busy state), this prevents
	// O(N*W) hardWrapRunes from running every 100ms on the same content.
	cw := m.chatWidth()
	if content == m.lastViewportContent && cw == m.lastViewportWidth && m.ready {
		return
	}
	m.lastViewportContent = content
	m.lastViewportWidth = cw

	// Track actual max width across all wrapped lines.
	// The performance optimization bypasses viewport's internal maxLineWidth()
	// scan (which was ~49% CPU), but we must pass the REAL max width — not cw —
	// to viewportSetLinesBypassMaxWidth. Otherwise viewport's visibleLines()
	// trusts longestLineWidth <= maxWidth and skips truncation. Any line wider
	// than maxWidth slips through, lipgloss.Wrap in viewport.View() re-wraps it,
	// producing extra rendered lines that overflow the viewport height and push
	// the input box off screen.
	maxW := 0
	var lines []string // pre-split lines to avoid viewport's strings.Split
	if cw > 0 {
		// Two-tier wrap: find the cachedHistory boundary in content.
		// The history portion is stable (doesn't change between ticks) — reuse
		// its wrapped version to avoid O(N*W) hardWrapRunes on the growing history.
		historyEnd := 0
		if len(m.cachedHistory) > 0 && strings.HasPrefix(content, m.cachedHistory) {
			historyEnd = len(m.cachedHistory)
		}

		if historyEnd > 0 && m.cachedWrappedHistoryRaw == m.cachedHistory && m.cachedWrappedHistoryWidth == cw {
			// Fast path: reuse cached history lines (avoids O(N) strings.Split every tick).
			// cachedHistoryLines is pre-split; only the dynamic suffix needs wrap.
			if len(m.cachedHistoryLines) > 0 {
				lines = make([]string, len(m.cachedHistoryLines))
				copy(lines, m.cachedHistoryLines)
				maxW = m.cachedHistoryMaxWidth
			}
			dynamicPart := content[historyEnd:]
			if dynamicPart != "" {
				for _, line := range strings.Split(dynamicPart, "\n") {
					trimmed := strings.TrimRight(line, " \t")
					if trimmed != line {
						visualW := lipgloss.Width(line)
						trimmedW := lipgloss.Width(trimmed)
						if visualW == trimmedW {
							line = trimmed
						}
					}
					wrapped := wrapPreservingGuide(line, cw)
					for _, wl := range wrapped {
						if w := lipgloss.Width(wl); w > maxW {
							maxW = w
						}
					}
					lines = append(lines, wrapped...)
				}
			}
		} else {
			// Slow path: wrap everything and cache the history portion
			rawLines := strings.Split(content, "\n")
			historyLineCount := 0
			if historyEnd > 0 {
				historyLineCount = strings.Count(m.cachedHistory, "\n")
				if len(m.cachedHistory) > 0 && m.cachedHistory[len(m.cachedHistory)-1] != '\n' {
					historyLineCount++
				}
			}
			var wrappedHistoryParts []string
			for i, line := range rawLines {
				trimmed := strings.TrimRight(line, " \t")
				if trimmed != line {
					visualW := lipgloss.Width(line)
					trimmedW := lipgloss.Width(trimmed)
					if visualW == trimmedW {
						line = trimmed
					}
				}
				wrapped := wrapPreservingGuide(line, cw)
				for _, wl := range wrapped {
					if w := lipgloss.Width(wl); w > maxW {
						maxW = w
					}
				}
				if i < historyLineCount {
					wrappedHistoryParts = append(wrappedHistoryParts, wrapped...)
				}
				lines = append(lines, wrapped...)
			}
			// Cache the wrapped history portion (both string and []string) for next tick
			if historyEnd > 0 && len(wrappedHistoryParts) > 0 {
				m.cachedWrappedHistory = strings.Join(wrappedHistoryParts, "\n") + "\n"
				m.cachedHistoryLines = wrappedHistoryParts
				m.cachedWrappedHistoryRaw = m.cachedHistory
				m.cachedWrappedHistoryWidth = cw
				m.cachedHistoryMaxWidth = maxW
			}
		}
	} else {
		lines = strings.Split(content, "\n")
	}

	// Use userScrolledUp to determine follow-bottom behavior.
	// AtBottom() alone can false-positive when content shrinks (maxYOffset
	// decreases below current yOffset). userScrolledUp is only set by
	// explicit user scroll-up actions, making it reliable.
	shouldFollowBottom := !m.userScrolledUp
	prevYOffset := m.viewport.YOffset()
	// Use SetContentLines with pre-split lines to avoid viewport's internal
	// strings.Split. We also bypass the expensive maxLineWidth scan inside
	// SetContentLines by directly setting the internal lines and width.
	viewportSetLinesBypassMaxWidth(&m.viewport, lines, maxW)
	if shouldFollowBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		// Defensive: if the viewport height was changed by autoExpandInput
		// or relayoutViewport, the yOffset may now exceed maxYOffset.
		// Restore user's previous scroll position, clamped to valid range.
		m.viewport.SetYOffset(prevYOffset)
		m.newContentHint = true
	}
}

// trimToolSummaryPayload releases large fields (ToolHints, Detail, Args) from
// tool_summary messages after rendering. The rendered output is cached in
// msg.rendered, so these multi-KB strings are no longer needed. Without this,
// N iterations × M tools × avg_diff_size causes O(N*M) GC pressure — the GC
// must scan all surviving strings every collection cycle, consuming CPU
// proportional to total iterations rather than viewport height.
func (m *cliModel) trimToolSummaryPayload(msg *cliMessage) {
	if msg.role != "tool_summary" || msg.dirty {
		return
	}
	for i := range msg.iterations {
		for k := range msg.iterations[i].Tools {
			msg.iterations[i].Tools[k].ToolHints = ""
			msg.iterations[i].Tools[k].Detail = ""
			msg.iterations[i].Tools[k].Args = ""
		}
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
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, " \t")
		count += strings.Count(hardWrapRunes(line, width), "\n") + 1
	}
	return count
}

// visibleTurnIndices 返回每个"对话轮次"的起始 slice 索引。
// 每个 turn 以 user 消息开头，包含之后所有的 assistant/tool_summary 消息
// 直到下一个 user 消息为止。tool_summary 自动归属其前面最近的 user 所在的 turn。
//
// 例如: [user(0), assistant(1), tool_summary(2), user(3), assistant(4)]
// turns: [0, 3] — 按"1"删最后 1 轮即 cutIdx=3，保留 [user(0), assistant(1), tool_summary(2)]
func visibleTurnIndices(messages []cliMessage) []int {
	var turns []int
	for i, msg := range messages {
		if msg.role == "user" {
			turns = append(turns, i)
		}
	}
	// 如果没有 user 消息但有其他消息，回退到旧逻辑（保留兼容）
	if len(turns) == 0 && len(messages) > 0 {
		turns = append(turns, 0)
	}
	return turns
}

// visibleMsgGroupIndices 是 visibleTurnIndices 的别名，保留向后兼容。
func visibleMsgGroupIndices(messages []cliMessage) []int {
	return visibleTurnIndices(messages)
}

// updateViewportContent 更新 viewport 显示内容（§1 增量渲染）
func (m *cliModel) updateViewportContent() {
	// 快速路径：流式消息 + 缓存有效
	if m.streamingMsgIdx >= 0 && m.renderCacheValid {
		m.updateStreamingOnly()
		return
	}

	// 快速路径：缓存有效 + 无流式消息 + 消息数未变，只刷新 progress block（tick 场景）
	if m.renderCacheValid && m.streamingMsgIdx < 0 && m.cachedMsgCount == len(m.messages) {
		// O(1) pre-check: compute composite FP without calling renderProgressBlock.
		// This avoids the O(N) renderProgressBlock call on every tick when nothing
		// changed (the dominant case during streaming within a single iteration).
		var elapsedSec int64
		if !m.typingStartTime.IsZero() {
			elapsedSec = time.Since(m.typingStartTime).Milliseconds() / 1000
		}
		bubbleWidth := m.chatWidth() - 4
		if bubbleWidth < 10 {
			bubbleWidth = 10
		}
		progressFP := m.progressBlockCompositeFP(elapsedSec, bubbleWidth)
		rewindBlock := m.renderRewindResultBlock()
		rewindFP := fnvHash64(rewindBlock)
		cachedHistoryLen := len(m.cachedHistory)
		if cachedHistoryLen == m.lastTickHistoryLen &&
			m.lastTickProgressFP == progressFP &&
			m.lastTickRewindFP == rewindFP {
			return
		}
		m.lastTickHistoryLen = cachedHistoryLen
		m.lastTickProgressFP = progressFP
		m.lastTickRewindFP = rewindFP

		// FP changed → call renderProgressBlock (it will also hit its own internal
		// cache via progressBlockCompositeFP, but we need the actual output string).
		progressBlock := m.renderProgressBlock()

		// --- Direct lines assembly with cached slice ---
		// Reuse the cached allLines slice across ticks to avoid O(N)
		// allocation + copy of cachedHistoryLines every 100ms.
		// Only progress/rewind sections are updated in-place.
		cw := m.chatWidth()
		if len(m.cachedHistoryLines) > 0 && cw > 0 {
			// Progress block lines: already pre-split by renderProgressBlock
			progressLines := m.cachedProgressBlockLines

			// Rewind block lines: cache wrapped version
			var rewindLines []string
			if rewindBlock == m.cachedDynamicRaw && cw == m.cachedDynamicWidth {
				rewindLines = m.cachedDynamicLines
			} else {
				rewindLines = wrapDynamicPart(rewindBlock, cw)
				m.cachedDynamicRaw = rewindBlock
				m.cachedDynamicLines = rewindLines
				m.cachedDynamicWidth = cw
			}

			// Reuse cached allLines when history section unchanged
			histLen := len(m.cachedHistoryLines)
			pl := len(progressLines)
			rl := len(rewindLines)
			totalLines := histLen + pl + rl

			if histLen == m.cachedAllLinesHistoryLen && totalLines <= cap(m.cachedAllLines) {
				// History unchanged — in-place update progress + rewind only
				m.cachedAllLines = m.cachedAllLines[:totalLines]
				copy(m.cachedAllLines[histLen:histLen+pl], progressLines)
				copy(m.cachedAllLines[histLen+pl:], rewindLines)
			} else if histLen > m.cachedAllLinesHistoryLen && cap(m.cachedAllLines) >= totalLines {
				// History grew (new iteration appended) — extend in-place.
				// Old history lines are unchanged, only append the new tail.
				newHistLines := m.cachedHistoryLines[m.cachedAllLinesHistoryLen:]
				m.cachedAllLines = m.cachedAllLines[:m.cachedAllLinesHistoryLen]
				m.cachedAllLines = append(m.cachedAllLines, newHistLines...)
				m.cachedAllLines = append(m.cachedAllLines, progressLines...)
				m.cachedAllLines = append(m.cachedAllLines, rewindLines...)
				m.cachedAllLinesHistoryLen = histLen
			} else if histLen > m.cachedAllLinesHistoryLen {
				// History grew but slice too small — grow with append
				m.cachedAllLines = m.cachedAllLines[:m.cachedAllLinesHistoryLen]
				m.cachedAllLines = append(m.cachedAllLines, m.cachedHistoryLines[m.cachedAllLinesHistoryLen:]...)
				m.cachedAllLines = append(m.cachedAllLines, progressLines...)
				m.cachedAllLines = append(m.cachedAllLines, rewindLines...)
				m.cachedAllLinesHistoryLen = histLen
			} else {
				// History shrank (rewind/compression) or first run — rebuild
				m.cachedAllLines = make([]string, totalLines)
				copy(m.cachedAllLines, m.cachedHistoryLines)
				copy(m.cachedAllLines[histLen:histLen+pl], progressLines)
				copy(m.cachedAllLines[histLen+pl:], rewindLines)
				m.cachedAllLinesHistoryLen = histLen
			}

			shouldFollowBottom := !m.userScrolledUp
			viewportSetLinesBypassMaxWidth(&m.viewport, m.cachedAllLines, cw)
			if shouldFollowBottom {
				m.viewport.GotoBottom()
				m.newContentHint = false
			} else {
				m.newContentHint = true
			}

			m.lastViewportWidth = cw
			return
		}

		// Fallback: string path (first tick before cachedHistoryLines is populated)
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(progressBlock)
		sb.WriteString(rewindBlock)
		m.setViewportContent(sb.String())
		return
	}

	// 快速路径：缓存有效 + 仅追加了新消息（无流式、无搜索）
	// 只渲染新增的 dirty 消息并追加到 cachedHistory，跳过全量重建。
	if m.renderCacheValid && m.streamingMsgIdx < 0 && !m.searchMode &&
		len(m.messages) > m.cachedMsgCount {
		m.appendNewMessagesToCache()
		return
	}

	// 慢速路径：全量重建
	m.fullRebuild()
}

// updateStreamingOnly 只重新渲染当前流式消息（快速路径）
func (m *cliModel) updateStreamingOnly() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// 只渲染当前流式消息
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
	cw := m.chatWidth()
	runningLines := 0
	if len(m.msgLineOffsets) > 0 {
		// Approximate: use the line count of cachedHistory at current width.
		// This is an estimate but sufficient for msgLineOffsets (used for Ctrl+E folding).
		runningLines = wrappedLineCount(m.cachedHistory, cw)
	}

	startIdx := m.cachedMsgCount
	for i := startIdx; i < len(m.messages); i++ {
		msg := &m.messages[i]
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		rendered := m.renderMessage(msg)
		msg.rendered = rendered
		msg.dirty = false
		msg.renderWidth = cw
		// Release large fields from tool_summary iterations after rendering.
		m.trimToolSummaryPayload(msg)
		sb.WriteString(rendered)
		runningLines += wrappedLineCount(rendered, cw)
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

// fullRebuild 全量重建渲染缓存（慢速路径）
func (m *cliModel) fullRebuild() {
	// splitIdx 确保当前流式消息不进入 cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 {
		splitIdx = m.streamingMsgIdx
	}

	// Fast path: if all messages are already cached at current width,
	// no re-rendering or re-wrapping is needed. Just rebuild cachedHistory
	// from per-message wrapped lines and rebuild msgLineOffsets.
	cw := m.chatWidth()
	allCached := splitIdx > 0
	for i := range m.messages[:splitIdx] {
		if m.messages[i].dirty || m.messages[i].renderWidth != cw || len(m.messages[i].wrappedLines) == 0 || m.messages[i].wrappedWidth != cw {
			allCached = false
			break
		}
	}
	if allCached {
		m.msgLineOffsets = m.msgLineOffsets[:0]
		runningLines := 0
		hmax := 0
		var allWrappedLines []string
		for i := range m.messages[:splitIdx] {
			m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
			wl := m.messages[i].wrappedLines
			if m.messages[i].wrappedMaxWidth > hmax {
				hmax = m.messages[i].wrappedMaxWidth
			}
			runningLines += len(wl)
			allWrappedLines = append(allWrappedLines, wl...)
		}
		var histBuf strings.Builder
		for _, line := range allWrappedLines {
			histBuf.WriteString(line)
			histBuf.WriteString("\n")
		}
		m.cachedHistory = histBuf.String()
		m.cachedHistoryLines = allWrappedLines
		m.cachedWrappedHistory = m.cachedHistory
		m.cachedWrappedHistoryRaw = m.cachedHistory
		m.cachedWrappedHistoryWidth = cw
		m.cachedHistoryMaxWidth = hmax
		m.renderCacheValid = true
		m.cachedMsgCount = len(m.messages)
		return
	}

	// §19 重置消息行号偏移（基于折行后的 viewport 行号）
	m.msgLineOffsets = m.msgLineOffsets[:0]
	runningLines := 0
	// cw already declared in fast path above

	// Collect wrapped lines incrementally to avoid the O(N) strings.Split +
	// lipgloss.Width + wrapPreservingGuide on the entire cachedHistory.
	// Each message contributes its own wrapped lines; cached messages reuse
	// their pre-computed wrapped lines (no re-parsing needed).
	var allWrappedLines []string
	hmax := 0
	for i := range m.messages[:splitIdx] {
		// §19 记录消息在 viewport 折行后内容中的起始行号
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != cw
		var rendered string
		if needsRender {
			rendered = m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = cw
			// Release large fields from tool_summary iterations after rendering.
			// The rendered output is cached in msg.rendered — ToolHints (diff),
			// Detail (tool output), and Args (raw JSON) are no longer needed.
			// Keeping them alive causes O(iterations × tool_size) GC pressure.
			m.trimToolSummaryPayload(&m.messages[i])
		} else {
			rendered = m.messages[i].rendered
		}
		// Wrap lines for this message only and collect into allWrappedLines.
		// For cached messages, pre-compute wrappedLines if not already set.
		msgWrapped := m.messages[i].wrappedLines
		msgMaxW := m.messages[i].wrappedMaxWidth
		if len(msgWrapped) == 0 || m.messages[i].wrappedWidth != cw {
			// Need to (re-)compute wrapped lines for this message
			rawLines := strings.Split(rendered, "\n")
			msgWrapped = make([]string, 0, len(rawLines))
			msgMaxW = 0
			for _, line := range rawLines {
				trimmed := strings.TrimRight(line, " \t")
				if trimmed != line {
					visualW := lipgloss.Width(line)
					trimmedW := lipgloss.Width(trimmed)
					if visualW == trimmedW {
						line = trimmed
					}
				}
				wrapped := wrapPreservingGuide(line, cw)
				for _, wl := range wrapped {
					if w := lipgloss.Width(wl); w > msgMaxW {
						msgMaxW = w
					}
				}
				msgWrapped = append(msgWrapped, wrapped...)
			}
			m.messages[i].wrappedLines = msgWrapped
			m.messages[i].wrappedMaxWidth = msgMaxW
			m.messages[i].wrappedWidth = cw
		}
		if msgMaxW > hmax {
			hmax = msgMaxW
		}
		// §21 搜索高亮：匹配消息前插入指示条
		if m.searchMode && m.isSearchMatch(i) {
			indicator := m.styles.SearchIndicator.Render("▸ ")
			allWrappedLines = append(allWrappedLines, indicator)
			runningLines++
			if w := lipgloss.Width(indicator); w > hmax {
				hmax = w
			}
		}
		runningLines += len(msgWrapped)
		allWrappedLines = append(allWrappedLines, msgWrapped...)
	}

	// Rebuild cachedHistory from allWrappedLines for setViewportContent.
	// This is O(total_lines) string join — unavoidable but much cheaper than
	// the previous O(total_content_chars) approach of re-parsing ANSI codes.
	var histBuf strings.Builder
	for _, line := range allWrappedLines {
		histBuf.WriteString(line)
		histBuf.WriteString("\n")
	}
	m.cachedHistory = histBuf.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// All wrapped lines are already computed per-message above.
	// Set the cache directly — no need to re-split + re-wrap the entire
	// cachedHistory string (that was the O(N) bottleneck).
	m.cachedHistoryLines = allWrappedLines
	m.cachedWrappedHistory = m.cachedHistory
	m.cachedWrappedHistoryRaw = m.cachedHistory
	m.cachedWrappedHistoryWidth = cw
	m.cachedHistoryMaxWidth = hmax

	// 拼接最终内容：历史 + 当前流式消息（如有） + progress block + rewind result
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderProgressBlock())
	sb.WriteString(m.renderRewindResultBlock())

	m.setViewportContent(sb.String())
}

// isSearchMatch 检查消息是否匹配当前搜索（§21）
func (m *cliModel) isSearchMatch(idx int) bool {
	for _, si := range m.searchResults {
		if si == idx {
			return true
		}
	}
	return false
}

// toggleMessageFold 批量切换所有 assistant 消息的折叠状态（§19）
// 如果当前有任一长消息未折叠 → 全部折叠；否则 → 全部展开。
func (m *cliModel) toggleMessageFold() {
	if len(m.messages) == 0 {
		return
	}
	// 决定目标状态：如果存在任何未折叠的长消息，则全部折叠
	anyUnfolded := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role == "assistant" && !msg.isPartial && !msg.folded {
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

// enterSearchMode 进入搜索模式（§21）
func (m *cliModel) enterSearchMode() {
	ti := textinput.New()
	ti.Placeholder = m.locale.SearchPlaceholder
	ti.Prompt = "/"
	ti.CharLimit = 100
	ti.Focus()
	w := m.chatWidth() - 20
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

// executeSearch 执行搜索（§21）
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
		if msg.role == "system" {
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

// exitSearch 退出搜索模式（§21）
func (m *cliModel) exitSearch() {
	m.searchMode = false
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.searchEditing = false
	m.renderCacheValid = false
	m.updateViewportContent()
}

// jumpToSearchResult 跳转到指定搜索结果（§21）
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

// typewriterTickCmd returns a command that advances the typewriter by 1 rune every 50ms.
// Runs independently from the main tick to give the typewriter its own update frequency.
func typewriterTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return typewriterTickMsg{}
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

// renderToolHint renders plugin-provided or built-in hint content.
// For ```diff code blocks, renders with line numbers and theme backgrounds (crush-style).
// For other markdown, renders with glamour.
// maxLines caps diff line rendering (0 = unlimited).
func (m *cliModel) renderToolHint(md string, maxW, maxLines int) (string, error) {
	if md == "" {
		return "", nil
	}
	// Diff: use provided width (no guide prefix, fills full width like crush)
	if strings.Contains(md, "```diff") {
		w := maxW
		if w < 40 {
			w = 40
		}
		return renderDiffStyled(md, w, maxLines), nil
	}
	// Non-diff markdown: render with glamour
	rendered, err := m.renderer.Render(md)
	if err != nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary)).Render(md), nil
	}
	return strings.TrimSpace(rendered), nil
}

// renderToolBody renders tool-specific body content below the tool line.
// Dispatches to specialized renderers based on tool name (crush-style per-tool rendering).
// Returns empty string if no body content should be shown.
func (m *cliModel) renderToolBody(tool protocol.ToolProgress, maxW int) string {
	if tool.Status == "error" {
		return "" // errors shown in the tool line itself
	}
	t := *currentTheme
	switch tool.Name {
	case "Read":
		return m.renderReadBody(tool, maxW, t)
	case "Shell":
		return m.renderShellBody(tool, maxW, t)
	case "Grep":
		return m.renderGrepBody(tool, maxW, t)
	case "Glob":
		return m.renderGlobBody(tool, maxW, t)
	}
	return ""
}

const toolBodyMaxLines = 10

// highlightCode performs Chroma syntax highlighting on code content.
// filePath is used to select the lexer; falls back to plain text if no match.
// Each token is rendered with lipgloss including the background color (crush xchroma approach).
// Returns a slice of highlighted lines (split by \n).
func highlightCode(content string, filePath string) []string {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Analyse(content)
	}
	if lexer == nil {
		return nil // no match → caller uses plain rendering
	}
	lexer = chroma.Coalesce(lexer)

	it, err := lexer.Tokenise(nil, content)
	if err != nil {
		return nil
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	// Walk tokens, format each with lipgloss (foreground only, no background), split by newline
	var lineBuf strings.Builder
	var result []string

	for _, tok := range it.Tokens() {
		if tok == chroma.EOF {
			break
		}
		entry := style.Get(tok.Type)
		s := lipgloss.NewStyle()
		if entry.Bold == chroma.Yes {
			s = s.Bold(true)
		}
		if entry.Italic == chroma.Yes {
			s = s.Italic(true)
		}
		if entry.Underline == chroma.Yes {
			s = s.Underline(true)
		}
		if entry.Colour.IsSet() {
			s = s.Foreground(lipgloss.Color(entry.Colour.String()))
		}

		val := tok.Value
		for val != "" {
			nl := strings.IndexByte(val, '\n')
			if nl < 0 {
				lineBuf.WriteString(s.Render(val))
				break
			}
			if nl > 0 {
				lineBuf.WriteString(s.Render(val[:nl]))
			}
			result = append(result, lineBuf.String())
			lineBuf.Reset()
			val = val[nl+1:]
		}
	}
	if lineBuf.Len() > 0 {
		result = append(result, lineBuf.String())
	}
	return result
}

// renderReadBody renders Read tool output as code with line numbers and syntax highlighting.
// The Read tool output (Detail/Summary) already has line numbers in format "%*d\t<code>".
// We parse those to extract pure code, highlight it with Chroma, then render with our own line numbers.
func (m *cliModel) renderReadBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}

	// Parse args for file path
	filePath := ""
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(tool.Args), &args) == nil {
		filePath = args.Path
	}

	// Read tool output has line numbers: "%*d\t<code>"
	// Parse to extract line numbers and pure code
	rawLines := strings.Split(content, "\n")
	if len(rawLines) == 0 || (len(rawLines) == 1 && rawLines[0] == "") {
		return ""
	}

	type parsedLine struct {
		num  int
		code string
	}
	var parsed []parsedLine

	for _, line := range rawLines {
		matches := readLineNumRe.FindStringSubmatch(line)
		if matches != nil {
			num, _ := strconv.Atoi(matches[1])
			parsed = append(parsed, parsedLine{num: num, code: matches[2]})
		}
		// Skip non-matching lines (e.g. truncation messages)
	}

	if len(parsed) == 0 {
		return "" // no parseable content
	}

	totalLines := len(parsed)
	displayParsed := parsed
	if len(displayParsed) > toolBodyMaxLines {
		displayParsed = displayParsed[:toolBodyMaxLines]
	}

	// Try Chroma highlighting on pure code
	pureCode := make([]string, len(parsed))
	for i, p := range parsed {
		pureCode[i] = p.code
	}
	pureCodeStr := strings.Join(pureCode, "\n")
	hlLines := highlightCode(pureCodeStr, filePath)

	// Layout calculations
	maxLineNum := parsed[totalLines-1].num
	digits := numDigits(maxLineNum)
	numFmt := fmt.Sprintf("%%%dd ", digits)
	lineNumW := digits + 1

	fgLineNum := lipgloss.Color(t.TextMuted)

	codeW := maxW - lineNumW
	if codeW < 10 {
		codeW = 10
	}

	var sb strings.Builder
	for i, p := range displayParsed {
		lineNumText := fmt.Sprintf(numFmt, p.num)
		lineNumText = strings.ReplaceAll(lineNumText, " ", "\u00a0")
		lineNum := lipgloss.NewStyle().Foreground(fgLineNum).Render(lineNumText)

		var codeLine string
		if hlLines != nil && i < len(hlLines) {
			codeLine = ansi.Truncate(hlLines[i], codeW, "")
		} else {
			codeLine = lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary)).
				Render(ansi.Truncate(p.code, codeW, ""))
		}
		sb.WriteString(lineNum + codeLine)
		sb.WriteString("\n")
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgLineNum).
			Width(maxW).Render(fmt.Sprintf("  ... %d more lines", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderShellBody renders Shell tool output with command indicator.
func (m *cliModel) renderShellBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	// Parse args for command
	command := ""
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(tool.Args), &args) == nil {
		command = args.Command
	}

	fgPrompt := lipgloss.Color(t.TextMuted)

	var sb strings.Builder

	// Show command
	if command != "" {
		commandLine := "$ " + ansi.Truncate(command, maxW-5, "") + "..."
		for _, wl := range strings.Split(hardWrapRunes(commandLine, maxW), "\n") {
			sb.WriteString(lipgloss.NewStyle().Foreground(fgPrompt).Render(wl))
			sb.WriteString("\n")
		}
	}

	// Show output (wrapped to fit)
	// Progress bars (tqdm etc.) use \r to overwrite the same line.
	// When captured as output, \r-embedded lines confuse the terminal:
	// \r moves cursor to column 0, overwriting the guide prefix.
	// sanitizeOutputLine handles \r stripping and ANSI removal.
	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}
	outputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary))
	for _, line := range displayLines {
		line = sanitizeOutputLine(line)
		// Skip empty lines after sanitization (fully overwritten frames)
		if strings.TrimSpace(line) == "" {
			continue
		}
		for _, wl := range strings.Split(hardWrapRunes(line, maxW), "\n") {
			sb.WriteString(outputStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgPrompt).
			Width(maxW).Render(fmt.Sprintf("  ... %d more lines", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderGrepBody renders Grep tool output with highlighted matches.
func (m *cliModel) renderGrepBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	fgMeta := lipgloss.Color(t.TextMuted)

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}

	var sb strings.Builder
	grepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextPrimary))
	for _, line := range displayLines {
		for _, wl := range strings.Split(hardWrapRunes(line, maxW), "\n") {
			sb.WriteString(grepStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).
			Width(maxW).Render(fmt.Sprintf("  ... %d more matches", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderGlobBody renders Glob tool output as a file list.
func (m *cliModel) renderGlobBody(tool protocol.ToolProgress, maxW int, t cliTheme) string {
	content := tool.Detail
	if content == "" {
		content = tool.Summary
	}
	if content == "" {
		return ""
	}
	fgFile := lipgloss.Color(t.TextPrimary)
	fgMeta := lipgloss.Color(t.TextMuted)

	lines := strings.Split(content, "\n")
	totalLines := len(lines)
	displayLines := lines
	if len(displayLines) > toolBodyMaxLines {
		displayLines = displayLines[:toolBodyMaxLines]
	}

	var sb strings.Builder
	indentStyle := lipgloss.NewStyle().Foreground(fgMeta)
	fileStyle := lipgloss.NewStyle().Foreground(fgFile)
	for _, line := range displayLines {
		// Account for "  " indent: wrap file path to maxW-2, then prepend indent.
		for _, wl := range strings.Split(hardWrapRunes(line, maxW-2), "\n") {
			sb.WriteString(indentStyle.Render("  "))
			sb.WriteString(fileStyle.Render(wl))
			sb.WriteString("\n")
		}
	}
	if totalLines > toolBodyMaxLines {
		hidden := totalLines - toolBodyMaxLines
		sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).
			Render(fmt.Sprintf("  ... %d more files", hidden)))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// numDigits returns the number of digits in n (minimum 1).
func numDigits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}

// padBgRight appends background-colored non-breaking spaces after content.
// Do NOT use ordinary spaces for terminal background fills: some renderers/terminals
// trim or don't paint trailing regular spaces at EOL. NBSP is visually blank but
// remains an actual cell, so the background is painted and selectable.
func padBgRight(content string, bgHex string, targetWidth int) string {
	visualW := lipgloss.Width(content)
	pad := targetWidth - visualW
	if pad <= 0 {
		return content
	}
	padding := lipgloss.NewStyle().Background(lipgloss.Color(bgHex)).Render(strings.Repeat("\u00a0", pad))
	return content + padding
}

// expandTabs replaces tab characters with spaces, respecting ANSI escape
// sequences so that escape codes don't affect tab-stop calculation.
func expandTabs(s string, tabWidth int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			b.WriteRune(r)
			continue
		}
		if inEscape {
			b.WriteRune(r)
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		if r == '\t' {
			spaces := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
		} else {
			b.WriteRune(r)
			col += uniseg.StringWidth(string(r))
		}
	}
	return b.String()
}

func renderBgLine(content string, fgHex string, bgHex string, targetWidth int) string {
	content = ansi.Truncate(content, targetWidth, "")
	st := lipgloss.NewStyle().Background(lipgloss.Color(bgHex))
	if fgHex != "" {
		st = st.Foreground(lipgloss.Color(fgHex))
	}
	return padBgRight(st.Render(content), bgHex, targetWidth)
}

// renderDiffStyled renders diff content with syntax highlighting, line numbers
// and theme semantic backgrounds. Uses the same approach as crush's xchroma:
// Chroma tokens are formatted with lipgloss including the diff background color,
// so ANSI codes are always correct without manual escape management.
// maxLines caps the number of diff lines rendered (0 = unlimited).
func renderDiffStyled(md string, maxW, maxLines int) string {
	if maxW < 40 {
		maxW = 40
	}
	t := currentTheme
	lines := strings.Split(md, "\n")

	// Extract diff content from ```diff ... ``` block
	var diffLines []string
	inDiff := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inDiff && trimmed == "```diff" {
			inDiff = true
			continue
		}
		if inDiff && trimmed == "```" {
			break
		}
		if inDiff {
			diffLines = append(diffLines, line)
		}
	}
	if len(diffLines) == 0 {
		trimmed := strings.TrimSpace(md)
		if trimmed == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(t.TextSecondary)).Render(trimmed)
	}

	// Cap diff lines before expensive chroma tokenisation.
	if maxLines > 0 && len(diffLines) > maxLines {
		diffLines = diffLines[:maxLines]
	}

	// --- Extract file path for syntax highlighting ---
	filePath := ""
	for _, line := range diffLines {
		if strings.HasPrefix(line, "--- a/") {
			filePath = line[6:]
		} else if strings.HasPrefix(line, "--- /dev/null") {
			filePath = ""
		}
		if strings.HasPrefix(line, "+++ b/") {
			if filePath == "" {
				filePath = line[6:]
			}
			break
		}
	}

	// --- Theme colors ---
	bgAdd := lipgloss.Color(t.SuccessBg)
	bgDel := lipgloss.Color(t.ErrorBg)
	fgAdd := lipgloss.Color(t.Success)
	fgDel := lipgloss.Color(t.Error)
	fgMeta := lipgloss.Color(t.TextMuted)

	// --- Syntax highlighting per line type (crush xchroma approach) ---
	// Highlight code with background baked in via lipgloss, so ANSI codes are clean.
	// Context lines use empty bg → transparent (no background color).
	highlightMap := diffHighlightLines(diffLines, filePath, t.SuccessBg, t.ErrorBg, "")

	oldLine := 0
	newLine := 0
	lineNumDigits := 3

	for _, line := range diffLines {
		if matches := diffHunkRe.FindStringSubmatch(line); matches != nil {
			os, _ := strconv.Atoi(matches[1])
			oc, _ := strconv.Atoi(matches[2])
			if oc == 0 {
				oc = 1
			}
			ns, _ := strconv.Atoi(matches[3])
			nc, _ := strconv.Atoi(matches[4])
			if nc == 0 {
				nc = 1
			}
			maxNum := os + oc
			if ns+nc > maxNum {
				maxNum = ns + nc
			}
			d := numDigits(maxNum)
			if d > lineNumDigits {
				lineNumDigits = d
			}
		}
	}

	numFmt := fmt.Sprintf("%%%dd", lineNumDigits)
	lineNumColW := lineNumDigits*2 + 2
	codeW := maxW - lineNumColW - 2
	if codeW < 10 {
		codeW = 10
	}

	lineNumStyleAdd := lipgloss.NewStyle().Foreground(fgMeta).Background(bgAdd)
	lineNumStyleDel := lipgloss.NewStyle().Foreground(fgMeta).Background(bgDel)
	lineNumStyleCtx := lipgloss.NewStyle().Foreground(fgMeta)
	symStyleAdd := lipgloss.NewStyle().Background(bgAdd).Foreground(fgAdd)
	symStyleDel := lipgloss.NewStyle().Background(bgDel).Foreground(fgDel)

	var sb strings.Builder
	for i, line := range diffLines {
		if strings.HasPrefix(line, `\ `) {
			continue
		}

		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			sb.WriteString(lipgloss.NewStyle().Foreground(fgMeta).Faint(true).Render(ansi.Truncate(line, maxW, "")))

		case strings.HasPrefix(line, "@@"):
			if matches := diffHunkRe.FindStringSubmatch(line); matches != nil {
				oldLine, _ = strconv.Atoi(matches[1])
				newLine, _ = strconv.Atoi(matches[3])
			}
			sb.WriteString(renderBgLine(line, t.Info, t.Border, maxW))

		case strings.HasPrefix(line, "+"):
			code := line[1:]
			if hl, ok := highlightMap[i]; ok {
				code = lipgloss.NewStyle().Background(bgAdd).Render(hl)
			} else {
				code = lipgloss.NewStyle().Background(bgAdd).Render(code)
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := strings.Repeat("\u00a0", lineNumDigits)
			newNum := fmt.Sprintf(numFmt, newLine)
			lineNums := lineNumStyleAdd.Render(oldNum + " " + newNum + " ")
			sym := symStyleAdd.Render("+ ")
			codeStyled := padBgRight(code, t.SuccessBg, codeW)
			sb.WriteString(lineNums + sym + codeStyled)
			newLine++

		case strings.HasPrefix(line, "-"):
			code := line[1:]
			if hl, ok := highlightMap[i]; ok {
				code = lipgloss.NewStyle().Background(bgDel).Render(hl)
			} else {
				code = lipgloss.NewStyle().Background(bgDel).Render(code)
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := fmt.Sprintf(numFmt, oldLine)
			newNum := strings.Repeat("\u00a0", lineNumDigits)
			lineNums := lineNumStyleDel.Render(oldNum + " " + newNum + " ")
			sym := symStyleDel.Render("- ")
			codeStyled := padBgRight(code, t.ErrorBg, codeW)
			sb.WriteString(lineNums + sym + codeStyled)
			oldLine++

		default:
			code := ""
			if len(line) > 0 {
				code = line[1:] // strip diff prefix char (space for context lines)
			}
			if hl, ok := highlightMap[i]; ok {
				code = hl
			}
			code = expandTabs(code, 4)
			code = ansi.Truncate(code, codeW, "")
			oldNum := fmt.Sprintf(numFmt, oldLine)
			newNum := fmt.Sprintf(numFmt, newLine)
			lineNums := lineNumStyleCtx.Render(oldNum + " " + newNum + " ")
			sb.WriteString(lineNums + "  " + code)
			oldLine++
			newLine++
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// diffHighlightLines performs Chroma-based syntax highlighting on code lines within a diff.
// Uses crush's xchroma approach: each token is rendered via lipgloss with the diff background
// color baked in, producing clean ANSI output that lipgloss can measure/Width correctly.
// Returns a map from diff line index to highlighted code content (plain code, no +/- prefix).
func diffHighlightLines(diffLines []string, filePath string, bgAdd, bgDel, bgCtx string) map[int]string {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		return nil
	}
	lexer = chroma.Coalesce(lexer)

	// Collect code spans with their line types
	type codeSpan struct {
		diffIdx int
		code    string
		bgHex   string
	}
	var spans []codeSpan
	var joined strings.Builder
	for i, line := range diffLines {
		code := ""
		bg := bgCtx
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "@@"):
			continue
		case strings.HasPrefix(line, "+"):
			code = line[1:]
			bg = bgAdd
		case strings.HasPrefix(line, "-"):
			code = line[1:]
			bg = bgDel
		default:
			if len(line) > 0 {
				code = line[1:] // strip diff prefix space for context lines
			}
		}
		if code != "" {
			spans = append(spans, codeSpan{diffIdx: i, code: code, bgHex: bg})
			joined.WriteString(code)
			joined.WriteString("\n")
		}
	}

	if joined.Len() == 0 {
		return nil
	}

	it, err := lexer.Tokenise(nil, joined.String())
	if err != nil {
		return nil
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	// Walk tokens, split by newline, format each token with lipgloss + background
	result := make(map[int]string, len(spans))
	var lineBuf strings.Builder
	spanIdx := 0

	flushLine := func() {
		if spanIdx < len(spans) {
			result[spans[spanIdx].diffIdx] = lineBuf.String()
		}
		lineBuf.Reset()
		spanIdx++
	}

	formatWithBg := func(tok chroma.Token, bgHex string) {
		entry := style.Get(tok.Type)
		s := lipgloss.NewStyle()
		if bgHex != "" {
			s = s.Background(lipgloss.Color(bgHex))
		}
		if entry.Bold == chroma.Yes {
			s = s.Bold(true)
		}
		if entry.Italic == chroma.Yes {
			s = s.Italic(true)
		}
		if entry.Underline == chroma.Yes {
			s = s.Underline(true)
		}
		if entry.Colour.IsSet() {
			s = s.Foreground(lipgloss.Color(entry.Colour.String()))
		}
		lineBuf.WriteString(s.Render(tok.Value))
	}

	for _, tok := range it.Tokens() {
		if tok == chroma.EOF {
			break
		}
		val := tok.Value
		for val != "" {
			nl := strings.IndexByte(val, '\n')
			if nl < 0 {
				if spanIdx < len(spans) {
					formatWithBg(chroma.Token{Type: tok.Type, Value: val}, spans[spanIdx].bgHex)
				}
				break
			}
			if nl > 0 {
				if spanIdx < len(spans) {
					formatWithBg(chroma.Token{Type: tok.Type, Value: val[:nl]}, spans[spanIdx].bgHex)
				}
			}
			flushLine()
			val = val[nl+1:]
		}
	}

	if lineBuf.Len() > 0 && spanIdx < len(spans) {
		result[spans[spanIdx].diffIdx] = lineBuf.String()
	}

	return result
}
