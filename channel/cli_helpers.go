package channel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"xbot/storage/sqlite"
)

// ParseSettingBool parses a boolean setting value.
// Accepts "true", "1", "yes" (case-insensitive) as true; everything else as false.
// Shared between serverapp and cmd/xbot-cli for consistent behavior.
func ParseSettingBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes")
}

// ParseSettingInt parses an integer setting value, returning fallback on failure.
// Shared between serverapp and cmd/xbot-cli for consistent behavior.
func ParseSettingInt(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return n
}

// isMaskedAPIKey detects API keys that were masked by the server for safe transport.
// Server masks keys as "<prefix>****" (e.g. "sk-a****"). Writing masked keys
// back to storage would destroy the real key — this function prevents that.
func isMaskedAPIKey(key string) bool {
	return strings.HasSuffix(key, "****") && len(key) <= 20
}

// Private scope-check wrappers — delegate to the unified registry in setting_keys.go.
func isSubscriptionScopedSettingKey(key string) bool { return IsSubscriptionScopedSettingKey(key) }
func cliSettingScope(key string) string              { return SettingScopeOf(key) }

// mergeCLISettingsValues delegates to cli_settings.go:readSettings.
func (m *cliModel) mergeCLISettingsValues() map[string]string { return m.readSettings() }

// persistCLISettingsValues delegates to cli_settings.go:saveSettings.
func (m *cliModel) persistCLISettingsValues(values map[string]string) { m.saveSettings(values) }

// ---------------------------------------------------------------------------
// Refactored common patterns (方案 B: 提取重复代码)
// ---------------------------------------------------------------------------

// invalidateAllCache marks the render cache invalid, dirties all messages,
// and optionally updates the viewport content.
// This pattern appears in theme change, locale change, resize, and tool-summary toggle.
func (m *cliModel) invalidateAllCache(updateViewport bool) {
	m.renderCacheValid = false
	m.lastViewportContent = "" // Force viewport refresh on next updateViewportContent
	m.lastViewportWidth = 0
	m.cachedWrappedHistory = ""
	m.cachedWrappedHistoryRaw = ""
	m.cachedWrappedHistoryWidth = 0
	for i := range m.messages {
		m.messages[i].dirty = true
	}
	if updateViewport {
		m.updateViewportContent()
	}
}

// toggleToolSummary toggles the tool-summary expanded state,
// invalidates all cached rendering, clears cachedHistory, and refreshes the viewport.
// It preserves the viewport scroll position anchored to the first visible message,
// so Ctrl+O doesn't cause a jarring jump when tool summary lines change.
func (m *cliModel) toggleToolSummary() {
	// Find the first visible message index before toggling.
	prevYOffset := m.viewport.YOffset()
	prevAtBottom := m.viewport.AtBottom()
	anchorMsgIdx := -1
	if !prevAtBottom && len(m.msgLineOffsets) > 0 {
		for i := len(m.msgLineOffsets) - 1; i >= 0; i-- {
			if m.msgLineOffsets[i] <= prevYOffset {
				anchorMsgIdx = i
				break
			}
		}
	}

	m.toolSummaryExpanded = !m.toolSummaryExpanded
	m.cachedHistory = ""
	m.invalidateAllCache(true)

	// Restore scroll position anchored to the same message.
	if !prevAtBottom && anchorMsgIdx >= 0 && anchorMsgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[anchorMsgIdx])
	}
}

// openSettingsFromQuickSwitch restores the settings panel after a subscription quick switch.
// The subscription generation guard (in onSubmit) prevents stale LLM fields from being
// written back. Here we only need to refresh LLM display values from the new active
// subscription and preserve global settings from the backup.
func (m *cliModel) openSettingsFromQuickSwitch() {
	if m.channel == nil || len(m.panelValuesBackup) == 0 {
		return
	}
	schema := m.channel.SettingsSchema()
	if len(schema) == 0 {
		return
	}
	// Refresh model list options in the schema (subscription change may affect available models)
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
	// Re-read ALL values fresh (including LLM fields from new active subscription)
	values := m.mergeCLISettingsValues()
	// Overlay non-subscription values from backup (preserves user's in-memory edits).
	// Subscription quick switch should only refresh the active subscription-backed keys.
	for k, v := range m.panelValuesBackup {
		if isSubscriptionScopedSettingKey(k) {
			continue
		}
		values[k] = v
	}
	cursor := m.panelCursorBackup
	onSubmit := m.panelOnSubmitBackup
	// Clear backup
	m.panelValuesBackup = nil
	m.panelOnSubmitBackup = nil
	// Open panel with restored state
	m.openSettingsPanel(schema, values, onSubmit)
	m.panelCursor = cursor
}

// startAgentTurn transitions the model into the "agent processing" state:
// sets typing=true, updates placeholder, disables input, resets progress,
// and queues a tick command to ensure the spinner/progress chain starts.
// This is the SINGLE source of truth for tick chain initiation — no other
// code path should emit tickCmd() on idle→typing transition.
func (m *cliModel) startAgentTurn() {
	m.agentTurnID++
	m.typing = true
	// Do NOT clear turnCancelled here — it must persist across turn boundaries
	// to block stale PhaseDone/tool_summary from a cancelled turn. It is cleared
	// when the new turn's first non-PhaseDone progress arrives (handleProgressMsg)
	// or by endAgentTurn for the matching turnID (normal cancel completion path).

	// Initialize turnDoneFlags for the new turn.
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	m.turnDoneFlags[m.agentTurnID] = &turnDoneFlag{}

	// Clean up old turn entries (keep last 3 for late-arrival safety).
	for id := range m.turnDoneFlags {
		if id+3 < m.agentTurnID {
			delete(m.turnDoneFlags, id)
		}
	}

	// Remote mode: optimistically show initial progress so the user sees
	// immediate feedback (progress bubble) without waiting for the server's
	// first progress_structured event (which has network round-trip latency).
	if m.remoteMode && m.progress == nil {
		m.progress = &protocol.ProgressEvent{
			Phase:     "thinking",
			Iteration: 0,
		}
		m.renderCacheValid = false
	}
	// NOTE: Callers are responsible for ensuring the tick chain starts:
	//   - Inside Bubble Tea Update: return tickCmd() in the cmd chain
	//   - Outside Update (callbacks): append to m.pendingCmds before calling
	// Sync checkpoint state turn index
	if m.checkpointState != nil {
		m.checkpointState.SetTurnIdx(int(m.agentTurnID))
	}
	// Clear rewind result when new turn starts
	m.rewindResult = nil
	m.updatePlaceholder()
	m.inputReady = false
	m.resetProgressState()
}

// removeLastToolSummary removes only the LAST tool_summary message from m.messages.
//
// When the agent turn is active, ConvertMessagesToHistory produces a tool_summary
// from intermediate assistant messages of the in-progress turn. The progress
// block (m.progress + m.iterationHistory) owns iteration display for the active
// turn — the static tool_summary from ConvertMessagesToHistory would duplicate
// content with mismatched (globally-cumulative vs per-turn) iteration numbers.
//
// Only the LAST tool_summary is removed. Previous turns' tool_summaries are
// preserved — those have no live progress panel to replace them.
// Earlier tool_summaries in the active turn are also preserved as fallback:
// if IterationHistory is empty (e.g. reconnect before RPC snapshot arrives),
// the tool_summary rendering is better than showing nothing at all.
func (m *cliModel) removeLastToolSummary() {
	// Find the last tool_summary message (closest to end of messages).
	lastIdx := -1
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].role == "tool_summary" {
			lastIdx = i
			break
		}
	}
	if lastIdx >= 0 {
		m.messages = append(m.messages[:lastIdx], m.messages[lastIdx+1:]...)
		m.renderCacheValid = false
	}
}

