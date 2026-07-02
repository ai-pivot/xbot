package cli

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"xbot/llm"
)

// openRunnerPanel 打开 Runner 管理面板
func (m *cliModel) openRunnerPanel() {
	m.panelState.mode = "runner"
	m.panelState.scrollY = 0
	m.relayoutViewport()

	// 确保 RunnerBridge 存在（正常 TUI 模式也需要，不只在 --share 时）
	if m.runnerBridge == nil && m.channel != nil {
		m.channel.ensureRunnerBridge()
	}

	// 初始化 textinput 字段
	serverURL := ""
	token := ""
	workspace := m.workDir

	// 从统一设置视图中读取已保存的值
	vals := m.mergeCLISettingsValues()
	if v, ok := vals["runner_server"]; ok && v != "" {
		serverURL = v
	}
	if v, ok := vals["runner_token"]; ok && v != "" {
		token = v
	}
	if v, ok := vals["runner_workspace"]; ok && v != "" {
		workspace = v
	}

	m.panelState.runner.runnerServerTI = m.newPanelTextInput(serverURL, m.locale.RunnerServerPlaceholder)
	m.panelState.runner.runnerTokenTI = m.newPanelTextInput(token, m.locale.RunnerTokenPlaceholder)
	m.panelState.runner.runnerTokenTI.EchoMode = 0 // password mode
	m.panelState.runner.runnerTokenTI.EchoCharacter = '•'
	m.panelState.runner.runnerWS = m.newPanelTextInput(workspace, m.locale.RunnerWorkspacePlaceholder)
	m.panelState.runner.runnerEditField = 0
}

// updateRunnerPanel 处理 Runner 面板的键盘事件
func (m *cliModel) updateRunnerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// Esc/popPanel 回到 parent 面板；Ctrl+C 关闭所有
	if msg.String() == "ctrl+c" {
		return m.closePanelAndResume()
	}
	if msg.Code == tea.KeyEsc {
		// Clean up runner panel state
		m.panelState.runner.runnerServerTI = textinput.Model{}
		m.panelState.runner.runnerTokenTI = textinput.Model{}
		m.panelState.runner.runnerWS = textinput.Model{}
		m.panelState.runner.runnerEditField = 0
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil
	}

	// runnerBridge 为 nil 时只显示表单，不允许连接操作
	if m.runnerBridge == nil {
		// 将按键传递给当前编辑的 textinput
		var cmd tea.Cmd
		switch m.panelState.runner.runnerEditField {
		case 0:
			m.panelState.runner.runnerServerTI, cmd = m.panelState.runner.runnerServerTI.Update(msg)
		case 1:
			m.panelState.runner.runnerTokenTI, cmd = m.panelState.runner.runnerTokenTI.Update(msg)
		case 2:
			m.panelState.runner.runnerWS, cmd = m.panelState.runner.runnerWS.Update(msg)
		}
		return true, m, cmd
	}

	status := m.runnerBridge.Status()

	// 连接中：只允许 Esc（已处理）
	if status == RunnerConnecting {
		return true, m, nil
	}

	// 已连接：断开按钮
	if status == RunnerConnected {
		if msg.Code == tea.KeyEnter {
			m.runnerBridge.Disconnect()
			m.panelState.mode = "settings"
			m.panelState.runner.runnerServerTI = textinput.Model{}
			m.panelState.runner.runnerTokenTI = textinput.Model{}
			m.panelState.runner.runnerWS = textinput.Model{}
			m.panelState.runner.runnerEditField = 0
			m.relayoutViewport()
			return true, m, nil
		}
		return true, m, nil
	}

	// 未连接：表单编辑
	switch msg.Code {
	case tea.KeyUp:
		if m.panelState.runner.runnerEditField > 0 {
			m.panelState.runner.runnerEditField--
		}
		return true, m, nil

	case tea.KeyDown:
		if m.panelState.runner.runnerEditField < 2 {
			m.panelState.runner.runnerEditField++
		}
		return true, m, nil

	case tea.KeyTab:
		m.panelState.runner.runnerEditField = (m.panelState.runner.runnerEditField + 1) % 3
		return true, m, nil

	case tea.KeyEnter:
		// 验证并连接
		serverURL := strings.TrimSpace(m.panelState.runner.runnerServerTI.Value())
		token := strings.TrimSpace(m.panelState.runner.runnerTokenTI.Value())
		workspace := strings.TrimSpace(m.panelState.runner.runnerWS.Value())

		if serverURL == "" {
			m.showTempStatus(m.locale.RunnerServerRequired)
			return true, m, m.clearTempStatusCmd()
		}
		if workspace == "" {
			m.showTempStatus(m.locale.RunnerWorkspaceRequired)
			return true, m, m.clearTempStatusCmd()
		}

		// 保存设置
		m.persistCLISettingsValues(map[string]string{
			"runner_server":    serverURL,
			"runner_token":     token,
			"runner_workspace": workspace,
		})

		// 回到 settings，发起连接
		m.panelState.mode = "settings"
		m.panelState.runner.runnerServerTI = textinput.Model{}
		m.panelState.runner.runnerTokenTI = textinput.Model{}
		m.panelState.runner.runnerWS = textinput.Model{}
		m.panelState.runner.runnerEditField = 0
		m.relayoutViewport()

		// 获取 LLM 客户端
		var llmClient llm.LLM
		var models []string
		var llmProvider string
		if m.channel != nil {
			llmClient = m.channel.getLLMClient()
			models = m.channel.getModelList()
			llmProvider = m.channel.getLLMProvider()
		}

		m.runnerBridge.Connect(serverURL, token, workspace, llmClient, models, llmProvider)

		m.showTempStatus(m.locale.RunnerConnecting)
		return true, m, m.clearTempStatusCmd()
	}

	// 将按键传递给当前编辑的 textinput
	var cmd tea.Cmd
	switch m.panelState.runner.runnerEditField {
	case 0:
		m.panelState.runner.runnerServerTI, cmd = m.panelState.runner.runnerServerTI.Update(msg)
	case 1:
		m.panelState.runner.runnerTokenTI, cmd = m.panelState.runner.runnerTokenTI.Update(msg)
	case 2:
		m.panelState.runner.runnerWS, cmd = m.panelState.runner.runnerWS.Update(msg)
	}
	return true, m, cmd
}

