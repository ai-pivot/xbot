package cli

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"os/exec"
	ch "xbot/channel"
	"xbot/protocol"
	"xbot/tools"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// qualifyChatID combines channel name and chatID into the "channel:chatID" format
// used throughout the codebase as the canonical session key.
func qualifyChatID(channel, chatID string) string {
	return channel + ":" + chatID
}

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
func isSubscriptionScopedSettingKey(key string) bool { return ch.IsSubscriptionScopedSettingKey(key) }
func cliSettingScope(key string) string              { return ch.SettingScopeOf(key) }

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
	m.rc.resetAll()
	for i := range m.messages {
		m.messages[i].dirty = true
		m.messages[i].wrappedLines = nil
		m.messages[i].wrappedWidth = 0
	}
	if updateViewport {
		m.updateViewportContent()
	}
}

func (m *cliModel) openSettingsFromQuickSwitch() {
	if m.channel == nil || len(m.panelState.stack) == 0 {
		return
	}
	entry := m.panelState.stack[len(m.panelState.stack)-1]
	if entry.mode != "settings" {
		return
	}
	// Pop the stack entry — we consume it to restore the settings panel.
	m.panelState.stack = m.panelState.stack[:len(m.panelState.stack)-1]

	schema := m.channel.SettingsSchema()
	if len(schema) == 0 {
		return
	}
	// Re-read ALL values fresh (including LLM fields from new active subscription)
	values := m.mergeCLISettingsValues()
	// Overlay non-subscription values from the saved settings state (preserves
	// user's in-memory edits to global/user-scoped settings like thinking_mode,
	// sandbox_mode).
	for k, v := range entry.values {
		if ch.IsSubscriptionScopedSettingKey(k) {
			continue
		}
		values[k] = v
	}
	m.openSettingsPanel(schema, values, entry.onSubmit)
	m.panelState.cursor = entry.cursor
}

func (m *cliModel) applyThemeAndRebuild(theme string) {
	setTheme(theme)
	// Rebuild styles cache (same as themeChangeCh handler in Update)
	m.styles = buildStyles(m.width)
	m.invalidateLayoutCache() // sidebar styles may have changed
	applyTAStyles(&m.textarea, &m.styles)
	m.ticker.style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
	// Rebuild glamour renderer
	cw := m.chatWidth()
	if cw > 4 {
		m.renderer = newGlamourRenderer(cw - 4)
	}
	// Mark all messages for re-render (new theme = new styles)
	m.rc.valid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}
}

func (m *cliModel) applyLanguageChange(lang string) {
	ch.SetLocale(lang)
	m.locale = ch.GetLocale(lang)
	m.rc.valid = false
}

// applyLayoutConfig updates layout-related model fields from settings values
// and invalidates the render cache so the viewport relayouts.
func (m *cliModel) applyLayoutConfig(vals map[string]string) {
	if v, ok := vals["layout_mode"]; ok && v != "" {
		m.layoutConfig.mode = v
	}
	if v, ok := vals["sidebar_enabled"]; ok {
		m.layoutConfig.sidebarEnabled = ParseSettingBool(v)
	}
	if v, ok := vals["sidebar_width"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			m.layoutConfig.sidebarWidth = n
		}
	}
	if v, ok := vals["sidebar_position"]; ok && v != "" {
		m.layoutConfig.sidebarPos = v
	}
	if v, ok := vals["chat_max_width"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			m.layoutConfig.maxWidth = n
		}
	}
	if v, ok := vals["chat_center"]; ok {
		m.layoutConfig.center = ParseSettingBool(v)
	}
	m.rc.valid = false
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
	if m.panelState.settings.isSetup {
		feedbackMsg = m.locale.SetupComplete
	}

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

	// Run onSubmit in the returned tea.Cmd (BubbleTea executes Cmds in a
	// background goroutine). This avoids blocking the Update loop while
	// preserving ordering — the TUI shows a "Saving..." overlay and blocks
	// user input until onSubmit completes and cliSettingsSavedMsg arrives.
	return func() tea.Msg {
		onSubmit(vals)
		// Capture the model name directly from the saved values.
		// This avoids a second GetDefault RPC which may not yet reflect
		// the newly created subscription (especially on first setup).
		savedModel := vals["llm_model"]
		return cliSettingsSavedMsg{
			themeChanged:  hasTheme && theme != "",
			theme:         theme,
			langChanged:   hasLang,
			lang:          lang,
			layoutChanged: layoutChanged,
			layoutVals:    layoutVals,
			feedbackMsg:   feedbackMsg,
			savedModel:    savedModel,
		}
	}
}

