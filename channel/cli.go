// Package channel provides the CLI (Command Line Interface) channel for xbot.
//
// It implements a terminal-based chat interface using the Bubble Tea TUI framework,
// featuring:
//   - Incremental streaming rendering (markdown + code blocks)
//   - Tool call visualization with live status indicators
//   - Built-in slash commands: /model, /models, /context, /new
//   - Tab completion for commands and input history
//   - /rewind conversation rewind
//   - Non-interactive (pipe) mode with streaming output
//   - Session restore via --new/--resume flags

package channel

import (
	"context"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
	"xbot/bus"
	"xbot/clipanic"
	"xbot/llm"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/tools"
	"xbot/version"
)

func NewCLIChannel(cfg *CLIChannelConfig) *CLIChannel {
	ch := &CLIChannel{
		config:     cfg,
		workDir:    cfg.WorkDir,
		msgChan:    make(chan OutboundMsg, cliMsgBufSize),
		progressCh: make(chan *protocol.ProgressEvent, 1), // buffered-1: latest progress wins
		asyncCh:    make(chan tea.Msg, 256),               // unified async send: progress + outbound + ticks
		stopCh:     make(chan struct{}),
	}
	// Global ticker goroutine: sends cliTickMsg every 100ms. This is the
	// SINGLE source of all timed UI updates (splash animation, spinner,
	// progress timers, queue flush, placeholder rotation). No BubbleTea
	// cmd chain is needed — eliminating the class of bugs where multiple
	// cmd chains accumulate and double the tick rate.
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case ch.asyncCh <- cliTickMsg{}:
				default:
					// Channel full — drop tick. Next tick will arrive in 100ms.
					// This prevents blocking the ticker goroutine.
				}
			case <-ch.stopCh:
				return
			}
		}
	}()
	return ch
}

// Name 返回渠道名称
func (c *CLIChannel) Name() string {
	return "cli"
}

// SupportsStreamRender returns true — CLI supports real-time stream rendering.
func (c *CLIChannel) SupportsStreamRender() bool {
	return true
}

