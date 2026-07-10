package cli

import (
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"xbot/session"
	"xbot/tools"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
)

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
		m.rc.history = ""
		m.exitSearch()

	case "/copy":
		// /copy — Copy message content to system clipboard.
		//   /copy          — copy last assistant reply
		//   /copy last     — same as above
		//   /copy all      — copy entire conversation (user + assistant)
		subCmd := ""
		if len(parts) > 1 {
			subCmd = strings.ToLower(parts[1])
		}
		var text string
		switch subCmd {
		case "", "last":
			// Find the last non-partial assistant message.
			for i := len(m.messages) - 1; i >= 0; i-- {
				msg := m.messages[i]
				if msg.role == "assistant" && !msg.isPartial && msg.content != "" {
					text = msg.content
					break
				}
			}
			if text == "" {
				return m.enqueueToast(m.locale.CopyNoAssistant, IconCross)
			}
		case "all":
			var lines []string
			for _, msg := range m.messages {
				if msg.role != "user" && msg.role != "assistant" {
					continue
				}
				if msg.isPartial || msg.content == "" {
					continue
				}
				prefix := "assistant"
				if msg.role == "user" {
					prefix = "user"
				}
				lines = append(lines, "["+prefix+"]\n"+msg.content)
			}
			if len(lines) == 0 {
				return m.enqueueToast(m.locale.CopyNoMessages, IconCross)
			}
			text = strings.Join(lines, "\n\n")
		default:
			m.showSystemMsg(m.locale.CopyUsage, feedbackInfo)
			return nil
		}
		go func() {
			if err := clipboard.WriteAll(text); err != nil {
				if m.channel != nil {
					m.channel.SendToast(m.locale.CopyFailed+": "+err.Error(), IconCross)
				}
				return
			}
			if m.channel != nil {
				m.channel.SendToast(m.locale.CopySuccess, IconCheck)
			}
		}()
		return nil
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
				m.panelState.settings.isSetup = false // regular settings, not setup wizard
				m.openSettingsPanel(schema, currentValues, func(values map[string]string) {
					// --- ch.Subscription generation guard ---
					// If the active subscription changed since this panel was opened,
					// the per-subscription LLM fields (provider/key/model/base_url) are STALE
					// and must NOT be written back — they would overwrite the new subscription.
					// This is the structural guarantee against subscription data corruption.
					if m.panelState.settings.subGeneration != m.subGeneration {
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
		if !m.sendInbound(m.newInbound("/compress", nil)) {
			return nil // send failed — connState already set to disconnected
		}
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
		// /su — Switch session/identity:
		//   /su                        — 切回默认身份（cli_user）
		//   /su <userID>               — 切换到指定用户身份
		//   /su web:<senderID>[:<token>] — 切换到 Web 端用户
		//   /su <channel>:<chatID>     — 切换到任意渠道的 session（admin 跨渠道，如 feishu:oc_xxx）
		m.saveCurrentSession()
		if len(parts) < 2 {
			if m.senderID == "cli_user" && m.chatID == m.defaultChatID && m.channelName == "cli" {
				m.showSystemMsg(m.locale.SuAlreadyDefault, feedbackInfo)
				return nil
			}
			m.senderID = "cli_user"
			m.chatID = m.defaultChatID
			m.channelName = "cli"
			// 切回默认时同步重置 workDir（header bar 显示用）
			if wd, _ := ParseChatID(m.defaultChatID); wd != "" {
				m.workDir = wd
			}
		} else {
			arg := strings.TrimSpace(parts[1])
			// Detect "channel:chatID" format for cross-channel session switching.
			if idx := strings.Index(arg, ":"); idx > 0 {
				chName := arg[:idx]
				rest := arg[idx+1:]
				if rest == "" {
					m.showSystemMsg("❌ 格式: /su <channel>:<chatID>（chatID 不能为空）", feedbackInfo)
					return nil
				}
				switch chName {
				case "web":
					// web:<senderID>[:<token>] — 保持兼容
					webParts := strings.SplitN(rest, ":", 2)
					if webParts[0] == "" {
						m.showSystemMsg("❌ 格式: /su web:<senderID>[:<token>]", feedbackInfo)
						return nil
					}
					m.channelName = "web"
					m.senderID = webParts[0]
					m.chatID = webParts[0]
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到 Web 用户: %s", webParts[0]), feedbackInfo)
				default:
					// 通用跨渠道切换: <channel>:<chatID>（如 feishu:oc_xxx, cli:/path）
					m.channelName = chName
					m.chatID = rest
					m.senderID = rest
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到 %s session: %s", chName, rest), feedbackInfo)
				}
				// 跨渠道切换：workDir 保持为 defaultChatID 的 workDir，
				// 避免 header bar 显示非路径的 chatID（如 feishu 的 oc_xxx）。
				if wd, _ := ParseChatID(m.defaultChatID); wd != "" {
					m.workDir = wd
				}
			} else {
				// 无冒号 → 普通用户ID切换（保持原有行为）
				if arg == "cli_user" || arg == "" {
					m.senderID = "cli_user"
					m.chatID = m.defaultChatID
					m.channelName = "cli"
				} else {
					m.senderID = arg
					m.chatID = arg
					m.channelName = "cli"
				}
			}
		}
		// Reset critical state after identity switch, then apply unified setup.
		m.messages = nil
		m.invalidateAllCache(false)
		m.scheduleSessionsRefresh() // 联动侧边栏：确保新 session 出现在列表且标记为活跃
		m.restoreSession()
		cmds := m.postRestoreSessionSetup()
		return tea.Batch(cmds...)

	case "/list-sessions":
		// /list-sessions — Admin-only: list ALL backend sessions across all channels.
		// Output format: "channel:chatID" — directly usable by /su.
		if m.isAdminFn == nil || !m.isAdminFn() {
			m.showSystemMsg("❌ /list-sessions 仅限 admin 使用", feedbackWarning)
			return nil
		}
		if m.channel == nil || m.channel.config.ListAllTenantsFn == nil {
			m.showSystemMsg("❌ 无法获取后端 session 列表", feedbackInfo)
			return nil
		}
		sessions, err := m.channel.config.ListAllTenantsFn()
		if err != nil {
			m.showSystemMsg("❌ 查询失败: "+err.Error(), feedbackInfo)
			return nil
		}
		if len(sessions) == 0 {
			m.showSystemMsg("（后端没有任何 session）", feedbackInfo)
			return nil
		}
		var lines []string
		lines = append(lines, fmt.Sprintf("📋 后端共 %d 个 session（可用 /su <channel>:<chatID> 切换）：", len(sessions)))
		for _, s := range sessions {
			active := ""
			if s.Channel == m.channelName && s.ChatID == m.chatID {
				active = " ← 当前"
			}
			label := s.Label
			if label == "" {
				label = "(无名称)"
			}
			model := s.Model
			if model != "" {
				model = "  模型: " + model
			}
			lastActive := s.LastActiveAt
			if lastActive != "" {
				lastActive = "  活跃: " + lastActive
			}
			lines = append(lines, fmt.Sprintf("  • %s:%s  名称: %s%s%s%s",
				s.Channel, s.ChatID, label, model, lastActive, active))
		}
		m.showSystemMsg(strings.Join(lines, "\n"), feedbackInfo)
		return nil

	case "/ss", "/sessions":
		// /ss — Open Sessions panel
		m.openSessionsPanel()
	case "/rename":
		// /rename — Rename current session (DB label):
		//   /rename <new name>  — rename current session
		if len(parts) < 2 {
			m.showSystemMsg("用法: /rename <新名称>", feedbackInfo)
			return nil
		}
		newName := strings.TrimSpace(strings.Join(parts[1:], " "))
		if newName == "" {
			m.showSystemMsg("❌ 名称不能为空", feedbackInfo)
			return nil
		}
		if m.channel != nil && m.channel.config.ChatRenameFn != nil {
			if err := m.channel.config.ChatRenameFn(m.channelName, m.chatID, newName); err != nil {
				m.showSystemMsg("❌ 重命名失败: "+err.Error(), feedbackInfo)
				return nil
			}
			m.scheduleSessionsRefresh()
			m.showSystemMsg(fmt.Sprintf("✅ 会话已重命名为: %s", newName), feedbackInfo)
		} else {
			m.showSystemMsg("❌ 重命名功能不可用", feedbackInfo)
		}

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

	case "/link-account":
		// /link-account <code> — link this CLI identity to another channel's user
		if len(parts) < 2 {
			m.showSystemMsg("用法: /link-account <code>\n\n在 Web 端「设置 → 账号关联」生成关联码，然后在此处输入。", feedbackWarning)
			return nil
		}
		code := parts[1]
		m.sendToAgent("/link-account " + code) // passthrough to agent-level handler

	case "/unlink-account":
		m.sendToAgent("/unlink-account") // passthrough to agent-level handler

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
				m.easterEggState.customArt = art
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
