package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	ch "xbot/channel"
	"xbot/clipanic"
	"xbot/internal/textarea"
	log "xbot/logger"
	"xbot/protocol"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// splashDoneMsg 启动画面结束消息
type splashDoneMsg struct{}

// suHistoryLoadMsg /su 切换用户后的历史加载完成消息
// suHistoryLoadMsg /su 切换用户后的历史加载完成消息
type suHistoryLoadMsg struct {
	history        []ch.HistoryMessage
	err            error
	channelName    string                  // target session at time of request
	chatID         string                  // target session at time of request
	activeProgress *protocol.ProgressEvent // non-nil if target session has an active agent turn
	// tokenState holds the last persisted token counts for the session.
	// Used as fallback when activeProgress is nil (idle session) so the
	// context bar still shows the session's last known token usage.
	tokenPrompt     int64
	tokenCompletion int64
	// LLM state for TUI status bar rendering (model name, context limits, etc.)
	modelName        string
	maxContextTokens int64
	maxOutputTokens  int64
	compressRatio    float64
	// todos is the server-side TODO list for the target session.
	// Populated by suLoadHistoryCmd via GetTodosFn RPC.
	// When non-nil, it overwrites the local TodoManager cache so
	// the first session switch after TUI startup shows fresh data.
	todos []protocol.TodoItem
}

// sessionState holds per-session state that should be preserved when switching sessions.
// Messages are NOT stored here — the DB is the source of truth for history.
// EXCEPTION: pendingUserMsg saves the most recent user message that was sent to the
// agent bus but may not yet be persisted to the DB. This prevents user messages from
// disappearing when quickly switching sessions before the agent's eager-save completes.
// sessionState holds per-session state that should be preserved when switching sessions.
// Messages are NOT stored here — the DB is the source of truth for history.
// EXCEPTION: pendingUserMsg saves the most recent user message that was sent to the
// agent bus but may not yet be persisted to the DB. This prevents user messages from
// disappearing when quickly switching sessions before the agent's eager-save completes.
type sessionState struct {
	progress             *protocol.ProgressEvent
	typing               bool
	agentTurnID          uint64
	inputReady           bool
	needFlushQueue       bool
	lastProgressSeq      uint64
	twVisible            int
	rwVisible            int
	iterationHistory     []cliIterationSnapshot
	lastSeenIteration    int
	streamingMsgIdx      int
	typingStartTime      time.Time
	lastReasoning        string
	lastThinking         string
	turnCancelled        bool
	typewriterTickActive bool
	reasoningByIter      map[int]string
	// Context bar state — preserved across session switches so the
	// token usage bar doesn't disappear when switching between sessions.
	lastTokenUsage         *protocol.TokenUsage
	cachedMaxContextTokens int
	cachedCompressRatio    float64
	cachedMaxOutputTokens  int64
	pendingUserMsg         *cliMessage // user message sent but not yet confirmed in DB
	messageQueue           []queuedMsg
	queueEditing           bool
	// Per-session LLM configuration (survives session switches)
	activeSubscriptionID string // subscription ID active in this session
	activeModel          string // model active in this session (may differ from subscription default)
	// Per-session input state (survives session switches)
	textareaValue   string   // current textarea content
	inputHistory    []string // sent message history for Up/Down browsing
	inputHistoryIdx int      // current position in input history (-1 = not browsing)
	inputDraft      string   // draft text before entering history browsing
	// Per-session background task count (survives session switches).
	// Refreshed from bgTaskCountFn on restore and tick; stored here so the
	// infobar and sidebar show the correct count immediately after a switch.
	bgTaskCount int
}

// sessionKey returns the map key for the current session.
// sessionKey returns the map key for the current session.
func (m *cliModel) sessionKey() string {
	return qualifyChatID(m.channelName, m.chatID)
}

