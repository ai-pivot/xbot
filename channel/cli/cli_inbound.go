package cli

import (
	"fmt"
	"os"
	"strings"
	"time"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	log "xbot/logger"
)

// newInbound creates an ch.InboundMsg with common fields pre-filled.
// metadata can be nil.
func (m *cliModel) newInbound(content string, metadata map[string]string) ch.InboundMsg {
	return ch.InboundMsg{
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

// sendInbound sends a message to the agent's inbound ch.
// Uses non-blocking send to prevent the BubbleTea event loop from freezing
// if the channel is full (e.g., agent is busy with a long LLM call).
// Returns false if the message was dropped.
// On failure, immediately marks the connection as disconnected so the
// splash screen appears without waiting for readPump timeout.
func (m *cliModel) sendInbound(msg ch.InboundMsg) bool {
	if m.sendInboundFn != nil {
		ok := m.sendInboundFn(msg)
		if ok {
			m.showDisconnect = false // user action confirmed connection works
			return true
		}
		// Write failed — connection is dead. Show splash immediately.
		log.WithFields(log.Fields{"fn_exists": true, "fn_returned": ok}).Warn("sendInbound: send failed, setting connState=disconnected")
		m.connState = "disconnected"
		m.showDisconnect = true
		fmt.Fprintf(os.Stderr, "\n\n!!! sendInbound: showDisconnect=TRUE connState=%q remoteMode=%v !!!\n\n", m.connState, m.remoteMode)
		return false
	}
	log.Warn("sendInbound: sendInboundFn is nil, connState NOT set")
	return false
}

// sendInboundWait sends a message to the agent's inbound channel with a timeout.
// Use for critical messages (ask_user answers) that MUST be delivered.
// Returns false if the message couldn't be sent within the deadline.
func (m *cliModel) sendInboundWait(msg ch.InboundMsg, timeout time.Duration) bool {
	if m.sendInboundFn != nil {
		return m.sendInboundFn(msg)
	}
	return false
}

// sendCancel sends a cancel request to the agent and adds a system notification.
func (m *cliModel) sendCancel() {
	m.cancelTargetTurnID = m.agentTurnID
	m.cancelAckProcessed = false // reset — awaiting first cancel ack for this turn
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
	if !m.sendInbound(m.newInbound(content, map[string]string{MetadataReplyPolicy: ReplyPolicyOptional})) {
		return // send failed — connState already set to disconnected by sendInbound
	}
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
	m.splashState.suLoading = false
	m.splashState.suPhaseConfirmed = false

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
	if !m.sendInbound(msg) {
		return nil // send failed — connState already set to disconnected by sendInbound
	}
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