// Start 启动 CLI 渠道（阻塞运行）
func (c *CLIChannel) Start() error {
	log.Info("CLI channel starting...")

	// Capture the real stdout for bubbletea, then redirect os.Stdout and
	// os.Stderr to /dev/null so that background goroutines (logger cleanup,
	// third-party libs, stray fmt.Print, etc.) cannot write to the terminal
	// and cause flickering or garbled output in the alt-screen TUI.
	origStdout := os.Stdout
	origStderr := os.Stderr
	if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		os.Stdout = devNull
		os.Stderr = devNull
		defer func() {
			os.Stdout = origStdout
			os.Stderr = origStderr
			_ = devNull.Close()
		}()
	}

	// 初始化 Bubble Tea model
	c.model = newCLIModel()
	c.model.channel = c
	c.model.workDir = c.workDir
	c.model.remoteMode = c.config.RemoteMode
	c.model.remoteServerURL = c.config.RemoteServerURL
	c.model.debugMode = c.config.DebugMode
	if c.config.RemoteMode {
		c.model.connState = "connected"
	}
	c.model.debugCaptureMs = c.config.DebugCaptureMs
	c.model.senderID = "cli_user"

	// CLI-side TodoManager for persisting todos across turns and session switches.
	// Updated by syncProgressTodos during active turns and consumed by endAgentTurn
	// and restoreSession to preserve unfinished todos in idle state.
	c.model.todoManager = tools.NewTodoManager()

	// Apply CLI flag overrides for layout
	if c.config.SidebarWidthOverride > 0 {
		c.model.sidebarWidth = c.config.SidebarWidthOverride
	}
	if c.config.NoSidebar {
		c.model.sidebarEnabled = false
	}

	// Apply pending injections that were set before model existed
	if c.pendingTrimHistoryFn != nil {
		c.model.trimHistoryFn = c.pendingTrimHistoryFn
	}
	if c.pendingResetTokenStateFn != nil {
		c.model.resetTokenStateFn = c.pendingResetTokenStateFn
	}
	if c.pendingCheckpointState != nil {
		c.model.checkpointState = c.pendingCheckpointState
	}
	if c.pendingSendInboundFn != nil {
		c.model.sendInboundFn = c.pendingSendInboundFn
	}
	// Apply pending remote bg task callbacks (remote mode: set before Start)
	if c.pendingBgTaskCountFn != nil {
		c.model.bgTaskCountFn = c.pendingBgTaskCountFn
	}
	if c.pendingBgTaskListFn != nil {
		c.model.bgTaskListFn = c.pendingBgTaskListFn
	}
	if c.pendingBgTaskKillFn != nil {
		c.model.bgTaskKillFn = c.pendingBgTaskKillFn
	}
	if c.pendingBgTaskCleanupFn != nil {
		c.model.bgTaskCleanupFn = c.pendingBgTaskCleanupFn
	}
	if c.pendingPluginMgrFn != nil {
		c.model.pluginMgrFn = c.pendingPluginMgrFn
	}
	if c.pendingWidgetRegistry != nil {
		c.model.widgetRegistry = c.pendingWidgetRegistry
		c.pendingWidgetRegistry.SetDefaultRenderFn(buildWidgetRenderFn(c.model.styles))
		c.pendingWidgetRegistry.OnUpdated(func() {
			select {
			case c.asyncCh <- cliWidgetUpdateMsg{}:
			default:
			}
		})
		c.pendingWidgetRegistry = nil
	}
	if c.pendingRemotePluginCache != nil {
		c.model.remotePluginCache = c.pendingRemotePluginCache
		c.pendingRemotePluginCache.SetOnUpdated(func() {
			select {
			case c.asyncCh <- cliWidgetUpdateMsg{}:
			default:
			}
		})
		c.pendingRemotePluginCache = nil
	}
	// Set identity fields on the model.
	c.model.channelName = "cli"
	c.model.defaultChatID = c.config.ChatID
	c.model.chatID = c.config.ChatID
	c.model.sessionName, _ = ParseChatID(c.config.ChatID)
	if c.model.sessionName == "" {
		c.model.sessionName = defaultSessionName
	}

	// Restore per-session subscription state (activeSubID + cachedModelName)
	// from Session JSON. Must happen after workDir AND chatID are both set
	// so LoadSessionLLMState can find the correct session file.
	c.model.refreshCachedModelName()

	// If a per-session subscription was restored, trigger async SwitchLLM
	// so the backend also uses the correct LLM.
	c.model.scheduleSessionLLMRestore()

	// If RestoreSession ran before Start() (c.model was nil), it cached
	// data in pendingHistory/pendingProgress. Convert to pendingSuRestore
	// so Init() emits it as a suHistoryLoadMsg via the event loop.
	if c.pendingHistory != nil || c.pendingProgress != nil {
		c.model.pendingSuRestore = &suHistoryLoadMsg{
			history:        c.pendingHistory,
			channelName:    "cli",
			chatID:         c.config.ChatID,
			activeProgress: c.pendingProgress,
		}
		c.pendingHistory = nil
		c.pendingProgress = nil
	}

	// Propagate late-injected services to model (set before Start() when model was nil)
	if c.subscriptionMgr != nil {
		c.model.SetSubscriptionMgr(c.subscriptionMgr)
	}
	if c.llmSubscriber != nil {
		c.model.SetLLMSubscriber(c.llmSubscriber)
	}

	// i18n: initialize locale from settings
	if c.settingsSvc != nil {
		if vals, err := c.settingsSvc.GetSettings("cli", "cli_user"); err == nil {
			if lang, ok := vals["language"]; ok {
				SetLocale(lang)
				c.model.locale = GetLocale(lang)
			}
		}
	}

	// Setup bg task count callback
	c.updateBgTaskCountFn()

	// 加载历史消息（会话恢复）
	if c.config.HistoryLoader != nil {
		if history, err := c.config.HistoryLoader(); err == nil && len(history) > 0 {
			for _, hm := range history {
				cm := cliMessage{
					role:      hm.Role,
					content:   hm.Content,
					timestamp: hm.Timestamp,
					isPartial: false,
					dirty:     true,
				}
				// 映射迭代快照
				if len(hm.Iterations) > 0 {
					cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
					for i, hi := range hm.Iterations {
						cm.iterations[i] = cliIterationSnapshot(hi)
					}
				}
				c.model.messages = append(c.model.messages, cm)
			}
			log.WithField("count", len(history)).Info("Restored session history")
		} else if err != nil {
			log.WithError(err).Warn("Failed to load session history")
		}
	}

	// Restore token state from DB for context bar display.
	// Without this, lastTokenUsage is nil after restart and the context
	// bar never shows until the first LLM response of the new session.
	if c.config.TokenStateLoader != nil {
		if pt, ct := c.config.TokenStateLoader(); pt > 0 {
			c.model.lastTokenUsage = &protocol.TokenUsage{
				PromptTokens:     pt,
				CompletionTokens: ct,
				TotalTokens:      pt + ct,
			}
			log.WithField("prompt_tokens", pt).Info("Restored token state")
		}
	}
	// Resolve max context tokens immediately (not lazily on first progress event)
	c.model.cachedMaxContextTokens = c.model.resolveMaxContextTokens()
	// Also resolve max output tokens and compress ratio so the context bar
	// threshold (red line) is correct from the first render, not defaulting to 8192.
	c.model.cachedMaxOutputTokens = c.model.resolveMaxOutputTokens()
	c.model.cachedCompressRatio = c.model.resolveCompressRatio()

	// 首次运行：打开 setup panel
	if c.config.IsFirstRun {
		c.model.openSetupPanel()
	}

	// 创建 Bubble Tea program
	programOpts := []tea.ProgramOption{
		tea.WithOutput(origStdout),
	}
	if os.Getenv("XBOT_BUBBLETEA_PANIC") == "1" {
		programOpts = append(programOpts, tea.WithoutCatchPanics())
	}
	c.programMu.Lock()
	c.program = tea.NewProgram(c.model, programOpts...)
	c.programMu.Unlock()

	// Wire CLIApprovalHandler into the ApprovalState now that the program exists
	if c.approvalState != nil {
		c.approvalState.SetHandler(NewCLIApprovalHandler(c.program))
	}

	// Ctrl+Z 紧急退出：双保险
	// 1) Key event handler (cli_update.go): raw mode 下终端可能直接传 0x1A 字节
	// 2) SIGTSTP 信号兜底: 某些终端 emulator 在 raw mode 下仍发信号
	// Note: SIGTSTP is Unix-only; handled by handleCtrlZSuspend (platform-specific).
	setupCtrlZSuspend(c, origStdout, origStderr)

	// 启动 outbound 消息处理 goroutine
	c.wg.Add(1)
	go c.handleOutbound()

	// 启动 progress coalescing goroutine: drains progressCh and forwards
	// to the unified async channel.
	c.wg.Add(1)
	clipanic.Go("channel.CLIChannel.handleProgressDrain", c.handleProgressDrain)

	// 启动 unified async drain goroutine: single sender to p.msgs
	c.wg.Add(1)
	clipanic.Go("channel.CLIChannel.handleAsyncDrain", c.handleAsyncDrain)

	// §13 异步检查更新（不阻塞 TUI 启动）
	c.CheckUpdateAsync()

	// Runner auto-connect: inject RunnerBridge into model and connect
	if c.runnerAutoConnect != nil {
		c.programMu.Lock()
		if c.model != nil && c.program != nil {
			rb := NewRunnerBridge(c.program)
			c.model.runnerBridge = rb
		}
		c.programMu.Unlock()
		// Delay connection slightly to let TUI render first
		clipanic.Go("channel.CLIChannel.runnerAutoConnect", func() {
			time.Sleep(500 * time.Millisecond)
			c.programMu.Lock()
			model := c.model
			c.programMu.Unlock()
			if model != nil && model.runnerBridge != nil {
				cfg := c.runnerAutoConnect
				model.runnerBridge.Connect(
					cfg.serverURL,
					cfg.token,
					cfg.workspace,
					c.getLLMClient(),
					c.getModelList(),
					c.getLLMProvider(),
				)
			}
		})
	}

	// --debug: start Unix socket for key injection
	var debugSock *debugSockListener
	if c.config.DebugMode {
		sockPath, err := debugSockPath()
		if err == nil {
			debugSock, err = startDebugSock(sockPath, func(msg tea.Msg) {
				c.program.Send(msg)
			})
			if err != nil {
				log.WithError(err).Warn("Failed to start debug socket")
			} else {
				log.WithField("socket", sockPath).Info("Debug socket listening")
			}
		}
		// --debug-input: auto-inject key sequence after startup
		if c.config.DebugInput != "" {
			startAutoInput(c.config.DebugInput, c.asyncCh, c.stopCh)
		}
	}

	// 运行 Bubble Tea（阻塞）
	if _, err := c.program.Run(); err != nil {
		log.WithError(err).Error("CLI channel exited with error")
		if debugSock != nil {
			debugSock.Stop()
		}
		return err
	}

	if debugSock != nil {
		debugSock.Stop()
	}
	log.Info("CLI channel stopped")
	return nil
}

