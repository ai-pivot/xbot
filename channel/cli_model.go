package channel

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/glamour"
	"strings"
	"time"
	"xbot/clipanic"
	"xbot/internal/textarea"
	log "xbot/logger"
	"xbot/plugin"
	"xbot/protocol"
	"xbot/tools"
	"xbot/version"
)

func newAnimTicker(frames []string, color string) *animTicker {
	altColor := currentTheme.AccentAlt
	return &animTicker{
		frames:   frames,
		style:    lipgloss.NewStyle().Foreground(lipgloss.Color(color)),
		styleAlt: lipgloss.NewStyle().Foreground(lipgloss.Color(altColor)),
		color:    color,
		colorAlt: altColor,
	}
}

func (t *animTicker) tick() {
	t.ticks++
	// Advance frame only every `speed` ticks (speed=1 → every tick, speed=3 → every 3rd)
	if t.speed <= 1 || t.ticks%int64(t.speed) == 0 {
		t.frame = (t.frame + 1) % len(t.frames)
	}
}

// view 渲染当前帧，带双色呼吸效果（每 10 tick 在两种颜色间切换）
func (t *animTicker) view() string {
	if t.ticks%20 < 10 {
		return t.style.Render(t.frames[t.frame])
	}
	return t.styleAlt.Render(t.frames[t.frame])
}

// viewFrames renders a frame from a given set using the ticker's current frame index.
// speedOverride controls per-call animation speed (0 = use ticker's default speed).
// 同样带呼吸效果。
func (t *animTicker) viewFrames(frames []string, speedOverride ...int) string {
	speed := t.speed
	if len(speedOverride) > 0 && speedOverride[0] > 0 {
		speed = speedOverride[0]
	}
	// Calculate effective frame based on speed
	effectiveFrame := t.frame
	if speed > 1 {
		// Use a separate counter for this frame set, keyed by speed
		effectiveFrame = int(t.ticks/int64(speed)) % len(frames)
	}
	idx := effectiveFrame % len(frames)
	if t.ticks%20 < 10 {
		return t.style.Render(frames[idx])
	}
	return t.styleAlt.Render(frames[idx])
}

// isCJK reports whether r is a CJK character (ideographs, kana, hangul, etc.).
func isCJK(r rune) bool {
	return r >= 0x2E80
}

// advanceTypewriter advances both typewriters (stream + reasoning) on each tick.
// Called every typewriterTickMsg (50ms) during streaming.
func (m *cliModel) advanceTypewriter() {
	if m.progress == nil {
		m.twVisible = 0
		m.rwVisible = 0
		return
	}

	// Advance reasoning writer
	if m.progress.ReasoningStreamContent != "" {
		target := len([]rune(m.progress.ReasoningStreamContent))
		m.advanceWriterCJK(&m.rwVisible, target, m.progress.ReasoningStreamContent, &m.rwCjkSkipTick)
	}

	// Advance stream writer
	if m.progress.StreamContent != "" {
		target := len([]rune(m.progress.StreamContent))
		m.advanceWriterCJK(&m.twVisible, target, m.progress.StreamContent, &m.twCjkSkipTick)
	}
}

// advanceWriterCJK is like advanceWriter but CJK-aware: when the next rune to reveal
// is CJK, it only advances every other tick (effectively half speed).
// skipFlip tracks alternating ticks within a single call chain.
func (m *cliModel) advanceWriterCJK(visible *int, target int, content string, skipFlip *bool) {
	if target == 0 {
		*visible = 0
		return
	}
	gap := target - *visible
	if gap <= 0 {
		return
	}

	// Check if the next rune to reveal is CJK
	runes := []rune(content)
	nextIsCJK := *visible < len(runes) && isCJK(runes[*visible])

	// Gap-based acceleration — smooth catch-up without visible jumps.
	// Max advance per 50ms tick is capped to avoid teleporting when
	// network coalesces multiple stream updates into one big gap.
	advance := 1
	switch {
	case gap > 80:
		advance = 20
	case gap > 40:
		advance = 10
	case gap > 20:
		advance = 3
	}

	// CJK penalty: if next rune is CJK and we're at normal speed, skip every other tick
	if nextIsCJK && advance <= 3 && gap <= 20 {
		*skipFlip = !*skipFlip
		if *skipFlip {
			return // skip this tick, revealing nothing
		}
	}

	*visible += advance
	if *visible > target {
		*visible = target
	}
}

// Ticker frame presets
var (
	// diamondPulseFrames: pulsing diamond sweep — thinking/loading spinner
	diamondPulseFrames = []string{"◇◇◇◇◇", "◇◆◇◇◇", "◇◆◆◇◇", "◇◆◆◆◇", "◇◇◆◆◇", "◇◇◇◆◇"}
	// waveFrames: rotating crescent moon phases — subagent feel
	waveFrames = []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	// orbitFrames: spinning orbit — processing feel
	orbitFrames = []string{"◌", "◔", "◕", "●", "◕", "◔", "◌", "◔", "◕", "●", "◕", "◔"}
	// splashFrames: loading bar animation — 启动画面进度条
	splashFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	// pulseFrames: pulsing circle — tool completion pulse
	pulseFrames = []string{"◌", "◎", "◉", "◎", "◌"}
	// sidebarSpinnerFrames: braille spinner for sidebar busy sessions
	sidebarSpinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
)

// errorKeywords — system 消息中的错误检测关键词
var errorKeywords = []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"}

// pickVerb returns a deterministic verb based on tick count (changes every ~2s at 10 FPS).
func (m *cliModel) pickVerb(ticks int64) string {
	verbs := m.locale.ThinkingVerbs
	if len(verbs) == 0 {
		return "Thinking"
	}
	idx := (ticks / 20) % int64(len(verbs))
	return verbs[idx]
}

// pickIdlePlaceholder 根据时间返回轮换的 placeholder（每 5 秒切换）
func (m *cliModel) pickIdlePlaceholder() string {
	placeholders := m.locale.IdlePlaceholders
	if len(placeholders) == 0 {
		return ""
	}
	idx := int(time.Now().Unix()/5) % len(placeholders)
	return placeholders[idx]
}

// updatePlaceholder refreshes the placeholder text based on typing state.
// We store it in m.placeholderText instead of m.textarea.Placeholder to avoid
// CJK rendering bugs caused by textarea's internal placeholder↔normal view switch.
func (m *cliModel) updatePlaceholder() {
	if m.typing {
		m.placeholderText = m.locale.ProcessingPlaceholder
	} else {
		m.placeholderText = m.pickIdlePlaceholder()
	}
}

