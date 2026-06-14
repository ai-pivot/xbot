package cli

import (
	"strings"
)

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
// Uses ch.SetLocale() instead of ch.SetLocale() to avoid sending on ch.LocaleChangeCh(),
// which would cause a redundant fullRebuild in the next Update cycle.