// saveCurrentSession saves the current session's live state into the savedSessions map.
// saveCurrentSession saves the current session's live state into the savedSessions map.
func (m *cliModel) saveCurrentSession() {
	key := m.sessionKey()
	if m.savedSessions == nil {
		m.savedSessions = make(map[string]*sessionState)
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	m.savedSessions[key] = &sessionState{
		progress:               m.progressState.current,
		typing:                 m.typing,
		agentTurnID:            m.agentTurnID,
		inputReady:             m.inputReady,
		needFlushQueue:         m.needFlushQueue,
		lastProgressSeq:        m.progressState.lastSeq,
		twVisible:              m.progressState.twVisible,
		rwVisible:              m.progressState.rwVisible,
		iterationHistory:       m.progressState.iterations,
		lastSeenIteration:      m.progressState.lastIter,
		streamingMsgIdx:        m.streamingMsgIdx,
		typingStartTime:        m.typingStartTime,
		lastReasoning:          m.lastReasoning,
		lastThinking:           m.lastThinking,
		turnCancelled:          m.turnCancelled,
		typewriterTickActive:   m.progressState.twActive,
		reasoningByIter:        m.reasoningByIter,
		lastTokenUsage:         m.lastTokenUsage,
		cachedMaxContextTokens: m.cachedMaxContextTokens,
		cachedCompressRatio:    m.cachedCompressRatio,
		cachedMaxOutputTokens:  m.cachedMaxOutputTokens,
		messageQueue:           m.messageQueue,
		queueEditing:           m.queueEditing,
		pendingUserMsg:         m.pendingUserMsg,
		activeSubscriptionID:   m.activeSubID,
		activeModel:            m.cachedModelName,
		textareaValue:          m.textarea.Value(),
		inputHistory:           m.inputHistory,
		inputHistoryIdx:        m.inputHistoryIdx,
		inputDraft:             m.inputDraft,
		bgTaskCount:            m.bgTaskCount,
	}
	// Persist todo list for current session (skip in ephemeral mode — nothing to persist)
	if m.todoManager != nil && !m.ephemeral {
		_ = m.todoManager.SaveToFile(key)
	}
}

// restoreSession restores a session's live state from the savedSessions map.
// If the session has saved state, restores it; otherwise resets to idle.
// restoreSession restores a session's live state from the savedSessions map.
// If the session has saved state, restores it; otherwise resets to idle.
func (m *cliModel) restoreSession() {
	key := m.sessionKey()
	if saved, ok := m.savedSessions[key]; ok {
		m.progressState.current = saved.progress
		m.typing = saved.typing
		m.agentTurnID = saved.agentTurnID
		m.inputReady = saved.inputReady
		m.needFlushQueue = saved.needFlushQueue
		m.progressState.lastSeq = saved.lastProgressSeq
		m.progressState.twVisible = saved.twVisible
		m.progressState.rwVisible = saved.rwVisible
		m.progressState.iterations = saved.iterationHistory
		m.progressState.lastIter = saved.lastSeenIteration
		m.streamingMsgIdx = saved.streamingMsgIdx
		m.typingStartTime = saved.typingStartTime
		m.lastReasoning = saved.lastReasoning
		m.lastThinking = saved.lastThinking
		m.turnCancelled = saved.turnCancelled
		m.progressState.twActive = saved.typewriterTickActive
		m.reasoningByIter = saved.reasoningByIter
		m.lastTokenUsage = saved.lastTokenUsage
		m.cachedMaxContextTokens = saved.cachedMaxContextTokens
		m.cachedCompressRatio = saved.cachedCompressRatio
		m.cachedMaxOutputTokens = saved.cachedMaxOutputTokens
		m.messageQueue = saved.messageQueue
		m.queueEditing = saved.queueEditing
		m.pendingUserMsg = saved.pendingUserMsg
		m.activeSubID = saved.activeSubscriptionID
		m.cachedModelName = saved.activeModel
		m.textarea.SetValue(saved.textareaValue)
		m.inputHistory = saved.inputHistory
		m.inputHistoryIdx = saved.inputHistoryIdx
		m.inputDraft = saved.inputDraft
		// Restore bg task count from saved state, then re-query from backend
		// so the count is fresh (tasks may have completed while switched away).
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		} else {
			m.bgTaskCount = saved.bgTaskCount
		}
		// Load todo list for the restored session and sync to display
		if m.todoManager != nil {
			_ = m.todoManager.LoadFromFile(key)
			if items := m.todoManager.GetTodos(key); len(items) > 0 {
				m.todos = make([]protocol.TodoItem, len(items))
				copy(m.todos, items)
				m.todosDoneCleared = false
			} else {
				m.todos = nil
				m.todosDoneCleared = false
			}
		} else {
			m.todos = nil
			m.todosDoneCleared = false
		}
		m.updatePlaceholder()
		delete(m.savedSessions, key) // clean up
	} else {
		// No saved state — reset to idle (NOT cancelled)
		m.progressState.current = nil
		m.typing = false
		m.streamingMsgIdx = -1
		m.progressState.iterations = nil
		m.rc.invalidateProgress()
		m.progressState.lastIter = 0
		m.typingStartTime = time.Time{}
		m.lastReasoning = ""
		m.reasoningByIter = nil
		m.progressState.streamReasoningByIter = nil
		m.lastThinking = ""
		m.inputReady = false
		m.needFlushQueue = false
		m.messageQueue = nil
		m.queueEditing = false
		m.progressState.lastSeq = 0
		m.progressState.twVisible = 0
		m.progressState.rwVisible = 0
		m.progressState.twActive = false
		m.pendingUserMsg = nil
		m.agentTurnID = 0       // prevent stale turnDoneFlags match
		m.textarea.SetValue("") // clear input for new/unsaved session
		m.inputDraft = ""
		m.inputHistory = nil
		m.inputHistoryIdx = -1
		m.lastTokenUsage = nil
		m.cachedMaxContextTokens = 0
		m.cachedMaxOutputTokens = 0
		m.cachedCompressRatio = 0
		// Reset per-session subscription/model state so it doesn't leak from previous session.
		// postRestoreSessionSetup() will restore the correct values from disk or global defaults.
		m.activeSubID = ""
		m.cachedModelName = ""
		m.hasNoSubCacheValid = false
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		} else {
			m.bgTaskCount = 0
		}
		// Clear todos — no saved state means no active turn,
		// but persist unfinished todos from TodoManager so they
		// remain visible across session switches.
		if m.todoManager != nil {
			_ = m.todoManager.LoadFromFile(key)
			if items := m.todoManager.GetTodos(key); len(items) > 0 {
				m.todos = make([]protocol.TodoItem, len(items))
				copy(m.todos, items)
				m.todosDoneCleared = false
			} else {
				m.todos = nil
				m.todosDoneCleared = false
			}
		} else {
			m.todos = nil
			m.todosDoneCleared = false
		}
		m.updatePlaceholder()
	}
}