// cycleModel switches to the next model across all subscriptions.
// Uses ListAllModels() so models from ALL subscriptions are visible (not just the
// current default LLM). Cycles through the model names displayed in the status bar.
// Note: this only changes the cached model name — the actual subscription switch
// happens when a new LLM call is made (or via quick switch panel).
func (m *cliModel) cycleModel() {
	if m.channel == nil {
		return
	}

	// Ensure models are loaded synchronously before cycling.
	// Without this, the first Ctrl+N sees only the single fallback model
	// (the async fetch hasn't completed yet).
	m.channel.modelLister.EnsureModelsLoaded()

	// Use ListModels (current subscription only) instead of ListAllModels.
	// Ctrl+N should cycle through the current subscription's models only.
	models := m.channel.modelLister.ListModels()
	if len(models) < 2 {
		m.showTempStatus("Only one model available")
		return
	}

	current := m.cachedModelName
	nextIdx := 0
	for i, name := range models {
		if name == current {
			nextIdx = (i + 1) % len(models)
			break
		}
	}
	nextModel := models[nextIdx]

	m.cachedModelName = nextModel
	m.showTempStatus(fmt.Sprintf("Model: %s", nextModel))

	// Switch model on the current subscription (no need to change subscription
	// since we're already cycling within the current subscription's models).
	if m.llmSubscriber != nil {
		m.llmSubscriber.SwitchModel(m.senderID, nextModel, m.chatID)
	}
	// Persist per-session model choice
	existing := LoadSessionLLMState(m.workDir, m.chatID)
	existing.SubscriptionID = m.activeSubID
	existing.Model = nextModel
	SaveSessionLLMState(m.workDir, m.chatID, existing)
	m.updateQuickSwitchModels(nextModel)
}

// tickerTickMsg 是 ticker 定时 tick 消息

// debugCaptureMsg triggers a UI capture (dump View() to file).
type debugCaptureMsg struct{}

// splashDoneMsg 启动画面结束消息
type splashDoneMsg struct{}

