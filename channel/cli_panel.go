package channel

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"xbot/config"
	"xbot/internal/textarea"
	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
	"xbot/tools"
)

// --- §12 Interactive Panel ---

// panelStackEntry saves the parent panel state before pushing to a child panel.
// When the child panel's ESC is pressed, popPanel restores this state.
type panelStackEntry struct {
	mode        string // panelMode value ("settings", etc.)
	cursor      int    // panelCursor
	scrollY     int    // panelScrollY
	values      map[string]string
	schema      []SettingDefinition
	onSubmit    func(values map[string]string)
	fromPalette bool // true = ESC should reopen command palette instead of restoring mode
}

// pushPanel saves the current panel state onto the navigation stack.
// The caller should set the new panelMode afterwards (via openXxxPanel).
// Used when navigating from a parent panel (e.g. Settings) to a child panel.
func (m *cliModel) pushPanel() {
	m.panelStack = append(m.panelStack, panelStackEntry{
		mode:     m.panelMode,
		cursor:   m.panelCursor,
		scrollY:  m.panelScrollY,
		values:   m.panelValues,
		schema:   m.panelSchema,
		onSubmit: m.panelOnSubmit,
	})
}

// pushPanelFromPalette saves a marker so that popPanel reopens the palette
// instead of restoring a previous panel. Called when a palette command opens a panel.
func (m *cliModel) pushPanelFromPalette() {
	m.panelStack = append(m.panelStack, panelStackEntry{fromPalette: true})
}

// popPanel restores the parent panel state from the navigation stack.
// Returns true if a parent panel was restored, false if the stack is empty
// (meaning we should close the panel entirely).
func (m *cliModel) popPanel() bool {
	if len(m.panelStack) == 0 {
		return false
	}
	// Pop the last entry
	entry := m.panelStack[len(m.panelStack)-1]
	m.panelStack = m.panelStack[:len(m.panelStack)-1]

	if entry.fromPalette {
		// Clean up current panel state entirely, then reopen palette
		m.closePanel()
		m.openCommandPalette()
		return true
	}

	// Restore parent panel state
	m.panelMode = entry.mode
	m.panelCursor = entry.cursor
	m.panelScrollY = entry.scrollY
	m.panelValues = entry.values
	m.panelSchema = entry.schema
	m.panelOnSubmit = entry.onSubmit
	m.panelEdit = false
	m.panelCombo = false
	m.relayoutViewport()
	return true
}

// panelAgentEntry represents an interactive sub-agent session in the unified panel.
type panelAgentEntry struct {
	Role         string // role name (e.g. "explore")
	Instance     string // instance ID
	Running      bool   // true = currently executing
	Background   bool   // true = background mode
	Task         string // one-shot subagent task description
	Preview      string // latest progress/last reply summary
	ParentChatID string // parent session chatID for session isolation
}

// renderSelLine renders a settings panel selected row left-aligned to the given width.
// lipgloss v2 Width() defaults to centering; this helper avoids that by manual padding.
func (m *cliModel) renderSelLine(line string, w int) string {
	// Use w-2 to leave room for scrollbar (1 char) + spacing (1 char).
	// When scrollbar is not shown, applyScrollbar won't be called, so
	// the shorter padding is fine (panel box clips the content anyway).
	padW := w - 2
	if padW < 10 {
		padW = 10
	}
	vw := lipgloss.Width(line)
	if vw < padW {
		line += strings.Repeat(" ", padW-vw)
	}
	return m.styles.SettingsSelBg.Render(line)
}

// openSettingsPanel activates the settings panel overlay.
func (m *cliModel) openSettingsPanel(schema []SettingDefinition, values map[string]string, onSubmit func(map[string]string)) {
	m.panelMode = "settings"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间
	m.panelCursor = 0
	m.panelEdit = false
	m.panelScrollY = 0
	m.panelSubGeneration = m.subGeneration // capture current subscription generation
	// Store full schema and pre-process defaults / options on the full copy.
	m.panelSchemaFull = make([]SettingDefinition, len(schema))
	copy(m.panelSchemaFull, schema)
	m.panelValues = make(map[string]string, len(values))
	for k, v := range values {
		m.panelValues[k] = v
	}
	// Fill defaults and mark global-scoped settings as read-only (admin-only).
	for i := range m.panelSchemaFull {
		def := &m.panelSchemaFull[i]
		cur, ok := m.panelValues[def.Key]
		needsDefault := !ok || cur == ""
		// For number fields, also treat "0" as needing default when the
		// default value is non-zero (handles stale DB entries from scope migrations).
		if !needsDefault && def.Type == SettingTypeNumber && def.DefaultValue != "" && def.DefaultValue != "0" {
			if cur == "0" {
				needsDefault = true
			}
		}
		if needsDefault && def.DefaultValue != "" {
			m.panelValues[def.Key] = def.DefaultValue
		}
		// Inject cross-subscription model list for tier model selectors.
		if (def.Key == "vanguard_model" || def.Key == "balance_model" || def.Key == "swift_model") && m.channel.modelLister != nil && len(def.Options) == 0 {
			models := m.channel.modelLister.ListModels()
			if len(models) > 0 {
				opts := make([]SettingOption, len(models))
				for j, mdl := range models {
					opts[j] = SettingOption{Label: mdl, Value: mdl}
				}
				def.Options = opts
				def.Type = SettingTypeCombo
			}
		}
		// Global-scoped settings require admin access — mark read-only for non-admin users.
		if !def.ReadOnly && IsGlobalScopedSettingKey(def.Key) && (m.isAdminFn == nil || !m.isAdminFn()) {
			def.ReadOnly = true
		}
	}
	// Auto-fill base_url on panel open if provider has a known default
	// and base_url is currently empty (typical for setup wizard).
	if provider := m.panelValues["llm_provider"]; provider != "" {
		if m.panelValues["llm_base_url"] == "" {
			if url, ok := ProviderDefaultURLs[provider]; ok {
				m.panelValues["llm_base_url"] = url
			}
		}
		if m.panelValues["llm_model"] == "" {
			if model, ok := ProviderRecommendedModels[provider]; ok {
				m.panelValues["llm_model"] = model
			}
		}
		m.panelPrevProvider = provider
		// Show provider-specific API key guidance on initial open.
		m.updateAPIKeyHint(provider)
	}
	// Build visible schema from full schema (filters DependsOn fields).
	m.rebuildVisibleSchema()
	m.panelOnSubmit = onSubmit
	m.panelOnCancel = nil
	// Pre-create textarea for editing
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.SetWidth(m.panelWidth(60))
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelEditTA = ta
}

// rebuildVisibleSchema rebuilds panelSchema from panelSchemaFull,
// filtering out fields whose DependsOn conditions are not met by current panelValues.
func (m *cliModel) rebuildVisibleSchema() {
	m.panelSchema = make([]SettingDefinition, 0, len(m.panelSchemaFull))
	for _, def := range m.panelSchemaFull {
		if IsFieldVisible(def, m.panelValues) {
			m.panelSchema = append(m.panelSchema, def)
		}
	}
	// Clamp cursor
	if m.panelCursor >= len(m.panelSchema) {
		m.panelCursor = max(0, len(m.panelSchema)-1)
	}
}

// autoFillBaseURL sets llm_base_url to the provider's default URL when the
// current base_url is empty or matches a known provider default (i.e., was
// previously auto-filled). Never overwrites a user's custom URL.
// Also auto-fills llm_model with the recommended model for the provider.
func (m *cliModel) autoFillBaseURL(provider string) {
	defaultURL, ok := ProviderDefaultURLs[provider]
	if !ok {
		// Provider has no known default (azure, custom) — clear base_url only
		// if it currently holds a previous provider's auto-filled URL.
		cur := m.panelValues["llm_base_url"]
		if cur != "" && IsProviderDefaultURL(cur) {
			m.panelValues["llm_base_url"] = ""
		}
	} else {
		cur := m.panelValues["llm_base_url"]
		if cur == "" || IsProviderDefaultURL(cur) {
			m.panelValues["llm_base_url"] = defaultURL
		}
	}
	// Auto-fill recommended model when model is empty or matches a previous provider default.
	if model, ok := ProviderRecommendedModels[provider]; ok {
		cur := m.panelValues["llm_model"]
		if cur == "" || isProviderRecommendedModel(cur) {
			m.panelValues["llm_model"] = model
		}
	}
	// Dynamically update the API Key field description with provider-specific
	// guidance and a clickable link to the key management page.
	m.updateAPIKeyHint(provider)
	// Rebuild visible schema (DependsOn fields may appear/disappear).
	m.rebuildVisibleSchema()
}

// updateAPIKeyHint updates the llm_api_key field's description in panelSchemaFull
// to show provider-specific guidance with a clickable OSC 8 hyperlink.
func (m *cliModel) updateAPIKeyHint(provider string) {
	hint := FormatProviderHint(provider)
	if hint == "" {
		return
	}
	for i := range m.panelSchemaFull {
		if m.panelSchemaFull[i].Key == "llm_api_key" {
			m.panelSchemaFull[i].Description = hint
			break
		}
	}
	// Also update in the visible schema if the field is there.
	for i := range m.panelSchema {
		if m.panelSchema[i].Key == "llm_api_key" {
			m.panelSchema[i].Description = hint
			break
		}
	}
}

// isProviderRecommendedModel checks if a model name matches any provider's recommended model.
func isProviderRecommendedModel(model string) bool {
	for _, m := range ProviderRecommendedModels {
		if m == model {
			return true
		}
	}
	return false
}

// openSetupPanel opens the first-run setup wizard as a settings-style panel.
// Pre-fills from GetCurrentValues (respects existing config), falls back to
// DefaultValue for keys not yet configured. This prevents misleading the user
// with "flat" when their config already says "letta".
func (m *cliModel) openSetupPanel() {
	schema := m.locale.SetupSchema
	values := make(map[string]string)
	// Start from current config so existing choices are preserved.
	if m.channel != nil && m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			values[k] = v
		}
	}
	// Fill gaps with schema defaults (e.g. keys not yet in config).
	for _, def := range schema {
		if _, ok := values[def.Key]; !ok && def.DefaultValue != "" {
			values[def.Key] = def.DefaultValue
		}
	}
	m.panelIsSetup = true
	m.openSettingsPanel(schema, values, func(vals map[string]string) {
		// Apply all settings including setup-only keys (provider, api_key, sandbox, memory)
		if m.channel.config.ApplySettings != nil {
			m.channel.config.ApplySettings(vals, m.chatID)
		}
		// NOTE: UI updates (theme/locale/viewport) are handled by
		// handleSettingsSavedMsg in Update() since this runs in a goroutine.
	})
}

// askItem represents a single question in the AskUser panel.
type askItem struct {
	Question string   // the question text
	Options  []string // choices (empty = free input only)
	Answer   string   // user's answer (set on submit)
	Other    string   // user's custom input when "Other" option selected
}

// askQItem is the JSON structure for questions metadata from the AskUser tool.
type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// rewindItem represents a candidate user message in the rewind selection list.
type rewindItem struct {
	MsgIndex int       // index in m.messages
	Preview  string    // first line of the message content (for display)
	Content  string    // full message content (for input box on select)
	Time     time.Time // message timestamp (for DB truncation cutoff)
}

// dangerItem represents a single clearable memory target in the danger zone panel.
type dangerItem struct {
	Action string // "session", "core_persona", etc.
	Label  string // display label
	Stat   string // current stat (e.g. "128 msgs")
}

// dangerConfirmStrings maps action keys to required confirmation strings.
var dangerConfirmStrings = map[string]string{
	"session":       "DELETE-SESSION",
	"core_persona":  "DELETE-PERSONA",
	"core_human":    "DELETE-HUMAN",
	"core_working":  "DELETE-WORKING",
	"core_all":      "DELETE-CORE-MEMORY",
	"long_term":     "DELETE-LONG-TERM",
	"event_history": "DELETE-HISTORY",
	"archival":      "DELETE-ARCHIVAL",
	"reset_all":     "RESET-ALL-MEMORY",
}

// openAskUserPanel activates the ask-user panel overlay.
func (m *cliModel) openAskUserPanel(items []askItem, onAnswer func(map[string]string), onCancel func()) {
	m.panelMode = "askuser"
	// Do NOT clear m.progress here — the viewport above the AskUser panel
	// still renders the progress block (iteration history, tool calls, etc).
	// Clearing it causes all iteration info from the current turn to disappear.
	// Progress will be cleaned up by endAgentTurn when the turn actually finishes.
	m.typing = false
	m.relayoutViewport() // viewport gets split-layout height
	m.panelItems = items
	m.panelTab = 0
	m.panelOptSel = make(map[int]map[int]bool)
	m.panelOptCursor = make(map[int]int)
	m.askPanelScrollY = 0
	m.askPanelTotalLines = 0
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.Prompt = "  "
	applyTAStyles(&ta, &m.styles)
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(50))
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()
	m.panelAnswerTA = ta
	// Initialize Other single-line input
	ti := textinput.New()
	ti.Placeholder = m.locale.PanelOtherPlaceholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	m.panelOtherTI = ti
	m.panelOnAnswer = onAnswer
	m.panelOnCancel = onCancel
}

// closePanel deactivates any active panel.
func (m *cliModel) closePanel() {
	m.panelMode = ""
	m.panelStack = nil
	m.panelEdit = false
	m.panelCombo = false
	m.panelSchema = nil
	m.panelValues = nil
	m.panelPrevProvider = ""
	m.panelOnSubmit = nil
	m.panelItems = nil
	m.panelTab = 0
	m.panelOptSel = nil
	m.panelOptCursor = nil
	// Bg tasks/agents panel cleanup
	m.cleanupCompletedBgTasks()
	m.panelBgTasks = nil
	m.panelBgAgents = nil
	m.panelBgViewing = false
	m.panelScrollY = 0
	m.panelBgLogLines = nil
	m.panelBgLogFollow = false
	// Danger zone cleanup
	m.panelDangerItems = nil
	m.panelDangerCursor = 0
	m.panelDangerConfirm = false
	m.panelDangerOnExec = nil
	// Runner panel cleanup
	m.panelRunnerServerTI = textinput.Model{}
	m.panelRunnerTokenTI = textinput.Model{}
	m.panelRunnerWorkspace = textinput.Model{}
	m.panelRunnerEditField = 0
	// 恢复 viewport 到正常模式高度
	m.panelScrollY = 0
	m.relayoutViewport()
}

// ---------------------------------------------------------------------------
// §9 Rewind Panel (/rewind command)
// ---------------------------------------------------------------------------