// resetToIdleState resets ALL model state to a clean idle state.
// Used when switching to a session with no saved state (new session,
// deleted session, default session after deletion).
// This replaces the old inline state reset blocks that were duplicated
// across deleteLocalSession and other paths.
// resetToIdleState resets ALL model state to a clean idle state.
// Used when switching to a session with no saved state (new session,
// deleted session, default session after deletion).
// This replaces the old inline state reset blocks that were duplicated
// across deleteLocalSession and other paths.
func (m *cliModel) resetToIdleState() {
	// --- Core Message State ---
	m.messages = nil
	m.lastTokenUsage = nil
	m.streamingMsgIdx = -1
	m.newContentHint = false
	m.userScrolledUp = false
	m.rc.resetAll()

	// --- Agent State ---
	m.agentTurnID = 0
	m.typing = false
	m.typingStartTime = time.Time{}
	m.inputReady = false
	m.lastCompletedTools = nil
	m.lastReasoning = ""
	m.reasoningByIter = make(map[int]string)
	m.progressState.streamReasoningByIter = nil
	m.lastThinking = ""

	// --- Message Queue ---
	m.messageQueue = nil
	m.queueEditing = false
	m.queueEditBuf = ""
	m.needFlushQueue = false
	m.pendingToolSummary = nil

	// --- Progress State ---
	m.progressState.current = nil
	m.progressState.iterations = nil
	m.progressState.lastIter = 0
	m.progressState.lastSeq = 0
	m.progressState.iterStart = time.Time{}

	// --- TODO State ---
	m.todos = nil
	m.todosDoneCleared = false
	m.toolSummaryExpanded = false
	if m.expandedTools == nil {
		m.expandedTools = make(map[string]bool)
	} else {
		clear(m.expandedTools)
	}

	// --- UI State ---
	m.completions = nil
	m.compIdx = 0
	m.fileCompletions = nil
	m.fileCompIdx = 0
	m.fileCompActive = false
	m.rewindMode = false
	m.rewindItems = nil
	m.rewindCursor = 0
	m.rewindResult = nil
	m.checkpointState = nil

	// --- Panel State ---
	m.panelState.mode = ""
	m.panelState.settingsSaving = false
	m.panelState.stack = nil
	m.panelState.cursor = 0
	m.panelState.editing = false
	m.panelState.scrollY = 0
	m.panelState.editTA = textarea.Model{}
	m.panelState.combo = false
	m.panelState.comboIdx = 0
	m.panelState.askItems = nil
	m.panelState.askTab = 0
	m.panelState.askOptSel = make(map[int]map[int]bool)
	m.panelState.askOptCursor = make(map[int]int)
	m.panelState.askAnswerTA = textarea.Model{}
	m.panelState.askOtherTI = textinput.Model{}
	m.panelState.askScrollY = 0
	m.panelState.askTotalLines = 0
	m.panelState.schema = nil
	m.panelState.approvalReq = nil
	m.panelState.approvalCh = nil
	m.panelState.approvalCursor = 0
	m.panelState.approvalDenyTA = textinput.Model{}
	m.panelState.approvalDenyMode = false
	m.panelState.values = make(map[string]string)
	m.panelState.prevProvider = ""
	m.panelState.onSubmit = nil
	m.panelState.onAnswer = nil
	m.panelState.onCancel = nil
	m.panelState.bgTasks = nil
	m.panelState.bgAgents = nil
	m.panelState.bgCursor = 0
	m.panelState.bgViewing = false
	m.panelState.bgLogLines = nil
	m.panelState.bgLogFollow = false
	m.panelState.sessItems = nil
	m.panelState.sessCursor = 0
	m.panelState.sessViewing = false
	m.panelState.sessConfirmDelete = false
	m.panelState.sessConfirmEntry = SessionPanelEntry{}
	m.panelState.dangerItems = nil
	m.panelState.dangerCursor = 0
	m.panelState.dangerConfirm = false
	m.panelState.dangerInput = textinput.Model{}
	m.panelState.dangerOnExec = nil
	m.panelState.runnerServerTI = textinput.Model{}
	m.panelState.runnerTokenTI = textinput.Model{}
	m.panelState.runnerWS = textinput.Model{}
	m.panelState.runnerEditField = 0
	m.updateNotice = nil
	m.checkingUpdate = false
	m.panelState.channelItems = nil
	m.panelState.channelCursor = 0
	m.panelState.channelCfg = make(map[string]map[string]string)

	// --- Quick Switch State ---
	m.quickSwitchMode = ""
	m.quickSwitchList = nil
	m.quickSwitchCursor = 0
	m.quickSwitchReturnToPanel = false

	// --- Command Palette ---
	m.paletteOpen = false
	m.paletteInput = textinput.Model{}
	m.paletteItems = nil
	m.paletteFiltered = nil
	m.paletteCursor = 0
	m.paletteScrollY = 0
	m.paletteActiveCategory = ""
	m.paletteContributor = nil

	// --- Typewriter State ---
	m.progressState.twActive = false
	m.progressState.twVisible = 0
	m.progressState.rwVisible = 0
	m.progressState.rwCjkSkip = false
	m.progressState.twCjkSkip = false

	// --- Other State ---
	m.inputHistory = nil
	m.inputHistoryIdx = -1
	m.inputDraft = ""
	m.placeholderText = ""
	m.tempStatus = ""
	m.pendingCmds = nil
	m.bgTaskCount = 0
	m.agentCount = 0

	// --- Per-session LLM state (prevents leaking from previous session) ---
	m.activeSubID = ""
	m.cachedModelName = ""
	m.hasNoSubCacheValid = false
	m.cachedMaxContextTokens = 0
	m.cachedMaxOutputTokens = 0
	m.cachedCompressRatio = 0
}