// suHistoryLoadMsg /su 切换用户后的历史加载完成消息
type suHistoryLoadMsg struct {
	history        []HistoryMessage
	err            error
	channelName    string                  // target session at time of request
	chatID         string                  // target session at time of request
	activeProgress *protocol.ProgressEvent // non-nil if target session has an active agent turn
	// tokenState holds the last persisted token counts for the session.
	// Used as fallback when activeProgress is nil (idle session) so the
	// context bar still shows the session's last known token usage.
	tokenPrompt     int64
	tokenCompletion int64
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
func (m *cliModel) sessionKey() string {
	return qualifyChatID(m.channelName, m.chatID)
}

// saveCurrentSession saves the current session's live state into the savedSessions map.
func (m *cliModel) saveCurrentSession() {
	key := m.sessionKey()
	if m.savedSessions == nil {
		m.savedSessions = make(map[string]*sessionState)
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	m.savedSessions[key] = &sessionState{
		progress:               m.progress,
		typing:                 m.typing,
		agentTurnID:            m.agentTurnID,
		inputReady:             m.inputReady,
		needFlushQueue:         m.needFlushQueue,
		lastProgressSeq:        m.lastProgressSeq,
		twVisible:              m.twVisible,
		rwVisible:              m.rwVisible,
		iterationHistory:       m.iterationHistory,
		lastSeenIteration:      m.lastSeenIteration,
		streamingMsgIdx:        m.streamingMsgIdx,
		typingStartTime:        m.typingStartTime,
		lastReasoning:          m.lastReasoning,
		lastThinking:           m.lastThinking,
		turnCancelled:          m.turnCancelled,
		typewriterTickActive:   m.typewriterTickActive,
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
func (m *cliModel) restoreSession() {
	key := m.sessionKey()
	if saved, ok := m.savedSessions[key]; ok {
		m.progress = saved.progress
		m.typing = saved.typing
		m.agentTurnID = saved.agentTurnID
		m.inputReady = saved.inputReady
		m.needFlushQueue = saved.needFlushQueue
		m.lastProgressSeq = saved.lastProgressSeq
		m.twVisible = saved.twVisible
		m.rwVisible = saved.rwVisible
		m.iterationHistory = saved.iterationHistory
		m.lastSeenIteration = saved.lastSeenIteration
		m.streamingMsgIdx = saved.streamingMsgIdx
		m.typingStartTime = saved.typingStartTime
		m.lastReasoning = saved.lastReasoning
		m.lastThinking = saved.lastThinking
		m.turnCancelled = saved.turnCancelled
		m.typewriterTickActive = saved.typewriterTickActive
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
		m.progress = nil
		m.typing = false
		m.streamingMsgIdx = -1
		m.iterationHistory = nil
		m.invalidateProgressHistoryCache()
		m.lastSeenIteration = 0
		m.typingStartTime = time.Time{}
		m.lastReasoning = ""
		m.reasoningByIter = nil
		m.lastThinking = ""
		m.turnCancelled = false
		m.inputReady = false
		m.needFlushQueue = false
		m.messageQueue = nil
		m.queueEditing = false
		m.lastProgressSeq = 0
		m.twVisible = 0
		m.rwVisible = 0
		m.typewriterTickActive = false
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
		// Reset bg task count from backend for this session
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

	// ── Session LLM state restoration ──────────────────────────
	// Only when in-memory caches are empty (new session or TUI restart).
	// Uses unified LoadSessionLLMState + applySessionLLMState to ensure
	// activeSubID, cachedModelName, cachedMaxContextTokens, cachedMaxOutputTokens
	// are ALWAYS consistent. No scattered field-by-field assignments.
	if m.activeSubID == "" && m.cachedModelName == "" {
		state := LoadSessionLLMState(m.workDir, m.chatID)
		if !state.IsZero() {
			// Found persisted LLM state on disk — apply to caches atomically
			m.applySessionLLMState(state)
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
				}
			}
			// Auto-discover: if model name is still empty after loading default sub,
			// try listing available models and pick the first one.
			if m.cachedModelName == "" && m.channel != nil && m.channel.modelLister != nil {
				m.channel.modelLister.EnsureModelsLoaded()
				if models := m.channel.modelLister.ListModels(); len(models) > 0 {
					m.cachedModelName = models[0]
					if m.llmSubscriber != nil {
						m.llmSubscriber.SwitchModel(m.senderID, models[0], m.chatID)
					}
					existing := LoadSessionLLMState(m.workDir, m.chatID)
					existing.Model = models[0]
					SaveSessionLLMState(m.workDir, m.chatID, existing)
				}
			}
		}
	}

	if isRemote {
		// Remote mode: discard all stale client-side turn state.
		// The server RPC (handleSuHistoryLoad) is the single source of truth.
		m.progress = nil
		m.typing = false
		m.needFlushQueue = false
		m.turnCancelled = false
		m.typewriterTickActive = false
		m.lastProgressSeq = 0
		m.suPhaseDoneConfirmed = false
		m.inputReady = false // stays false until handleSuHistoryLoad completes
		m.suLoading = true
		m.splashFrame = 0

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
			m.progress = nil
			m.typing = false
			m.lastProgressSeq = 0
			m.inputReady = false
			m.suLoading = true
			cmds = append(cmds, m.suLoadHistoryCmd())
		} else {
			m.inputReady = true
		}
	}

	return cmds
}

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
	if m.panelMode == "askuser" && m.askUserSession == m.chatID {
		return nil
	}
	// Don't restore if currently in another panel mode.
	if m.panelMode != "" && m.panelMode != "askuser" {
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
type cliHistoryReloadMsg struct {
	channelName string
	chatID      string
	history     []HistoryMessage
	err         error
}

// cliTokenRefreshMsg refreshes the context bar after compression.
// Pushed through asyncCh by refreshTokenStateAfterReload.
type cliTokenRefreshMsg struct {
	channelName     string
	chatID          string
	tokenPrompt     int64
	tokenCompletion int64
}

// cliToastItem 单条 Toast 通知数据
type cliToastItem struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastMsg Toast 通知消息（入队显示，自动消失）
type cliToastMsg struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastClearMsg Toast 通知自动清除消息（弹出队列头部）
type cliToastClearMsg struct{}

// cliSessionControlMsg AI-triggered TUI session control message.
// Sent from tui_control tool via asyncCh to operate sidebar sessions.
type cliSessionControlMsg struct {
	action string                 // "switch" | "close"
	chatID string                 // target session chatID
	params map[string]string      // extra params (e.g. confirm=true)
	result chan *cliSessionResult // result channel (tool blocks on this)
}

// cliSessionResult carries the outcome of a TUI session control operation.
type cliSessionResult struct {
	ok  bool
	err string
}

// cliModel Bubble Tea 状态模型
type cliModel struct {
	// --- Core UI ---
	viewport viewport.Model // 消息显示区
	textarea textarea.Model // 用户输入区

	// §22 输入历史
	inputHistory    []string    // 已发送输入历史（新 → 旧），仅会话内
	inputHistoryIdx int         // -1 = 不在浏览模式, >=0 = 当前浏览索引
	inputDraft      string      // 进入历史浏览前的输入草稿
	ticker          *animTicker // 进度动画 ticker
	width           int         // 终端宽度
	height          int         // 终端高度
	styles          cliStyles
	locale          *UILocale // i18n: current UI locale

	// §23 Placeholder: stored separately from textarea to avoid CJK rendering bug.
	// Textarea's built-in Placeholder causes a view-mode switch (placeholder→normal)
	// that triggers cellbuf incremental diff issues on Windows Terminal with CJK chars.
	placeholderText string // current placeholder string to display in View

	// --- Message state ---
	messages        []cliMessage          // 消息历史
	renderer        *glamour.TermRenderer // Markdown 渲染器
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）
	newContentHint  bool                  // 有新内容但用户未在底部（显示 ↓ 提示）
	ready           bool                  // 是否已初始化

	// --- Agent state ---
	agentTurnID       uint64                       // monotonically increasing turn counter
	typing            bool                         // agent 是否正在回复
	typingStartTime   time.Time                    // 本次处理开始时间
	inputReady        bool                         // 输入就绪状态（agent 回复期间禁止发送）
	sendInboundFn     func(InboundMsg) bool        // forward to server via backend.SendInbound
	tempStatus        string                       // 临时状态提示（自动过期）
	pendingCmds       []tea.Cmd                    // commands queued by helpers (auto-drained in Update)
	shouldQuit        bool                         // Smart quit: quit after current operation completes
	trimHistoryFn     func(cutoff time.Time) error // /rewind: delete DB messages at or after cutoff timestamp
	resetTokenStateFn func()                       // /rewind: clear stale prompt/completion token counts

	// --- Message queue (typing 期间排队的消息) ---
	messageQueue   []queuedMsg // 排队等待发送的消息（绑定 chatID 防止跨 session 误投）
	queueEditing   bool        // true = 正在编辑/查看最后一条排队消息
	queueEditBuf   string      // 编辑中的排队消息内容
	needFlushQueue bool        // true = handleAgentMessage 后需要刷新队列

	// --- Background tasks ---
	bgTaskCount     int                       // running background tasks (0 = no indicator)
	bgTaskCountFn   func() int                // callback to get current bg task count (set by channel)
	bgTaskListFn    func() []*BgTask          // callback to list running tasks (remote mode)
	bgTaskKillFn    func(taskID string) error // callback to kill a task (remote mode)
	bgTaskCleanupFn func()                    // callback to cleanup completed tasks (remote mode)

	// --- Interactive agents ---
	agentCount      int                                                            // active interactive agent sessions (0 = no indicator)
	agentCountFn    func() int                                                     // callback to get current agent count (set by channel)
	agentListFn     func() []panelAgentEntry                                       // callback to list active agents for panel
	agentInspectFn  func(roleName, instance string, tailCount int) (string, error) // callback to inspect agent activity
	agentMessagesFn func(roleName, instance string) []SessionChatMessage           // callback to get agent conversation messages
	sessionsListFn  func() []SessionPanelEntry                                     // callback to list all sessions for Sessions panel

	// --- Usage query ---
	usageQueryFn func(senderID string, days int) (cumulative *UserTokenUsage, daily []DailyTokenUsage, err error)

	// --- Plugin management ---
	pluginMgrFn       func() *plugin.PluginManager
	widgetRegistry    *plugin.WidgetRegistry // UI widget registry from plugin system
	pluginReloading   bool                   // true when a reload operation is in progress
	remotePluginCache *remotePluginCache     // plugin data cache for remote mode (nil = local mode)

	// --- Web user management (admin only) ---
	createWebUserFn func(username string) (password string, err error)
	listWebUsersFn  func() ([]map[string]any, error)
	deleteWebUserFn func(username string) error
	isAdminFn       func() bool

	// --- Progress ---
	progress               *protocol.ProgressEvent
	iterationHistory       []cliIterationSnapshot       // 已完成迭代快照
	lastSeenIteration      int                          // 上次进度事件的迭代号
	lastProgressSeq        uint64                       // 上次进度事件的序列号（单调递增校验）
	iterationStartTime     time.Time                    // current iteration wall-clock start time
	sidebarHasBusySessions bool                         // true when any non-active sidebar session is busy (needs spinner tick)
	unreadSessions         map[string]bool              // chatID → has unread results the user hasn't viewed yet
	lastBusyStates         map[string]bool              // previous busy state per session, for detecting busy→idle transition
	liveSessionStates      map[string]*liveSessionState // server-pushed session state overrides
	typewriterTickActive   bool                         // true when typewriter tick chain (50ms) is running
	twVisible              int                          // typewriter: runes currently visible in stream content
	rwVisible              int                          // typewriter: runes currently visible in reasoning stream content
	rwCjkSkipTick          bool                         // alternates each tick to halve CJK speed (reasoning)
	twCjkSkipTick          bool                         // alternates each tick to halve CJK speed (stream)

	// --- Session ---
	workDir                string    // 工作目录（标题栏显示用）
	remoteMode             bool      // 是否连接 remote backend（标题栏提示用）
	remoteServerURL        string    // remote server host for header display (e.g. "host:port")
	connState              string    // WS connection state: "connected"|"disconnected"|"reconnecting"
	debugMode              bool      // --debug: UI capture + key injection via SIGUSR1
	debugCaptureMs         int       // --debug-capture-ms: UI capture interval in ms (0 = default 1000)
	ephemeral              bool      // --ephemeral: no persistence, clean slate for benchmarking
	senderID               string    // 当前身份 ID（默认 "cli_user"，/su 命令可切换）
	channelName            string    // 当前 channel（默认 "cli"，/su 切换时可能变为 "web"）
	defaultChatID          string    // 默认 chatID（/su 切换回来时恢复）
	chatID                 string    // 会话 ID（按工作目录区分）
	sessionName            string    // 当前会话名称（同目录多 session 支持）
	shiftHintUntil         time.Time // 鼠标无 Shift 操作时显示选中提示的截止时间（zero = 隐藏）
	newContentHintRendered string    // rendered "↓ 新内容" string for zone width measurement
	newContentHintXStart   int       // X position of newContentHint in status bar

	// --- §1 增量渲染 ---
	renderCacheValid    bool   // 全局缓存是否有效（resize 后置 false）
	cachedHistory       string // 缓存的历史消息渲染结果（不含当前流式消息）
	cachedMsgCount      int    // messages count when cache was built
	lastViewportContent string // 上次 setViewportContent 的原始内容（去重用）
	lastViewportWidth   int    // 上次 setViewportContent 的宽度（去重用）
	// Two-tier wrap cache: avoid O(N*W) hardWrapRunes on the growing history every tick.
	// cachedWrappedHistory stores the hard-wrapped version of cachedHistory at the current width.
	// Only the dynamic suffix (progress block, rewind result) is re-wrapped on each tick.
	cachedWrappedHistory      string // hard-wrapped cachedHistory (already split/wrapped at m.width)
	cachedWrappedHistoryRaw   string // the raw cachedHistory that was wrapped (for invalidation)
	cachedWrappedHistoryWidth int    // width at which cachedWrappedHistory was built

	// --- progress block cache ---
	cachedProgressHistory      string // cached rendered output of completed iterations (dimmed)
	cachedProgressHistoryLen   int    // len(iterationHistory) when cache was built
	cachedProgressHistoryWidth int    // viewport width when cache was built

	// Current iteration static content cache — avoids re-rendering reasoning,
	// completed tools, tool content, and SubAgent tree on every 100ms tick.
	cachedCurrentStatic      string // rendered static parts of current iteration
	cachedCurrentStaticWidth int    // bubbleWidth when cache was built
	cachedCurrentIter        int    // progress.Iteration when cache was built
	cachedCurrentStaticFP    uint64 // fingerprint of static content for dirty detection

	// Reasoning/stream/thinking block caches — avoids per-line Style.Render,
	// lipgloss.Width, and hardWrapRunes on every tick when content is static.
	cachedReasoningBlock      string // rendered reasoning lines with guides and cursor
	cachedReasoningBlockFP    uint64 // fingerprint for dirty detection
	cachedReasoningBlockWidth int    // innerWidth when cache was built

	cachedStreamBlock      string // rendered stream lines with guides and cursor
	cachedStreamBlockFP    uint64 // fingerprint for dirty detection
	cachedStreamBlockWidth int    // innerWidth when cache was built

	cachedThinkingBlock      string // rendered thinking lines with guides
	cachedThinkingBlockFP    uint64 // fingerprint for dirty detection
	cachedThinkingBlockWidth int    // innerWidth when cache was built

	// --- §2 工具可视化 ---
	lastCompletedTools []protocol.ToolProgress // 每轮结束时快照，不依赖 m.progress 生命周期
	lastReasoning      string                  // 最后一次迭代的 reasoning_content，在 progress 清除前捕获
	reasoningByIter    map[int]string          // per-iteration reasoning，snapshot 时用于精确查找
	lastThinking       string                  // 最后一次迭代的 thinking_content，在 progress 清除前捕获

	// --- §8 Tab 补全 ---
	completions []string // 当前补全候选项
	compIdx     int      // 当前选中的补全索引

	// --- §8b @ 文件引用补全 ---
	fileCompletions []string // @ 文件路径补全候选项
	fileCompIdx     int      // 当前选中的文件补全索引
	fileCompActive  bool     // true = Tab 循环中，阻止重新 glob

	// --- §9 Rewind (/rewind command) ---
	rewindMode      bool                      // true = rewind overlay active
	rewindItems     []rewindItem              // candidate user messages for rewind selection
	rewindCursor    int                       // selected index in rewindItems
	rewindResult    *protocol.RewindResult    // result of the last rewind operation (for display)
	checkpointState *protocol.CheckpointState // file checkpoint state for rewind file rollback (nil = no file tracking)

	// --- §10 TODO 进度条 ---
	todos            []protocol.TodoItem // 从 progress 事件同步的 TODO 列表
	todosDoneCleared bool                // 全完成后已被用户输入清除，阻止 progress 重填

	// --- §11 Tool Summary 折叠 ---
	toolSummaryExpanded bool // Ctrl+O 切换

	// --- §11b Pending Tool Summary ---
	// PhaseDone may arrive before handleAgentMessage. Store the tool_summary
	// here so handleAgentMessage can insert it at the correct position.
	pendingToolSummary *cliMessage

	// --- §12 Interactive Panel ---
	// panelMode: ""=normal, "settings"=settings panel, "askuser"=ask user panel
	panelMode      string
	settingsSaving bool              // true while onSubmit is running in background; blocks user input
	panelStack     []panelStackEntry // push/pop navigation stack for nested panels
	panelCursor    int               // settings panel: selected item index
	panelEdit      bool              // settings panel: editing current item
	panelScrollY   int               // panel 滚动偏移（手动管理，不依赖 viewport）
	panelEditTA    textarea.Model    // settings panel: inline editor
	panelCombo     bool              // settings panel: combo dropdown open
	panelComboIdx  int               // settings panel: combo selected option index
	// --- Panel state backup (for quick switch round-trip) ---
	panelValuesBackup   map[string]string              // saved panelValues before quick switch
	panelCursorBackup   int                            // saved panelCursor before quick switch
	panelOnSubmitBackup func(values map[string]string) // saved onSubmit callback
	// --- AskUser panel ---
	panelItems         []askItem            // askuser panel: question items
	panelTab           int                  // askuser panel: current tab (question index)
	panelOptSel        map[int]map[int]bool // askuser panel: selected option indices per question
	panelOptCursor     map[int]int          // askuser panel: highlighted option index per question
	panelAnswerTA      textarea.Model       // askuser panel: free-input editor (no-options mode)
	panelOtherTI       textinput.Model      // askuser panel: single-line Other input
	askPanelScrollY    int                  // askuser panel: internal scroll offset for long content
	askPanelTotalLines int                  // cached total line count for scroll clamping
	panelSchema        []SettingDefinition  // settings panel: schema copy
	// --- Approval panel ---
	approvalRequest      *protocol.ApprovalRequest // pending approval request
	approvalResultCh     chan<- protocol.ApprovalResult
	approvalCursor       int                             // 0=approve, 1=deny
	approvalDenyInput    textinput.Model                 // deny reason input
	approvalEnteringDeny bool                            // true when editing deny reason
	panelValues          map[string]string               // settings panel: current values
	panelPrevProvider    string                          // previous llm_provider value for base_url auto-fill
	panelOnSubmit        func(values map[string]string)  // callback on settings submit
	panelOnAnswer        func(answers map[string]string) // callback on askuser answers (key=index, value=answer)
	panelOnCancel        func()                          // callback on cancel

	// --- Bg Tasks Panel ---
	panelBgTasks   []*BgTask         // cached task list
	panelBgAgents  []panelAgentEntry // cached agent list
	panelBgCursor  int               // selected item index (tasks first, then agents)
	panelBgViewing bool              // true = viewing log of selected task

	panelBgLogLines  []string // cached log lines for viewing
	panelBgLogFollow bool     // auto-scroll to bottom on new output (follow-tail)

	// --- Sessions Panel ---
	panelSessionItems         []SessionPanelEntry // cached session list
	panelSessionCursor        int                 // selected item index
	panelSessionViewing       bool                // true = viewing session messages
	panelSessionConfirmDelete bool                // true = showing delete confirmation
	panelSessionConfirmEntry  SessionPanelEntry   // entry pending deletion

	// --- Danger Zone Panel ---
	panelDangerItems   []dangerItem
	panelDangerCursor  int
	panelDangerConfirm bool // true = showing confirm input
	panelDangerInput   textinput.Model
	panelDangerOnExec  func(targetType string) error // callback to execute clear

	// --- §13 Update Check ---

	// --- Runner Panel ---
	panelRunnerServerTI  textinput.Model     // server URL 输入
	panelRunnerTokenTI   textinput.Model     // token 输入
	panelRunnerWorkspace textinput.Model     // workspace 输入
	panelRunnerEditField int                 // 当前编辑字段 (0=server, 1=token, 2=workspace)
	updateNotice         *version.UpdateInfo // nil=nothing, non-nil=show notice
	checkingUpdate       bool                // true while /update is in progress

	// --- Channel Config Panel ---
	panelChannelItems  []string                     // channel names: ["web", "feishu", "qq", "napcat"]
	panelChannelCursor int                          // selected channel index
	panelChannelCfg    map[string]map[string]string // cached channel configs

	// --- §15 Subscription / Model Quick Switch ---
	quickSwitchMode          string              // ""=off, "subscription"=selecting subscription, "model"=selecting model
	quickSwitchList          []Subscription      // available subscriptions or models
	quickSwitchCursor        int                 // selected index
	quickSwitchReturnToPanel bool                // true = return to settings panel after switch completes
	subscriptionMgr          SubscriptionManager // injected by CLIChannel
	llmSubscriber            LLMSubscriber       // injected by CLIChannel

	// --- §23 Command Palette (Ctrl+K) ---
	paletteOpen           bool               // true = command palette overlay is active
	paletteInput          textinput.Model    // filter input
	paletteItems          []paletteCommand   // all available commands
	paletteFiltered       []paletteCommand   // commands after fuzzy filter
	paletteCursor         int                // selected index in filtered list
	paletteScrollY        int                // scroll offset for visible items
	paletteActiveCategory PaletteCategory    // active category tab (empty = all)
	paletteContributor    PaletteContributor // external command provider (plugins/skills/agents)

	// --- §16 Subscription generation guard ---
	// subGeneration increments every time the active subscription actually changes.
	// panelSubGeneration captures the generation when the settings panel opens.
	// ApplySettings REFUSES to write per-subscription LLM fields if generations don't match.
	// This is the structural guarantee against stale LLM values overwriting a new subscription.
	subGeneration      int
	panelSubGeneration int

	// --- §14 Splash 画面 ---
	splashDone           bool // true = splash 动画结束，进入正常界面
	splashFrame          int  // 当前 splash 动画帧索引
	suLoading            bool // true = /su 切换用户后正在加载历史，显示 loading 画面
	suPhaseDoneConfirmed bool // true = PhaseDone received during suLoading (server confirmed idle)

	// --- §16 Toast 通知队列 ---
	toasts     []cliToastItem // Toast 队列（头部=当前显示）
	toastTimer bool           // true = toast 消除计时器已启动

	// --- §19 长消息折叠 ---
	msgLineOffsets []int // 每条消息在 viewport 折行后 content 中的起始行号

	// --- §Session state save/restore ---
	// Per-session saved state so switching sessions doesn't lose in-progress state.
	// Key = "channelName:chatID". Messages are NOT saved here — DB is source of truth.
	savedSessions    map[string]*sessionState
	pendingUserMsg   *cliMessage       // most recent user message sent but not yet confirmed in DB
	pendingSuRestore *suHistoryLoadMsg // pre-start restore data, consumed by Init()
	turnCancelled    bool              // true after Ctrl+C — prevents auto-start on stale progress
	idleTickCounter  int               // counts 100ms ticks in idle state; placeholder rotates every 30

	// --- Deterministic rendering: per-turn completion tracking ---
	// turnDoneFlags tracks whether specific events have been processed for a turn.
	// Keyed by agentTurnID. Entries for old turns are cleaned up in startAgentTurn.
	turnDoneFlags map[uint64]*turnDoneFlag

	// --- §21 消息搜索 /search ---
	searchMode    bool            // 是否处于搜索模式
	searchQuery   string          // 搜索关键词
	searchResults []int           // 匹配的消息索引列表
	searchIdx     int             // 当前导航到的搜索结果索引（-1 = 未选择）
	searchEditing bool            // true = 编辑搜索词, false = 导航结果
	searchTI      textinput.Model // 搜索输入框

	// --- Mouse support ---
	mouseZones mouseZoneBuilder // zone tracker for mouse hit testing (rebuilt each View())

	// --- Layout configuration ---
	chatMaxWidth             int             // max content width (0 = unlimited)
	chatCenter               bool            // center content in middle-width screens
	layoutMode               string          // "auto" / "single" / "dual"
	sidebarEnabled           bool            // show sidebar in wide screens
	sidebarWidth             int             // sidebar width in chars
	sidebarPosition          string          // "left" / "right"
	sidebarVisible           bool            // runtime: is sidebar currently shown (user toggled with Ctrl+B)?
	sidebarCollapsedSections map[string]bool // per-section collapse state: "sessions"/"todo"/"tasks" → true=collapsed
	xShift                   int             // sidebar X offset for middleBlock, set during trackMainLayoutZones

	// Cached layout metrics (invalidated on resize / sidebar toggle).
	// Eliminates repeated lipgloss.Render + ansi.StringWidth per chatWidth() call.
	cachedSidebarRenderedWidth int // measured visual width of sidebar (0 = not yet computed)
	cachedSidebarWidthKey      int // sidebarWidth at time of measurement (for invalidation)
	cachedChatWidth            int // effective chat width (0 = not yet computed)
	cachedChatWidthKey         int // m.width at time of measurement (for invalidation)

	// toolDisplayInfo

	// --- 🥚 Easter Eggs 彩蛋 ---
	easterEgg       easterEggMode // 当前激活的彩蛋类型（"" = 无）
	easterEggCustom string        // 彩蛋自定义内容（版本成就 art 等）
	konamiBuffer    []string      // Konami Code 按键缓冲区
	matrixCols      int           // Matrix 代码雨列数
	matrixRows      int           // Matrix 代码雨行数
	matrixDrops     []int         // Matrix 每列头部位置
	matrixSpeeds    []int         // Matrix 每列下落速度
	matrixTrailLen  []int         // Matrix 每列拖尾长度
	matrixBuffer    [][]rune      // Matrix 字符缓冲区
	versionHitTimes []time.Time   // /version 命令调用时间戳（三连检测）

	channel             *CLIChannel     // back-reference to owning channel (set during Start)
	cachedModelName     string          // cached model name for View() performance
	modelNameZoneXStart int             // rendered X start of model name in status bar (-1 = not rendered)
	modelNameZoneXEnd   int             // rendered X end of model name in status bar (exclusive)
	activeSubID         string          // active subscription ID for current session
	todoManager         *cliTodoManager // per-session todo persistence
	askUserSession      string          // chatID of the session that triggered current AskUser panel (empty = no pending AskUser)
	modelCount          int             // cached model list length for View() performance

	// Context usage display (persisted across turns for ready-status bar)
	lastTokenUsage         *protocol.TokenUsage // last known token usage from progress events
	cachedMaxContextTokens int                  // max context tokens (from settings/config, cached for View())
	cachedMaxOutputTokens  int64                // max output tokens (from progress events, cached for View())
	cachedCompressRatio    float64              // compression threshold ratio, cached for View()

	// === Runner Bridge ===
	runnerBridge *RunnerBridge
}

// turnDoneFlag tracks completion events for a single agent turn.
// Used to prevent duplicate message insertion when events arrive out of order
// (e.g. PhaseDone before cliOutboundMsg, or late tool completion after cancel).
type turnDoneFlag struct {
	doneProcessed bool      // true after handleProgressDone has created the tool_summary
	replyReceived bool      // true after handleAgentMessage has appended the assistant reply
	doneTime      time.Time // when doneProcessed was set (for flush timeout fallback)
}

// cliMessage 单条消息
// queuedMsg is a user message waiting in the queue, bound to a specific chatID.
// When flushed, it is only delivered to the session that was active when queued.
type queuedMsg struct {
	content string
	chatID  string
}

type cliMessage struct {
	role      string
	content   string
	timestamp time.Time
	isPartial bool
	// --- turn identification for deterministic rendering ---
	turnID uint64 // agentTurnID when this message was created (0 = not agent-generated)
	// --- thinking/reasoning content (displayed in a collapsible box) ---
	thinking string // raw reasoning text (stored when message is finalized)
	// --- §1 增量渲染 ---
	rendered    string // 缓存的渲染结果（ANSI 字符串）
	dirty       bool   // 是否需要重新渲染
	renderWidth int    // 渲染时的终端宽度（用于 resize 失效检测）

	// --- §2 工具可视化 ---
	tools      []protocol.ToolProgress // 扁平化工具列表（兼容旧逻辑）
	iterations []cliIterationSnapshot  // 按迭代分组的快照（优先使用）

	// --- §19 长消息折叠 ---
	renderedLines         int  // 渲染后的总行数（每次 dirty 重算）
	originalRenderedLines int  // fold 前的原始行数（fold 时保存，用于 unfold 判断）
	folded                bool // 是否折叠

	// --- Markdown rendering for system messages ---
	markdown bool // when true, system messages go through glamour renderer (e.g. /usage tables)
	styled   bool // when true, content is pre-rendered with ANSI codes, output as-is in renderMessage
}

// newCLIModel 创建 CLI model
func newCLIModel() *cliModel {
	ta := textarea.New()
	ta.Placeholder = "" // disabled; placeholder rendered in View() to avoid CJK bug
	ta.Focus()
	ta.SetWidth(72)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	// Enable DynamicHeight so textarea auto-grows/shrinks based on visual lines
	// (including soft wraps from CJK characters). This replaces our manual autoExpandInput.
	ta.DynamicHeight = true
	ta.MinHeight = minTaHeight
	ta.MaxHeight = maxTaHeight
	ta.SetHeight(minTaHeight)
	initStyles := buildStyles(76)
	applyTAStyles(&ta, &initStyles)
	// Disable textarea's virtual (block) cursor. We use BubbleTea's real
	// terminal cursor instead — this avoids a double-cursor and ensures IME
	// candidate windows anchor to the correct position during streaming output.
	ta.SetVirtualCursor(false)

	// Keep textarea's native newline bindings intact.
	// Plain Enter is intercepted by the outer CLI handler and used for send,
	// while modified/newline-intent keys (for example Ctrl+J depending on
	// terminal encoding) are allowed to reach the textarea so its built-in
	// multiline + internal-scroll behavior continues to work at MaxHeight.

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	// 滚动方式：Up/Down（逐行，也响应鼠标滚轮的转义序列）、PgUp/PgDn（翻页）
	// 注意：Up/Down 会同时被 textarea 的光标移动和 viewport 的滚动处理。
	// handleKeyPress 里对 KeyUp/KeyDown 的 input history 逻辑会优先拦截，
	// 但仅在 idle + 输入框为空时才触发，所以滚轮滚动在 typing 时不冲突。
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.Left.SetKeys()
	vp.KeyMap.Right.SetKeys()
	vp.KeyMap.PageUp.SetKeys("pgup")
	vp.KeyMap.PageDown.SetKeys("pgdown")
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()
	vp.SetHorizontalStep(0) // 禁用水平滚动步长

	renderer := newGlamourRenderer(maxBubbleWidth(80) - 2)

	// Ticker
	tk := newAnimTicker(diamondPulseFrames, currentTheme.Warning)

	return &cliModel{
		viewport:        vp,
		textarea:        ta,
		ticker:          tk,
		placeholderText: GetLocale(currentLocaleLang).IdlePlaceholders[0],
		messages:        make([]cliMessage, 0, cliMsgBufSize),
		styles:          buildStyles(80),
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		progress:        nil,
		inputReady:      true,
		locale:          GetLocale(""),
		inputHistory:    make([]string, 0, 100),
		inputHistoryIdx: -1,
		inputDraft:      "",
		senderID:        "cli_user",
		channelName:     "cli",
		// Layout defaults
		chatMaxWidth:             76,
		chatCenter:               true,
		layoutMode:               "auto",
		sidebarEnabled:           true,
		sidebarVisible:           true,
		sidebarWidth:             30,
		sidebarPosition:          "left",
		sidebarCollapsedSections: make(map[string]bool),
		unreadSessions:           make(map[string]bool),
		lastBusyStates:           make(map[string]bool),
		liveSessionStates:        make(map[string]*liveSessionState),
	}
}

// SetSubscriptionMgr sets the subscription manager for quick switch.
func (m *cliModel) SetSubscriptionMgr(mgr SubscriptionManager) {
	m.subscriptionMgr = mgr
}

// SetLLMSubscriber sets the LLM subscriber for quick switch.
func (m *cliModel) SetLLMSubscriber(sub LLMSubscriber) {
	m.llmSubscriber = sub
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (内部消息类型)
// ---------------------------------------------------------------------------

// cliOutboundMsg 从 agent 收到的消息
type cliOutboundMsg struct {
	msg OutboundMsg
}

// cliProgressMsg 实时进度更新消息（来自 WS eventStream 或本地 Transport）。
// 经过 seq 去重、turnID 守卫等实时处理。
type cliProgressMsg struct {
	payload *protocol.ProgressEvent
}

// cliProcessingMsg sets the typing/processing state externally (remote reconnect).
type cliProcessingMsg struct {
	processing bool
}

// cliConnStateMsg updates the WS connection state for the header bar indicator.
type cliConnStateMsg struct {
	state string // "connected" | "disconnected" | "reconnecting"
}

// cliSessionStateMsg carries a server-pushed session state change event
// (busy/idle, subagent started/stopped) into the BubbleTea Update loop.
type cliSessionStateMsg struct {
	event protocol.SessionEvent
}

// liveSessionState holds event-driven session state received from the server
// via SessionEvent push. This provides instant sidebar updates (<100ms)
// instead of waiting for the safety-net poll. Keyed by session ID:
//   - main sessions: chatID
//   - agent sessions: "agent:role/instance"
type liveSessionState struct {
	busy     bool
	role     string // non-empty for subagent sessions
	instance string // non-empty for subagent sessions
	parentID string // parent chatID for subagent sessions
}

// cliHistoryLoadMsg loads history messages into the model from a goroutine-safe context.
// Data is pre-converted, so the Update handler only appends and rebuilds viewport.
type cliHistoryLoadMsg struct {
	channelName string
	chatID      string
	history     []cliMessage
}

// cliTickMsg 全局 ticker 定时刷新消息（100ms 间隔，由全局 goroutine 驱动）。
// 不再使用 BubbleTea cmd chain，消除了 chain 累积导致 tick 加倍的问题。
type cliTickMsg struct{}

// typewriterTickMsg 独立的打字机刷新（50ms 间隔，逐 rune 输出）
type typewriterTickMsg struct{}

// cliTempStatusClearMsg 临时状态提示自动清除
type cliTempStatusClearMsg struct{}

// cliSettingsSavedMsg settings save completed (async callback result)
type cliSettingsSavedMsg struct {
	themeChanged  bool
	theme         string
	langChanged   bool
	lang          string
	layoutChanged bool
	layoutVals    map[string]string // layout-related settings for field update
	feedbackMsg   string
	savedModel    string // model name from saved values (avoids GetDefault RPC timing issues)
}

// cliSwitchLLMDoneMsg is sent when an async subscription switch completes.
// resolveSubMaxContext returns the per-model max_context from a subscription.
// Priority: per-model config for the subscription's model → 0 (let global config decide).
func resolveSubMaxContext(sub *Subscription) int {
	if sub.Model != "" {
		if pmc, ok := sub.PerModelConfigs[sub.Model]; ok && pmc.MaxContext > 0 {
			return pmc.MaxContext
		}
	}
	return 0
}

// resolveSubMaxOutputTokens returns the per-model max_output_tokens from a subscription.
func resolveSubMaxOutputTokens(sub *Subscription) int {
	if sub.Model != "" {
		if pmc, ok := sub.PerModelConfigs[sub.Model]; ok && pmc.MaxOutputTokens > 0 {
			return pmc.MaxOutputTokens
		}
	}
	return sub.MaxOutputTokens
}

type cliSwitchLLMDoneMsg struct {
	err       error
	subID     string
	subName   string
	subModel  string
	maxCtx    int // per-model max_context from subscription (for session persistence)
	maxOutTok int // per-model max_output_tokens from subscription
	mgr       SubscriptionManager
}

// cliInjectedUserMsg 通知 CLI 有 user 消息被注入（如 bg task 完成通知）
type cliInjectedUserMsg struct {
	content string
	chatID  string // session key "channel:chatID", empty for legacy (always apply)
}

// cliUpdateCheckMsg 更新检查结果消息
type cliUpdateCheckMsg struct {
	info *version.UpdateInfo
}

type cliPluginReloadResultMsg struct {
	pluginID string
	err      error
}

type cliPluginReloadAllResultMsg struct {
	err error
}

type cliPluginHealthResultMsg struct {
	results map[string]error
}

type cliPluginInstallResultMsg struct {
	pluginID  string
	pluginDir string
	err       error
}

type cliPluginUninstallResultMsg struct {
	pluginID string
	err      error
}

// cliWidgetUpdateMsg signals that a plugin widget's content has been updated
// and the TUI should re-render to show the new content.
type cliWidgetUpdateMsg struct{}

// cliModelDiscoverMsg triggers a delayed auto-discover retry for the model name.
// Sent when the initial refreshCachedModelName fails to find a model (e.g. LLM
// client not yet ready after setup). The handler retries the auto-discover logic.
type cliModelDiscoverMsg struct {
	attempt int // retry attempt number (0-based)
}

// isCtrlEnter 检测 Ctrl+Enter 按键。
// 终端对 Ctrl+Enter 没有统一标准，常见 raw sequences：
//   - CSI u 协议: \x1b[13;5u   (kitty, Ghostty, Windows Terminal)
//   - 旧格式:     \x1b[27;5;13~ (部分 xterm 变体)
//
// 注意：Bubble Tea 不识别这些序列，会作为 unknownCSISequenceMsg 传递，
// 其 String() 格式为 "?CSI[49 51 59 53 117]?"（%+v 对 []byte 输出字节值数组）。
// 因此需要同时匹配 KeyMsg 和 unknownCSISequenceMsg 的字符串表示。
func isCtrlEnter(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	// CSI u 协议: \x1b[13;5u → "?CSI[49 51 59 53 117]?" 或 KeyRunes "\x1b[13;5u"
	// 旧格式:     \x1b[27;5;13~ → "?CSI[50 55 59 53 59 49 51 126]?" 或 KeyRunes "\x1b[27;5;13~"
	return s == "?CSI[49 51 59 53 117]?" || s == "\x1b[13;5u" ||
		s == "?CSI[50 55 59 53 59 49 51 126]?" || s == "\x1b[27;5;13~"
}

// isCtrlO 检测 Ctrl+O 按键（部分终端发送 CSI u 序列，Bubble Tea 无法识别）。
// Ctrl+O = ASCII 15, CSI u 协议: \x1b[15;5u → "?CSI[49 53 59 53 117]?"
func isCtrlO(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 53 59 53 117]?" || s == "\x1b[15;5u"
}

// isCtrlJ detects Ctrl+J (newline). Ctrl+J = ASCII 10.
// CSI u protocol: \x1b[10;5u → "?CSI[49 48 59 53 117]?"
func isCtrlJ(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 48 59 53 117]?" || s == "\x1b[10;5u" || s == "ctrl+j"
}

// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, and settings saves.
// Prefers per-session override (from disk or in-memory state) over global default.
func (m *cliModel) refreshCachedModelName() {
	if m.channel == nil {
		return
	}
	// Prefer per-session model from disk (persistent across restarts)
	if state := LoadSessionLLMState(m.workDir, m.chatID); state.Model != "" {
		m.cachedModelName = state.Model
		// Also restore activeSubID so the sub panel checkmark is consistent.
		if state.SubscriptionID != "" {
			m.activeSubID = state.SubscriptionID
		}
		return
	}
	// Fallback: in-memory saved state (for sessions that were saved but not yet persisted)
	if saved, ok := m.savedSessions[m.sessionKey()]; ok && saved.activeModel != "" {
		m.cachedModelName = saved.activeModel
		if saved.activeSubscriptionID != "" {
			m.activeSubID = saved.activeSubscriptionID
		}
		return
	}
	// Fallback: only use global default when no per-session override exists
	if m.cachedModelName == "" && m.channel.subscriptionMgr != nil {
		if sub, err := m.channel.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
			m.cachedModelName = sub.Model
		}
	}
	// Auto-discover: if model name is still empty, try listing available models
	// and pick the first one.
	if m.cachedModelName == "" && m.channel.modelLister != nil {
		m.channel.modelLister.EnsureModelsLoaded()
		if models := m.channel.modelLister.ListModels(); len(models) > 0 {
			m.cachedModelName = models[0]
			// Persist the discovered model
			if m.llmSubscriber != nil {
				m.llmSubscriber.SwitchModel(m.senderID, models[0], m.chatID)
			}
			existing := LoadSessionLLMState(m.workDir, m.chatID)
			existing.Model = models[0]
			SaveSessionLLMState(m.workDir, m.chatID, existing)
		}
	}
	// Cache model count for View() (avoids ListAllModels RPC per frame)
	if m.channel.modelLister != nil {
		m.modelCount = len(m.channel.modelLister.ListAllModels())
	}
}

// scheduleModelDiscoverRetry returns a tea.Cmd that sends a delayed
// cliModelDiscoverMsg to retry auto-discovering the model name.
// Used when ListModels returns empty (e.g. LLM client not ready after setup).
func (m *cliModel) scheduleModelDiscoverRetry(attempt int) tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return cliModelDiscoverMsg{attempt: attempt}
	})
}