// openRewindPanel collects user messages from history and opens the rewind overlay.
// Messages before the most recent [Compacted context] marker are excluded —
// they were replaced by compression and no longer exist in the session DB.
func (m *cliModel) openRewindPanel() {
	var items []rewindItem
	compressIdx := -1 // index in items where [Compacted context] appears
	for i, msg := range m.messages {
		if msg.role != "user" {
			continue
		}
		content := msg.content
		// Build preview: first line, truncated
		preview := content
		if idx := strings.Index(preview, "\n"); idx >= 0 {
			preview = preview[:idx]
		}
		if runes := []rune(preview); len(runes) > 60 {
			preview = string(runes[:57]) + "..."
		}
		items = append(items, rewindItem{
			MsgIndex: i,
			Preview:  preview,
			Content:  content,
			Time:     msg.timestamp,
		})
		if strings.HasPrefix(content, "[Compacted context]") {
			compressIdx = len(items) - 1
		}
	}
	// If compression happened, only allow rewinding to the compressed context
	// or later — messages before it were deleted from the session DB.
	if compressIdx > 0 {
		items = items[compressIdx:]
	}
	if len(items) == 0 {
		m.showTempStatus(m.locale.NoMessagesToDelete)
		return
	}
	m.rewindItems = items
	m.rewindCursor = len(items) - 1 // default to most recent
	m.rewindMode = true
	m.renderCacheValid = false
}

// closeRewindPanel deactivates the rewind overlay.
func (m *cliModel) closeRewindPanel() {
	m.rewindMode = false
	m.rewindItems = nil
	m.rewindCursor = 0
}

// viewRewindPanel renders the rewind selection overlay (centered panel).
func (m *cliModel) viewRewindPanel(width, height int) string {
	if !m.rewindMode || len(m.rewindItems) == 0 {
		return ""
	}

	var lines []string

	// Header
	lines = append(lines, m.styles.PanelHeader.Render(m.locale.RewindTitle))
	lines = append(lines, m.styles.PanelDesc.Render(m.locale.RewindHint))
	lines = append(lines, "") // spacer

	// Items (newest first for natural selection)
	total := len(m.rewindItems)
	maxVisible := height - 10 // leave room for header + hints + borders
	if maxVisible < 3 {
		maxVisible = 3
	}

	// Calculate scroll offset to keep cursor visible
	scrollStart := 0
	scrollEnd := total
	if total > maxVisible {
		scrollStart = m.rewindCursor - maxVisible/2
		if scrollStart < 0 {
			scrollStart = 0
		}
		scrollEnd = scrollStart + maxVisible
		if scrollEnd > total {
			scrollEnd = total
			scrollStart = scrollEnd - maxVisible
		}
	}

	for i := scrollStart; i < scrollEnd; i++ {
		item := m.rewindItems[i]
		cursor := " "
		style := m.styles.TextMutedSt
		if i == m.rewindCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		// Show turn number (newest = 1)
		turnNum := total - i
		line := style.Render(fmt.Sprintf(" %s #%d  %s", cursor, turnNum, item.Preview))
		lines = append(lines, line)
	}

	// Scroll indicator with position
	if total > maxVisible {
		scrollInfo := fmt.Sprintf("  [%d-%d/%d]", scrollStart+1, scrollEnd, total)
		lines = append(lines, m.styles.TextMutedSt.Render(scrollInfo))
	}

	// Build panel with border
	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint line
	hint := m.styles.PanelHint.Render(" ↑↓ Navigate  PgUp/PgDn Page  Home/End Jump  Enter Rewind  Esc Cancel")

	// Center vertically
	listH := len(lines) + 3
	blankLines := max(0, (height-listH)/2)
	var b strings.Builder
	for i := 0; i < blankLines; i++ {
		b.WriteString("\n")
	}
	b.WriteString(box)
	b.WriteString("\n")
	b.WriteString(hint)

	return b.String()
}

// handleRewindKey handles key events for the rewind overlay.
// Returns (handled, cmd). Called from Update() at same priority as quickSwitch.
func (m *cliModel) handleRewindKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if !m.rewindMode {
		return false, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		m.closeRewindPanel()
		return true, nil
	case tea.KeyUp:
		if m.rewindCursor > 0 {
			m.rewindCursor--
		}
		return true, nil
	case tea.KeyDown:
		if m.rewindCursor < len(m.rewindItems)-1 {
			m.rewindCursor++
		}
		return true, nil
	case tea.KeyPgUp:
		maxVisible := m.height - 10
		if maxVisible < 3 {
			maxVisible = 3
		}
		if m.rewindCursor > 0 {
			m.rewindCursor -= min(maxVisible, m.rewindCursor)
		}
		return true, nil
	case tea.KeyPgDown:
		maxVisible := m.height - 10
		if maxVisible < 3 {
			maxVisible = 3
		}
		maxIdx := len(m.rewindItems) - 1
		if m.rewindCursor < maxIdx {
			m.rewindCursor += min(maxVisible, maxIdx-m.rewindCursor)
		}
		return true, nil
	case tea.KeyHome:
		m.rewindCursor = 0
		return true, nil
	case tea.KeyEnd:
		m.rewindCursor = len(m.rewindItems) - 1
		return true, nil
	case tea.KeyEnter:
		m.applyRewind()
		return true, nil
	}
	return true, nil // block all other keys
}

// applyRewind executes the rewind: truncate history at selected message,
// run file checkpoint rollback, and place selected message content in input box.
func (m *cliModel) applyRewind() {
	if m.rewindCursor < 0 || m.rewindCursor >= len(m.rewindItems) {
		m.closeRewindPanel()
		return
	}
	item := m.rewindItems[m.rewindCursor]
	selectedContent := item.Content
	cutoff := item.Time

	// Truncate UI messages: keep everything BEFORE the selected message
	cutIdx := item.MsgIndex
	m.messages = m.messages[:cutIdx]

	// Truncate DB session messages (synchronous, by timestamp).
	// Must be synchronous — Ctrl+Z calls os.Exit(0) which kills all goroutines.
	// If we used async (go func()), the DELETE might not complete before exit,
	// leaving the DB in an inconsistent state with modernc.org/sqlite WAL.
	if m.channel != nil && m.channel.config.TrimHistoryFn != nil {
		// Dynamic callback with current session's channel+chatID — works across
		// session switches unlike the static trimHistoryFn which was captured
		// at TUI startup with the initial chatID.
		if err := m.channel.config.TrimHistoryFn(m.channelName, m.chatID, cutoff); err != nil {
			log.WithError(err).Warn("Failed to trim session history after rewind")
		}
	} else if m.trimHistoryFn != nil {
		log.WithFields(log.Fields{"cutIdx": cutIdx, "cutoff": cutoff, "totalMsgs": len(m.messages)}).Info("Rewind: truncating DB messages (legacy callback)")
		if err := m.trimHistoryFn(cutoff); err != nil {
			log.WithError(err).Warn("Failed to trim session history after rewind")
		}
	} else if cutoff.IsZero() {
		log.Warn("Rewind: cutoff timestamp is zero, DB messages will NOT be truncated")
	} else {
		log.Warn("Rewind: trimHistoryFn is nil, DB messages will NOT be truncated")
	}

	// Reset cached token counts so maybeCompress doesn't use stale values
	// from before the rewind and trigger an immediate (incorrect) compression.
	if m.resetTokenStateFn != nil {
		m.resetTokenStateFn()
	}

	// File rollback via checkpoint state
	if m.checkpointState != nil && m.checkpointState.Store() != nil {
		// Compute the absolute turn index for the selected user message.
		// m.agentTurnID is the turn index of the most recent user message.
		// Each rewindItem corresponds to one user turn (startAgentTurn increments
		// agentTurnID by 1). The selected item at rewindCursor has turn index:
		//   agentTurnID - (totalItems - 1 - rewindCursor)
		// This correctly handles multiple rewind-send-cancel cycles where
		// agentTurnID has grown beyond the number of visible user messages.
		totalItems := len(m.rewindItems)
		absTurnIdx := int(m.agentTurnID) - (totalItems - 1 - m.rewindCursor)
		if absTurnIdx < 1 {
			absTurnIdx = 1
		}
		result, _ := m.checkpointState.Store().Rewind(absTurnIdx)
		m.rewindResult = &result
	}

	// Put selected message content into input box
	m.textarea.SetValue(selectedContent)
	m.textarea.CursorEnd()
	m.textarea.Focus()

	// Reset state
	m.rewindMode = false
	m.rewindItems = nil
	m.rewindCursor = 0
	m.renderCacheValid = false
	m.cachedHistory = ""
	m.updateViewportContent()
}

// openBgTasksPanel opens the background tasks management panel.
func (m *cliModel) openBgTasksPanel() {
	m.panelMode = "bgtasks"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间

	// Fetch tasks — use callback (works for both local and remote mode)
	m.panelBgTasks = m.listBgTasks()

	// Fetch agents and filter by current session
	m.panelBgAgents = nil
	if m.agentListFn != nil {
		allAgents := m.agentListFn()
		for _, ag := range allAgents {
			if ag.ParentChatID == "" || ag.ParentChatID == m.chatID {
				m.panelBgAgents = append(m.panelBgAgents, ag)
			}
		}
	}

	m.panelBgCursor = 0
	m.panelBgViewing = false
	m.panelScrollY = 0
	m.panelBgLogLines = nil
	m.panelBgLogFollow = false
	// Clamp cursor
	totalItems := len(m.panelBgTasks) + len(m.panelBgAgents)
	if totalItems == 0 {
		m.panelBgCursor = -1
	} else if m.panelBgCursor >= totalItems {
		m.panelBgCursor = totalItems - 1
	}
}

// listBgTasks returns running background tasks via callback or direct access.
func (m *cliModel) listBgTasks() []*BgTask {
	if m.bgTaskListFn != nil {
		return m.bgTaskListFn()
	}
	return nil
}

// cleanupCompletedBgTasks removes completed/errored tasks from the task manager
// so they don't accumulate indefinitely. Running tasks are preserved.
func (m *cliModel) cleanupCompletedBgTasks() {
	if m.bgTaskCleanupFn != nil {
		m.bgTaskCleanupFn()
	}
}

// killBgTask kills a background task via callback or direct access.
func (m *cliModel) killBgTask(taskID string) error {
	if m.bgTaskKillFn != nil {
		return m.bgTaskKillFn(taskID)
	}
	return fmt.Errorf("background tasks not available")
}

// updateBgTasksPanel handles key events in the bg tasks panel.
// Returns (handled, newModel, cmd).
func (m *cliModel) updateBgTasksPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// Refresh task list
	m.panelBgTasks = m.listBgTasks()
	totalItems := len(m.panelBgTasks)

	// Log viewing sub-mode
	if m.panelBgViewing {
		switch {
		case msg.Code == tea.KeyEsc || msg.String() == "ctrl+c":
			// If navigator stack has a parent (e.g. sidebar direct-click),
			// pop back to it (which closes the panel to main view).
			// Otherwise, just exit log view back to task list.
			if !m.popPanel() {
				m.panelBgViewing = false
				m.panelScrollY = 0
				m.panelBgLogLines = nil
			}
			return true, m, nil
		case msg.Code == tea.KeyUp:
			m.panelScrollY -= 5
			if m.panelScrollY < 0 {
				m.panelScrollY = 0
			}
			m.panelBgLogFollow = false
			return true, m, nil
		case msg.Code == tea.KeyDown:
			m.panelScrollY += 5
			m.panelBgLogFollow = false
			return true, m, nil
		case msg.Code == tea.KeyPgUp:
			m.panelScrollY -= m.panelVisibleHeight()
			if m.panelScrollY < 0 {
				m.panelScrollY = 0
			}
			m.panelBgLogFollow = false
			return true, m, nil
		default:
			// PgDn: bubbletea doesn't have a constant, match by string
			if msg.String() == "pgdown" {
				m.panelScrollY += m.panelVisibleHeight()
				m.panelBgLogFollow = false
				return true, m, nil
			}
		}
		return true, m, nil
	}

	// Task list mode
	switch {
	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()
	case msg.Code == tea.KeyEsc:
		if !m.popPanel() {
			return m.closePanelAndResume()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if m.panelBgCursor > 0 {
			m.panelBgCursor--
			m.ensureBgCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyDown || msg.String() == "ctrl+j":
		if m.panelBgCursor < totalItems-1 {
			m.panelBgCursor++
			m.ensureBgCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			// Task entry: view output log
			task := m.panelBgTasks[m.panelBgCursor]
			m.panelBgLogLines = sanitizeOutputLines(task.Output)
			if len(m.panelBgLogLines) == 0 {
				m.panelBgLogLines = []string{"(no output)"}
			}
			m.panelBgViewing = true
			m.panelScrollY = 0
			m.panelBgLogFollow = true
		}
		return true, m, nil

	case msg.Code == tea.KeyDelete || msg.String() == "ctrl+d":
		// Kill selected running task
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			task := m.panelBgTasks[m.panelBgCursor]
			if task.Status == BgTaskRunning {
				if err := m.killBgTask(task.ID); err != nil {
					m.showTempStatus(fmt.Sprintf(m.locale.KillFailed, err))
					return true, m, m.clearTempStatusCmd()
				}
				// Refresh list after kill, filter out killed tasks
				m.panelBgTasks = m.listBgTasks()
				var running []*BgTask
				for _, t := range m.panelBgTasks {
					if t.Status == BgTaskRunning {
						running = append(running, t)
					}
				}
				m.panelBgTasks = running
				if len(m.panelBgTasks) == 0 {
					handled, m2, cmd := m.closePanelAndResume()
					return handled, m2, cmd
				}
				if m.panelBgCursor >= len(m.panelBgTasks) {
					m.panelBgCursor = len(m.panelBgTasks) - 1
				}
				return true, m, nil
			}
		}
		return true, m, nil
	}

	return true, m, nil
}

// viewBgTasksPanel renders the bg tasks panel.
func (m *cliModel) viewBgTasksPanel() string {
	if m.panelBgViewing {
		return m.viewBgTaskLog()
	}
	return m.viewBgTaskList()
}

// viewBgTaskList renders the background task list view.
func (m *cliModel) viewBgTaskList() string {
	// §20 使用缓存样式
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render(m.locale.BgTasksTitle)
	help := s.PanelDesc.Render(m.locale.BgTasksHelp)

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	// Calculate dynamic truncation width.
	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	totalItems := len(m.panelBgTasks)

	if totalItems == 0 {
		sb.WriteString(s.PanelEmpty.Render(m.locale.BgTasksEmpty))
	} else {
		idx := 0
		// Render tasks
		for _, task := range m.panelBgTasks {
			elapsed := time.Since(task.StartedAt).Round(time.Second)
			if task.FinishedAt != nil {
				elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
			}
			statusIcon := "●"
			statusStyle := s.ProgressRunning
			switch task.Status {
			case BgTaskDone:
				if task.Error != "" || task.ExitCode != 0 {
					statusIcon = "✗"
					statusStyle = s.ProgressError
				} else {
					statusIcon = "✓"
					statusStyle = s.ProgressDone
				}
			case BgTaskKilled:
				statusIcon = "✗"
				statusStyle = s.ProgressError
			}

			prefix := "  "
			if idx == m.panelBgCursor {
				prefix = cursorStyle.Render("▸")
			}

			cmd := task.Command
			cmdW := contentW - 23
			if cmdW < 10 {
				cmdW = 10
			}
			cmd = truncateToWidth(cmd, cmdW)

			line := fmt.Sprintf("%s %s  %-8s %s  %s",
				prefix,
				statusStyle.Render(statusIcon),
				task.ID,
				formatElapsed(int64(elapsed.Milliseconds())),
				cmd,
			)
			sb.WriteString(truncateToWidth(line, contentW))
			sb.WriteString("\n")
			idx++
		}
	}

	return sb.String()
}