// Stop 停止 CLI 渠道
func (c *CLIChannel) Stop() {
	log.Info("CLI channel stopping...")
	// Disconnect runner bridge if active
	c.programMu.Lock()
	if c.model != nil && c.model.runnerBridge != nil {
		c.model.runnerBridge.Disconnect()
	}
	c.programMu.Unlock()
	close(c.stopCh)
	c.programMu.Lock()
	if c.program != nil {
		c.program.Quit()
	}
	c.programMu.Unlock()
	c.wg.Wait()
	log.Info("CLI channel stopped")
}

// Send 发送消息到 CLI（实现 Channel 接口）
func (c *CLIChannel) Send(msg bus.OutboundMessage) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	// Convert bus type to local OutboundMsg for internal processing.
	local := OutboundMsg{
		Channel:     msg.Channel,
		ChatID:      msg.ChatID,
		Content:     msg.Content,
		Metadata:    msg.Metadata,
		WaitingUser: msg.WaitingUser,
		IsPartial:   msg.IsPartial,
		ToolsUsed:   msg.ToolsUsed,
		Media:       msg.Media,
	}

	// 发送到消息通道，由 handleOutbound 处理
	log.WithField("msg_id", msgID).WithField("content_len", len(local.Content)).Debug("CLIChannel.Send: queuing")
	select {
	case c.msgChan <- local:
	default:
		log.Warn("CLI message channel full, dropping message")
	}

	return msgID, nil
}