// endAgentTurn resets all agent-turn tracking state and returns to idle.
// Takes the turnID that triggered this end. If a new turn has already
// started (turnID != m.agentTurnID), the call is a no-op — this prevents
// stale completion signals (cliOutboundMsg / PhaseDone) from killing a
// new turn's animation.
func (m *cliModel) endAgentTurn(turnID uint64) {
	if turnID != m.agentTurnID {
		return // new turn already started — stale signal, ignore
	}
	// Persist token usage for ready-status bar before clearing progress
	if m.progress != nil {
		m.cacheTokenUsage(m.progress.TokenUsage)
	}
	m.lastCompletedTools = nil
	m.iterationHistory = nil
	m.invalidateProgressHistoryCache()
	m.lastSeenIteration = 0
	m.lastReasoning = ""
	m.reasoningByIter = nil
	m.lastThinking = ""
	m.typingStartTime = time.Time{}
	m.progress = nil
	m.twVisible = 0
	m.rwVisible = 0
	m.typing = false
	m.typewriterTickActive = false
	// Do NOT set turnCancelled here — this is normal turn completion,
	// not a user cancel. Setting turnCancelled=true here prevents
	// the next turn (from message queue flush) from receiving progress
	// events, causing Issue #30: queue-flushed messages appear idle.
	m.turnCancelled = false
	// Collapse todos on turn end. If all done, fully clear.
	// Otherwise restore unfinished todos from TodoManager so they
	// persist across turns and are visible in idle state.
	if m.todoManager != nil {
		key := m.sessionKey()
		if items := m.todoManager.GetTodos(key); len(items) > 0 {
			allDone := true
			for _, t := range items {
				if !t.Done {
					allDone = false
					break
				}
			}
			if !allDone {
				m.todos = make([]protocol.TodoItem, len(items))
				for i, t := range items {
					m.todos[i] = protocol.TodoItem{ID: t.ID, Text: t.Text, Done: t.Done}
				}
				m.todosDoneCleared = false
			} else {
				// All todos done — clear display, underlying TodoManager,
				// AND disk file so they don't resurrect on next TUI restart.
				m.todos = nil
				m.todosDoneCleared = true
				m.todoManager.SetTodos(key, nil)
				_ = m.todoManager.SaveToFile(key)
			}
		} else {
			m.todos = nil
			m.todosDoneCleared = false
		}
	} else {
		m.todos = nil
		m.todosDoneCleared = false
	}
	m.relayoutViewport()
	// Refresh agent count so the tick chain continues if agents exist
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	m.updatePlaceholder()
}

// --- Deterministic rendering helpers ---

// getTurnFlag returns the turnDoneFlag for the given turn, or nil if not tracked.
func (m *cliModel) getTurnFlag(turnID uint64) *turnDoneFlag {
	if m.turnDoneFlags == nil {
		return nil
	}
	return m.turnDoneFlags[turnID]
}

// isTurnDoneProcessed returns true if handleProgressDone has already processed
// the given turn (created tool_summary and ended the turn).
func (m *cliModel) isTurnDoneProcessed(turnID uint64) bool {
	f := m.getTurnFlag(turnID)
	return f != nil && f.doneProcessed
}

// isTurnReplyReceived returns true if handleAgentMessage has already received
// the assistant reply for the given turn.
func (m *cliModel) isTurnReplyReceived(turnID uint64) bool {
	f := m.getTurnFlag(turnID)
	return f != nil && f.replyReceived
}

// setTurnDoneProcessed marks the turn as having been processed by handleProgressDone.
func (m *cliModel) setTurnDoneProcessed(turnID uint64) {
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	f, ok := m.turnDoneFlags[turnID]
	if !ok {
		f = &turnDoneFlag{}
		m.turnDoneFlags[turnID] = f
	}
	f.doneProcessed = true
	f.doneTime = time.Now()
}

// setTurnReplyReceived marks the turn as having received the assistant reply.
func (m *cliModel) setTurnReplyReceived(turnID uint64) {
	if m.turnDoneFlags == nil {
		m.turnDoneFlags = make(map[uint64]*turnDoneFlag)
	}
	f, ok := m.turnDoneFlags[turnID]
	if !ok {
		f = &turnDoneFlag{}
		m.turnDoneFlags[turnID] = f
	}
	f.replyReceived = true
}

// findMessageByTurn finds the index of the last message with the given turnID and role.
// Returns -1 if not found.
func (m *cliModel) findMessageByTurn(turnID uint64, role string) int {
	// Search from end — the most recent message is the most likely match.
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].turnID == turnID && m.messages[i].role == role {
			return i
		}
	}
	return -1
}

// upsertMessageByTurn finds an existing message with the given turnID+role and
// updates it in-place. If not found, appends the message. Returns the final index.
// This is the core mechanism for deterministic rendering: duplicate events update
// existing slots instead of creating new messages.
func (m *cliModel) upsertMessageByTurn(turnID uint64, role string, msg cliMessage) int {
	idx := m.findMessageByTurn(turnID, role)
	if idx >= 0 {
		// Update in-place: preserve position in the message list.
		m.messages[idx] = msg
		m.messages[idx].turnID = turnID
		return idx
	}
	// Not found: append at end.
	msg.turnID = turnID
	m.messages = append(m.messages, msg)
	return len(m.messages) - 1
}

// removeMessageByTurn removes the last message with the given turnID+role.
// Returns true if a message was removed.
// flushMessageQueue sends the first queued message (if any) when input becomes ready.
// Returns a tea.Cmd to send the message, or nil if queue is empty.
func (m *cliModel) flushMessageQueue() {
	if len(m.messageQueue) == 0 {
		return
	}
	// Only flush messages queued for the current session.
	// If user queued a message in main session and switched to a SubAgent session,
	// skip until we're back in the correct session.
	msg := m.messageQueue[0]
	if msg.chatID != m.chatID {
		return // wrong session, wait for the correct one
	}
	m.messageQueue = m.messageQueue[1:]
	m.queueEditing = false
	m.queueEditBuf = ""
	// Put message into textarea and trigger send
	m.textarea.SetValue(msg.content)
	m.sendMessageFromQueue()
}

// sendMessageFromQueue sends the current textarea content as a queued message.
// Does NOT return tickCmd() — startAgentTurn() inside sendMessage() handles that.
func (m *cliModel) sendMessageFromQueue() {
	content := strings.TrimSpace(m.textarea.Value())
	if content == "" {
		return
	}
	m.textarea.Reset()
	m.autoExpandInput()
	m.sendMessage(content)
}

// applyThemeAndRebuild applies a theme change synchronously: sets the theme,
// rebuilds styles cache, glamour renderer, and marks all messages dirty.
// Uses setTheme() instead of ApplyTheme() to avoid sending on themeChangeCh,
// which would cause a redundant fullRebuild in the next Update cycle.
func (m *cliModel) applyThemeAndRebuild(theme string) {
	setTheme(theme)
	// Rebuild styles cache (same as themeChangeCh handler in Update)
	m.styles = buildStyles(m.width)
	applyTAStyles(&m.textarea, &m.styles)
	m.ticker.style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
	// Rebuild glamour renderer
	cw := m.chatWidth()
	if cw > 4 {
		m.renderer = newGlamourRenderer(cw - 4)
	}
	// Mark all messages for re-render (new theme = new styles)
	m.renderCacheValid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}
}

// ensurePanelCursorVisible ensures the panel cursor line is within the visible area.
// For settings panel: uses precise line calculation with inline overlay awareness.
func (m *cliModel) ensurePanelCursorVisible() {
	if m.panelMode == "settings" {
		extra := 0
		if m.panelEdit {
			extra = 3
		} else if m.panelCombo && m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			extra = min(len(def.Options), 8) + 1
		}
		m.ensureSettingsCursorVisible(extra)
		return
	}
}