// viewBgTaskLog renders the log viewer for a selected task.
// Returns the FULL content — scrolling is handled by the outer clampPanelScroll + cli_view.go slicing.
// Refreshes task data on each render so the log updates in real-time while the task runs.
func (m *cliModel) viewBgTaskLog() string {
	// §20 使用缓存样式
	s := &m.styles

	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	// Refresh task list to get latest output for running tasks
	latestTasks := m.listBgTasks()

	var title string
	if m.panelBgCursor >= 0 && m.panelBgCursor < len(latestTasks) {
		task := latestTasks[m.panelBgCursor]
		cmd := truncateToWidth(task.Command, contentW-12)
		title = fmt.Sprintf(m.locale.BgTaskLogTitle, task.ID, cmd)
		// Update log lines from latest task output
		oldCount := len(m.panelBgLogLines)
		m.panelBgLogLines = sanitizeOutputLines(task.Output)
		newCount := len(m.panelBgLogLines)
		// Follow-tail: auto-scroll when new lines appear
		if m.panelBgLogFollow && newCount > oldCount {
			visibleH := m.panelVisibleHeight() - 1 // -1 for header line
			m.panelScrollY = max(0, newCount-visibleH)
		}
	} else if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
		// Fallback to cached task list if refresh returned empty
		task := m.panelBgTasks[m.panelBgCursor]
		cmd := truncateToWidth(task.Command, contentW-12)
		title = fmt.Sprintf(m.locale.BgTaskLogTitle, task.ID, cmd)
	}
	help := s.PanelDesc.Render(m.locale.BgTaskLogHelp)

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(truncateToWidth(title, contentW)))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	lines := m.panelBgLogLines
	if len(lines) == 0 {
		lines = []string{"(no output yet)"}
	}
	for _, line := range lines {
		sb.WriteString(truncateToWidth(line, contentW))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (m *cliModel) openSessionsPanel() {
	m.panelMode = "sessions"
	m.relayoutViewport()

	// sessionsListFn now handles everything (main + local dir + subagents).
	// Only fall back to local dir sessions when there's no callback.
	if m.sessionsListFn != nil {
		m.panelSessionItems = m.sessionsListFn()
	} else {
		m.panelSessionItems = m.listLocalDirSessions()
	}
	// Position cursor on the currently active session
	m.panelSessionCursor = 0
	for i, entry := range m.panelSessionItems {
		if entry.Active {
			m.panelSessionCursor = i
			break
		}
		// For agent sessions, match by chatID (Active is only set for main sessions)
		if entry.Type == "agent" {
			agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
			if entry.Instance != "" {
				agentChatID += ":" + entry.Instance
			}
			if agentChatID == m.chatID {
				m.panelSessionCursor = i
				break
			}
		}
	}
	m.panelSessionViewing = false
	m.panelScrollY = 0
	m.ensureSessionCursorVisible()
}

// updateSessionsPanel handles key events for the sessions panel.
// Returns (handled, model, cmd).
func (m *cliModel) updateSessionsPanel(msg tea.KeyPressMsg) (bool, *cliModel, tea.Cmd) {
	switch {
	case msg.Code == tea.KeyEsc:
		if m.panelSessionViewing {
			m.panelSessionViewing = false
			m.panelScrollY = 0
			return true, m, nil
		}
		if !m.popPanel() {
			m.panelMode = ""
			m.panelSessionItems = nil
			m.relayoutViewport()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if !m.panelSessionViewing && m.panelSessionCursor > 0 {
			m.panelSessionCursor--
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyDown:
		if !m.panelSessionViewing && m.panelSessionCursor < len(m.panelSessionItems)-1 {
			m.panelSessionCursor++
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelSessionViewing {
			// Viewing mode: Esc goes back, Enter does nothing
			return true, m, nil
		}
		if m.panelSessionCursor >= 0 && m.panelSessionCursor < len(m.panelSessionItems) {
			entry := m.panelSessionItems[m.panelSessionCursor]
			switch entry.Type {
			case "main":
				// Switch to this chatroom + close panel
				if entry.ID != m.chatID {
					m.saveCurrentSession() // save current session state
					m.chatID = entry.ID
					SetLastActiveSession(m.defaultChatID, entry.ID)
					m.channelName = entry.Channel
					// Update workdir to match the session's workdir.
					workDir, _ := ParseChatID(entry.ID)
					if workDir != "" {
						m.workDir = workDir
						if m.channel != nil && m.channel.config.SetCWDFn != nil {
							_ = m.channel.config.SetCWDFn(entry.Channel, entry.ID, workDir)
						}
					}
					// Update background task session key for isolation
					if m.channel != nil {
						m.channel.bgSessionKey = "cli:" + entry.ID
						m.channel.updateBgTaskCountFn()
					}
					m.messages = nil
					m.lastTokenUsage = nil // clear stale token bar on session switch
					m.invalidateAllCache(false)
					m.todos = nil // clear stale todos from previous session
					m.todosDoneCleared = false
					m.restoreSession() // restore target session state (or reset to idle)
					cmds := m.postRestoreSessionSetup()
					m.panelMode = ""
					m.panelSessionItems = nil
					if len(cmds) > 0 {
						return true, m, tea.Batch(cmds...)
					}
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到会话: %s", entry.Label), feedbackInfo)
				} else {
					// Already on this session, just close panel
					m.panelMode = ""
					m.panelSessionItems = nil
					m.relayoutViewport()
				}
			case "agent":
				// Switch to agent session — same logic as normal session switch
				// Agent chatID uses interactiveKey format: "channel:chatID/roleName:instance"
				agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
				if entry.Instance != "" {
					agentChatID += ":" + entry.Instance
				}
				if agentChatID != m.chatID {
					m.saveCurrentSession() // save current session state
					m.chatID = agentChatID
					m.channelName = "agent"
					// Update workdir to match the parent session's workdir.
					workDir, _ := ParseChatID(entry.ParentID)
					if workDir != "" {
						m.workDir = workDir
						if m.channel != nil && m.channel.config.SetCWDFn != nil {
							_ = m.channel.config.SetCWDFn(entry.Channel, entry.ParentID, workDir)
						}
					}
					// Update background task session key for isolation
					if m.channel != nil {
						m.channel.bgSessionKey = "agent:" + agentChatID
						m.channel.updateBgTaskCountFn()
					}
					m.messages = nil
					m.lastTokenUsage = nil // clear stale token bar on session switch
					m.invalidateAllCache(false)
					m.todos = nil // clear stale todos from previous session
					m.todosDoneCleared = false
					m.restoreSession() // restore target session state (or reset to idle)
					cmds := m.postRestoreSessionSetup()
					m.panelMode = ""
					m.panelSessionItems = nil
					if len(cmds) > 0 {
						return true, m, tea.Batch(cmds...)
					}
					m.showSystemMsg(fmt.Sprintf("✅ 已切换到 agent 会话: %s/%s", entry.Role, entry.Instance), feedbackInfo)
				} else {
					// Already on this session, just close panel
					m.panelMode = ""
					m.panelSessionItems = nil
					m.relayoutViewport()
				}
			}
		}
		return true, m, nil

	case msg.String() == "r":
		// Refresh sessions list
		if m.sessionsListFn != nil {
			m.panelSessionItems = m.sessionsListFn()
		}
		return true, m, nil

	case msg.Code == tea.KeyHome:
		if !m.panelSessionViewing && len(m.panelSessionItems) > 0 {
			m.panelSessionCursor = 0
			m.panelScrollY = 0
		}
		return true, m, nil

	case msg.Code == tea.KeyEnd:
		if !m.panelSessionViewing && len(m.panelSessionItems) > 0 {
			m.panelSessionCursor = len(m.panelSessionItems) - 1
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	case msg.Code == tea.KeyPgUp:
		if !m.panelSessionViewing {
			visibleH := m.panelVisibleHeight()
			m.panelSessionCursor -= visibleH
			if m.panelSessionCursor < 0 {
				m.panelSessionCursor = 0
			}
			m.ensureSessionCursorVisible()
		}
		return true, m, nil

	// N: create new session in current directory
	case msg.String() == "n" || msg.String() == "N":
		if !m.panelSessionViewing {
			return true, m, m.showSessionCreateDialog()
		}

	// D: delete selected session (except default) — with confirmation
	case msg.String() == "d" || msg.String() == "D":
		if !m.panelSessionViewing && m.panelSessionCursor >= 0 && m.panelSessionCursor < len(m.panelSessionItems) {
			entry := m.panelSessionItems[m.panelSessionCursor]
			if entry.Type == "main" && entry.Label != defaultSessionName {
				m.panelSessionConfirmDelete = true
				m.panelSessionConfirmEntry = entry
			}
		}
		return true, m, nil

	// Y: confirm delete (follows D)
	case (msg.String() == "y" || msg.String() == "Y") && m.panelSessionConfirmDelete:
		m.panelSessionConfirmDelete = false
		cmd := m.deleteLocalSession(m.panelSessionConfirmEntry)
		return true, m, cmd

	// Any other key cancels delete confirmation
	case m.panelSessionConfirmDelete:
		m.panelSessionConfirmDelete = false
		return true, m, nil

	default:
		if msg.String() == "pgdown" && !m.panelSessionViewing {
			visibleH := m.panelVisibleHeight()
			m.panelSessionCursor += visibleH
			if m.panelSessionCursor >= len(m.panelSessionItems) {
				m.panelSessionCursor = len(m.panelSessionItems) - 1
			}
			m.ensureSessionCursorVisible()
			return true, m, nil
		}
	}
	return false, m, nil
}

// viewSessionsPanel renders the sessions management panel.
func (m *cliModel) viewSessionsPanel() string {
	if m.panelSessionViewing {
		return m.viewSessionsDetail()
	}
	return m.viewSessionsList()
}

// viewSessionsList renders the session list.
func (m *cliModel) viewSessionsList() string {
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render("Sessions")
	help := s.PanelDesc.Render("↑↓ Navigate  Enter Switch/View  n New  d Delete  r Refresh  Esc Close")
	total := len(m.panelSessionItems)
	scrollHint := ""
	if total > 1 {
		scrollHint = s.PanelDesc.Render(fmt.Sprintf(" [%d/%d]", m.panelSessionCursor+1, total))
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString(scrollHint)
	sb.WriteString("\n")

	// Show delete confirmation prompt
	if m.panelSessionConfirmDelete {
		sb.WriteString(s.ErrorMsg.Render(
			fmt.Sprintf("  ⚠ Delete session %q? [Y]es / [N]o", m.panelSessionConfirmEntry.Label)))
		sb.WriteString("\n")
	}

	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	if len(m.panelSessionItems) == 0 {
		sb.WriteString(s.PanelEmpty.Render("(no active sessions)"))
		return sb.String()
	}

	for i, entry := range m.panelSessionItems {
		prefix := "  "
		if i == m.panelSessionCursor {
			prefix = cursorStyle.Render("▸")
		}

		var icon, line string
		switch entry.Type {
		case "main":
			// Determine busy state with liveSessionStates override.
			mainBusy := false
			if entry.Active {
				mainBusy = m.typing
			} else {
				mainBusy = entry.Busy
				if ls, ok := m.liveSessionStates[entry.ID]; ok {
					mainBusy = ls.busy
				}
			}
			iconChar := "●"
			iconColor := lipgloss.Color("#10b981")
			if entry.Active {
				// Active: ● — user sees it, no extra mark needed.
			} else if mainBusy {
				// Non-active busy: spinner.
				iconChar = m.ticker.viewFrames(sidebarSpinnerFrames, 3)
			} else if m.unreadSessions[entry.ID] {
				// Non-active idle, but has unread results.
				iconChar = "✦"
				iconColor = lipgloss.Color("#f59e0b")
			} else {
				iconChar = "○"
			}
			icon = lipgloss.NewStyle().Foreground(iconColor).Render(iconChar)
			label := entry.Label
			if label == "" {
				label = entry.ID
			}
			labelW := contentW - 6
			if labelW < 10 {
				labelW = 10
			}
			label = truncateToWidth(label, labelW)
			line = fmt.Sprintf("%s %s  %s", prefix, icon, label)
		case "agent":
			roleColor := lipgloss.Color(RoleColor(entry.Role))
			// Use liveSessionStates for running state if available.
			agentRunning := entry.Running
			if ls, ok := m.liveSessionStates[entry.ID]; ok {
				agentRunning = ls.busy
			}
			statusIcon := "●"
			statusStyle := lipgloss.NewStyle().Foreground(roleColor)
			if !agentRunning {
				if m.unreadSessions[entry.ID] {
					statusIcon = "✦"
					statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b"))
				} else {
					statusIcon = "◦"
					statusStyle = statusStyle.Faint(true)
				}
			}
			label := fmt.Sprintf("🤖 %s/%s", entry.Role, entry.Instance)
			if entry.MessageHint != "" {
				hint := entry.MessageHint
				if runes := []rune(hint); len(runes) > 35 {
					hint = string(runes[:32]) + "..."
				}
				hint = strings.ReplaceAll(hint, "\n", " ")
				label = fmt.Sprintf("🤖 %s: %s", entry.Role, hint)
			}
			labelW := contentW - 5
			if labelW < 10 {
				labelW = 10
			}
			label = truncateToWidth(label, labelW)
			labelStyle := lipgloss.NewStyle().Foreground(roleColor)
			if !agentRunning {
				labelStyle = labelStyle.Faint(true)
			}
			line = fmt.Sprintf("%s %s  %s", prefix, statusStyle.Render(statusIcon), labelStyle.Render(label))
			if agentRunning {
				line += " ⏳"
			}
		default:
			line = fmt.Sprintf("%s   %s", prefix, entry.Label)
		}
		sb.WriteString(truncateToWidth(line, contentW))
		sb.WriteString("\n")
	}

	return sb.String()
}

// viewSessionsDetail renders the detail/log view for a selected session.
func (m *cliModel) viewSessionsDetail() string {
	s := &m.styles

	var title string
	if m.panelSessionCursor >= 0 && m.panelSessionCursor < len(m.panelSessionItems) {
		entry := m.panelSessionItems[m.panelSessionCursor]
		switch entry.Type {
		case "main":
			title = "👤 " + entry.Label
		case "agent":
			title = fmt.Sprintf("🤖 %s/%s", entry.Role, entry.Instance)
		case "group":
			title = "💬 " + entry.Label
		}
	}
	help := s.PanelDesc.Render("Esc Back")

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(title))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	for _, line := range m.panelBgLogLines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// viewDangerPanel renders the danger zone panel.
func (m *cliModel) viewDangerPanel() string {
	s := &m.styles
	var sb strings.Builder

	sb.WriteString(s.PanelHeader.Render(m.locale.DangerTitle))
	sb.WriteString("\n")

	if m.panelDangerConfirm && m.panelDangerCursor < len(m.panelDangerItems) {
		// Confirmation sub-mode
		item := m.panelDangerItems[m.panelDangerCursor]
		confirmStr := dangerConfirmStrings[item.Action]
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerConfirmClear, s.WarningSt.Render(item.Label)))
		sb.WriteString(s.PanelDesc.Render("  " + m.locale.DangerIrreversible))
		sb.WriteString("\n\n")
		fmt.Fprintf(&sb, "  %s\n", fmt.Sprintf(m.locale.DangerTypeConfirm, s.ProgressError.Render(confirmStr)))
		sb.WriteString("  ")
		sb.WriteString(m.panelDangerInput.View())
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.DangerNavHint))
	} else {
		// Item selection mode
		for i, item := range m.panelDangerItems {
			var prefix string
			statText := ""
			if item.Stat != "" {
				statText = fmt.Sprintf("  %s", s.InfoSt.Render(item.Stat))
			}
			if i == m.panelDangerCursor {
				prefix = s.PanelCursor.Render("▸")
			} else {
				prefix = "  "
			}
			line := fmt.Sprintf("%s %s%s", prefix, item.Label, statText)
			if i == m.panelDangerCursor {
				line = m.renderSelLine(line, m.panelWidth(60)-4)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.DangerNavHint))
	}

	return sb.String()
}

// sanitizeOutputLine sanitizes a single output line for safe TUI rendering.
// Delegates to the shared tools.SanitizeOutputLine which handles \r
// carriage-return overwrites (progress bars like tqdm) and ANSI escape
// sequences (color codes, cursor movement).
func sanitizeOutputLine(line string) string {
	return tools.SanitizeOutputLine(line)
}

// sanitizeOutputLines splits raw process output into sanitized display lines.
// Delegates to tools.SanitizeOutputLines for \r stripping, ANSI removal, and
// empty-line filtering.
func sanitizeOutputLines(raw string) []string {
	return tools.SanitizeOutputLines(raw)
}

// splitLines splits a string into lines, preserving trailing empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// updatePanel handles key events when a panel is active.
// Returns (handled, newModel, cmd).
func (m *cliModel) updatePanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelMode == "" {
		return false, m, nil
	}

	handled, newModel, cmd := func() (bool, tea.Model, tea.Cmd) {
		switch m.panelMode {
		case "settings":
			return m.updateSettingsPanel(msg)
		case "askuser":
			return m.updateAskUserPanel(msg)
		case "bgtasks":
			return m.updateBgTasksPanel(msg)
		case "sessions":
			return m.updateSessionsPanel(msg)
		case "danger":
			return m.updateDangerPanel(msg)
		case "runner":
			return m.updateRunnerPanel(msg)
		case "approval":
			return m.updateApprovalPanel(msg)
		case "channel":
			return m.updateChannelPanel(msg)
		}
		return false, m, nil
	}()

	// 对有 cursor 导航的 panel：cursor 超出可见区域时自动滚动
	if handled {
		switch m.panelMode {
		case "settings":
			m.ensurePanelCursorVisible()
		case "askuser":
			m.ensureAskUserVisible()
		}
	}

	return handled, newModel, cmd
}

// updateDangerPanel handles key events in the danger zone panel.
func (m *cliModel) updateDangerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelDangerConfirm {
		// Confirmation input mode
		switch msg.Code {
		case tea.KeyEsc:
			m.panelDangerConfirm = false
			m.panelDangerInput.SetValue("")
			return true, m, nil
		case tea.KeyEnter:
			if m.panelDangerOnExec == nil || m.panelDangerCursor >= len(m.panelDangerItems) {
				return true, m, nil
			}
			item := m.panelDangerItems[m.panelDangerCursor]
			confirmStr := dangerConfirmStrings[item.Action]
			if m.panelDangerInput.Value() != confirmStr {
				m.showSystemMsg(m.locale.DangerMismatch, feedbackWarning)
				return true, m, nil
			}
			// Execute the clear action
			if err := m.panelDangerOnExec(item.Action); err != nil {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerClearFailed, err), feedbackWarning)
			} else {
				m.showSystemMsg(fmt.Sprintf(m.locale.DangerCleared, item.Label), feedbackInfo)
			}
			m.closePanel()
			return true, m, nil
		default:
			var cmd tea.Cmd
			m.panelDangerInput, cmd = m.panelDangerInput.Update(msg)
			return true, m, cmd
		}
	}

	// Item selection mode
	switch {
	case msg.Code == tea.KeyEsc:
		// ESC: pop back to parent panel via navigation stack
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil

	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()

	case msg.Code == tea.KeyUp:
		if m.panelDangerCursor > 0 {
			m.panelDangerCursor--
		}

	case msg.Code == tea.KeyDown:
		if m.panelDangerCursor < len(m.panelDangerItems)-1 {
			m.panelDangerCursor++
		}

	case msg.Code == tea.KeyEnter:
		if m.panelDangerCursor < len(m.panelDangerItems) {
			m.panelDangerConfirm = true
			m.panelDangerInput.SetValue("")
			m.panelDangerInput.Focus()
			return true, m, m.panelDangerInput.Focus()
		}
	}

	return true, m, nil
}