// reloadSettingsCaches re-resolves all settings-dependent cached values.
// Called when settings change (panel save or config tool write) to ensure
// the TUI reflects the latest configuration (context bar, model name, etc.).
func (m *cliModel) reloadSettingsCaches() {
	m.refreshCachedModelName()
	m.cachedMaxContextTokens = m.resolveMaxContextTokens()
	m.cachedMaxOutputTokens = m.resolveMaxOutputTokens()
	m.cachedCompressRatio = m.resolveCompressRatio()
	m.invalidateSubCache()
}

// handleSettingsSavedMsg processes the async settings save result.
// Called from Update() to apply theme/locale/layout changes and refresh the viewport.
func (m *cliModel) handleSettingsSavedMsg(msg cliSettingsSavedMsg) tea.Cmd {
	m.panelState.settings.settingsSaving = false // unblock user input
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
	m.reloadSettingsCaches()
	// msg.savedModel comes from the user's explicit choice in the wizard/settings
	// panel. It is authoritative — refreshCachedModelName may fail due to RPC
	// timing (subscription just created, not yet visible to GetDefault).
	if msg.savedModel != "" {
		m.cachedModelName = msg.savedModel
	}
	if msg.feedbackMsg != "" {
		m.appendSystem(msg.feedbackMsg)
	}
	// After setup wizard completes, show welcome message with TUI usage tips.
	if m.panelState.settings.isSetup {
		m.panelState.settings.isSetup = false
		if m.locale.SetupWelcome != "" {
			m.appendSystem(m.locale.SetupWelcome)
		}
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
	if m.panelState.askUser.onAnswer != nil {
		m.panelState.askUser.onAnswer(answers)
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
	// Clear after 5 seconds via dedicated goroutine — does NOT use pendingCmds
	// which can be cleared by postRestoreSessionSetup() during reconnect.
	// Safety: m.channel outlives this 5s timer — a session's CLIChannel is
	// only replaced when the whole model is garbage-collected, which cannot
	// happen before 5s since this goroutine holds a reference to m.
	go func() {
		time.Sleep(5 * time.Second)
		if m.channel != nil {
			select {
			case m.channel.asyncCh <- cliTempStatusClearMsg{}:
			default:
			}
		}
	}()
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
// Now handled by agent-level /usage command; kept for future send_slash/remote use.
//
//nolint:unused
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
	var todayEntries []ch.DailyTokenUsage
	var todayTotal ch.DailyTokenUsage
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
		slices.SortFunc(todayEntries, func(a, b ch.DailyTokenUsage) int {
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
	daySummary := make(map[string]*ch.DailyTokenUsage)
	var sortedDates []string
	for _, d := range daily {
		if _, ok := daySummary[d.Date]; !ok {
			daySummary[d.Date] = &ch.DailyTokenUsage{Date: d.Date}
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

// fmtTokens formats large token counts with K/M suffixes for usage tables.
//
//nolint:unused
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
	m.refreshCachedSubName()
}

// ---------------------------------------------------------------------------
// /plugin command
// ---------------------------------------------------------------------------

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
// Falls back to 0 if unavailable (renderContextTopBorder will use config.DefaultMaxOutputTokens as default).
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
				// Clean up worktree / peer registration for closed session.
				sessionKey := "cli:" + entry.ID
				tools.GlobalWorktreeRegistry.CleanupSession(sessionKey)
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
		m.rc.valid = false
		m.updateViewportContent()
		sc.result <- &cliSessionResult{ok: true}
		m.persistCLISettingsValues(map[string]string{"theme": theme})

	case "send_slash":
		cmd := sc.params["command"]
		if cmd == "" {
			sc.result <- &cliSessionResult{ok: false, err: "command required for send_slash"}
			return nil
		}
		// Return success IMMEDIATELY to unblock the caller (agent goroutine).
		// handleSlashCommand may call back into the agent via sendToAgent (non-blocking)
		// or run local handlers that invoke agent RPC (e.g. usageQueryFn → agent RPC).
		// If we don't release the caller first, the RPC callback deadlocks because
		// the agent goroutine is blocked in SendTUIControl waiting on resultCh.
		sc.result <- &cliSessionResult{ok: true}
		return m.handleSlashCommand(cmd)

	case "reload_settings":
		// Triggered by config tool after writing settings (e.g. max_context_tokens).
		// Re-resolves all settings-dependent cached values so the TUI reflects
		// the change immediately (context bar, model name, compress ratio, etc.).
		m.reloadSettingsCaches()
		m.updateViewportContent()
		sc.result <- &cliSessionResult{ok: true}

	default:
		sc.result <- &cliSessionResult{ok: false, err: "unknown action: " + sc.action}
	}
	return nil
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // linux, freebsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