// SendProgress 发送结构化进度事件到 CLI（非阻塞）。
// ALL messages (including PhaseDone) go through asyncCh to ensure there is only
// ONE goroutine (handleAsyncDrain) calling program.Send(). This prevents multiple
// senders from competing on the unbuffered p.msgs channel, which would starve
// the Bubble Tea readLoop (keyboard events) and cause Ctrl+C freeze.
func (c *CLIChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if payload == nil || c.program == nil {
		return
	}
	if payload.ChatID == "" {
		payload.ChatID = chatID
	}

	// Stream-only events (Phase=="", Iteration==0) are high-frequency
	// streaming animation updates that carry no structured data. They
	// should never evict structured events that carry TokenUsage and
	// iteration state for the context bar and progress panel.
	isStreamOnly := payload.Phase == "" && payload.Iteration == 0

	select {
	case c.progressCh <- payload:
	default:
		if isStreamOnly {
			// Channel has a structured event waiting. Drop this
			// stream-only event — the structured event is more
			// important for the context bar and progress panel.
			return
		}
		// Both old (queued) and new are structured. Drain the old
		// one, but merge its TokenUsage and CWD into the new one
		// so context-bar data is never lost to eviction.
		select {
		case old := <-c.progressCh:
			if payload.TokenUsage == nil && old.TokenUsage != nil {
				payload.TokenUsage = old.TokenUsage
			}
			if payload.CWD == "" && old.CWD != "" {
				payload.CWD = old.CWD
			}
		default:
		}
		select {
		case c.progressCh <- payload:
		default:
		}
	}
}

// SetProcessing externally sets the typing/processing state (for remote reconnect).
func (c *CLIChannel) SetProcessing(processing bool) {
	if c.program == nil {
		return
	}
	select {
	case c.asyncCh <- cliProcessingMsg{processing: processing}:
	default:
		// Drop if asyncCh full — processing state will recover on next message
	}
}

// SendSessionState delivers a server-pushed session state change event
// (busy/idle, subagent started/stopped) to the BubbleTea Update loop.
// Non-blocking — drops if asyncCh is full. The 5s safety-net poll will recover.
func (c *CLIChannel) SendSessionState(ev protocol.SessionEvent) {
	if c.program == nil {
		return
	}
	select {
	case c.asyncCh <- cliSessionStateMsg{event: ev}:
	default:
	}
}