// openDangerPanelFromSettings builds danger items with stats and opens the danger zone panel.
func (m *cliModel) openDangerPanelFromSettings() {
	stats := map[string]string{}
	if m.channel != nil && m.channel.config.GetMemoryStats != nil {
		stats = m.channel.config.GetMemoryStats()
	}

	items := []dangerItem{
		{"session", m.locale.DangerSessionHistory, stats["session"]},
		{"core_persona", "Core Memory: persona", stats["persona"]},
		{"core_human", "Core Memory: human", stats["human"]},
		{"core_working", "Core Memory: working_context", stats["working_context"]},
		{"core_all", m.locale.DangerCoreAll, ""},
		{"long_term", m.locale.DangerLongTerm, stats["long_term"]},
		{"event_history", m.locale.DangerEventHistory, stats["event_history"]},
		{"archival", m.locale.DangerArchival, stats["archival"]},
	}

	m.panelMode = "danger"
	m.panelScrollY = 0
	m.relayoutViewport()
	m.panelDangerItems = items
	m.panelDangerCursor = 0
	m.panelDangerConfirm = false
	m.panelDangerOnExec = func(targetType string) error {
		if m.channel != nil && m.channel.config.ClearMemory != nil {
			return m.channel.config.ClearMemory(targetType)
		}
		return fmt.Errorf("clear memory not configured")
	}
	// Pre-create text input for confirmation
	ti := textinput.New()
	ti.Placeholder = m.locale.DangerConfirmPlaceholder
	ti.CharLimit = 50
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	m.panelDangerInput = ti
}

func (m *cliModel) updateSettingsPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelEdit {
		// Editing mode
		switch msg.Code {
		case tea.KeyEnter:
			// Save value
			newVal := strings.TrimSpace(m.panelEditTA.Value())
			if m.panelCursor < len(m.panelSchema) {
				key := m.panelSchema[m.panelCursor].Key
				m.panelValues[key] = newVal
			}
			m.panelEdit = false
			return true, m, nil
		case tea.KeyEsc:
			m.panelEdit = false
			return true, m, nil
		default:
			// Delegate to textarea
			var cmd tea.Cmd
			m.panelEditTA, cmd = m.panelEditTA.Update(msg)
			return true, m, cmd
		}
	}

	// Combo dropdown mode
	if m.panelCombo {
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			opts := def.Options
			switch msg.Code {
			case tea.KeyEsc:
				m.panelCombo = false
				return true, m, nil
			case tea.KeyUp:
				if m.panelComboIdx > 0 {
					m.panelComboIdx--
				}
				return true, m, nil
			case tea.KeyDown:
				if m.panelComboIdx < len(opts)-1 {
					m.panelComboIdx++
				}
				return true, m, nil
			case tea.KeyEnter:
				if m.panelComboIdx < len(opts) {
					m.panelValues[def.Key] = opts[m.panelComboIdx].Value
				}
				m.panelCombo = false
				// Auto-fill llm_base_url when llm_provider changes via combo
				if def.Key == "llm_provider" && m.panelValues["llm_provider"] != m.panelPrevProvider {
					m.autoFillBaseURL(m.panelValues["llm_provider"])
					m.panelPrevProvider = m.panelValues["llm_provider"]
				}
				return true, m, nil
			case tea.KeySpace:
				m.panelCombo = false
				// Switch to edit mode for custom input
				m.panelEdit = true
				ta := m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
				var cmd tea.Cmd
				m.panelEditTA, cmd = ta.Update(msg)
				return true, m, cmd
			default:
				// Any printable key: auto-switch to edit mode for custom input
				if len(msg.Text) > 0 {
					m.panelCombo = false
					m.panelEdit = true
					ta := m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
					var cmd tea.Cmd
					m.panelEditTA, cmd = ta.Update(msg)
					return true, m, cmd
				}
			}
		}
		return true, m, nil
	}

	// Navigation mode
	switch {
	case msg.Code == tea.KeyEsc:
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil
	case msg.String() == "ctrl+s":
		// If currently editing a field, commit the edit before saving.
		if m.panelEdit && m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			if !def.ReadOnly {
				key := def.Key
				m.panelValues[key] = strings.TrimSpace(m.panelEditTA.Value())
			}
			m.panelEdit = false
		}
		// Submit all settings — async to avoid blocking the UI.
		// Close panel immediately to restore responsiveness, then run
		// the save callback in a goroutine and send back results.
		onSubmit := m.panelOnSubmit
		panelVals := m.panelValues
		m.closePanel()
		if onSubmit != nil && panelVals != nil {
			m.settingsSaving = true // block input until cliSettingsSavedMsg arrives
			return true, m, m.doSaveSettings(onSubmit, panelVals)
		}
		return true, m, nil
	case msg.Code == tea.KeyUp || msg.String() == "shift+tab":
		if m.panelCursor > 0 {
			m.panelCursor--
			m.ensureSettingsCursorVisible(0)
		}
		return true, m, nil
	case msg.Code == tea.KeyDown || msg.Code == tea.KeyTab:
		if m.panelCursor < len(m.panelSchema)-1 {
			m.panelCursor++
			m.ensureSettingsCursorVisible(0)
		}
		return true, m, nil
	case msg.Code == tea.KeyEnter:
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			// Read-only items: skip editing (display-only)
			if def.ReadOnly {
				return true, m, nil
			}
			// Runner panel entry — push settings state before opening child panel
			if def.Key == "runner_panel" {
				m.pushPanel()
				m.openRunnerPanel()
				return true, m, nil
			}
			// Danger zone entry — push settings state before opening child panel
			if def.Key == "danger_zone" {
				m.pushPanel()
				m.openDangerPanelFromSettings()
				return true, m, nil
			}
			// Subscription management entry — save panel state, open quick switch
			if def.Key == "subscription_manage" {
				// Backup current panel state so we can restore after quick switch
				m.panelValuesBackup = make(map[string]string, len(m.panelValues))
				for k, v := range m.panelValues {
					m.panelValuesBackup[k] = v
				}
				m.panelCursorBackup = m.panelCursor
				m.panelOnSubmitBackup = m.panelOnSubmit
				m.panelMode = ""
				m.relayoutViewport()
				m.quickSwitchReturnToPanel = true
				m.openQuickSwitch("subscription")
				return true, m, nil
			}
			switch def.Type {
			case SettingTypeToggle:
				// Toggle on Enter
				cur := m.panelValues[def.Key]
				if cur == "true" {
					m.panelValues[def.Key] = "false"
				} else {
					m.panelValues[def.Key] = "true"
				}
				return true, m, nil
			case SettingTypeSelect:
				// Cycle through options
				cur := m.panelValues[def.Key]
				if cur == "" && def.DefaultValue != "" {
					cur = def.DefaultValue
				}
				idx := slices.IndexFunc(def.Options, func(opt SettingOption) bool {
					return opt.Value == cur
				})
				if idx >= 0 && idx < len(def.Options)-1 {
					m.panelValues[def.Key] = def.Options[idx+1].Value
				} else if len(def.Options) > 0 {
					m.panelValues[def.Key] = def.Options[0].Value
				}
				return true, m, nil
			case SettingTypeCombo:
				// Open combo dropdown if options available, otherwise edit
				if len(def.Options) > 0 {
					m.panelCombo = true
					m.panelComboIdx = 0
					// Pre-select current value if it matches an option
					cur := m.panelValues[def.Key]
					for i, opt := range def.Options {
						if opt.Value == cur {
							m.panelComboIdx = i
							break
						}
					}
					return true, m, nil
				}
				// No options: fall through to default edit mode
				fallthrough
			default:
				// Enter edit mode for text/number/textarea/combo(fallback)
				m.panelEdit = true
				m.panelEditTA = m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
				return true, m, nil
			}
		}
		return true, m, nil
	}
	return true, m, nil
}