// viewRunnerPanel 渲染 Runner 管理面板
func (m *cliModel) viewRunnerPanel() string {
	s := &m.styles
	var sb strings.Builder

	sb.WriteString(s.PanelHeader.Render("🔧 " + m.locale.RunnerPanelTitle))
	sb.WriteString("\n")

	var status RunnerStatus
	if m.runnerBridge != nil {
		status = m.runnerBridge.Status()
	}

	switch status {
	case RunnerConnecting:
		sb.WriteString("\n")
		sb.WriteString(s.ProgressRunning.Render("⟳ " + m.locale.RunnerConnecting))
		sb.WriteString("\n")
		sb.WriteString(s.PanelDesc.Render("  " + m.runnerBridge.ServerURL()))
		sb.WriteString("\n\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.RunnerPleaseWait))

	case RunnerConnected:
		stats := m.runnerBridge.Stats()
		elapsed := time.Since(stats.ConnectedAt).Round(time.Minute)
		elapsedStr := formatElapsed(int64(elapsed.Milliseconds()))

		sb.WriteString("\n")
		fmt.Fprintf(&sb, "  %s %s (%s)\n",
			s.ProgressDone.Render("●"),
			m.locale.RunnerStatusConnected,
			s.InfoSt.Render(elapsedStr),
		)
		sb.WriteString(s.PanelDesc.Render("  Server: "))
		sb.WriteString(s.InfoSt.Render(m.runnerBridge.ServerURL()))
		sb.WriteString("\n")
		sb.WriteString(s.PanelDesc.Render("  " + m.locale.RunnerWorkspaceLabel + ": "))
		sb.WriteString(s.InfoSt.Render(m.runnerBridge.Workspace()))
		sb.WriteString("\n")
		logPath := m.runnerBridge.LogPath()
		if logPath != "" {
			sb.WriteString(s.PanelDesc.Render("  " + m.locale.RunnerLogLabel + ": "))
			sb.WriteString(s.InfoSt.Render(logPath))
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(s.WarningSt.Render("  [ " + m.locale.RunnerDisconnect + " ]"))
		sb.WriteString("\n\n")
		sb.WriteString(s.PanelHint.Render("  Enter " + m.locale.RunnerDisconnectAction + "  Esc " + m.locale.RunnerBack))

	default: // RunnerDisconnected 或 runnerBridge == nil
		// 显示连接表单
		sb.WriteString("\n")

		fields := []struct {
			label  string
			input  textinput.Model
			active bool
		}{
			{m.locale.RunnerServerLabel, m.panelState.runner.runnerServerTI, m.panelState.runner.runnerEditField == 0},
			{m.locale.RunnerTokenLabel, m.panelState.runner.runnerTokenTI, m.panelState.runner.runnerEditField == 1},
			{m.locale.RunnerWorkspaceLabel, m.panelState.runner.runnerWS, m.panelState.runner.runnerEditField == 2},
		}

		for _, f := range fields {
			prefix := "  "
			if f.active {
				prefix = s.PanelCursor.Render("▸")
			}
			fmt.Fprintf(&sb, "%s %s\n", prefix, f.label)
			sb.WriteString("  ")
			sb.WriteString(f.input.View())
			sb.WriteString("\n")
		}

		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.RunnerNavHint))
	}

	return sb.String()
}