// postRestoreSessionSetup handles the common setup after restoreSession() in all
// session switch paths. ALL session switches (panel, /su, /chat, create, delete)
// must call this — never manually reset state as a substitute.
//
// Resets turn tracking, clears stale progress/tokens, subscribes to WS events,
// starts async history loading, and checks for pending AskUser.
// postRestoreSessionSetup handles the common setup after restoreSession() in all
// session switch paths. ALL session switches (panel, /su, /chat, create, delete)
// must call this — never manually reset state as a substitute.
//
// Resets turn tracking, clears stale progress/tokens, subscribes to WS events,
// starts async history loading, and checks for pending AskUser.
func (m *cliModel) postRestoreSessionSetup() []tea.Cmd {
	isRemote := m.channel != nil && m.channel.config.DynamicHistoryLoader != nil
	var cmds []tea.Cmd

	// Clear token display state — new session should not show stale token bar.
	// NOTE: Do NOT clear cachedMaxContextTokens / cachedMaxOutputTokens here.
	// They are LLM session state, not display artifacts. If restoreSession() got
	// them from savedSessions, they're correct. If not, we'll load from session JSON below.
	m.lastTokenUsage = nil
	m.cachedCompressRatio = 0

	// Clear session-scoped flags that must NOT leak across sessions.
	// compressionReloading: if set by a previous session's HistoryCompacted event,
	//   it permanently blocks auto-start in the new session (Bug: stale flag leak).
	// turnDoneFlags: per-turn reply/done tracking from the old session is meaningless
	//   in the new session and can cause premature queue flush (Bug: stale flags).
	m.splashState.compReloading = false
	m.turnDoneFlags = make(map[uint64]*turnDoneFlag)

	// ── Session LLM state restoration ──────────────────────────
	// Only when in-memory caches are empty (new session or TUI restart).
	// Uses unified LoadSessionLLMState + applySessionLLMState to ensure
	// activeSubID, cachedModelName, cachedMaxContextTokens, cachedMaxOutputTokens
	// are ALWAYS consistent. No scattered field-by-field assignments.
	if m.activeSubID == "" && m.cachedModelName == "" {
		// Agent sessions: skip default subscription fallback — the model name,
		// context limits, and token usage come from the SubAgent's config via
		// handleSuHistoryLoad (AgentSessionLLMStateFn). Setting the default
		// subscription here would show the parent agent's model name instead.
		if m.channelName == "agent" {
			// No-op: model name will be populated by handleSuHistoryLoad
		} else {
			state := LoadSessionLLMState(m.workDir, m.chatID)
			if !state.IsZero() {
				// Found persisted LLM state on disk — apply to caches atomically
				m.applySessionLLMState(state)
				// Refresh values cache so GetCurrentValues() reflects the session's
				// per-session subscription, not the previous session's or the startup default.
				if m.channel != nil && m.channel.config.RefreshValuesCache != nil && state.SubscriptionID != "" {
					m.channel.config.RefreshValuesCache(state.SubscriptionID)
				}
				// Restore the actual LLM client via SwitchLLM (creates new client)
				if state.SubscriptionID != "" && m.subscriptionMgr != nil {
					if subs, err := m.subscriptionMgr.List(""); err == nil {
						for i := range subs {
							if subs[i].ID == state.SubscriptionID {
								if m.channel != nil && m.channel.config.SwitchLLM != nil {
									switchFn := m.channel.config.SwitchLLM
									target := subs[i]
									m.pendingCmds = append(m.pendingCmds, func() tea.Msg {
										err := switchFn(target.Provider, target.BaseURL, target.APIKey, target.Model)
										return cliSwitchLLMDoneMsg{
											err:       err,
											subID:     target.ID,
											subName:   target.Name,
											subModel:  target.Model,
											maxCtx:    resolveSubMaxContext(&target),
											maxOutTok: resolveSubMaxOutputTokens(&target),
											mgr:       m.subscriptionMgr,
										}
									})
								}
								break
							}
						}
					}
				}
			} else {
				// No per-session override on disk — load global default subscription
				if m.subscriptionMgr != nil {
					if defSub, err := m.subscriptionMgr.GetDefault(""); err == nil && defSub != nil {
						m.activeSubID = defSub.ID
						m.cachedModelName = defSub.Model
						m.cachedMaxContextTokens = resolveSubMaxContext(defSub)
						m.cachedMaxOutputTokens = int64(resolveSubMaxOutputTokens(defSub))
					}
				}
				// Resolve cachedMaxContextTokens if still zero (e.g. default sub
				// had no per-model config and resolveSubMaxContext returned 0).
				// Without this, the context bar stays as a white line until
				// the first progress event triggers the lazy resolve.
				if m.cachedMaxContextTokens == 0 {
					m.cachedMaxContextTokens = m.resolveMaxContextTokens()
				}
				if m.cachedMaxOutputTokens == 0 {
					m.cachedMaxOutputTokens = m.resolveMaxOutputTokens()
				}
			}
		}
	}

	if isRemote {
		// Remote mode: discard all stale client-side turn state.
		// The server RPC (handleSuHistoryLoad) is the single source of truth.
		m.progressState.current = nil
		m.typing = false
		m.needFlushQueue = false
		m.turnCancelled = false
		m.progressState.twActive = false
		m.progressState.lastSeq = 0
		m.splashState.suPhaseConfirmed = false
		m.inputReady = false // stays false until handleSuHistoryLoad completes
		m.splashState.suLoading = true
		m.splashState.frame = 0

		// Subscribe to the new session's chatID on the Hub.
		if m.channel.config.BindChatFn != nil {
			_ = m.channel.config.BindChatFn(m.chatID)
		} else {
			m.showSystemMsg("⏳ 该会话的消息推送订阅未初始化，进度可能无法实时更新", feedbackWarning)
		}

		// Async history loading — handleSuHistoryLoad sets inputReady=true
		// and reconciles typing/progress with server state.
		cmds = append(cmds, m.checkAndRestorePendingAskUser())
		cmds = append(cmds, m.suLoadHistoryCmd())
	} else {
		// Local mode: for regular CLI sessions, restored state is authoritative.
		// Agent sessions also need suLoadHistoryCmd (uses AgentSessionDumpFn
		// for in-memory agent state, not DB RPC) to load progress + history.
		if m.channelName == "agent" {
			m.progressState.current = nil
			m.typing = false
			m.progressState.lastSeq = 0
			m.splashState.suPhaseConfirmed = false
			m.inputReady = false
			m.splashState.suLoading = true
			cmds = append(cmds, m.suLoadHistoryCmd())
		} else {
			m.inputReady = true
		}
	}

	return cmds
}