func (m *cliModel) updateAskUserPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return true, m, nil
	}

	// Panel-internal scroll for long content.
	// Two separate scroll targets:
	//   Shift+↑/↓ — scroll the conversation viewport (history above)
	//   Ctrl+↑/↓  — scroll the ask panel content (question/options)
	//   PgUp/PgDn — scroll the ask panel content (page at a time)
	switch {
	case msg.String() == "ctrl+o":
		// §11 Ctrl+O toggles tool summary expand/collapse — must work in askuser mode too
		m.toggleToolSummary()
		return true, m, nil
	case msg.Code == tea.KeyHome:
		// Home/End jump to top/bottom of viewport (iteration history above the panel)
		m.viewport.GotoTop()
		return true, m, nil
	case msg.Code == tea.KeyEnd:
		m.viewport.GotoBottom()
		m.newContentHint = false
		m.userScrolledUp = false
		return true, m, nil
	case msg.String() == "shift+up":
		m.viewport.ScrollUp(1)
		return true, m, nil
	case msg.String() == "shift+down":
		m.viewport.ScrollDown(1)
		return true, m, nil
	case msg.String() == "ctrl+up":
		m.askPanelScrollY -= 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
		return true, m, nil
	case msg.String() == "ctrl+down":
		m.askPanelScrollY += 1
		// clamp happens in View via clampAskUserPanelScroll
		return true, m, nil
	case msg.String() == "pgup":
		m.askPanelScrollY -= 5
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
		return true, m, nil
	case msg.String() == "pgdown":
		m.askPanelScrollY += 5
		// clamp happens in View via clampAskUserPanelScroll
		return true, m, nil
	}

	item := &m.panelItems[m.panelTab]
	numOpts := len(item.Options)
	hasOpts := numOpts > 0
	isLastTab := m.panelTab == len(m.panelItems)-1
	// Cursor: 0..numOpts-1 (checkbox), numOpts (Other input), numOpts+1 (Submit, last tab only)
	cursor := m.panelOptCursor[m.panelTab]
	onOther := hasOpts && cursor == numOpts
	onSubmit := hasOpts && isLastTab && cursor == numOpts+1

	switch {
	case msg.String() == "ctrl+s":
		return m.submitAskAnswers()
	case msg.Code == tea.KeyEsc:
		if m.panelOnCancel != nil {
			m.panelOnCancel()
		}
		m.closePanel()
		return true, m, nil
	case msg.Code == tea.KeyRight || msg.Code == tea.KeyTab:
		if len(m.panelItems) > 1 && m.panelTab < len(m.panelItems)-1 {
			m.saveCurrentFreeInput()
			m.panelTab++
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.String() == "shift+tab" || msg.Code == tea.KeyLeft:
		if len(m.panelItems) > 1 && m.panelTab > 0 {
			m.saveCurrentFreeInput()
			m.panelTab--
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.Code == tea.KeyUp:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts - 1
				m.ensureAskUserCursorVisible()
				return true, m, nil
			}
			if onSubmit {
				m.panelOptCursor[m.panelTab] = numOpts
				m.ensureAskUserCursorVisible()
				return true, m, nil
			}
			if cursor > 0 {
				m.panelOptCursor[m.panelTab] = cursor - 1
				// Auto-scroll panel up when cursor moves above visible area
				m.ensureAskUserCursorVisible()
			} else if cursor == 0 && m.askPanelScrollY > 0 {
				// At top option and panel is scrolled — scroll content up
				m.askPanelScrollY -= 1
				if m.askPanelScrollY < 0 {
					m.askPanelScrollY = 0
				}
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyDown:
		if hasOpts {
			maxCursor := numOpts // Other input is the last item
			if isLastTab {
				maxCursor = numOpts + 1 // Submit button only on last tab
			}
			if onOther {
				if isLastTab {
					m.panelOptCursor[m.panelTab] = numOpts + 1
					m.ensureAskUserCursorVisible()
				}
				return true, m, nil
			}
			if cursor < maxCursor {
				m.panelOptCursor[m.panelTab] = cursor + 1
				// Auto-scroll panel down when cursor moves below visible area
				m.ensureAskUserCursorVisible()
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyEnter:
		if hasOpts {
			if onSubmit {
				return m.submitAskAnswers()
			}
			// On checkbox: toggle; on Other: do nothing (let user type)
			if !onOther {
				m.toggleOptAtCursor()
			}
			return true, m, nil
		}
		// No options (textarea): submit only on last tab, otherwise advance
		if isLastTab {
			return m.submitAskAnswers()
		}
		m.saveCurrentFreeInput()
		m.panelTab++
		m.restoreFreeInput()
		return true, m, nil
	case msg.Code == tea.KeySpace:
		if hasOpts && !onOther {
			if cursor < numOpts {
				m.toggleOptAtCursor()
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		if onOther {
			// Other 输入框：空格传给 textinput
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		// No options: fall through to textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case len(msg.Text) > 0:
		if hasOpts && !onOther {
			m.panelOptCursor[m.panelTab] = numOpts
			m.restoreOtherInput()
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			// With options, all input goes through Other textinput
			return true, m, nil
		}
		// No options: textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	default:
		if isCtrlJ(msg) {
			if !hasOpts {
				m.panelAnswerTA.InsertString("\n")
				m.autoExpandAskTA()
			}
			return true, m, nil
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	}

}

// toggleOptAtCursor toggles the checkbox at the current cursor position.
func (m *cliModel) toggleOptAtCursor() {
	tab := m.panelTab
	if m.panelOptSel[tab] == nil {
		m.panelOptSel[tab] = make(map[int]bool)
	}
	cursor := m.panelOptCursor[tab]
	m.panelOptSel[tab][cursor] = !m.panelOptSel[tab][cursor]
}

// collectAskAnswers gathers answers from all questions.
func (m *cliModel) collectAskAnswers() map[string]string {
	answers := make(map[string]string)
	for i, item := range m.panelItems {
		key := fmt.Sprintf("q%d", i)
		hasOpts := len(item.Options) > 0
		var parts []string
		if hasOpts {
			if sel, ok := m.panelOptSel[i]; ok && len(sel) > 0 {
				// Iterate by index order (maps are unordered in Go)
				for idx := 0; idx < len(item.Options); idx++ {
					if sel[idx] {
						parts = append(parts, item.Options[idx])
					}
				}
			}
			var otherText string
			if i == m.panelTab {
				otherText = strings.TrimSpace(m.panelOtherTI.Value())
			} else {
				otherText = strings.TrimSpace(item.Other)
			}
			if otherText != "" {
				parts = append(parts, otherText)
			}
			answers[key] = strings.Join(parts, ", ")
		} else {
			if i == m.panelTab {
				answers[key] = strings.TrimSpace(m.panelAnswerTA.Value())
			} else {
				answers[key] = strings.TrimSpace(item.Other)
			}
		}
	}
	return answers
}

// saveCurrentFreeInput saves textarea/textinput content for the current tab.
func (m *cliModel) saveCurrentFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		item.Other = m.panelOtherTI.Value()
	} else {
		item.Other = m.panelAnswerTA.Value()
	}
}

// restoreFreeInput restores textarea/textinput content for the current tab.
func (m *cliModel) restoreFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		m.panelOtherTI.SetValue(item.Other)
		m.panelOtherTI.CursorEnd()
		m.panelOtherTI.Focus()
	} else {
		m.panelAnswerTA.SetValue(item.Other)
		m.panelAnswerTA.CursorEnd()
		m.panelAnswerTA.Focus()
		m.autoExpandAskTA()
	}
}

// restoreOtherInput restores the Other textinput for the current tab (options mode).
func (m *cliModel) restoreOtherInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	m.panelOtherTI.SetValue(m.panelItems[m.panelTab].Other)
	m.panelOtherTI.CursorEnd()
}

// autoExpandAskTA dynamically grows the textarea height based on content.
func (m *cliModel) autoExpandAskTA() {
	lines := strings.Count(m.panelAnswerTA.Value(), "\n") + 1
	if lines < 2 {
		lines = 2
	}
	if lines > 6 {
		lines = 6
	}
	if m.panelAnswerTA.Height() != lines {
		m.panelAnswerTA.SetHeight(lines)
	}
}

// ensureAskUserCursorVisible adjusts askPanelScrollY so the current option
// cursor stays within the visible panel area. This provides automatic
// edge-scrolling when navigating options with ↑/↓ keys.
func (m *cliModel) ensureAskUserCursorVisible() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) == 0 {
		return
	}
	cursor := m.panelOptCursor[m.panelTab]
	// Calculate exact line offset of the cursor by counting actual header lines.
	// Tab bar: 2 lines (tabs + blank) if multiple questions, 0 otherwise.
	headerLines := 0
	if len(m.panelItems) > 1 {
		headerLines = 2 // tab bar + blank line
	}
	// Question: may be multiple lines after hardWrap.
	qWrapWidth := m.width - 6
	if qWrapWidth < 20 {
		qWrapWidth = 20
	}
	wrapped := hardWrapRunes("❓ "+item.Question, qWrapWidth)
	headerLines += strings.Count(wrapped, "\n") + 1 // question lines
	headerLines++                                   // blank line between question and options

	cursorLine := headerLines + cursor
	// Visible height — use askUserPanelVisibleHeight for the askuser split layout.
	askVisibleH := m.askUserPanelVisibleHeight()
	if askVisibleH <= 0 {
		return
	}
	// Scroll up if cursor is above visible area
	if cursorLine < m.askPanelScrollY+1 {
		m.askPanelScrollY = cursorLine - 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
	}
	// Scroll down if cursor is below visible area
	if cursorLine > m.askPanelScrollY+askVisibleH-1 {
		m.askPanelScrollY = cursorLine - askVisibleH + 1
		if m.askPanelScrollY < 0 {
			m.askPanelScrollY = 0
		}
	}
}

// viewPanel renders the active panel as a string.
func (m *cliModel) viewPanel() string {
	var raw string
	switch m.panelMode {
	case "settings":
		raw = m.viewSettingsPanel()
	case "askuser":
		raw = m.viewAskUserPanel()
	case "bgtasks":
		raw = m.viewBgTasksPanel()
	case "sessions":
		raw = m.viewSessionsPanel()
	case "danger":
		raw = m.viewDangerPanel()
	case "runner":
		raw = m.viewRunnerPanel()
	case "approval":
		raw = m.viewApprovalPanel()
	case "channel":
		raw = m.viewChannelPanel()
	default:
		return ""
	}
	return raw
}