// SetConnState updates the connection state indicator in the header bar.
// Non-blocking — drops if asyncCh is full.
func (c *CLIChannel) SetConnState(state string) {
	if c.program == nil {
		return
	}
	select {
	case c.asyncCh <- cliConnStateMsg{state: state}:
	default:
	}
}

// SendToast shows a toast notification in the CLI (non-blocking).
func (c *CLIChannel) SendToast(text, icon string) {
	if c.program == nil {
		return
	}
	select {
	case c.asyncCh <- cliToastMsg{text: text, icon: icon}:
	default:
		// Drop if asyncCh full — toast is non-critical
	}
}

// SetApprovalState stores the ApprovalState reference so that Start() can wire
// the CLIApprovalHandler after the tea.Program is created.
func (c *CLIChannel) SetApprovalState(state *protocol.ApprovalState) {
	c.approvalState = state
}

// SetSendInboundFn overrides the default sendInbound behavior.
// In remote mode, this forwards user messages to the server via backend.SendInbound
// instead of the local bus (which has no agent loop).
func (c *CLIChannel) SetSendInboundFn(fn func(InboundMsg) bool) {
	c.pendingSendInboundFn = fn
}

// SetBgTaskManager configures the background task manager for status display.
func (c *CLIChannel) SetBgTaskManager(mgr *tools.BackgroundTaskManager, sessionKey string) {
	c.bgTaskMgr = mgr
	c.bgSessionKey = sessionKey
	c.updateBgTaskCountFn()
}

// SetBgTaskRemoteCallbacks configures remote-mode background task callbacks.
// Used when BgTaskManager is not available (remote CLI mode) to enable
// background task display and management via RPC.
func (c *CLIChannel) SetBgTaskRemoteCallbacks(sessionKey string, countFn func() int, listFn func() []*BgTask, killFn func(taskID string) error, cleanupFn func()) {
	c.bgSessionKey = sessionKey
	c.bgTaskKill = killFn
	if c.model != nil {
		c.model.bgTaskCountFn = countFn
		c.model.bgTaskListFn = listFn
		c.model.bgTaskKillFn = killFn
		c.model.bgTaskCleanupFn = cleanupFn
	} else {
		// Model not created yet (Start() not called) — save as pending
		c.pendingBgTaskCountFn = countFn
		c.pendingBgTaskListFn = listFn
		c.pendingBgTaskKillFn = killFn
		c.pendingBgTaskCleanupFn = cleanupFn
	}
}

// SetPluginManager sets the plugin manager callback for the /plugin command.
// If the model hasn't been created yet (Start() not called), the callback
// is saved as pending and applied when the model is created.
func (c *CLIChannel) SetPluginManager(fn func() *plugin.PluginManager) {
	if c.model != nil {
		c.model.pluginMgrFn = fn
	} else {
		c.pendingPluginMgrFn = fn
	}
}

// SetWidgetRegistry wires the plugin system's widget registry into the TUI.
// Must be called after SetPluginManager (when the PluginManager is available).
// Sets the default render function based on the current theme, and registers
// a notifier that triggers TUI redraw when plugin widget content changes.
// If the model hasn't been created yet, the registry is cached and applied later.
func (c *CLIChannel) SetWidgetRegistry(wr *plugin.WidgetRegistry) {
	if c.model != nil {
		c.model.widgetRegistry = wr
		if wr != nil {
			wr.SetDefaultRenderFn(buildWidgetRenderFn(c.model.styles))
			// When a widget updates, send a message through asyncCh to trigger View() redraw
			wr.OnUpdated(func() {
				select {
				case c.asyncCh <- cliWidgetUpdateMsg{}:
				default:
				}
			})
		}
	} else {
		c.pendingWidgetRegistry = wr
	}
}

// SetRemotePluginCache wires the remote plugin cache into the TUI for /plugin commands
// and widget rendering in remote mode.
// If the model hasn't been created yet, the cache is saved as pending and applied later.
func (c *CLIChannel) SetRemotePluginCache(cache *remotePluginCache) {
	if c.model != nil {
		c.model.remotePluginCache = cache
		if cache != nil {
			// When widget content is fetched from server, trigger TUI redraw.
			cache.SetOnUpdated(func() {
				select {
				case c.asyncCh <- cliWidgetUpdateMsg{}:
				default:
				}
			})
		}
	} else {
		c.pendingRemotePluginCache = cache
	}
}