// checkAndRestorePendingAskUser checks disk for a pending AskUser question for
// the current session and opens the panel if found.
// checkAndRestorePendingAskUser checks disk for a pending AskUser question for
// the current session and opens the panel if found.
func (m *cliModel) checkAndRestorePendingAskUser() tea.Cmd {
	if m.channelName == "" || m.chatID == "" {
		return nil
	}
	pu := m.loadPendingAskUser(m.chatID)
	if pu == nil {
		return nil
	}
	// Don't restore if already in an AskUser panel for this session.
	if m.panelState.mode == "askuser" && m.askUserSession == m.chatID {
		return nil
	}
	// Don't restore if currently in another panel mode.
	if m.panelState.mode != "" && m.panelState.mode != "askuser" {
		return nil
	}

	// Parse questions from saved metadata.
	var qs []askQItem
	if err := json.Unmarshal([]byte(pu.Questions), &qs); err != nil || len(qs) == 0 {
		log.WithError(err).WithField("chat_id", m.chatID).Warn("Failed to parse pending ask_user questions, removing corrupt file")
		m.deletePendingAskUser(m.chatID)
		return nil
	}
	items := make([]askItem, len(qs))
	for i, q := range qs {
		items[i] = askItem{Question: q.Question, Options: q.Options}
	}

	requestID := pu.RequestID // capture for callbacks

	log.WithField("chat_id", m.chatID).Info("Restoring pending AskUser panel from disk")
	m.askUserSession = m.chatID // bind AskUser to current session
	m.openAskUserPanel(items, m.pendingAskUserOnAnswer(requestID), m.pendingAskUserOnCancel(requestID))
	return nil
}