// ensureBgCursorVisible adjusts panelScrollY so the bg task/agent cursor is within the visible area.
// Accounts for preview lines (an agent with a preview takes 2 rendered lines).
func (m *cliModel) ensureBgCursorVisible() {
	visibleH := m.panelVisibleHeight()
	// Calculate the cursor item's approximate line number.
	// Tasks take 1 line each; agents take 1 line + 1 extra if they have a preview.
	cursorLine := 0
	// Header line
	cursorLine = 1
	idx := 0
	for _, task := range m.panelBgTasks {
		_ = task // tasks are always 1 line
		if idx == m.panelBgCursor {
			break
		}
		cursorLine++
		idx++
	}
	for _, ag := range m.panelBgAgents {
		if idx == m.panelBgCursor {
			break
		}
		cursorLine++ // agent label line
		if ag.Preview != "" {
			cursorLine++ // preview line
		}
		idx++
	}

	totalLines := cursorLine + 2 // +2 for header and bottom padding
	if totalLines <= visibleH {
		m.panelScrollY = 0
		return
	}
	if cursorLine >= m.panelScrollY+visibleH {
		m.panelScrollY = cursorLine - visibleH + 1
	}
	if cursorLine < m.panelScrollY {
		m.panelScrollY = cursorLine
	}
}

// ensureSessionCursorVisible adjusts panelScrollY so the session cursor is within the visible area.
// Each session entry takes exactly 1 rendered line.
func (m *cliModel) ensureSessionCursorVisible() {
	visibleH := m.panelVisibleHeight()
	// +1 for header line
	cursorLine := m.panelSessionCursor + 1
	totalLines := len(m.panelSessionItems) + 1
	if totalLines <= visibleH {
		m.panelScrollY = 0
		return
	}
	if cursorLine >= m.panelScrollY+visibleH {
		m.panelScrollY = cursorLine - visibleH + 1
	}
	if cursorLine < m.panelScrollY {
		m.panelScrollY = cursorLine
	}
}

// panelVisibleHeight 返回 panel 可见区域高度。
func (m *cliModel) panelVisibleHeight() int {
	h := m.height - 5 // titleBar(1) + footer(1) + toast(1) + PanelBox borders(2)
	if h < 3 {
		h = 3
	}
	return h
}

// clampPanelScroll 确保 panelScrollY 不超出范围。
// rawContent 是已渲染的 panel 内容，避免重复调用 viewPanel()。
func (m *cliModel) clampPanelScroll(rawContent string) {
	total := strings.Count(rawContent, "\n") + 1
	visible := m.panelVisibleHeight()
	if total <= visible {
		m.panelScrollY = 0
		return
	}
	if m.panelScrollY < 0 {
		m.panelScrollY = 0
	}
	if m.panelScrollY > total-visible {
		m.panelScrollY = total - visible
	}
}

// settingsCursorLine computes the 0-based line number where the current
// settings panel cursor item starts rendering. This mirrors the layout in
// viewSettingsPanel: 2 header lines, then per-category (2 lines header) and
// per-item (1 line). Inline overlays (edit/combo) after items add extra lines.
func (m *cliModel) settingsCursorLine() int {
	const headerLines = 2 // title + divider
	line := headerLines
	lastCat := ""
	for i, def := range m.panelSchema {
		if def.Category != lastCat {
			lastCat = def.Category
			line += 2 // blank line + category header
		}
		if i == m.panelCursor {
			return line
		}
		line++
	}
	return line
}

// ensureSettingsCursorVisible adjusts panelScrollY so that the cursor item
// and its inline edit/combo overlay are visible. Call after opening edit/combo
// or changing cursor. extraLines is the number of additional lines below the
// cursor item (e.g. edit overlay = 3, combo = min(options, 8) + 1).
func (m *cliModel) ensureSettingsCursorVisible(extraLines int) {
	cursorLine := m.settingsCursorLine()
	visibleH := m.panelVisibleHeight()
	if visibleH <= 0 {
		return
	}
	// Ensure cursor + overlay fit within the visible area
	neededBottom := cursorLine + 1 + extraLines // item line + overlay
	neededTop := cursorLine

	// If overlay extends below visible area, scroll down
	if neededBottom > m.panelScrollY+visibleH {
		m.panelScrollY = neededBottom - visibleH
	}
	// If cursor is above visible area, scroll up
	if neededTop < m.panelScrollY {
		m.panelScrollY = neededTop
	}
	if m.panelScrollY < 0 {
		m.panelScrollY = 0
	}
}

// clampAskUserPanelScroll adjusts askPanelScrollY for the askuser split layout.
// The visible height depends on viewport height + fixed chrome, not panelVisibleHeight().
// Default scroll is 0 (show question at top), not bottom (hints).
// Caches total line count in askPanelTotalLines for use by ensureAskUserVisible.
func (m *cliModel) clampAskUserPanelScroll(rawContent string) {
	total := strings.Count(rawContent, "\n") + 1
	m.askPanelTotalLines = total
	fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	visible := m.height - fixedLines - viewportH - panelBorder
	if visible < 3 {
		visible = 3
	}
	if total <= visible {
		m.askPanelScrollY = 0
		return
	}
	if m.askPanelScrollY < 0 {
		m.askPanelScrollY = 0
	}
	if m.askPanelScrollY > total-visible {
		m.askPanelScrollY = total - visible
	}
}

// askUserPanelVisibleHeight returns how many lines the askuser panel can display.
func (m *cliModel) askUserPanelVisibleHeight() int {
	fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	visible := m.height - fixedLines - viewportH - panelBorder
	if visible < 3 {
		return 3
	}
	return visible
}

// applyLanguageChange applies a language/locale change and invalidates cache.
// Uses setLocale() instead of SetLocale() to avoid sending on localeChangeCh,
// which would cause a redundant fullRebuild in the next Update cycle.
func (m *cliModel) applyLanguageChange(lang string) {
	setLocale(lang)
	m.locale = GetLocale(lang)
	m.renderCacheValid = false
}

// applyLayoutConfig updates layout-related model fields from settings values
// and invalidates the render cache so the viewport relayouts.
func (m *cliModel) applyLayoutConfig(vals map[string]string) {
	if v, ok := vals["layout_mode"]; ok && v != "" {
		m.layoutMode = v
	}
	if v, ok := vals["sidebar_enabled"]; ok {
		m.sidebarEnabled = ParseSettingBool(v)
	}
	if v, ok := vals["sidebar_width"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			m.sidebarWidth = n
		}
	}
	if v, ok := vals["sidebar_position"]; ok && v != "" {
		m.sidebarPosition = v
	}
	if v, ok := vals["chat_max_width"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			m.chatMaxWidth = n
		}
	}
	if v, ok := vals["chat_center"]; ok {
		m.chatCenter = ParseSettingBool(v)
	}
	m.renderCacheValid = false
}

// doSaveSettings runs the settings save callback synchronously and returns
// a tea.Cmd that sends the result back as cliSettingsSavedMsg.
// The callback is pure local I/O (config.json write, SQLite write, LLM rebuild)
// with no network calls, so it completes in milliseconds.
func (m *cliModel) doSaveSettings(onSubmit func(map[string]string), vals map[string]string) tea.Cmd {
	// Capture the values we need for the UI update
	theme, hasTheme := vals["theme"]
	lang, hasLang := vals["language"]
	// Capture feedback string now (m.locale is only safe to read in Update)
	feedbackMsg := m.locale.SettingsSaved

	// Detect layout changes and collect layout values
	layoutKeys := map[string]bool{
		"layout_mode": true, "sidebar_enabled": true, "sidebar_width": true,
		"sidebar_position": true, "sidebar_sections": true, "chat_max_width": true, "chat_center": true,
	}
	layoutChanged := false
	layoutVals := make(map[string]string)
	for k, v := range vals {
		if layoutKeys[k] {
			layoutChanged = true
			layoutVals[k] = v
		}
	}

	// Run synchronously — all operations are local I/O, no network calls
	onSubmit(vals)

	return func() tea.Msg {
		return cliSettingsSavedMsg{
			themeChanged:  hasTheme && theme != "",
			theme:         theme,
			langChanged:   hasLang,
			lang:          lang,
			layoutChanged: layoutChanged,
			layoutVals:    layoutVals,
			feedbackMsg:   feedbackMsg,
		}
	}
}