// CurrentChatID returns the current session chatID from the model.
// Used by the widget Subscribe handler to filter push events for the correct session.
func (c *CLIChannel) CurrentChatID() string {
	if c.model != nil {
		return c.model.chatID
	}
	return ""
}

// SyncPluginWidgetChatID updates the remote plugin cache's chatID after Cd
// so that refreshWidgets() RPC fetches widgets for the correct session.
func (c *CLIChannel) SyncPluginWidgetChatID(chatID string) {
	if c.model != nil && c.model.remotePluginCache != nil {
		c.model.remotePluginCache.UpdateChatID(chatID)
		c.model.remotePluginCache.Refresh()
	}
}

// RestoreSession restores history + active progress + todos in one atomic step.
// Uses the same suHistoryLoadMsg path as session switch, guaranteeing identical
// rendering behavior for initial connect and reconnect.
func (c *CLIChannel) RestoreSession(history []HistoryMessage, activeProgress *protocol.ProgressEvent, todos []protocol.TodoItem) {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model == nil {
		// Model not created yet — cache for Start().
		c.pendingHistory = history
		c.pendingProgress = activeProgress
		return
	}
	if c.program == nil {
		// Program not started yet — cache on model for Init() to consume
		// via pendingSuRestore. Init() returns a tea.Cmd that emits
		// suHistoryLoadMsg, guaranteeing the handler's returned cmds
		// (tickCmd, typewriterTick) are properly batched by BubbleTea.
		c.model.pendingSuRestore = &suHistoryLoadMsg{
			history:        history,
			channelName:    "cli",
			chatID:         c.config.ChatID,
			activeProgress: activeProgress,
			todos:          todos,
		}
		return
	}
	// Program running — send as suHistoryLoadMsg (same as session switch).
	select {
	case c.asyncCh <- suHistoryLoadMsg{
		history:        history,
		channelName:    "cli",
		chatID:         c.config.ChatID,
		activeProgress: activeProgress,
		todos:          todos,
	}:
	default:
		log.Warn("RestoreSession: asyncCh full, dropping restore")
	}
}

// SetTrimHistoryFn sets the callback for /rewind DB truncation.
// cutoff is the timestamp threshold — all DB messages with created_at < cutoff will be deleted.
// If the model hasn't been created yet, the callback is cached and applied later.
func (c *CLIChannel) SetTrimHistoryFn(fn func(cutoff time.Time) error) {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil {
		c.model.trimHistoryFn = fn
	}
	c.pendingTrimHistoryFn = fn
}

// SetResetTokenStateFn sets the callback for /rewind token state reset.
// Must be called to prevent stale prompt_tokens from triggering immediate
// compression after a rewind truncates history.
func (c *CLIChannel) SetResetTokenStateFn(fn func()) {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil {
		c.model.resetTokenStateFn = fn
	}
	c.pendingResetTokenStateFn = fn
}

// SyncLayoutSettings applies layout settings (sidebar_width, sidebar_position, etc.)
// from the given values map to the TUI model. Applies directly to model for startup,
// and also sends through asyncCh for Update() handler.
func (c *CLIChannel) SyncLayoutSettings(vals map[string]string) {
	layoutVals := map[string]string{}
	for _, k := range []string{"sidebar_width", "sidebar_enabled", "sidebar_position", "chat_max_width", "chat_center", "layout_mode"} {
		if v, ok := vals[k]; ok {
			layoutVals[k] = v
		}
	}
	if len(layoutVals) == 0 {
		return
	}
	// Send through asyncCh — the ONLY safe path. Direct model calls
	// from background goroutines (refreshRemoteValuesCache) race with
	// the BubbleTea event loop and cause glamour render panics.
	select {
	case c.asyncCh <- cliSettingsSavedMsg{layoutVals: layoutVals, layoutChanged: true, syncOnly: true}:
	default:
	}
}