func (m *cliModel) viewSettingsPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	valueStyle := s.InfoSt
	cursorStyle := s.PanelCursor
	descStyle := s.PanelDesc
	hintStyle := s.PanelHint

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(m.locale.PanelSettingsTitle))
	sb.WriteString("\n")
	// 表头下方精致分割线，区分标题与内容
	sb.WriteString(s.SettingsDivider.Render("┈" + strings.Repeat("┈", 30)))
	sb.WriteString("\n")

	// Group by category
	lastCat := ""
	ln := 0 // 当前渲染行号
	for i, def := range m.panelSchema {
		if def.Category != lastCat {
			lastCat = def.Category
			sb.WriteString("\n")
			sb.WriteString(s.SettingsCat.Render("▸ " + lastCat))
			sb.WriteString("\n")
			ln += 2
		}

		cur := m.panelValues[def.Key]
		// If value is empty, fall back to DefaultValue for display
		if cur == "" && def.DefaultValue != "" {
			cur = def.DefaultValue
		}
		var prefix string
		if i == m.panelCursor && !m.panelEdit {
			prefix = cursorStyle.Render("▸")
		} else {
			prefix = "  "
		}

		// Read-only items: show lock icon, dim label, skip value interaction
		labelSt := lipgloss.NewStyle()
		valueSt := valueStyle
		if def.ReadOnly {
			prefix = s.PanelDesc.Render("🔒")
			labelSt = s.PanelDesc
			valueSt = s.PanelDesc
		}

		// Runner panel entry: render with connection status
		if def.Key == "runner_panel" {
			statusHint := ""
			if m.runnerBridge != nil {
				switch m.runnerBridge.Status() {
				case RunnerConnected:
					statusHint = " " + s.ProgressDone.Render("● "+m.locale.RunnerStatusConnected)
				case RunnerConnecting:
					statusHint = " " + s.ProgressRunning.Render("● "+m.locale.RunnerConnecting)
				}
			}
			line := fmt.Sprintf("%s %s%s", prefix, s.ProgressDone.Render(def.Label), statusHint)
			if i == m.panelCursor && !m.panelEdit {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// Danger zone entry: render with warning style
		if def.Key == "danger_zone" {
			line := fmt.Sprintf("%s %s", prefix, s.WarningSt.Render(def.Label))
			if i == m.panelCursor && !m.panelEdit {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// Subscription management entry: show count + CURRENT session's subscription
		if def.Key == "subscription_manage" {
			subHint := ""
			if m.subscriptionMgr != nil {
				if subs, err := m.subscriptionMgr.List(""); err == nil && len(subs) > 0 {
					var activeName string
					// Use m.activeSubID (per-session) to find the current subscription,
					// NOT sub.Active (global default). The settings panel must reflect
					// the session's active subscription, not the global default.
					if m.activeSubID != "" {
						for _, sub := range subs {
							if sub.ID == m.activeSubID {
								activeName = sub.Name
								break
							}
						}
					}
					// Fallback: if no per-session subscription is set, use the global default.
					if activeName == "" {
						for _, sub := range subs {
							if sub.Active {
								activeName = sub.Name
								break
							}
						}
					}
					if activeName != "" {
						subHint = " " + s.ProgressDone.Render("● "+activeName)
					}
					subHint += descStyle.Render(fmt.Sprintf(" (%d)", len(subs)))
				}
			}
			line := fmt.Sprintf("%s %s%s", prefix, s.ProgressDone.Render(def.Label), subHint)
			if i == m.panelCursor && !m.panelEdit {
				line = m.renderSelLine(line, m.width-6)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			ln++
			continue
		}

		// Format value display
		var displayVal string
		switch def.Type {
		case SettingTypeToggle:
			if cur == "true" {
				displayVal = valueSt.Render(m.locale.PanelToggleOn)
			} else {
				displayVal = valueSt.Render(m.locale.PanelToggleOff)
			}
		case SettingTypeSelect:
			// Find label for current value
			displayVal = cur
			for _, opt := range def.Options {
				if opt.Value == cur {
					displayVal = valueSt.Render(opt.Label)
					break
				}
			}
		case SettingTypeCombo:
			// Show current value with dropdown hint
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render(cur)
			}
			if !def.ReadOnly && len(def.Options) > 0 {
				displayVal += descStyle.Render(" ▾")
			}
		case SettingTypePassword:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render("••••••")
			}
		default:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueSt.Render(cur)
			}
		}

		line := fmt.Sprintf("%s %s: %s", prefix, labelSt.Render(def.Label), displayVal)
		if i == m.panelCursor && !m.panelEdit && !m.panelCombo {
			line = m.renderSelLine(line, m.width-6)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
		ln++

		// Show field description when cursor is on this field (not in edit/combo mode).
		// The description may contain OSC 8 hyperlinks for clickable URLs.
		if i == m.panelCursor && !m.panelEdit && !m.panelCombo && def.Description != "" {
			descLines := strings.Split(def.Description, "\n")
			for _, dl := range descLines {
				sb.WriteString(descStyle.Render("    " + dl))
				sb.WriteString("\n")
				ln++
			}
		}

		// ── Inline edit/combo overlay (Crush-style: render right below the item) ──
		if i == m.panelCursor {
			if m.panelEdit {
				sb.WriteString("  ")
				sb.WriteString(cursorStyle.Render("✎ "))
				sb.WriteString(m.panelEditTA.View())
				sb.WriteString("\n")
				sb.WriteString(descStyle.Render("    " + m.locale.PanelEditHint))
				sb.WriteString("\n")
				ln += 3
			} else if m.panelCombo {
				maxShow := 8
				start := 0
				if m.panelComboIdx >= maxShow {
					start = m.panelComboIdx - maxShow + 1
				}
				end := start + maxShow
				if end > len(def.Options) {
					end = len(def.Options)
				}
				for j := start; j < end; j++ {
					opt := def.Options[j]
					label := opt.Label
					runes := []rune(label)
					if len(runes) > 40 {
						label = string(runes[:37]) + "..."
					}
					if j == m.panelComboIdx {
						sb.WriteString(cursorStyle.Render("    ▸ " + label))
					} else {
						sb.WriteString("      " + label)
					}
					// Show option description on the selected combo item.
					if j == m.panelComboIdx && opt.Description != "" {
						sb.WriteString("\n")
						sb.WriteString(descStyle.Render("        " + opt.Description))
					}
					sb.WriteString("\n")
					ln++
					if j == m.panelComboIdx && opt.Description != "" {
						ln++
					}
				}
				sb.WriteString(descStyle.Render("    " + m.locale.PanelComboHint))
				sb.WriteString("\n")
				ln++
			}
		}
	}

	// Bottom hint when no overlay is active
	if !m.panelEdit && !m.panelCombo {
		sb.WriteString("\n")
		sb.WriteString(hintStyle.Render("  " + m.locale.PanelNavHint))
	}

	return sb.String()
}

func (m *cliModel) viewAskUserPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	questionStyle := s.WarningSt.Bold(true)
	activeTabStyle := s.PanelHeader
	inactiveTabStyle := s.PanelDesc
	checkStyle := s.ToolItem
	cursorStyle := s.PanelCursor
	submitStyle := s.TodoDone

	var sb strings.Builder

	// Tab bar (if multiple questions)
	if len(m.panelItems) > 1 {
		for i := range m.panelItems {
			label := fmt.Sprintf(" %d ", i+1)
			if i == m.panelTab {
				sb.WriteString(activeTabStyle.Render(label))
			} else {
				sb.WriteString(inactiveTabStyle.Render(label))
			}
			if i < len(m.panelItems)-1 {
				sb.WriteString(inactiveTabStyle.Render("│"))
			}
		}
		sb.WriteString("\n\n")
	}

	// Current question
	if m.panelTab >= 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		isLastTab := m.panelTab == len(m.panelItems)-1
		// Wrap question text to fit inside PanelBox (border 2 + padding 2 + scrollbar reserve 2)
		qWrapWidth := m.width - 6
		if qWrapWidth < 20 {
			qWrapWidth = 20
		}
		wrapped := hardWrapRunes("❓ "+item.Question, qWrapWidth)
		sb.WriteString(questionStyle.Render(wrapped))
		sb.WriteString("\n")

		hasOpts := len(item.Options) > 0

		if hasOpts {
			sb.WriteString("\n")
			sel := m.panelOptSel[m.panelTab]
			cursor := m.panelOptCursor[m.panelTab]
			numOpts := len(item.Options)

			for i, opt := range item.Options {
				checked := sel != nil && sel[i]
				var box string
				if checked {
					box = "☑"
				} else {
					box = "☐"
				}
				var line string
				if i == cursor {
					prefix := cursorStyle.Render("▸ ")
					if checked {
						line = checkStyle.Render(prefix + box + " " + opt)
					} else {
						line = prefix + box + " " + opt
					}
				} else {
					if checked {
						line = checkStyle.Render("  " + box + " " + opt)
					} else {
						line = "  " + box + " " + opt
					}
				}
				sb.WriteString(line)
				sb.WriteString("\n")
			}

			// Other input (single-line)
			otherLabel := m.locale.PanelOther
			var prefix string
			if cursor == numOpts {
				prefix = cursorStyle.Render("▸ ")
			} else {
				prefix = "  "
			}
			sb.WriteString(prefix + otherLabel)
			// Resize textinput to fit within panel content width (qWrapWidth)
			// minus label width and scrollbar column.  The textinput View()
			// (specifically placeholderView) always outputs Width()+1 chars
			// (cursor+placeholder+padding), so we need -2 instead of -1.
			tiWidth := qWrapWidth - lipgloss.Width(prefix+otherLabel) - 2
			if tiWidth < 10 {
				tiWidth = 10
			}
			m.panelOtherTI.SetWidth(tiWidth)
			// Strip NUL bytes from textinput View(). When the input is empty,
			// placeholderView() copies the placeholder string into a rune slice
			// sized to Width()+1 and renders the unwritten slots as \x00.
			// lipgloss.Width() counts these as 0-width, but lipgloss.Render()
			// (used by PanelBox) treats them as 1-column during word-wrap,
			// causing the scrollbar "▐" to wrap to the next line.
			tiView := strings.Map(func(r rune) rune {
				if r == 0 {
					return -1
				}
				return r
			}, m.panelOtherTI.View())
			sb.WriteString(tiView)
			sb.WriteString("\n")

			// Submit button (only on last tab)
			if isLastTab {
				submitLabel := m.locale.PanelSubmit
				if cursor == numOpts+1 {
					sb.WriteString(cursorStyle.Render("▸ ") + submitStyle.Render(submitLabel))
				} else {
					sb.WriteString("  " + submitStyle.Render(submitLabel))
				}
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString("\n")
			sb.WriteString(m.panelAnswerTA.View())
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// --- SettingsCapability implementation for CLIChannel ---

// SettingsSchema returns the settings definitions for CLI channel.
func (c *CLIChannel) SettingsSchema() []SettingDefinition {
	loc := GetLocale(currentLocaleLang)
	return loc.SettingsSchema
}

// HandleSettingSubmit processes a setting value submission from the CLI channel.
func (c *CLIChannel) HandleSettingSubmit(ctx context.Context, rawInput string) (map[string]string, error) {
	// CLI uses interactive panel, this is for programmatic access
	return nil, fmt.Errorf("CLI uses interactive settings panel")
}

// SetSettingsService injects the settings service for the interactive panel.
func (c *CLIChannel) SetSettingsService(svc SettingsService) {
	c.settingsSvc = svc
}

// SetModelLister injects the model lister for combo settings.
func (c *CLIChannel) SetModelLister(lister ModelLister) {
	c.modelLister = lister
}

// SetSubscriptionManager sets the subscription manager for multi-subscription support.
func (c *CLIChannel) SetSubscriptionManager(mgr SubscriptionManager) {
	c.subscriptionMgr = mgr
	if c.model != nil {
		c.model.SetSubscriptionMgr(mgr)
	}
}

// SetLLMSubscriber sets the LLM subscriber for quick switch actions.
// Stores on channel for propagation to model in Start().
func (c *CLIChannel) SetLLMSubscriber(sub LLMSubscriber) {
	c.llmSubscriber = sub
	if c.model != nil {
		c.model.SetLLMSubscriber(sub)
	}
}

// ---------------------------------------------------------------------------
// §15 Quick Switch: Subscription / Model picker overlay
// ---------------------------------------------------------------------------

// openQuickSwitch opens the quick switch overlay for subscription or model selection.
func (m *cliModel) openQuickSwitch(mode string) {
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) == 0 {
		// Even with no subscriptions, allow adding one
		subs = nil
	}

	m.quickSwitchMode = mode
	m.quickSwitchList = subs
	m.quickSwitchCursor = 0

	// Append "Add subscription" entry for subscription mode
	if mode == "subscription" {
		m.quickSwitchList = append(m.quickSwitchList, Subscription{
			ID:   "__add__",
			Name: "➕ Add subscription",
		})
	}

	// Pre-select the active subscription (per-session, not DB default)
	if m.activeSubID != "" {
		for i, s := range subs {
			if s.ID == m.activeSubID {
				m.quickSwitchCursor = i
				break
			}
		}
	} else {
		for i, s := range subs {
			if s.Active {
				m.quickSwitchCursor = i
				break
			}
		}
	}
}

// applyQuickSwitch applies the selected item from the quick switch overlay.
// For subscription switches, the LLM creation (which may hit the network) runs
// asynchronously so the UI never freezes.
func (m *cliModel) applyQuickSwitch() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		m.quickSwitchMode = ""
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]

	// "Add subscription" entry — open a mini settings panel
	if selected.ID == "__add__" {
		m.quickSwitchMode = ""
		addSchema := []SettingDefinition{
			{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: SettingTypeText, DefaultValue: ""},
			{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: SettingTypeText, DefaultValue: "openai"},
			{Key: "sub_model", Label: "Model", Description: "Model name", Type: SettingTypeText, DefaultValue: ""},
			{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: SettingTypeText, DefaultValue: ""},
			{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: SettingTypePassword, DefaultValue: ""},
			{Key: "sub_max_output_tokens", Label: "Max Output Tokens", Description: fmt.Sprintf("Default max output tokens (0 = use %d)", config.DefaultMaxOutputTokens), Type: SettingTypeNumber, DefaultValue: "0"},
			{Key: "sub_thinking_mode", Label: "Thinking Mode", Description: "Thinking/reasoning mode", Type: SettingTypeSelect, DefaultValue: "", Options: []SettingOption{
				{Label: "Auto (default)", Value: ""},
				{Label: "Enabled", Value: "enabled"},
				{Label: "Disabled", Value: "disabled"},
			}},
		}
		// Inject model list into combo for model field
		if m.channel.modelLister != nil {
			models := m.channel.modelLister.ListModels()
			if len(models) > 0 {
				opts := make([]SettingOption, len(models))
				for j, mdl := range models {
					opts[j] = SettingOption{Label: mdl, Value: mdl}
				}
				addSchema[2].Options = opts
			}
		}
		m.openSettingsPanel(addSchema, map[string]string{}, func(values map[string]string) {
			name := values["sub_name"]
			if name == "" {
				name = values["sub_provider"]
			}
			if name == "" {
				name = "unnamed"
			}
			maxOut, _ := strconv.Atoi(values["sub_max_output_tokens"])
			sub := &Subscription{
				ID:              fmt.Sprintf("sub_%d", time.Now().UnixNano()),
				Name:            name,
				Provider:        values["sub_provider"],
				BaseURL:         values["sub_base_url"],
				APIKey:          values["sub_api_key"],
				Model:           values["sub_model"],
				MaxOutputTokens: maxOut,
				ThinkingMode:    values["sub_thinking_mode"],
				Active:          false,
			}
			if err := m.subscriptionMgr.Add(sub); err != nil {
				m.showTempStatus(fmt.Sprintf("Failed to add subscription: %v", err))
			} else {
				m.showTempStatus(fmt.Sprintf("Added subscription: %s (%s)", sub.Name, sub.Model))
			}
		})
		return
	}

	switch m.quickSwitchMode {
	case "subscription":
		if m.subscriptionMgr == nil {
			break
		}
		// Find the full subscription config first
		var target *Subscription
		if subs, err := m.subscriptionMgr.List(""); err == nil {
			for i := range subs {
				if subs[i].ID == selected.ID {
					target = &subs[i]
					break
				}
			}
		}
		if target == nil {
			m.showTempStatus("Subscription not found")
			break
		}
		if m.channel == nil || m.channel.config.SwitchLLM == nil {
			break
		}
		// ── IMMEDIATE frontend state update ──────────────────────
		// Must happen synchronously before the async SwitchLLM call.
		// Without this, activeSubID and cachedModelName stay stale between
		// Enter press and handleSwitchLLMDoneMsg processing. The settings
		// panel and context bar read from these fields, so they would show
		// the OLD subscription until the async callback completes.
		m.activeSubID = selected.ID
		m.cachedModelName = selected.Model
		m.subGeneration++ // subscription actually changed
		// Persist immediately so refreshCachedModelName (called on settings save)
		// loads the correct state from disk instead of stale old data.
		state := SessionLLMState{
			SubscriptionID:   selected.ID,
			Model:            selected.Model,
			MaxContextTokens: resolveSubMaxContext(target),
			MaxOutputTokens:  resolveSubMaxOutputTokens(target),
		}
		SaveSessionLLMState(m.workDir, m.chatID, state)
		m.applySessionLLMState(state)
		// ── Async backend switch ─────────────────────────────────
		m.showTempStatus(fmt.Sprintf("Switching to: %s …", selected.Name))
		switchFn := m.channel.config.SwitchLLM
		subID := selected.ID
		subName := selected.Name
		subModel := selected.Model
		mgr := m.subscriptionMgr
		m.pendingCmds = append(m.pendingCmds, func() tea.Msg {
			err := switchFn(target.Provider, target.BaseURL, target.APIKey, target.Model)
			return cliSwitchLLMDoneMsg{
				err:       err,
				subID:     subID,
				subName:   subName,
				subModel:  subModel,
				maxCtx:    resolveSubMaxContext(target),
				maxOutTok: resolveSubMaxOutputTokens(target),
				mgr:       mgr,
			}
		})
	case "model":
		if m.llmSubscriber != nil {
			m.llmSubscriber.SwitchModel(m.senderID, selected.Model, m.chatID)
			m.cachedModelName = selected.Model
			m.subGeneration++ // model switch also changes effective subscription state
			// Update quickSwitchList so the panel reflects the new model
			m.updateQuickSwitchModels(selected.Model)
			// Persist per-session model choice so it survives restarts
			SaveSessionLLMState(m.workDir, m.chatID, SessionLLMState{
				SubscriptionID:   m.activeSubID,
				Model:            selected.Model,
				MaxContextTokens: m.cachedMaxContextTokens,
				MaxOutputTokens:  int(m.cachedMaxOutputTokens),
			})
			m.showTempStatus(fmt.Sprintf("Model switched to: %s", selected.Model))
		}
	}

	m.quickSwitchMode = ""
}

// editQuickSwitchEntry opens a mini panel to edit all fields of the selected subscription.
func (m *cliModel) editQuickSwitchEntry() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]
	if selected.ID == "__add__" {
		return
	}
	// Find the full subscription config (including APIKey) from the manager
	var target *Subscription
	if m.subscriptionMgr != nil {
		if subs, err := m.subscriptionMgr.List(""); err == nil {
			for i := range subs {
				if subs[i].ID == selected.ID {
					target = &subs[i]
					break
				}
			}
		}
	}
	if target == nil {
		m.showTempStatus("Subscription not found")
		return
	}

	editSchema := []SettingDefinition{
		{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: SettingTypeText, DefaultValue: target.Name},
		{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: SettingTypeText, DefaultValue: target.Provider},
		{Key: "sub_model", Label: "Model", Description: "Model name", Type: SettingTypeCombo, DefaultValue: target.Model},
		{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: SettingTypeText, DefaultValue: target.BaseURL},
		{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: SettingTypePassword, DefaultValue: target.APIKey},
		{Key: "sub_max_output_tokens", Label: "Max Output Tokens", Description: fmt.Sprintf("Default max output tokens (0 = use %d)", config.DefaultMaxOutputTokens), Type: SettingTypeNumber, DefaultValue: strconv.Itoa(target.MaxOutputTokens)},
		{Key: "sub_thinking_mode", Label: "Thinking Mode", Description: "Thinking/reasoning mode", Type: SettingTypeSelect, DefaultValue: target.ThinkingMode, Options: []SettingOption{
			{Label: "Auto (default)", Value: ""},
			{Label: "Enabled", Value: "enabled"},
			{Label: "Disabled", Value: "disabled"},
		}},
		{Key: "__pm_header__", Label: "─── Model-Specific Overrides ───", Description: "Override max tokens and context per model. Set 0 to use subscription default.", Type: SettingTypeText, DefaultValue: ""},
	}
	// Build per-model override rows: only models that belong to THIS subscription.
	// Use target.Model + keys from existing PerModelConfigs (not ListAllModels which
	// returns models from ALL subscriptions).
	subModels := make(map[string]bool)
	if target.Model != "" {
		subModels[target.Model] = true
	}
	for mdl := range target.PerModelConfigs {
		subModels[mdl] = true
	}
	for mdl := range subModels {
		pmOut := 0
		pmCtx := 0
		if target.PerModelConfigs != nil {
			if cfg, ok := target.PerModelConfigs[mdl]; ok {
				pmOut = cfg.MaxOutputTokens
				pmCtx = cfg.MaxContext
			}
		}
		editSchema = append(editSchema, SettingDefinition{
			Key: "pm_" + mdl + "_max_output", Label: mdl + " Max Tokens",
			Description: "Max output tokens for " + mdl + " (0 = use default)",
			Type:        SettingTypeNumber, DefaultValue: strconv.Itoa(pmOut),
		})
		editSchema = append(editSchema, SettingDefinition{
			Key: "pm_" + mdl + "_max_context", Label: mdl + " Max Context",
			Description: "Max context tokens for " + mdl + " (0 = use default)",
			Type:        SettingTypeNumber, DefaultValue: strconv.Itoa(pmCtx),
		})
	}
	editValues := map[string]string{
		"sub_name":              target.Name,
		"sub_provider":          target.Provider,
		"sub_model":             target.Model,
		"sub_base_url":          target.BaseURL,
		"sub_api_key":           target.APIKey,
		"sub_max_output_tokens": strconv.Itoa(target.MaxOutputTokens),
		"sub_thinking_mode":     target.ThinkingMode,
	}
	m.quickSwitchMode = "" // close overlay while editing
	m.openSettingsPanel(editSchema, editValues, func(values map[string]string) {
		if m.subscriptionMgr == nil {
			return
		}
		apiKey := values["sub_api_key"]
		// Never write back a masked API key — it would destroy the real key in storage.
		if isMaskedAPIKey(apiKey) {
			apiKey = target.APIKey
		}
		maxOut, _ := strconv.Atoi(values["sub_max_output_tokens"])
		// Collect per-model overrides: only models belonging to THIS subscription
		perModelConfigs := make(map[string]PerModelConfig)
		for mdl := range target.PerModelConfigs {
			pmOut, _ := strconv.Atoi(values["pm_"+mdl+"_max_output"])
			pmCtx, _ := strconv.Atoi(values["pm_"+mdl+"_max_context"])
			if pmOut > 0 || pmCtx > 0 {
				perModelConfigs[mdl] = PerModelConfig{MaxOutputTokens: pmOut, MaxContext: pmCtx}
			}
		}
		// Also check the current model (may have been newly added)
		if modelFromCombo := values["sub_model"]; modelFromCombo != "" {
			pmOut, _ := strconv.Atoi(values["pm_"+modelFromCombo+"_max_output"])
			pmCtx, _ := strconv.Atoi(values["pm_"+modelFromCombo+"_max_context"])
			if pmOut > 0 || pmCtx > 0 {
				perModelConfigs[modelFromCombo] = PerModelConfig{MaxOutputTokens: pmOut, MaxContext: pmCtx}
			}
		}
		updated := &Subscription{
			ID:              target.ID,
			Name:            values["sub_name"],
			Provider:        values["sub_provider"],
			Model:           values["sub_model"],
			BaseURL:         values["sub_base_url"],
			APIKey:          apiKey,
			MaxOutputTokens: maxOut,
			ThinkingMode:    values["sub_thinking_mode"],
			PerModelConfigs: perModelConfigs,
			Active:          target.Active,
		}
		if err := m.subscriptionMgr.Update(target.ID, updated); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to update: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Updated: %s", updated.Name))
		}
	})
}