// handleSettingsSavedMsg processes the async settings save result.
// Called from Update() to apply theme/locale/layout changes and refresh the viewport.
func (m *cliModel) handleSettingsSavedMsg(msg cliSettingsSavedMsg) tea.Cmd {
	visualChanged := false
	if msg.themeChanged {
		m.applyThemeAndRebuild(msg.theme)
		visualChanged = true
	}
	if msg.langChanged {
		m.applyLanguageChange(msg.lang)
		visualChanged = true
	}
	if msg.layoutChanged {
		m.applyLayoutConfig(msg.layoutVals)
		visualChanged = true
	}
	m.refreshCachedModelName()
	// Invalidate cached context settings so they're re-resolved from user settings.
	// Without this, changing max_context_tokens/max_output_tokens/compression_threshold
	// in the settings panel has no effect on the context progress bar.
	// Skip this when msg.syncOnly is true — periodic layout syncs from
	// SyncLayoutSettings (every 5s in remote mode) must NOT reset context
	// caches, otherwise the context bar flashes to solid line repeatedly.
	if !msg.syncOnly {
		m.cachedMaxContextTokens = 0
		m.cachedMaxOutputTokens = 0
		m.cachedCompressRatio = 0
	}
	if msg.feedbackMsg != "" {
		m.appendSystem(msg.feedbackMsg)
	}
	if visualChanged {
		m.invalidateAllCache(true)
	} else {
		m.updateViewportContent()
	}
	return nil
}

// submitAskAnswers collects answers from the AskUser panel, invokes the answer
// callback, closes the panel, and returns the appropriate tea.Cmd.
// This pattern appears 3 times in updateAskUserPanel (ctrl+s, Enter with options, Enter without options).
func (m *cliModel) submitAskAnswers() (bool, tea.Model, tea.Cmd) {
	m.saveCurrentFreeInput()
	answers := m.collectAskAnswers()
	if m.panelOnAnswer != nil {
		m.panelOnAnswer(answers)
	}
	m.closePanel()
	// NOTE: tickCmd() is NOT returned here. If agent is typing, the tick chain
	// is already running from startAgentTurn(). Returning tickCmd() while busy
	// creates a duplicate chain → 2x spinner speed.
	return true, m, nil
}

// closePanelAndResume closes the active panel and returns the appropriate
// tea.Cmd based on whether the agent is still typing.
// This pattern appears in bgtasks panel (Esc/Ctrl+C), settings panel (Esc),
// and askuser panel (Esc/cancel) handlers.
func (m *cliModel) closePanelAndResume() (bool, tea.Model, tea.Cmd) {
	m.closePanel()
	// NOTE: do NOT return tickCmd() here — same reason as submitAskAnswers.
	// The tick chain is already running if agent is typing.
	return true, m, nil
}

// iterToolsFlat returns a flat slice of all tools from either msg.iterations
// or msg.tools, handling the dual-source pattern used in tool_summary rendering.
// If iterations are present, it also counts them. Returns (tools, iterationCount).
func (msg *cliMessage) iterToolsFlat() (tools []protocol.ToolProgress, iterCount int) {
	if len(msg.iterations) > 0 {
		iterCount = len(msg.iterations)
		for _, it := range msg.iterations {
			tools = append(tools, it.Tools...)
		}
	} else {
		tools = msg.tools
		iterCount = 0
	}
	return
}

// ---------------------------------------------------------------------------
// Unified error & feedback helpers (方案 A: 统一错误处理)
// ---------------------------------------------------------------------------

// feedbackLevel represents the severity level of user feedback.
type feedbackLevel int

const (
	// feedbackInfo indicates an informational message (e.g. "settings saved").
	feedbackInfo feedbackLevel = iota
	// feedbackWarning indicates a non-critical issue (e.g. "waiting for operation").
	feedbackWarning
	// feedbackError indicates an error that needs user attention (e.g. "kill failed").
	feedbackError
)

// showTempStatus displays a temporary status message in the status bar that
// automatically clears after the given duration. This replaces the repetitive
// pattern of setting m.tempStatus + returning a tea.Tick with cliTempStatusClearMsg.
//
// Usage (in Update method):
//
// showTempStatus sets a temporary status message that auto-clears after 5s.
// Queues the clear command into pendingCmds (auto-drained by Update).
func (m *cliModel) showTempStatus(text string) {
	m.tempStatus = text
	m.pendingCmds = append(m.pendingCmds, m.clearTempStatusCmd(5*time.Second))
}