// SetCheckpointState sets the file checkpoint state for /rewind file rollback.
// If the model hasn't been created yet, the state is cached and applied later.
func (c *CLIChannel) SetCheckpointState(state *protocol.CheckpointState) {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil {
		c.model.checkpointState = state
	}
	c.pendingCheckpointState = state
}

// InjectUserMessage 通知 CLI 有 user 消息被 agent 注入（如 bg task 完成通知）。
// 在 CLI 界面上显示为一条 user 消息，和用户手动输入的效果一致。
// chatID is the target session's chatID (used for session filtering). Empty = legacy, always apply.
func (c *CLIChannel) InjectUserMessage(chatID, content string) {
	if c.program != nil {
		select {
		case c.asyncCh <- cliInjectedUserMsg{content: content, chatID: chatID}:
		default:
		}
	}
}

// updateBgTaskCountFn updates the model's bg task count and agent count callbacks.
func (c *CLIChannel) updateBgTaskCountFn() {
	if c.model == nil {
		return
	}
	if c.bgTaskMgr != nil && c.bgSessionKey != "" {
		c.model.bgTaskCountFn = func() int {
			return len(c.bgTaskMgr.ListRunning(c.bgSessionKey))
		}
		c.model.bgTaskListFn = func() []*BgTask {
			tasks := c.bgTaskMgr.ListAllForSession(c.bgSessionKey)
			result := make([]*BgTask, len(tasks))
			for i, t := range tasks {
				result[i] = &BgTask{
					ID:         t.ID,
					Command:    t.Command,
					Status:     BgTaskStatus(t.Status),
					StartedAt:  t.StartedAt,
					FinishedAt: t.FinishedAt,
					Output:     t.Output,
					ExitCode:   t.ExitCode,
					Error:      t.Error,
				}
			}
			return result
		}
		c.model.bgTaskKillFn = func(taskID string) error {
			return c.bgTaskMgr.Kill(taskID)
		}
	}
	// Wire agent count/list callbacks
	if c.config.AgentCount != nil {
		c.model.agentCountFn = c.config.AgentCount
	}
	if c.config.AgentList != nil {
		c.model.agentListFn = func() []panelAgentEntry {
			entries := c.config.AgentList()
			result := make([]panelAgentEntry, len(entries))
			for i, e := range entries {
				result[i] = panelAgentEntry(e)
			}
			return result
		}
	}
	if c.config.AgentInspect != nil {
		c.model.agentInspectFn = c.config.AgentInspect
	}
	if c.config.AgentMessages != nil {
		c.model.agentMessagesFn = c.config.AgentMessages
	}
	// Wire sessions list callback
	if c.config.SessionsList != nil {
		c.model.sessionsListFn = c.config.SessionsList
	}
	// Wire usage query callback
	if c.config.UsageQuery != nil {
		c.model.usageQueryFn = c.config.UsageQuery
	}
	// Wire web user management callbacks
	if c.config.CreateWebUserFn != nil {
		c.model.createWebUserFn = c.config.CreateWebUserFn
	}
	if c.config.ListWebUsersFn != nil {
		c.model.listWebUsersFn = c.config.ListWebUsersFn
	}
	if c.config.DeleteWebUserFn != nil {
		c.model.deleteWebUserFn = c.config.DeleteWebUserFn
	}
	if c.config.IsAdminFn != nil {
		c.model.isAdminFn = c.config.IsAdminFn
	}
	if c.config.PaletteContributor != nil {
		c.model.paletteContributor = c.config.PaletteContributor
	}
}

// CheckUpdateAsync starts a background goroutine to check for updates.
// The result is sent to the TUI via program.Send.
func (c *CLIChannel) CheckUpdateAsync() {
	if c.program == nil {
		return
	}
	clipanic.Go("channel.CLIChannel.CheckUpdateAsync", func() {
		info := version.CheckUpdate(context.Background())
		select {
		case c.asyncCh <- cliUpdateCheckMsg{info: info}:
		default:
		}
	})
}