// deleteQuickSwitchEntry deletes the selected subscription (with confirmation if it's active).
func (m *cliModel) deleteQuickSwitchEntry() {
	if m.quickSwitchCursor >= len(m.quickSwitchList) {
		return
	}
	selected := m.quickSwitchList[m.quickSwitchCursor]
	if selected.ID == "__add__" {
		return
	}
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) <= 1 {
		m.showTempStatus("Cannot delete the last subscription")
		return
	}
	// Don't allow deleting the per-session active subscription without a fallback.
	// Check m.activeSubID first (per-session), then fall back to Active flag (global default).
	activeID := m.activeSubID
	if activeID == "" {
		for _, s := range subs {
			if s.Active {
				activeID = s.ID
				break
			}
		}
	}
	if selected.ID == activeID {
		m.showTempStatus("Cannot delete active subscription — switch to another first")
		return
	}
	if err := m.subscriptionMgr.Remove(selected.ID); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to delete: %v", err))
		return
	}
	m.showTempStatus(fmt.Sprintf("Deleted: %s", selected.Name))
	// Refresh the list
	m.openQuickSwitch(m.quickSwitchMode)
}

// updateQuickSwitchModels updates the model field in quickSwitchList for the active subscription.
func (m *cliModel) updateQuickSwitchModels(newModel string) {
	if len(m.quickSwitchList) == 0 {
		return
	}
	// Use m.activeSubID (per-session) to find the current subscription,
	// NOT Active flag (global default). The quick-switch list must reflect
	// the session's active subscription.
	if m.activeSubID != "" {
		for i := range m.quickSwitchList {
			if m.quickSwitchList[i].ID == m.activeSubID {
				m.quickSwitchList[i].Model = newModel
				return
			}
		}
	}
	// Fallback: if no per-session subscription, use global default.
	for i := range m.quickSwitchList {
		if m.quickSwitchList[i].Active {
			m.quickSwitchList[i].Model = newModel
			return
		}
	}
}

func (m *cliModel) viewQuickSwitch(width, height int) string {
	if m.quickSwitchMode == "" || len(m.quickSwitchList) == 0 {
		return ""
	}

	title := "Switch Subscription"
	if m.quickSwitchMode == "model" {
		title = "Switch Model"
	}

	var lines []string

	// Header
	lines = append(lines, m.styles.PanelHeader.Render(title))
	lines = append(lines, "") // spacer

	// Items
	for i, s := range m.quickSwitchList {
		// Separator before "Add" entry
		if s.ID == "__add__" && i > 0 {
			lines = append(lines, m.styles.TextMutedSt.Render(" ─────────────────────────────────"))
		}
		cursor := " "
		style := m.styles.TextMutedSt
		if i == m.quickSwitchCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		active := ""
		if m.activeSubID != "" {
			if s.ID == m.activeSubID {
				active = " ✓"
			}
		} else if s.Active {
			active = " ✓"
		}
		name := s.Name
		if name == "" {
			name = s.ID
		}
		line := style.Render(fmt.Sprintf(" %s %-30s %-16s%s", cursor, name, s.Model, active))
		lines = append(lines, line)
	}

	// Build panel with border
	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint line below the box
	hint := m.styles.PanelHint.Render(" ↑↓ Navigate  Enter Select  E Edit  D Delete  Esc Close")

	// Center vertically
	sepLines := 0
	for _, s := range m.quickSwitchList {
		if s.ID == "__add__" {
			sepLines = 1
			break
		}
	}
	listH := len(m.quickSwitchList) + 3 + sepLines // header + spacer + items + separator + borders(~2)
	blankLines := max(0, (height-listH)/2)
	var b strings.Builder
	for i := 0; i < blankLines; i++ {
		b.WriteString("\n")
	}
	b.WriteString(box)
	b.WriteString("\n")
	b.WriteString(hint)

	return b.String()
}

// handleQuickSwitchKey handles key events for the quick switch overlay.
// Returns (handled, cmd). Called from Update() BEFORE panelMode check
// so quick switch has higher priority than panels.
func (m *cliModel) handleQuickSwitchKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.quickSwitchMode == "" {
		return false, nil
	}
	switch msg.Code {
	case tea.KeyEsc:
		returnToSettings := m.quickSwitchReturnToPanel
		m.quickSwitchReturnToPanel = false
		m.quickSwitchMode = ""
		if returnToSettings {
			m.openSettingsFromQuickSwitch()
		}
		return true, nil
	case tea.KeyUp:
		if m.quickSwitchCursor > 0 {
			m.quickSwitchCursor--
		}
		return true, nil
	case tea.KeyDown:
		if m.quickSwitchCursor < len(m.quickSwitchList)-1 {
			m.quickSwitchCursor++
		}
		return true, nil
	case tea.KeyEnter:
		m.applyQuickSwitch()
		if len(m.pendingCmds) > 0 {
			pending := m.pendingCmds
			m.pendingCmds = nil
			return true, tea.Batch(pending...)
		}
		return true, nil
	}
	// E: edit selected subscription
	if msg.String() == "e" {
		m.editQuickSwitchEntry()
		return true, nil
	}
	// D: delete selected subscription
	if msg.String() == "d" {
		m.deleteQuickSwitchEntry()
		return true, nil
	}
	return true, nil // block all other keys
}

// ---------------------------------------------------------------------------
// Runner Panel
// ---------------------------------------------------------------------------

// openRunnerPanel 打开 Runner 管理面板
func (m *cliModel) openRunnerPanel() {
	m.panelMode = "runner"
	m.panelScrollY = 0
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

	m.panelRunnerServerTI = m.newPanelTextInput(serverURL, m.locale.RunnerServerPlaceholder)
	m.panelRunnerTokenTI = m.newPanelTextInput(token, m.locale.RunnerTokenPlaceholder)
	m.panelRunnerTokenTI.EchoMode = 0 // password mode
	m.panelRunnerTokenTI.EchoCharacter = '•'
	m.panelRunnerWorkspace = m.newPanelTextInput(workspace, m.locale.RunnerWorkspacePlaceholder)
	m.panelRunnerEditField = 0
}

// newPanelTextInput 创建一个配置好的 textinput 用于面板输入
func (m *cliModel) newPanelTextInput(value, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(50))
	ti.SetValue(value)
	if value != "" {
		ti.CursorEnd()
	}
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	return ti
}