// pendingAskUserOnAnswer returns a callback for answered pending questions.
// It sends the answer back and cleans up the persisted file.
// pendingAskUserOnAnswer returns a callback for answered pending questions.
// It sends the answer back and cleans up the persisted file.
func (m *cliModel) pendingAskUserOnAnswer(requestID string) func(map[string]string) {
	return func(answers map[string]string) {
		var parts []string
		for k, v := range answers {
			if k == "other_input" {
				parts = append(parts, v)
			} else {
				parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", k, v))
			}
		}
		content := strings.Join(parts, "\n\n")
		meta := map[string]string{"ask_user_answered": "true"}
		if requestID != "" {
			meta["request_id"] = requestID
		}
		m.sendInboundWait(m.newInbound(content, meta), 5*time.Second)
		m.deletePendingAskUser(m.askUserSession)
		m.startAgentTurn()
	}
}

// pendingAskUserOnCancel returns a callback for cancelled pending questions.
// It cleans up the persisted file and closes the panel.
// pendingAskUserOnCancel returns a callback for cancelled pending questions.
// It cleans up the persisted file and closes the panel.
func (m *cliModel) pendingAskUserOnCancel(requestID string) func() {
	// Capture chatID at panel open time, not at cancel time.
	chatID := m.askUserSession
	return func() {
		m.deletePendingAskUser(chatID)
		m.showSystemMsg(m.locale.AskCancelled, feedbackInfo)
		m.typing = false
		m.updatePlaceholder()
		m.inputReady = true
		m.resetProgressState()
		m.updateViewportContent()
	}
}