// handleOutbound 处理从 agent 发来的消息 — 通过 asyncCh 合并发送
func (c *CLIChannel) handleOutbound() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case msg := <-c.msgChan:
			c.programMu.Lock()
			p := c.program
			c.programMu.Unlock()
			if p == nil {
				continue
			}
			// Route through asyncCh: non-blocking send, drops if full.
			// WaitingUser messages (AskUser) must not be dropped, send directly.
			if msg.WaitingUser {
				p.Send(cliOutboundMsg{msg: msg})
				continue
			}
			select {
			case c.asyncCh <- cliOutboundMsg{msg: msg}:
			default:
				// asyncCh full — drain one stale message, then send
				select {
				case <-c.asyncCh:
				default:
				}
				select {
				case c.asyncCh <- cliOutboundMsg{msg: msg}:
				default:
				}
			}
		}
	}
}

// handleProgressDrain drains the progress coalescing channel and forwards
// handleProgressDrain drains progressCh and forwards non-blockingly
// to the unified asyncCh. Drops stale progress when event loop is behind
// (asyncCh full) — the next progress event will be fresher.
func (c *CLIChannel) handleProgressDrain() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case payload := <-c.progressCh:
			select {
			case c.asyncCh <- cliProgressMsg{payload: payload}:
			default:
				// asyncCh full — event loop is behind. Cache token
				// usage directly so the context bar doesn't flash
				// blank before the next progress event arrives.
				if payload.TokenUsage != nil && payload.TokenUsage.PromptTokens > 0 {
					c.programMu.Lock()
					if c.model != nil {
						c.model.cacheTokenUsage(payload.TokenUsage)
					}
					c.programMu.Unlock()
				}
			}
		}
	}
}

// handleAsyncDrain is the SINGLE goroutine that forwards messages from asyncCh
// to the Bubble Tea event loop via program.Send. This is the only non-readLoop
// sender to p.msgs, ensuring key events get fair scheduling (~50% instead of ~25%).
func (c *CLIChannel) handleAsyncDrain() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case msg := <-c.asyncCh:
			c.programMu.Lock()
			p := c.program
			c.programMu.Unlock()
			if p != nil {
				p.Send(msg)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Bubble Tea Model
// ---------------------------------------------------------------------------

// animTicker 是一个简单的字符动画 ticker，不依赖 bubbles/spinner。
// 支持双色呼吸效果：颜色在 Accent 和 AccentAlt 之间平滑过渡。
// speed 字段控制动画速度：每 speed 个 tick 才推进一帧。
//
//	speed=1 → 100ms/frame (快), speed=3 → 300ms/frame (中等), speed=5 → 500ms/frame (慢)
type animTicker struct {
	frames   []string
	frame    int
	ticks    int64          // total ticks for phase-aware behavior
	speed    int            // ticks per frame advance (1=fast, 3=medium, 5=slow)
	style    lipgloss.Style // 主色调
	styleAlt lipgloss.Style // 备选色（呼吸效果用）
	color    string         // 主色值（主题切换时重建样式用）
	colorAlt string         // 备选色值
}

// SetRunnerLLM sets the LLM client and model list for the runner bridge.
func (c *CLIChannel) SetRunnerLLM(client llm.LLM, models []string, provider string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.llmClient = client
	c.modelList = models
	c.llmProvider = provider
}

// getLLMClient returns the LLM client for runner use.
func (c *CLIChannel) getLLMClient() llm.LLM {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.llmClient
}

// getModelList returns the available model list for runner use.
func (c *CLIChannel) getModelList() []string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.modelList
}

// getLLMProvider returns the LLM provider name for runner use.
func (c *CLIChannel) getLLMProvider() string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.llmProvider
}

// StartWithRunner starts the CLI channel and auto-connects as runner after TUI initializes.
func (c *CLIChannel) StartWithRunner(shareURL, token, workspace string) error {
	// Wrap the original Start to inject runner bridge before the TUI runs.
	// We set a callback that creates the RunnerBridge after model init.
	c.runnerAutoConnect = &runnerAutoConnectConfig{
		serverURL: shareURL,
		token:     token,
		workspace: workspace,
	}
	return c.Start()
}

// ensureRunnerBridge 确保 RunnerBridge 存在（供 settings 面板使用）。
func (c *CLIChannel) ensureRunnerBridge() {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil && c.model.runnerBridge == nil && c.program != nil {
		c.model.runnerBridge = NewRunnerBridge(c.program)
	}
}

// runnerAutoConnectConfig holds the auto-connect parameters.
type runnerAutoConnectConfig struct {
	serverURL string
	token     string
	workspace string
}