// handleModelDiscoverMsg processes a delayed model auto-discover retry.
func (m *cliModel) handleModelDiscoverMsg(msg cliModelDiscoverMsg) tea.Cmd {
	if m.cachedModelName != "" {
		return nil // already resolved
	}
	// Retry auto-discover
	if m.channel != nil && m.channel.modelLister != nil {
		if models := m.channel.modelLister.ListModels(); len(models) > 0 {
			m.cachedModelName = models[0]
			if m.llmSubscriber != nil {
				m.llmSubscriber.SwitchModel(m.senderID, models[0], m.chatID)
			}
			existing := LoadSessionLLMState(m.workDir, m.chatID)
			existing.Model = models[0]
			SaveSessionLLMState(m.workDir, m.chatID, existing)
			m.updateViewportContent()
			return nil
		}
	}
	// Max 5 retries (15s total)
	if msg.attempt < 5 {
		return m.scheduleModelDiscoverRetry(msg.attempt + 1)
	}
	return nil
}

// scheduleSessionLLMRestore triggers an async SwitchLLM + SetDefault RPC when
// a per-session subscription was restored from Session JSON during startup.
// This ensures the backend (server or local agent) uses the correct LLM,
// not just the frontend display.
func (m *cliModel) scheduleSessionLLMRestore() {
	if m.activeSubID == "" || m.channel == nil || m.channel.subscriptionMgr == nil {
		return
	}
	if m.channel.config.SwitchLLM == nil {
		return
	}
	subs, err := m.channel.subscriptionMgr.List("")
	if err != nil {
		return
	}
	for i := range subs {
		if subs[i].ID == m.activeSubID {
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
			break
		}
	}
}