// clearTempStatusCmd returns a tea.Cmd that clears the temp status after the
// given duration. The default is 2 seconds.
func (m *cliModel) clearTempStatusCmd(d ...time.Duration) tea.Cmd {
	dur := 2 * time.Second
	if len(d) > 0 {
		dur = d[0]
	}
	return tea.Tick(dur, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
}

// showSystemMsg appends a system message to the chat history and refreshes
// the viewport. This is the unified replacement for the scattered pattern of
// appendSystem() + updateViewportContent() calls throughout the codebase.
//
// The level parameter controls the visual presentation:
//   - feedbackInfo: rendered as a normal system message
//   - feedbackWarning / feedbackError: rendered with warning/error styling
//     if the content matches errorKeywords
func (m *cliModel) showSystemMsg(content string, level feedbackLevel) {
	m.appendSystem(content)
	m.updateViewportContent()
}

// isErrorContent checks whether the given content string contains any of the
// predefined error keywords. This extracts the inline error-detection loop
// that was previously duplicated in message rendering.
func isErrorContent(content string) bool {
	lower := toLowerASCII(content)
	for _, kw := range errorKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// toLowerASCII returns a lowercase version of s using only ASCII rules.
// This avoids the overhead of strings.ToLower for keyword matching.
func toLowerASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte(byte(r + 32))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// enqueueToast adds a toast notification to the display queue.
// Toasts appear at the bottom of the screen and auto-dismiss after 3 seconds.
// The icon parameter controls visual style: "✓" (success), "✗"/"⚠" (error), "ℹ" (info).
func (m *cliModel) enqueueToast(text, icon string) tea.Cmd {
	return func() tea.Msg {
		return cliToastMsg{text: text, icon: icon}
	}
}

// handleUsageCommand renders token usage statistics for the current user.
func (m *cliModel) handleUsageCommand() {
	if m.usageQueryFn == nil {
		m.showSystemMsg("Usage tracking not available", feedbackWarning)
		return
	}

	cumulative, daily, err := m.usageQueryFn(m.senderID, 30)
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("Failed to query usage: %v", err), feedbackError)
		return
	}

	var sb strings.Builder
	sb.WriteString("# Token Usage\n\n")

	// --- Cumulative totals ---
	if cumulative != nil && cumulative.TotalTokens > 0 {
		// Calculate usage duration from daily data
		usageDays := 0
		if len(daily) > 0 {
			// daily is sorted by date DESC; last entry = earliest date
			earliest := daily[len(daily)-1].Date
			if first, err := time.Parse("2006-01-02", earliest); err == nil {
				usageDays = int(time.Since(first).Hours()/24) + 1
			}
		}

		sb.WriteString("## Summary\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		fmt.Fprintf(&sb, "| **Total tokens** | **%s** |\n", fmtTokens(cumulative.TotalTokens))
		fmt.Fprintf(&sb, "| Input | %s |\n", fmtTokens(cumulative.InputTokens))
		fmt.Fprintf(&sb, "| Output | %s |\n", fmtTokens(cumulative.OutputTokens))
		fmt.Fprintf(&sb, "| Cached | %s |\n", fmtTokens(cumulative.CachedTokens))
		fmt.Fprintf(&sb, "| Conversations | %d |\n", cumulative.ConversationCount)
		fmt.Fprintf(&sb, "| LLM calls | %d |\n", cumulative.LLMCallCount)
		if usageDays > 0 {
			fmt.Fprintf(&sb, "| **Usage duration** | **%d days** |\n", usageDays)
			avgDaily := cumulative.TotalTokens / int64(usageDays)
			fmt.Fprintf(&sb, "| Avg daily tokens | %s |\n", fmtTokens(avgDaily))
		}

		// Analysis section
		sb.WriteString("\n### Analysis\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		if cumulative.InputTokens > 0 {
			cacheRate := float64(cumulative.CachedTokens) / float64(cumulative.InputTokens) * 100
			fmt.Fprintf(&sb, "| **Cache hit rate** | **%.1f%%** |\n", cacheRate)
			nonCachedInput := cumulative.InputTokens - cumulative.CachedTokens
			fmt.Fprintf(&sb, "| Actual input (non-cached) | %s |\n", fmtTokens(nonCachedInput))
		}
		if cumulative.LLMCallCount > 0 {
			avgIn := cumulative.InputTokens / cumulative.LLMCallCount
			avgOut := cumulative.OutputTokens / cumulative.LLMCallCount
			fmt.Fprintf(&sb, "| Avg input/call | %s |\n", fmtTokens(avgIn))
			fmt.Fprintf(&sb, "| Avg output/call | %s |\n", fmtTokens(avgOut))
		}
		if cumulative.ConversationCount > 0 {
			avgCalls := float64(cumulative.LLMCallCount) / float64(cumulative.ConversationCount)
			fmt.Fprintf(&sb, "| Avg calls/conversation | %.1f |\n", avgCalls)
		}
	} else {
		sb.WriteString("No usage data recorded yet.\n")
	}

	// --- Today's usage by model ---
	today := time.Now().Format("2006-01-02")
	var todayEntries []sqlite.DailyTokenUsage
	var todayTotal sqlite.DailyTokenUsage
	for _, d := range daily {
		if d.Date == today {
			todayEntries = append(todayEntries, d)
			todayTotal.InputTokens += d.InputTokens
			todayTotal.OutputTokens += d.OutputTokens
			todayTotal.CachedTokens += d.CachedTokens
			todayTotal.LLMCallCount += d.LLMCallCount
			todayTotal.ConversationCount += d.ConversationCount
		}
	}
	if len(todayEntries) > 0 {
		sb.WriteString("\n## Today's Usage by Model\n\n")
		sb.WriteString("| Model | Input | Output | Cached | Cache% | Calls |\n")
		sb.WriteString("|-------|-------|--------|--------|--------|-------|\n")
		slices.SortFunc(todayEntries, func(a, b sqlite.DailyTokenUsage) int {
			return int((b.InputTokens + b.OutputTokens) - (a.InputTokens + a.OutputTokens))
		})
		for _, d := range todayEntries {
			model := d.Model
			if model == "" {
				model = "(unknown)"
			}
			cacheRate := ""
			if d.InputTokens > 0 {
				cacheRate = fmt.Sprintf("%.0f%%", float64(d.CachedTokens)/float64(d.InputTokens)*100)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %d |\n",
				model,
				fmtTokens(d.InputTokens),
				fmtTokens(d.OutputTokens),
				fmtTokens(d.CachedTokens),
				cacheRate,
				d.LLMCallCount,
			)
		}
		// Today's total row
		totalCacheRate := ""
		if todayTotal.InputTokens > 0 {
			totalCacheRate = fmt.Sprintf("%.0f%%", float64(todayTotal.CachedTokens)/float64(todayTotal.InputTokens)*100)
		}
		fmt.Fprintf(&sb, "| **Today total** | **%s** | **%s** | **%s** | **%s** | **%d** |\n",
			fmtTokens(todayTotal.InputTokens),
			fmtTokens(todayTotal.OutputTokens),
			fmtTokens(todayTotal.CachedTokens),
			totalCacheRate,
			todayTotal.LLMCallCount,
		)
	}

	// --- Last 10 days daily summary ---
	daySummary := make(map[string]*sqlite.DailyTokenUsage)
	var sortedDates []string
	for _, d := range daily {
		if _, ok := daySummary[d.Date]; !ok {
			daySummary[d.Date] = &sqlite.DailyTokenUsage{Date: d.Date}
			sortedDates = append(sortedDates, d.Date)
		}
		s := daySummary[d.Date]
		s.InputTokens += d.InputTokens
		s.OutputTokens += d.OutputTokens
		s.CachedTokens += d.CachedTokens
		s.LLMCallCount += d.LLMCallCount
		s.ConversationCount += d.ConversationCount
	}
	// sortedDates is already in date DESC order (daily is ordered by date DESC)
	if len(sortedDates) > 10 {
		sortedDates = sortedDates[:10]
	}
	if len(sortedDates) > 0 {
		sb.WriteString("\n## Last 10 Days Summary\n\n")
		sb.WriteString("| Date | Input | Output | Cached | Cache% | Calls |\n")
		sb.WriteString("|------|-------|--------|--------|--------|-------|\n")
		for _, date := range sortedDates {
			d := daySummary[date]
			cacheRate := ""
			if d.InputTokens > 0 {
				cacheRate = fmt.Sprintf("%.0f%%", float64(d.CachedTokens)/float64(d.InputTokens)*100)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %d |\n",
				d.Date,
				fmtTokens(d.InputTokens),
				fmtTokens(d.OutputTokens),
				fmtTokens(d.CachedTokens),
				cacheRate,
				d.LLMCallCount,
			)
		}
	}

	m.appendSystemMarkdown(sb.String())
	m.updateViewportContent()
}

// formatTokenCount is defined in cli_view.go — do not duplicate here.

// fmtTokens formats large token counts with K/M suffixes for usage tables.
func fmtTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// handleUserCommand manages web users from TUI.
// Usage:
//
//	/user add <username>  — create a web user, returns auto-generated password
//	/user list            — list all web users
//	/user del <username>  — delete a web user
func (m *cliModel) handleUserCommand(arg string) {
	// All subcommands require admin.
	if m.isAdminFn != nil && !m.isAdminFn() {
		m.showSystemMsg("⛔ Admin only", feedbackError)
		return
	}
	if m.createWebUserFn == nil && m.listWebUsersFn == nil {
		m.showSystemMsg("Web user management not available (server-client mode or web not configured)", feedbackWarning)
		return
	}

	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "list" || arg == "ls" {
		m.handleUserList()
		return
	}

	parts := strings.Fields(arg)
	if len(parts) < 2 {
		m.showSystemMsg("Usage: /user add <username> | /user list | /user del <username>", feedbackInfo)
		return
	}

	subcmd := parts[0]
	username := parts[1]

	switch subcmd {
	case "add", "create":
		if m.createWebUserFn == nil {
			m.showSystemMsg("Create web user not available", feedbackWarning)
			return
		}
		password, err := m.createWebUserFn(username)
		if err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to create user: %v", err), feedbackError)
			return
		}
		m.showSystemMsg(fmt.Sprintf("✅ Web user created!\n\n| | |\n|---|---|\n| **Username** | `%s` |\n| **Password** | `%s` |\n\n⚠️ Save the password — it won't be shown again.", username, password), feedbackInfo)

	case "del", "delete", "rm", "remove":
		if m.deleteWebUserFn == nil {
			m.showSystemMsg("Delete web user not available", feedbackWarning)
			return
		}
		if err := m.deleteWebUserFn(username); err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to delete user: %v", err), feedbackError)
			return
		}
		m.showSystemMsg(fmt.Sprintf("✅ Web user `%s` deleted", username), feedbackInfo)

	default:
		m.showSystemMsg("Usage: /user add <username> | /user list | /user del <username>", feedbackInfo)
	}
}

