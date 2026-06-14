package cli

import (
	"fmt"
	"strings"
	ch "xbot/channel"

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
							opts := make([]ch.SettingOption, len(allModels))
							for j, ml := range allModels {
								opts[j] = ch.SettingOption{Label: ml, Value: ml}
							}
							schema[i].Options = opts
						}
					}
				}
				m.panelIsSetup = false // regular settings, not setup wizard
				m.openSettingsPanel(schema, currentValues, func(values map[string]string) {
					// --- ch.Subscription generation guard ---
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