// updateRunnerPanel 处理 Runner 面板的键盘事件
func (m *cliModel) updateRunnerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// Esc/popPanel 回到 parent 面板；Ctrl+C 关闭所有
	if msg.String() == "ctrl+c" {
		return m.closePanelAndResume()
	}
	if msg.Code == tea.KeyEsc {
		// Clean up runner panel state
		m.panelRunnerServerTI = textinput.Model{}
		m.panelRunnerTokenTI = textinput.Model{}
		m.panelRunnerWorkspace = textinput.Model{}
		m.panelRunnerEditField = 0
		if !m.popPanel() {
			m.closePanel()
		}
		return true, m, nil
	}

	// runnerBridge 为 nil 时只显示表单，不允许连接操作
	if m.runnerBridge == nil {
		// 将按键传递给当前编辑的 textinput
		var cmd tea.Cmd
		switch m.panelRunnerEditField {
		case 0:
			m.panelRunnerServerTI, cmd = m.panelRunnerServerTI.Update(msg)
		case 1:
			m.panelRunnerTokenTI, cmd = m.panelRunnerTokenTI.Update(msg)
		case 2:
			m.panelRunnerWorkspace, cmd = m.panelRunnerWorkspace.Update(msg)
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
			m.panelMode = "settings"
			m.panelRunnerServerTI = textinput.Model{}
			m.panelRunnerTokenTI = textinput.Model{}
			m.panelRunnerWorkspace = textinput.Model{}
			m.panelRunnerEditField = 0
			m.relayoutViewport()
			return true, m, nil
		}
		return true, m, nil
	}

	// 未连接：表单编辑
	switch msg.Code {
	case tea.KeyUp:
		if m.panelRunnerEditField > 0 {
			m.panelRunnerEditField--
		}
		return true, m, nil

	case tea.KeyDown:
		if m.panelRunnerEditField < 2 {
			m.panelRunnerEditField++
		}
		return true, m, nil

	case tea.KeyTab:
		m.panelRunnerEditField = (m.panelRunnerEditField + 1) % 3
		return true, m, nil

	case tea.KeyEnter:
		// 验证并连接
		serverURL := strings.TrimSpace(m.panelRunnerServerTI.Value())
		token := strings.TrimSpace(m.panelRunnerTokenTI.Value())
		workspace := strings.TrimSpace(m.panelRunnerWorkspace.Value())

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
		m.panelMode = "settings"
		m.panelRunnerServerTI = textinput.Model{}
		m.panelRunnerTokenTI = textinput.Model{}
		m.panelRunnerWorkspace = textinput.Model{}
		m.panelRunnerEditField = 0
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
	switch m.panelRunnerEditField {
	case 0:
		m.panelRunnerServerTI, cmd = m.panelRunnerServerTI.Update(msg)
	case 1:
		m.panelRunnerTokenTI, cmd = m.panelRunnerTokenTI.Update(msg)
	case 2:
		m.panelRunnerWorkspace, cmd = m.panelRunnerWorkspace.Update(msg)
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
			{m.locale.RunnerServerLabel, m.panelRunnerServerTI, m.panelRunnerEditField == 0},
			{m.locale.RunnerTokenLabel, m.panelRunnerTokenTI, m.panelRunnerEditField == 1},
			{m.locale.RunnerWorkspaceLabel, m.panelRunnerWorkspace, m.panelRunnerEditField == 2},
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

func (m *cliModel) ensureAskUserVisible() {
	if m.panelMode != "askuser" || m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	visible := m.askUserPanelVisibleHeight()
	if visible <= 0 {
		return
	}
	total := m.askPanelTotalLines
	if total == 0 {
		return
	}
	if total <= visible {
		m.askPanelScrollY = 0
		return
	}
	if m.askPanelScrollY < 0 {
		m.askPanelScrollY = 0
	}
	maxScroll := total - visible
	if m.askPanelScrollY > maxScroll {
		m.askPanelScrollY = maxScroll
	}
}

// ---------------------------------------------------------------------------
// §Channel Config Panel — /channel command
// ---------------------------------------------------------------------------

var builtinChannelNames = []string{"web", "feishu", "qq", "napcat"}
var channelLabels = map[string]string{
	"web":    "🌐 Web Channel",
	"feishu": "🐦 Feishu (飞书)",
	"qq":     "💬 QQ",
	"napcat": "🐱 NapCat",
}

// openChannelPanel opens the channel configuration panel.
func (m *cliModel) openChannelPanel() {
	m.panelMode = "channel"
	m.relayoutViewport()
	m.panelChannelCursor = 0

	// Fetch current channel configs (includes plugin channels)
	if m.channel != nil && m.channel.config.ChannelConfigGetFn != nil {
		cfgs, err := m.channel.config.ChannelConfigGetFn()
		if err != nil {
			m.showSystemMsg("Failed to load channel configs: "+err.Error(), feedbackWarning)
			cfgs = nil
		}
		m.panelChannelCfg = cfgs
	}

	// Build channel list: built-in first (in fixed order), then plugin channels
	items := make([]string, 0, len(builtinChannelNames)+len(m.panelChannelCfg))
	seen := make(map[string]bool)
	for _, name := range builtinChannelNames {
		items = append(items, name)
		seen[name] = true
	}
	// Add plugin channels from config keys
	if m.panelChannelCfg != nil {
		for name := range m.panelChannelCfg {
			if !seen[name] {
				items = append(items, name)
				seen[name] = true
			}
		}
	}
	m.panelChannelItems = items
}

// updateChannelPanel handles key events in the channel config panel.
func (m *cliModel) updateChannelPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()
	case msg.Code == tea.KeyEsc:
		m.panelChannelItems = nil
		m.panelChannelCfg = nil
		if !m.popPanel() {
			m.panelMode = ""
			m.relayoutViewport()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if m.panelChannelCursor > 0 {
			m.panelChannelCursor--
		}
		return true, m, nil

	case msg.Code == tea.KeyDown:
		if m.panelChannelCursor < len(m.panelChannelItems)-1 {
			m.panelChannelCursor++
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelChannelCursor >= 0 && m.panelChannelCursor < len(m.panelChannelItems) {
			ch := m.panelChannelItems[m.panelChannelCursor]
			m.openChannelSettingsPanel(ch)
		}
		return true, m, nil
	}
	return true, m, nil
}

// viewChannelPanel renders the channel config panel.
func (m *cliModel) viewChannelPanel() string {
	s := &m.styles
	header := s.PanelHeader.Render("📡 Channel Configuration")
	help := s.PanelDesc.Render("↑↓ Navigate  Enter Configure  Esc Close")

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n\n")

	contentW := m.width - 4
	if contentW < 20 {
		contentW = 20
	}

	for i, ch := range m.panelChannelItems {
		prefix := "  "
		if i == m.panelChannelCursor {
			prefix = s.PanelCursor.Render("▸")
		}

		label := channelLabels[ch]
		if label == "" {
			label = ch
		}

		// Show enabled/disabled status
		status := "◦ disabled"
		statusStyle := s.TextMutedSt
		if m.panelChannelCfg != nil {
			if cfg, ok := m.panelChannelCfg[ch]; ok {
				if v, ok2 := cfg["enabled"]; ok2 && v == "true" {
					status = "● enabled"
					statusStyle = s.ProgressDone
				}
			}
		}

		line := fmt.Sprintf("%s %-25s %s", prefix, label, statusStyle.Render(status))
		sb.WriteString(truncateToWidth(line, contentW))
		sb.WriteString("\n")
	}

	sb.WriteString("\n")
	sb.WriteString(s.PanelHint.Render("  Select a channel to configure. Changes are saved to config.json."))

	return sb.String()
}

// pluginChannelSchema extracts the settings schema for a plugin channel
// from the _schema field in panelChannelCfg (populated by getChannelConfigs RPC).
func (m *cliModel) pluginChannelSchema(channel string) []SettingDefinition {
	if m.panelChannelCfg == nil {
		return nil
	}
	cfg, ok := m.panelChannelCfg[channel]
	if !ok {
		return nil
	}
	schemaJSON, ok := cfg["_schema"]
	if !ok {
		return nil
	}

	var rawSchema []struct {
		Key          string `json:"key"`
		Label        string `json:"label"`
		Description  string `json:"description"`
		Type         string `json:"type"`
		DefaultValue string `json:"default_value"`
		Category     string `json:"category"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &rawSchema); err != nil {
		return nil
	}

	schema := make([]SettingDefinition, 0, len(rawSchema))
	for _, r := range rawSchema {
		sd := SettingDefinition{
			Key:          r.Key,
			Label:        r.Label,
			Description:  r.Description,
			Type:         SettingType(r.Type),
			DefaultValue: r.DefaultValue,
			Category:     r.Category,
		}
		schema = append(schema, sd)
	}
	return schema
}

// channelSettingsSchema returns the settings schema for a specific channel.
func channelSettingsSchema(channel string) []SettingDefinition {
	switch channel {
	case "web":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable Web channel", Type: SettingTypeToggle, Category: "Web Channel", DefaultValue: "false"},
			{Key: "host", Label: "Host", Description: "Listen host (e.g. 0.0.0.0)", Type: SettingTypeText, Category: "Web Channel", DefaultValue: "0.0.0.0"},
			{Key: "port", Label: "Port", Description: "Listen port (e.g. 8080)", Type: SettingTypeText, Category: "Web Channel", DefaultValue: "8080"},
		}
	case "feishu":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable Feishu channel", Type: SettingTypeToggle, Category: "Feishu (飞书)", DefaultValue: "false"},
			{Key: "app_id", Label: "App ID", Description: "Feishu app ID", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "app_secret", Label: "App Secret", Description: "Feishu app secret", Type: SettingTypePassword, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "encrypt_key", Label: "Encrypt Key", Description: "Feishu event encrypt key", Type: SettingTypePassword, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "verification_token", Label: "Verification Token", Description: "Feishu event verification token", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "domain", Label: "Domain", Description: "Custom Feishu API domain (optional)", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
		}
	case "qq":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable QQ channel", Type: SettingTypeToggle, Category: "QQ", DefaultValue: "false"},
			{Key: "app_id", Label: "App ID", Description: "QQ Bot AppID", Type: SettingTypeText, Category: "QQ", DefaultValue: ""},
			{Key: "client_secret", Label: "Client Secret", Description: "QQ Bot client secret", Type: SettingTypePassword, Category: "QQ", DefaultValue: ""},
		}
	case "napcat":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable NapCat channel", Type: SettingTypeToggle, Category: "NapCat", DefaultValue: "false"},
			{Key: "ws_url", Label: "WebSocket URL", Description: "NapCat WebSocket URL", Type: SettingTypeText, Category: "NapCat", DefaultValue: ""},
			{Key: "token", Label: "Token", Description: "NapCat access token", Type: SettingTypePassword, Category: "NapCat", DefaultValue: ""},
		}
	default:
		// Plugin channel: try to parse _schema from channel config
		return nil
	}
}

// openChannelSettingsPanel opens the settings panel for a specific channel.
func (m *cliModel) openChannelSettingsPanel(channel string) {
	schema := channelSettingsSchema(channel)
	if schema == nil {
		// Plugin channel: try to parse _schema from panelChannelCfg
		schema = m.pluginChannelSchema(channel)
	}
	if schema == nil {
		m.showSystemMsg("Unknown channel: "+channel, feedbackWarning)
		return
	}

	// Get current values from cached config or fetch from backend
	values := make(map[string]string)
	if m.panelChannelCfg != nil {
		if cfg, ok := m.panelChannelCfg[channel]; ok {
			for k, v := range cfg {
				values[k] = v
			}
		}
	}

	// Fill defaults for unset values
	for _, def := range schema {
		if _, ok := values[def.Key]; !ok {
			values[def.Key] = def.DefaultValue
		}
	}

	m.openSettingsPanel(schema, values, func(vals map[string]string) {
		if m.channel == nil || m.channel.config.ChannelConfigSetFn == nil {
			m.showTempStatus("Channel config save not available")
			return
		}
		if err := m.channel.config.ChannelConfigSetFn(channel, vals); err != nil {
			m.showTempStatus("Failed to save channel config: " + err.Error())
		} else {
			// Refresh cached configs
			if m.channel.config.ChannelConfigGetFn != nil {
				if cfgs, err := m.channel.config.ChannelConfigGetFn(); err == nil {
					m.panelChannelCfg = cfgs
				}
			}
			m.showTempStatus(fmt.Sprintf("✅ %s config saved", channel))
		}
	})
}

// ── Session management (same-directory multi-session) ──

// showSessionCreateDialog creates a new session with an auto-generated name.
func (m *cliModel) showSessionCreateDialog() tea.Cmd {
	m.panelMode = "" // close sessions panel
	ds, err := LoadDirSessions(m.workDir)
	if err != nil {
		m.showTempStatus(fmt.Sprintf("Failed: %v", err))
		return nil
	}
	name, chatID, err := ds.addSessionAuto()
	if err != nil {
		m.showTempStatus(fmt.Sprintf("Failed: %v", err))
		return nil
	}
	// --- Pre-creation cleanup: nuke ALL residual state for this chatID ---
	// Even with UUID-based IDs (guaranteed unique), we clean defensively to
	// handle edge cases like race conditions or manual chatID reuse.
	sessionKey := "cli:" + chatID
	// 1. Delete any residual DB tenant (from a previously failed deletion)
	if m.channel != nil && m.channel.config.SessionsDeleteFn != nil {
		_ = m.channel.config.SessionsDeleteFn("cli", chatID)
	}
	// 2. Clean up any leftover worktree registration + physical git worktree.
	// This handles the case where a previous session with the same chatID had
	// auto_worktree enabled and left a worktree behind.
	tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
	// 3. Nuke persisted CWD — prevent inheriting a stale worktree directory.
	session.DeletePersistedCWD("cli", chatID)
	// 4. Nuke saved session state from memory.
	delete(m.savedSessions, sessionKey)
	// 5. Nuke persisted todo data.
	if m.todoManager != nil {
		m.todoManager.SetTodos(sessionKey, nil)
		_ = m.todoManager.SaveToFile(sessionKey)
	}

	m.saveCurrentSession()
	// Inherit parent session's LLM state atomically.
	// SaveSessionLLMState writes ALL fields (sub, model, maxContext, maxOutput) in one shot.
	if m.activeSubID != "" {
		SaveSessionLLMState(m.workDir, chatID, SessionLLMState{
			SubscriptionID:   m.activeSubID,
			Model:            m.cachedModelName,
			MaxContextTokens: m.cachedMaxContextTokens,
			MaxOutputTokens:  int(m.cachedMaxOutputTokens),
		})
	}
	m.chatID = chatID
	SetLastActiveSession(m.defaultChatID, chatID)
	m.sessionName = name
	m.channelName = "cli"
	// Update workdir and persist CWD for the new session (same as switchToSession)
	workDir, _ := ParseChatID(chatID)
	if workDir != "" {
		m.workDir = workDir
		if m.channel != nil && m.channel.config.SetCWDFn != nil {
			_ = m.channel.config.SetCWDFn("cli", chatID, workDir)
		}
	}
	// Reset ALL session state to prevent inheritance from deleted sessions
	m.resetToIdleState()
	m.restoreSession()
	// Unified session setup — handles BindChatFn, suLoadHistoryCmd,
	// checkAndRestorePendingAskUser, inputReady, etc.
	cmds := m.postRestoreSessionSetup()
	// Refresh sessions list cache so sidebar/sessions panel shows the new session
	if m.sessionsListFn != nil {
		m.panelSessionItems = m.sessionsListFn()
	}
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
	m.showTempStatus(fmt.Sprintf("Created session: %s", name))
	return tea.Batch(cmds...)
}

// deleteLocalSession deletes the selected session and switches to default if active.
func (m *cliModel) deleteLocalSession(entry SessionPanelEntry) tea.Cmd {
	// 1. Try to delete from backend DB. Ignore "not found" errors — the session
	// may be local-only (created in CLI JSON but never synced to server/runner).
	if m.channel != nil && m.channel.config.SessionsDeleteFn != nil {
		if err := m.channel.config.SessionsDeleteFn("cli", entry.ID); err != nil {
			errMsg := err.Error()
			if !strings.Contains(errMsg, "not found") {
				log.WithError(err).WithField("chatID", entry.ID).Warn("Backend session delete failed")
				m.showTempStatus(fmt.Sprintf("Delete failed: %v", err))
				return nil
			}
			// "not found" is fine — session is local-only, proceed to clean local JSON
		}
	}
	// 2. Remove from local JSON file (always, regardless of backend result).
	ds, err := LoadDirSessions(m.workDir)
	if err == nil {
		if err := ds.removeSessionByChatID(entry.ID); err != nil {
			log.WithError(err).WithField("chatID", entry.ID).Warn("Local session remove failed")
		}
	}
	// 3. Clean up worktree / peer registration for this session.
	sessionKey := "cli:" + entry.ID
	tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
	// 3b. Clean up persisted CWD so a future session with the same chatID
	// (e.g. the default workDir-based session) does not inherit a stale CWD.
	session.DeletePersistedCWD("cli", entry.ID)
	// 3c. Remove saved session state from memory to prevent stale state leaks.
	delete(m.savedSessions, sessionKey)
	if m.todoManager != nil {
		m.todoManager.SetTodos(sessionKey, nil)
		_ = m.todoManager.SaveToFile(sessionKey) // delete persisted todo file
	}
	// If we deleted the active session, switch to default
	if entry.Active {
		m.saveCurrentSession()
		m.chatID = m.defaultChatID
		SetLastActiveSession(m.defaultChatID, m.defaultChatID)
		m.sessionName = defaultSessionName
		m.workDir = m.defaultChatID
		if m.channel != nil && m.channel.config.SetCWDFn != nil {
			_ = m.channel.config.SetCWDFn("cli", m.defaultChatID, m.defaultChatID)
		}
		m.resetToIdleState()
		m.invalidateAllCache(false)
		m.restoreSession()
		cmds := m.postRestoreSessionSetup()
		// Refresh sessions list so sidebar/sessions panel reflects the deletion
		if m.sessionsListFn != nil {
			m.panelSessionItems = m.sessionsListFn()
		}
		if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
			m.channel.config.SessionsListRefresh()
		}
		m.showTempStatus(fmt.Sprintf("Deleted session: %s", entry.Label))
		return tea.Batch(cmds...)
	}
	// Non-active session deleted: refresh sidebar so it disappears immediately.
	if m.sessionsListFn != nil {
		m.panelSessionItems = m.sessionsListFn()
	}
	if m.channel != nil && m.channel.config.SessionsListRefresh != nil {
		m.channel.config.SessionsListRefresh()
	}
	m.showTempStatus(fmt.Sprintf("Deleted session: %s", entry.Label))
	return nil
}

// switchToSession switches to the given session entry directly (used by sidebar click).
// Extracted from the sessions panel Enter key handler for reuse.
func (m *cliModel) switchToSession(entry SessionPanelEntry) (bool, tea.Cmd) {
	switch entry.Type {
	case "main":
		if entry.ID != m.chatID {
			// Clear unread flag — user is now viewing this session.
			delete(m.unreadSessions, entry.ID)
			// Close AskUser panel if it belongs to a different session
			if m.panelMode == "askuser" && m.askUserSession != entry.ID {
				m.panelMode = ""
				m.panelItems = nil
				m.relayoutViewport()
			}
			m.saveCurrentSession()
			m.chatID = entry.ID
			SetLastActiveSession(m.defaultChatID, entry.ID)
			m.channelName = entry.Channel
			// Update workdir to match the session's workdir.
			workDir, _ := ParseChatID(entry.ID)
			if workDir != "" {
				m.workDir = workDir
				if m.channel != nil && m.channel.config.SetCWDFn != nil {
					_ = m.channel.config.SetCWDFn(entry.Channel, entry.ID, workDir)
				}
			}
			// Update background task session key for isolation
			if m.channel != nil {
				m.channel.bgSessionKey = "cli:" + entry.ID
				m.channel.updateBgTaskCountFn()
			}
			m.messages = nil
			m.lastTokenUsage = nil
			m.invalidateAllCache(false)
			m.todos = nil // clear stale todos from previous session
			m.todosDoneCleared = false
			m.restoreSession()
			cmds := m.postRestoreSessionSetup()
			if len(cmds) > 0 {
				return true, tea.Batch(cmds...)
			}
		}
	case "agent":
		agentChatID := entry.Channel + ":" + entry.ParentID + "/" + entry.Role
		if entry.Instance != "" {
			agentChatID += ":" + entry.Instance
		}
		if agentChatID != m.chatID {
			// Clear unread flag — user is now viewing this session.
			delete(m.unreadSessions, entry.ID)
			// Close AskUser panel if it doesn't belong to the new agent session
			if m.panelMode == "askuser" && m.askUserSession != agentChatID {
				m.panelMode = ""
				m.panelItems = nil
				m.relayoutViewport()
			}
			m.saveCurrentSession()
			m.chatID = agentChatID
			m.channelName = "agent"
			// Update workdir to match the parent session's workdir.
			workDir, _ := ParseChatID(entry.ParentID)
			if workDir != "" {
				m.workDir = workDir
				if m.channel != nil && m.channel.config.SetCWDFn != nil {
					_ = m.channel.config.SetCWDFn(entry.Channel, entry.ParentID, workDir)
				}
			}
			// Update background task session key for isolation
			if m.channel != nil {
				m.channel.bgSessionKey = "agent:" + agentChatID
				m.channel.updateBgTaskCountFn()
			}
			m.messages = nil
			m.lastTokenUsage = nil
			m.invalidateAllCache(false)
			m.todos = nil // clear stale todos from previous session
			m.todosDoneCleared = false
			m.restoreSession()
			cmds := m.postRestoreSessionSetup()
			if len(cmds) > 0 {
				return true, tea.Batch(cmds...)
			}
		}
	}
	return true, nil
}