// Init 初始化。全局 ticker goroutine 已在 NewCLIChannel 中启动，
// 不需要 Init 启动任何 tick chain。
func (m *cliModel) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}
	// If RestoreSession cached data before program start, emit it as
	// a tea.Cmd so handleSuHistoryLoad runs inside the event loop.
	if m.pendingSuRestore != nil {
		msg := *m.pendingSuRestore
		m.pendingSuRestore = nil
		cmds = append(cmds, func() tea.Msg { return msg })
	}
	if m.debugMode {
		cmds = append(cmds, m.debugCaptureTick())
	}
	return tea.Batch(cmds...)
}

// debugCaptureTick returns a tea.Cmd that fires periodically to capture UI state.
func (m *cliModel) debugCaptureTick() tea.Cmd {
	interval := time.Duration(m.debugCaptureMs) * time.Millisecond
	if interval < 50*time.Millisecond {
		interval = 1 * time.Second
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return debugCaptureMsg{}
	})
}

// suLoadHistoryCmd 异步加载 /su 目标用户的历史消息
func (m *cliModel) suLoadHistoryCmd() tea.Cmd {
	chatID := m.chatID
	channelName := m.channelName
	progressFn := m.channel.config.GetActiveProgressFn
	todosFn := m.channel.config.GetTodosFn

	// Agent sessions: load from in-memory interactiveSubAgents (not DB).
	if channelName == "agent" {
		dumpFn := m.channel.config.AgentSessionDumpFn
		if dumpFn != nil {
			return func() tea.Msg {
				history, err := dumpFn(chatID)
				var activeProgress *protocol.ProgressEvent
				if progressFn != nil {
					activeProgress = progressFn(channelName, chatID)
				}
				var todos []protocol.TodoItem
				if todosFn != nil {
					todos = todosFn(channelName, chatID)
				}
				return suHistoryLoadMsg{history: history, err: err, channelName: channelName, chatID: chatID, activeProgress: activeProgress, todos: todos}
			}
		}
	}

	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return func() tea.Msg {
			return suHistoryLoadMsg{err: fmt.Errorf("no dynamic history loader"), channelName: channelName, chatID: chatID}
		}
	}
	tokenFn := m.channel.config.GetTokenStateFn
	return func() tea.Msg {
		history, err := loader(channelName, chatID)
		// Also fetch active progress for seamless session switch recovery.
		var activeProgress *protocol.ProgressEvent
		if progressFn != nil {
			activeProgress = progressFn(channelName, chatID)
		}
		// Fetch server-side TODO list to overwrite local cache on first switch.
		var todos []protocol.TodoItem
		if todosFn != nil {
			todos = todosFn(channelName, chatID)
		}
		// Fetch last token state so the context bar shows immediately
		// even when the session is idle (no active turn).
		var tokenPrompt, tokenCompletion int64
		if tokenFn != nil {
			tokenPrompt, tokenCompletion = tokenFn(channelName, chatID)
		}
		return suHistoryLoadMsg{
			history: history, err: err,
			channelName:     channelName,
			chatID:          chatID,
			activeProgress:  activeProgress,
			tokenPrompt:     tokenPrompt,
			tokenCompletion: tokenCompletion,
			todos:           todos,
		}
	}
}