// handleUserList displays all web users in a table.
func (m *cliModel) handleUserList() {
	if m.listWebUsersFn == nil {
		m.showSystemMsg("List web users not available", feedbackWarning)
		return
	}
	users, err := m.listWebUsersFn()
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("❌ Failed to list users: %v", err), feedbackError)
		return
	}
	if len(users) == 0 {
		m.showSystemMsg("No web users found. Use `/user add <username>` to create one.", feedbackInfo)
		return
	}

	var sb strings.Builder
	sb.WriteString("# Web Users\n\n")
	sb.WriteString("| # | Username | Created |\n|---|---|---|\n")
	for i, u := range users {
		username, _ := u["username"].(string)
		createdAt, _ := u["created_at"].(string)
		if createdAt != "" {
			// Trim to date+hour
			if t, err := time.Parse("2006-01-02T15:04:05Z07:00", createdAt); err == nil {
				createdAt = t.Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(&sb, "| %d | `%s` | %s |\n", i+1, username, createdAt)
	}
	sb.WriteString("\n`/user add <name>` to create · `/user del <name>` to delete")
	m.showSystemMsg(sb.String(), feedbackInfo)
}

// cacheTokenUsage caches token usage data for the context bar display.
// Called from all progress paths to avoid duplication.
func (m *cliModel) cacheTokenUsage(tu *protocol.TokenUsage) {
	if tu != nil && tu.PromptTokens > 0 {
		m.lastTokenUsage = tu
		if tu.MaxOutputTokens > 0 {
			m.cachedMaxOutputTokens = tu.MaxOutputTokens
		}
	}
}

// resolveMaxContextTokens delegates to cli_settings.go:resolveMaxContext.
func (m *cliModel) resolveMaxContextTokens() int { return m.resolveMaxContext() }

// applySessionLLMState applies a session's LLM state to the in-memory caches.
// This is the ONLY way to update activeSubID/cachedModelName/cachedMaxContextTokens
// from a SessionLLMState. Ensures all caches are consistent.
func (m *cliModel) applySessionLLMState(state SessionLLMState) {
	m.activeSubID = state.SubscriptionID
	m.cachedModelName = state.Model
	m.cachedMaxContextTokens = ResolveEffectiveMaxContext(state, m.subscriptionMgr)
	m.cachedMaxOutputTokens = int64(ResolveEffectiveMaxOutputTokens(state, m.subscriptionMgr))
}

// ---------------------------------------------------------------------------
// /plugin command
// ---------------------------------------------------------------------------

// handlePluginCommand dispatches /plugin subcommands.
func (m *cliModel) handlePluginCommand(parts []string) tea.Cmd {
	subcmd := ""
	if len(parts) > 1 {
		subcmd = strings.ToLower(parts[1])
	}

	if subcmd == "" {
		return m.handlePluginStatus()
	}

	switch subcmd {
	case "list":
		return m.handlePluginList()
	case "reload":
		if len(parts) < 3 {
			m.showSystemMsg("Usage: /plugin reload <plugin-id>", feedbackInfo)
			return nil
		}
		return m.handlePluginReload(strings.Join(parts[2:], " "))
	case "reload-all":
		return m.handlePluginReloadAll()
	case "health":
		return m.handlePluginHealth()
	case "metrics":
		return m.handlePluginMetrics()
	case "install":
		if len(parts) < 3 {
			m.showSystemMsg("Usage: /plugin install <source-directory>", feedbackInfo)
			return nil
		}
		return m.handlePluginInstall(strings.Join(parts[2:], " "))
	case "uninstall":
		if len(parts) < 3 {
			m.showSystemMsg("Usage: /plugin uninstall <plugin-id>", feedbackInfo)
			return nil
		}
		return m.handlePluginUninstall(strings.Join(parts[2:], " "))
	case "widgets":
		return m.handlePluginWidgets()
	case "refresh":
		return m.handlePluginRefresh()
	default:
		m.showSystemMsg(fmt.Sprintf("Unknown subcommand: %s\nUsage: /plugin [list|refresh|install <dir>|uninstall <id>|reload <id>|reload-all|health|metrics|widgets]", subcmd), feedbackInfo)
		return nil
	}
}

func (m *cliModel) handlePluginStatus() tea.Cmd {
	// Try local plugin manager first
	if m.pluginMgrFn == nil {
		// Fallback to remote plugin cache
		if m.remotePluginCache != nil {
			m.remotePluginCache.Refresh()
			m.showSystemMsg(m.remotePluginCache.FormatStatus(), feedbackInfo)
			m.renderCacheValid = false
			m.relayoutViewport()
			return nil
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	entries := mgr.ListPlugins()
	if len(entries) == 0 {
		m.showSystemMsg("🔌 No plugins loaded.", feedbackInfo)
		return nil
	}
	active := mgr.ActiveCount()
	m.showSystemMsg(fmt.Sprintf("🔌 Plugins: %d loaded, %d active\nUse /plugin list for details, /plugin health for status.", len(entries), active), feedbackInfo)
	return nil
}

func (m *cliModel) handlePluginList() tea.Cmd {
	// Try local plugin manager first
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.remotePluginCache.Refresh()
			m.showSystemMsg(m.remotePluginCache.FormatList(), feedbackInfo)
			m.renderCacheValid = false
			m.relayoutViewport()
		} else {
			m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		}
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	entries := mgr.ListPlugins()
	if len(entries) == 0 {
		m.showSystemMsg("No plugins loaded.", feedbackInfo)
		return nil
	}

	var sb strings.Builder
	sb.WriteString(m.styles.ToolHeader.Render("🔌 Plugins"))
	sb.WriteString("\n\n")
	fmt.Fprintf(&sb, "  %-20s %-16s %-10s %-14s %s\n",
		"ID", "Name", "Version", "State", "Runtime")
	sb.WriteString("  " + m.styles.Separator.Render("─────────────────────────────────────────────────────────") + "\n")
	for _, e := range entries {
		stateStr := m.pluginStateStyled(string(e.State))
		fmt.Fprintf(&sb, "  %-20s %-16s %-10s %s %-8s\n",
			e.Manifest.ID, e.Manifest.Name, e.Manifest.Version, stateStr, string(e.Manifest.Runtime))
	}
	m.appendSystemStyled(sb.String())
	m.updateViewportContent()
	return nil
}

func (m *cliModel) handlePluginReload(pluginID string) tea.Cmd {
	// Try local plugin manager first
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			if m.pluginReloading {
				m.showSystemMsg("Plugin reload already in progress, please wait...", feedbackWarning)
				return nil
			}
			m.pluginReloading = true
			m.showSystemMsg(fmt.Sprintf("🔄 Reloading plugin: %s...", pluginID), feedbackInfo)
			m.updateViewportContent()
			cache := m.remotePluginCache
			return func() tea.Msg {
				err := cache.PluginReload(pluginID)
				return cliPluginReloadResultMsg{pluginID: pluginID, err: err}
			}
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	if m.pluginReloading {
		m.showSystemMsg("Plugin reload already in progress, please wait...", feedbackWarning)
		return nil
	}
	m.pluginReloading = true
	m.showSystemMsg(fmt.Sprintf("🔄 Reloading plugin: %s...", pluginID), feedbackInfo)
	m.updateViewportContent()

	return func() tea.Msg {
		err := mgr.Reload(context.Background(), pluginID)
		return cliPluginReloadResultMsg{pluginID: pluginID, err: err}
	}
}

func (m *cliModel) handlePluginReloadAll() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			if m.pluginReloading {
				m.showSystemMsg("Plugin reload already in progress, please wait...", feedbackWarning)
				return nil
			}
			m.pluginReloading = true
			m.showSystemMsg("🔄 Reloading all plugins...", feedbackInfo)
			m.updateViewportContent()
			cache := m.remotePluginCache
			return func() tea.Msg {
				err := cache.PluginReloadAll()
				return cliPluginReloadAllResultMsg{err: err}
			}
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	if m.pluginReloading {
		m.showSystemMsg("Plugin reload already in progress, please wait...", feedbackWarning)
		return nil
	}
	m.pluginReloading = true
	m.showSystemMsg("🔄 Reloading all plugins...", feedbackInfo)
	m.updateViewportContent()

	return func() tea.Msg {
		err := mgr.ReloadAll(context.Background())
		return cliPluginReloadAllResultMsg{err: err}
	}
}

func (m *cliModel) handlePluginHealth() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.showSystemMsg("🔍 Checking plugin health...", feedbackInfo)
			m.updateViewportContent()
			cache := m.remotePluginCache
			return func() tea.Msg {
				results := cache.RefreshHealth()
				return cliPluginHealthResultMsg{results: results}
			}
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	m.showSystemMsg("🔍 Checking plugin health...", feedbackInfo)
	m.updateViewportContent()

	return func() tea.Msg {
		results := mgr.HealthCheck(context.Background())
		return cliPluginHealthResultMsg{results: results}
	}
}