// savePendingToSessionState syncs m.pendingUserMsg into the saved session state
// for the current session. Call after setting m.pendingUserMsg to ensure it survives
// a subsequent saveCurrentSession() call during session switch.
// savePendingToSessionState syncs m.pendingUserMsg into the saved session state
// for the current session. Call after setting m.pendingUserMsg to ensure it survives
// a subsequent saveCurrentSession() call during session switch.
func (m *cliModel) savePendingToSessionState() {
	key := m.sessionKey()
	if m.savedSessions == nil {
		m.savedSessions = make(map[string]*sessionState)
	}
	if existing, ok := m.savedSessions[key]; ok {
		existing.pendingUserMsg = m.pendingUserMsg
	}
	// If no existing state, it'll be created in saveCurrentSession() which
	// reads m.pendingUserMsg directly.
}

// cliHistoryReloadMsg context compression 后重新加载历史完成消息
// cliHistoryReloadMsg context compression 后重新加载历史完成消息
type cliHistoryReloadMsg struct {
	channelName      string
	chatID           string
	history          []ch.HistoryMessage
	err              error
	forceFullRebuild bool
}

// cliTokenRefreshMsg refreshes the context bar after compression.
// Pushed through asyncCh by refreshTokenStateAfterReload.
// cliSessionControlMsg AI-triggered TUI session control message.
// Sent from tui_control tool via asyncCh to operate sidebar sessions.
type cliSessionControlMsg struct {
	action string                 // "switch" | "close"
	chatID string                 // target session chatID
	params map[string]string      // extra params (e.g. confirm=true)
	result chan *cliSessionResult // result channel (tool blocks on this)
}

// cliSessionResult carries the outcome of a TUI session control operation.
// cliSessionResult carries the outcome of a TUI session control operation.
type cliSessionResult struct {
	ok  bool
	err string
}

// cliModel Bubble Tea 状态模型
// reloadMessagesFromSession triggers async history reload after context compression.
// The engine has replaced its internal message list and persisted to session DB;
// CLI must rebuild m.messages to stay in sync.
func (m *cliModel) reloadMessagesFromSession(forceFullRebuild bool) {
	if m.channel == nil {
		return
	}
	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return
	}
	chatID := m.chatID
	channelName := m.channelName
	clipanic.Go("ch.cliModel.reloadMessagesFromSession", func() {
		history, err := loader(channelName, chatID)
		// Send result via async channel (goroutine-safe).
		// Use blocking send with timeout instead of silent drop.
		// The old `default` drop was a bug: if asyncCh was full (256 buffer),
		// the reload message was lost forever — compReloading stayed true,
		// streaming message was never recreated, TUI froze permanently.
		// Now we block up to 10s to ensure delivery.
		if m.channel != nil {
			reloadMsg := cliHistoryReloadMsg{
				channelName:      channelName,
				chatID:           chatID,
				history:          history,
				err:              err,
				forceFullRebuild: forceFullRebuild,
			}
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case m.channel.asyncCh <- reloadMsg:
			case <-timer.C:
				log.Warn("reloadMessagesFromSession: asyncCh full after 10s, reload dropped")
			case <-m.channel.stopCh:
				return
			}
		}
	})
}

// toggleSidebarSection toggles the collapse state of a sidebar section and persists to preferences.