// reloadMessagesFromSession triggers async history reload after context compression.
// The engine has replaced its internal message list and persisted to session DB;
// CLI must rebuild m.messages to stay in sync.
func (m *cliModel) reloadMessagesFromSession() {
	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return
	}
	chatID := m.chatID
	channelName := m.channelName
	clipanic.Go("channel.cliModel.reloadMessagesFromSession", func() {
		history, err := loader(channelName, chatID)
		// Send result via async channel (goroutine-safe)
		if m.channel != nil {
			select {
			case m.channel.asyncCh <- cliHistoryReloadMsg{
				channelName: channelName,
				chatID:      chatID,
				history:     history,
				err:         err,
			}:
			default:
				// channel full, drop — next progress event will retry
			}
		}
	})
}

// toggleSidebarSection toggles the collapse state of a sidebar section and persists to preferences.
func (m *cliModel) toggleSidebarSection(section string) {
	if m.sidebarCollapsedSections == nil {
		m.sidebarCollapsedSections = make(map[string]bool)
	}
	if m.sidebarCollapsedSections[section] {
		delete(m.sidebarCollapsedSections, section)
	} else {
		m.sidebarCollapsedSections[section] = true
	}
	m.saveSidebarCollapsedPrefs()
}

// saveSidebarCollapsedPrefs persists the current sidebar section collapse state to preferences.json.
func (m *cliModel) saveSidebarCollapsedPrefs() {
	if m.workDir == "" {
		return
	}
	prefs := tools.LoadPreferences(m.workDir, m.senderID)
	prefs.SidebarCollapsed = m.sidebarCollapsedSections
	_ = tools.SavePreferences(m.workDir, m.senderID, prefs)
}