func (m *cliModel) handlePluginMetrics() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.remotePluginCache.RefreshMetrics()
			m.appendSystemMarkdown(m.remotePluginCache.FormatMetrics())
			m.updateViewportContent()
			return nil
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	metrics := mgr.Metrics()
	var sb strings.Builder
	sb.WriteString("# Plugin Metrics\n\n")
	sb.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&sb, "| **Total plugins** | **%d** |\n", metrics.TotalPlugins)
	fmt.Fprintf(&sb, "| Active plugins | %d |\n", metrics.ActivePlugins)
	fmt.Fprintf(&sb, "| Registered tools | %d |\n", metrics.TotalTools)
	fmt.Fprintf(&sb, "| Registered hooks | %d |\n", metrics.TotalHooks)
	fmt.Fprintf(&sb, "| Registered enrichers | %d |\n", metrics.TotalEnrichers)
	if metrics.TotalPlugins > 0 {
		activeRate := float64(metrics.ActivePlugins) / float64(metrics.TotalPlugins) * 100
		fmt.Fprintf(&sb, "| **Active rate** | **%.0f%%** |\n", activeRate)
	}
	m.appendSystemMarkdown(sb.String())
	m.updateViewportContent()
	return nil
}

func (m *cliModel) handlePluginInstall(sourceDir string) tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			if m.pluginReloading {
				m.showSystemMsg("Plugin operation already in progress, please wait...", feedbackWarning)
				return nil
			}
			m.pluginReloading = true
			m.showSystemMsg(fmt.Sprintf("📦 Installing plugin from: %s...", sourceDir), feedbackInfo)
			m.updateViewportContent()
			cache := m.remotePluginCache
			return func() tea.Msg {
				pluginID, pluginDir, err := cache.PluginInstall(sourceDir)
				return cliPluginInstallResultMsg{pluginID: pluginID, pluginDir: pluginDir, err: err}
			}
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	if m.pluginReloading {
		m.showSystemMsg("Plugin operation already in progress, please wait...", feedbackWarning)
		return nil
	}
	m.pluginReloading = true
	m.showSystemMsg(fmt.Sprintf("📦 Installing plugin from: %s...", sourceDir), feedbackInfo)
	m.updateViewportContent()

	return func() tea.Msg {
		expanded := sourceDir
		if strings.HasPrefix(sourceDir, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				expanded = filepath.Join(home, sourceDir[2:])
			}
		}
		entry, err := mgr.InstallPlugin(context.Background(), expanded)
		var pluginID, pluginDir string
		if entry != nil {
			pluginID = entry.Manifest.ID
			pluginDir = entry.Dir
		}
		return cliPluginInstallResultMsg{
			pluginID:  pluginID,
			pluginDir: pluginDir,
			err:       err,
		}
	}
}

func (m *cliModel) handlePluginUninstall(pluginID string) tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			if m.pluginReloading {
				m.showSystemMsg("Plugin operation already in progress, please wait...", feedbackWarning)
				return nil
			}
			m.pluginReloading = true
			m.showSystemMsg(fmt.Sprintf("🗑️  Uninstalling plugin: %s...", pluginID), feedbackInfo)
			m.updateViewportContent()
			cache := m.remotePluginCache
			return func() tea.Msg {
				err := cache.PluginUninstall(pluginID)
				return cliPluginUninstallResultMsg{pluginID: pluginID, err: err}
			}
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	if m.pluginReloading {
		m.showSystemMsg("Plugin operation already in progress, please wait...", feedbackWarning)
		return nil
	}
	m.pluginReloading = true
	m.showSystemMsg(fmt.Sprintf("🗑️  Uninstalling plugin: %s...", pluginID), feedbackInfo)
	m.updateViewportContent()

	return func() tea.Msg {
		err := mgr.UninstallPlugin(context.Background(), pluginID)
		return cliPluginUninstallResultMsg{pluginID: pluginID, err: err}
	}
}

// pluginStateIcon returns an emoji icon for a plugin state.
func pluginStateIcon(state string) string {
	switch state {
	case "active":
		return "🟢"
	case "error":
		return "🔴"
	case "inactive", "discovered":
		return "⚪"
	case "deactivating", "activating":
		return "🟡"
	default:
		return "⚫"
	}
}

// pluginStateStyled returns a lipgloss-styled state string using cached theme styles.
func (m *cliModel) pluginStateStyled(state string) string {
	icon := pluginStateIcon(state)
	switch state {
	case "active":
		return icon + " " + m.styles.PluginActive.Render(state)
	case "error":
		return icon + " " + m.styles.PluginError.Render(state)
	case "discovered":
		return icon + " " + m.styles.PluginDiscovered.Render(state)
	case "inactive":
		return icon + " " + m.styles.PluginInactive.Render(state)
	case "activating", "deactivating":
		return icon + " " + m.styles.PluginTransition.Render(state)
	default:
		return icon + " " + m.styles.PluginInactive.Render(state)
	}
}

// resolveCompressRatio returns the compression threshold ratio from settings.
// Falls back to 0 if unavailable (renderContextUsage will use its own default).
func (m *cliModel) resolveCompressRatio() float64 {
	if m.channel == nil || m.channel.config.GetCurrentValues == nil {
		return 0
	}
	values := m.channel.config.GetCurrentValues()
	if v, ok := values["compression_threshold"]; ok && v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && f > 0 {
			return f
		}
	}
	return 0
}

// resolveMaxOutputTokens returns the max output tokens from settings values.
// Falls back to 0 if unavailable (renderContextTopBorder will use 8192 as default).
func (m *cliModel) resolveMaxOutputTokens() int64 {
	if m.channel == nil || m.channel.config.GetCurrentValues == nil {
		return 0
	}
	values := m.channel.config.GetCurrentValues()
	if v, ok := values["max_output_tokens"]; ok && v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// handlePluginRefresh forces a full refresh of plugin status and widget content.
// Useful when widget content changes on the server but the periodic refresh hasn't fired yet.
func (m *cliModel) handlePluginRefresh() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.remotePluginCache.Refresh()
			m.showSystemMsg("🔄 Plugin data refreshed.", feedbackInfo)
			m.renderCacheValid = false
			m.relayoutViewport()
			return nil
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	widgets := mgr.WidgetRegistry()
	if widgets != nil {
		widgets.RefreshAllWidgets(0, nil) // force re-render
	}
	m.showSystemMsg("🔄 Plugin widgets refreshed.", feedbackInfo)
	return nil
}

// handlePluginWidgets lists all UI widgets registered by plugins.
func (m *cliModel) handlePluginWidgets() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.remotePluginCache.refreshWidgets()
			msg := m.remotePluginCache.FormatWidgets()
			// Also show zone content for diagnosis
			zones := []string{"infoBar", "titleBarLeft", "titleBarRight", "statusBarLeft", "statusBarRight", "footer"}
			for _, z := range zones {
				content := m.remotePluginCache.WidgetZone(z)
				if content != "" {
					msg += fmt.Sprintf("\n  [%s] = %q", z, content)
				}
			}
			m.showSystemMsg(msg, feedbackInfo)
			m.renderCacheValid = false
			m.relayoutViewport()
			return nil
		}
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	mgr := m.pluginMgrFn()
	if mgr == nil {
		m.showSystemMsg("Plugin system is not enabled", feedbackWarning)
		return nil
	}
	wr := mgr.WidgetRegistry()
	infos := wr.WidgetInfo()
	if len(infos) == 0 {
		activeCount := mgr.ActiveCount()
		m.showSystemMsg(fmt.Sprintf("🖼️  No UI widgets registered.\n   Plugin system: %d active plugins, %d total widgets in registry.",
			activeCount, wr.Count()), feedbackInfo)
		return nil
	}
	var sb strings.Builder
	sb.WriteString("🖼️  UI Widgets:\n")
	for _, info := range infos {
		fmt.Fprintf(&sb, "  [%s/%s] zone=%s priority=%d\n",
			info.PluginID, info.WidgetID, info.Zone, info.Priority)
	}
	m.showSystemMsg(sb.String(), feedbackInfo)
	return nil
}

// renderScrollbar generates a vertical scrollbar string for a panel.
// contentWidth is the available width for the content (excluding scrollbar).
// visibleH is the number of visible lines.
// totalLines is the total content lines.
// scrollY is the current scroll offset.
// The scrollbar is 1 character wide (█ for thumb, ░ for track, ▒ for gutter).
func (m *cliModel) renderScrollbar(contentWidth, visibleH, totalLines, scrollY int) string {
	if totalLines <= visibleH || visibleH <= 0 || totalLines <= 0 {
		return ""
	}

	// Thumb size proportional to visible/total ratio
	thumbH := max(1, visibleH*visibleH/totalLines)
	if thumbH > visibleH {
		thumbH = visibleH
	}

	// Thumb position
	trackH := visibleH - thumbH
	maxScroll := totalLines - visibleH
	var thumbStart int
	if maxScroll > 0 {
		thumbStart = scrollY * trackH / maxScroll
	} else {
		thumbStart = 0
	}

	// Build scrollbar characters
	sb := m.styles.PanelHint // use subtle color
	thumbStyle := m.styles.PanelDesc

	var lines []string
	for i := 0; i < visibleH; i++ {
		if i >= thumbStart && i < thumbStart+thumbH {
			lines = append(lines, thumbStyle.Render("▐"))
		} else {
			lines = append(lines, sb.Render("│"))
		}
	}
	return strings.Join(lines, "\n")
}

// applyScrollbar appends a scrollbar to each line of the panel content.
// All lines are padded to exactly contentWidth before the scrollbar character,
// ensuring the scrollbar is vertically aligned regardless of line content.
func (m *cliModel) applyScrollbar(content string, contentWidth, totalLines, scrollY int) string {
	if totalLines <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	visibleH := len(lines)

	sbStr := m.renderScrollbar(contentWidth, visibleH, totalLines, scrollY)
	if sbStr == "" {
		return content
	}
	sbLines := strings.Split(sbStr, "\n")

	var b strings.Builder
	for i, line := range lines {
		visW := lipgloss.Width(line)
		// Truncate lines that reach or exceed contentWidth to leave room for
		// at least 1 space padding before the scrollbar.  Without truncation,
		// the scrollbar overflows PanelBox and wraps to the next line; without
		// the >= check, lines exactly at contentWidth get forced to padding=1
		// pushing the scrollbar 1 column right vs other lines (misalignment).
		if visW >= contentWidth {
			line = ansi.Truncate(line, contentWidth-1, "")
			visW = lipgloss.Width(line)
		}
		padding := contentWidth - visW
		if padding < 1 {
			padding = 1
		}
		b.WriteString(line)
		b.WriteString(strings.Repeat(" ", padding))
		if i < len(sbLines) {
			b.WriteString(sbLines[i])
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// handleSessionControlMsg processes AI-triggered TUI session control operations
// from the tui_control tool. It supports "switch", "close", "layout", and "theme" actions.
func (m *cliModel) handleSessionControlMsg(sc cliSessionControlMsg) tea.Cmd {
	switch sc.action {
	case "switch":
		if m.sessionsListFn == nil {
			sc.result <- &cliSessionResult{ok: false, err: "session list not available"}
			return nil
		}
		sessions := m.sessionsListFn()
		if len(sessions) == 0 {
			sc.result <- &cliSessionResult{ok: false, err: "no sessions found"}
			return nil
		}
		var best *SessionPanelEntry
		for i, entry := range sessions {
			if entry.ID == sc.chatID {
				best = &sessions[i]
				break
			}
		}
		if best == nil {
			lower := strings.ToLower(sc.chatID)
			for i, entry := range sessions {
				if strings.Contains(strings.ToLower(entry.Label), lower) ||
					strings.HasPrefix(strings.ToLower(entry.ID), lower) {
					best = &sessions[i]
					break
				}
			}
		}
		if best == nil {
			sc.result <- &cliSessionResult{ok: false, err: "session not found: " + sc.chatID}
			return nil
		}
		// Pure frontend switch — return success immediately, let history load async.
		_, cmd := m.switchToSession(*best)
		sc.result <- &cliSessionResult{ok: true}
		return cmd

	case "close":
		if sc.chatID == m.defaultChatID {
			sc.result <- &cliSessionResult{ok: false, err: "cannot close main session"}
			return nil
		}
		if sc.params["confirm"] != "true" {
			sc.result <- &cliSessionResult{ok: false, err: "confirmation_required: close session " + sc.chatID}
			return nil
		}
		sessions := m.sessionsListFn()
		for _, entry := range sessions {
			if entry.ID == sc.chatID || strings.Contains(strings.ToLower(entry.Label), strings.ToLower(sc.chatID)) {
				// RPC: deletes session on server to keep state consistent.
				// readPump is responsive (goroutine in transport_remote.go), so RPC works.
				if m.channel != nil && m.channel.config.SessionsDeleteFn != nil {
					if err := m.channel.config.SessionsDeleteFn(entry.Channel, entry.ID); err != nil {
						sc.result <- &cliSessionResult{ok: false, err: err.Error()}
						return nil
					}
				}
				sc.result <- &cliSessionResult{ok: true}
				return nil
			}
		}
		sc.result <- &cliSessionResult{ok: false, err: "session not found: " + sc.chatID}

	case "layout":
		key := sc.params["key"]
		val := sc.params["value"]
		if key == "" || val == "" {
			sc.result <- &cliSessionResult{ok: false, err: "layout requires key and value"}
			return nil
		}
		m.applyLayoutConfig(map[string]string{key: val})
		m.relayoutViewport()
		sc.result <- &cliSessionResult{ok: true}
		m.persistCLISettingsValues(map[string]string{key: val})

	case "theme":
		theme := sc.params["theme"]
		if theme == "" {
			sc.result <- &cliSessionResult{ok: false, err: "theme requires theme name"}
			return nil
		}
		m.applyThemeAndRebuild(theme)
		m.renderCacheValid = false
		m.updateViewportContent()
		sc.result <- &cliSessionResult{ok: true}
		m.persistCLISettingsValues(map[string]string{"theme": theme})

	case "send_slash":
		cmd := sc.params["command"]
		if cmd == "" {
			sc.result <- &cliSessionResult{ok: false, err: "command required for send_slash"}
			return nil
		}
		retCmd := m.handleSlashCommand(cmd)
		sc.result <- &cliSessionResult{ok: true}
		return retCmd

	default:
		sc.result <- &cliSessionResult{ok: false, err: "unknown action: " + sc.action}
	}
	return nil
}
