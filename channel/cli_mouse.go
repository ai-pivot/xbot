package channel

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"xbot/protocol"
)

// mouseZone represents a clickable region on screen.
// Zones are rebuilt each frame during View() and used by Update() for hit testing.
type mouseZone struct {
	YStart int    // first terminal line (inclusive, 0-based)
	YEnd   int    // last terminal line (inclusive)
	XStart int    // first terminal column (inclusive), -1 means full row (default)
	XEnd   int    // last terminal column (exclusive), -1 means full row (default)
	ID     string // zone identifier (e.g., "panelItem", "paletteItem", "textarea", "footerHint")
	Index  int    // item index within zone (e.g., list item index)
}

// mouseZoneBuilder tracks Y offsets during View() rendering to build hit-test zones.
type mouseZoneBuilder struct {
	zones []mouseZone
	y     int // current Y cursor (line number in the final rendered output)
}

// reset clears all zones and resets the Y cursor.
func (zb *mouseZoneBuilder) reset() {
	zb.zones = zb.zones[:0]
	zb.y = 0
}

// add records a zone at the current Y position with the given height,
// then advances the Y cursor. The zone spans the full row width (X is ignored during hit testing).
func (zb *mouseZoneBuilder) add(h int, id string, index int) {
	zb.zones = append(zb.zones, mouseZone{
		YStart: zb.y,
		YEnd:   zb.y + h - 1,
		XStart: -1,
		XEnd:   -1,
		ID:     id,
		Index:  index,
	})
	zb.y += h
}

// skip advances the Y cursor by n lines (for non-interactive regions).
func (zb *mouseZoneBuilder) skip(n int) {
	zb.y += n
}

// addX registers an inline zone with X-coordinate bounds (for same-row clickable regions).
func (zb *mouseZoneBuilder) addX(yOffset, xStart, xEnd int, id string, index int) {
	zb.zones = append(zb.zones, mouseZone{
		YStart: zb.y + yOffset,
		YEnd:   zb.y + yOffset,
		XStart: xStart,
		XEnd:   xEnd,
		ID:     id,
		Index:  index,
	})
}

// findZone returns the zone containing the given terminal Y and X coordinates.
// When XStart/XEnd are negative, the zone spans the full row (X is ignored).
// Iterates in reverse order so that later-registered (more specific) zones
// take priority over earlier (broader) ones — e.g. × delete button over
// the full-row session click zone.
func (zb *mouseZoneBuilder) findZone(y, x int) (mouseZone, bool) {
	for i := len(zb.zones) - 1; i >= 0; i-- {
		z := zb.zones[i]
		if y >= z.YStart && y <= z.YEnd {
			if z.XStart < 0 || (x >= z.XStart && x < z.XEnd) {
				return z, true
			}
		}
	}
	return mouseZone{}, false
}

// handleMouseMsg dispatches mouse events to the appropriate handler.
// Returns (handled, model, cmd).
func (m *cliModel) handleMouseMsg(msg tea.MouseMsg) (bool, tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseClickMsg:
		handled, model, cmd := m.handleMouseClick(msg)
		// Unhandled click without Shift → user likely tried to select text,
		// show hint in title bar.
		if !handled && msg.Mouse().Mod&tea.ModShift == 0 {
			m.shiftHintUntil = time.Now().Add(3 * time.Second)
		}
		return handled, model, cmd
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(msg)
	case tea.MouseMotionMsg:
		mouse := msg.Mouse()
		// Mouse drag without Shift → user tries to drag-select text.
		if mouse.Button != 0 && mouse.Mod&tea.ModShift == 0 {
			m.shiftHintUntil = time.Now().Add(3 * time.Second)
		}
		return false, m, nil
	}
	return false, m, nil
}

// handleMouseClick processes mouse click events.
func (m *cliModel) handleMouseClick(msg tea.MouseClickMsg) (bool, tea.Model, tea.Cmd) {
	zone, found := m.mouseZones.findZone(msg.Y, msg.X)
	if !found {
		return false, m, nil
	}

	switch zone.ID {
	case "panelItem":
		return m.clickPanelItem(zone.Index)
	case "panelToggle":
		return m.clickPanelToggle(zone.Index)
	case "panelCombo":
		return m.clickPanelCombo(zone.Index)
	case "panelComboItem":
		return m.clickPanelComboItem(zone.Index)
	case "panelOpenURL":
		return m.clickPanelOpenURL()
	case "panelSave":
		return m.clickPanelSave()
	case "panelCancel":
		return m.clickPanelCancel()
	case "askUserOption":
		return m.clickAskUserOption(zone.Index)
	case "askUserTab":
		return m.clickAskUserTab(zone.Index)
	case "askUserSubmit":
		return m.clickAskUserSubmit()
	case "paletteItem":
		return m.clickPaletteItem(zone.Index)
	case "paletteTab":
		return m.clickPaletteTab(zone.Index)
	case "quickSwitchItem":
		return m.clickQuickSwitchItem(zone.Index)
	case "rewindItem":
		return m.clickRewindItem(zone.Index)
	case "approvalBtn":
		return m.clickApprovalBtn(zone.Index)
	case "textarea":
		// y is absolute; compute relative y from zone start
		relY := msg.Y - zone.YStart
		return m.clickTextarea(msg.X, relY)
	case "panelTextarea":
		relY := msg.Y - zone.YStart
		return m.clickPanelTextarea(msg.X, relY)
	case "completionsItem":
		return m.clickCompletionsItem(zone.Index)
	case "sessionsItem":
		return m.clickSessionsItem(zone.Index)
	case "sidebarSession":
		return m.clickSidebarSession(zone.Index)
	case "sidebarDeleteSession":
		return m.clickSidebarDeleteSession(zone.Index)
	case "sidebarNewSession":
		return m.clickSidebarNewSession()
	case "sidebarBgTask":
		return m.clickSidebarBgTask(zone.Index)
	case "sidebarSectionHeader":
		return m.clickSidebarSectionHeader(zone.ID)
	case "bgtaskItem":
		return m.clickBgTasksItem(zone.Index)
	case "dangerItem":
		return m.clickDangerItem(zone.Index)
	case "channelItem":
		return m.clickChannelItem(zone.Index)
	case "runnerField":
		return m.clickRunnerField(zone.Index)
	case "footerHint":
		return m.clickFooterHint(zone.Index)
	case "modelName":
		m.cycleModel()
		return true, m, nil
	case "scrollToBottom":
		m.viewport.GotoBottom()
		m.newContentHint = false
		m.userScrolledUp = false
		return true, m, nil
	}
	// Handle prefixed zone IDs (e.g. "sidebarSectionHeader:sessions")
	if strings.HasPrefix(zone.ID, "sidebarSectionHeader:") {
		return m.clickSidebarSectionHeader(zone.ID)
	}
	return false, m, nil
}

// clickFooterHint handles mouse clicks on footer hint items.
// Returns (handled, model, cmd) matching handleMouseClick signature.
func (m *cliModel) clickFooterHint(index int) (bool, tea.Model, tea.Cmd) {
	if index < 0 || index >= len(footerHints) {
		return false, m, nil
	}
	action := footerHints[index].action

	switch action {
	case "ctrl+k":
		if !m.paletteOpen {
			m.openCommandPalette()
		}
		return true, m, nil
	case "ctrl+c":
		m.sendCancel()
		return true, m, nil
	case "ctrl+e":
		m.toggleMessageFold()
		return true, m, nil
	case "ctrl+p":
		if m.subscriptionMgr != nil {
			m.openQuickSwitch("subscription")
		}
		return true, m, nil
	case "ctrl+t":
		m.openSessionsPanel()
		return true, m, nil
	case "ctrl+j":
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return true, m, nil
	case "esc":
		if m.paletteOpen {
			m.closeCommandPalette()
		} else if m.quickSwitchMode != "" {
			m.quickSwitchMode = ""
		} else if m.rewindMode {
			m.rewindMode = false
		} else if m.panelMode != "" {
			m.closePanel()
		}
		return true, m, nil
	case "enter":
		if m.panelMode != "" {
			return m.clickPanelItem(m.panelCursor)
		}
		return true, m, nil
	case "tab":
		m.handleTabComplete()
		return true, m, nil
	case "^":
		m.openBgTasksPanel()
		return true, m, nil
	default:
		return false, m, nil
	}
}

// isYInPanelBox checks if a Y coordinate falls within the panel box area.
// Layout: titleBar(1) + PanelBox border(1) + content(visibleH) + border(1).
// The scrollable content area is lines [2, 2+visibleH).
func (m *cliModel) isYInPanelBox(y int) bool {
	const titleBarLines = 1
	const boxBorderTop = 1
	panelTop := titleBarLines + boxBorderTop // first content line
	panelBottom := panelTop + m.panelVisibleHeight()
	return y >= panelTop && y < panelBottom
}

// handleMouseWheel processes mouse wheel events for panel/overlay scrolling.
func (m *cliModel) handleMouseWheel(msg tea.MouseWheelMsg) (bool, tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseWheelUp:
		// AskUser split layout: wheel always scrolls the askuser panel.
		// The user controls the main viewport via Shift+↑/↓.
		if m.panelMode == "askuser" {
			m.askPanelScrollY = max(0, m.askPanelScrollY-3)
			return true, m, nil
		}
		// Check if wheel is in panel area (non-askuser panels)
		if m.panelMode != "" {
			if m.isYInPanelBox(msg.Y) {
				m.panelScrollY = max(0, m.panelScrollY-3)
				return true, m, nil
			}
		}
		// Check overlays
		if m.paletteOpen {
			zone, found := m.mouseZones.findZone(msg.Y, msg.X)
			if found && zone.ID == "paletteItem" {
				m.paletteScrollY = max(0, m.paletteScrollY-1)
				return true, m, nil
			}
		}
		if m.rewindMode {
			zone, found := m.mouseZones.findZone(msg.Y, msg.X)
			if found && zone.ID == "rewindItem" {
				if m.rewindCursor > 0 {
					m.rewindCursor--
				}
				return true, m, nil
			}
		}
		// Let viewport handle it (will be done by viewport.Update in Update())
		return false, m, nil

	case tea.MouseWheelDown:
		// AskUser split layout: wheel always scrolls the askuser panel.
		// The user controls the main viewport via Shift+↑/↓.
		if m.panelMode == "askuser" {
			m.askPanelScrollY += 3
			return true, m, nil
		}
		// Check if wheel is in panel area (non-askuser panels)
		if m.panelMode != "" {
			if m.isYInPanelBox(msg.Y) {
				m.panelScrollY += 3
				return true, m, nil
			}
		}
		if m.paletteOpen {
			zone, found := m.mouseZones.findZone(msg.Y, msg.X)
			if found && zone.ID == "paletteItem" {
				maxScroll := max(0, len(m.paletteFiltered)-paletteMaxVisible)
				m.paletteScrollY = min(maxScroll, m.paletteScrollY+1)
				return true, m, nil
			}
		}
		if m.rewindMode {
			zone, found := m.mouseZones.findZone(msg.Y, msg.X)
			if found && zone.ID == "rewindItem" {
				if m.rewindCursor < len(m.rewindItems)-1 {
					m.rewindCursor++
				}
				return true, m, nil
			}
		}
		return false, m, nil
	}
	return false, m, nil
}

// --- Panel click handlers ---

// commitPanelEdit saves the current edit value back to panelValues.
func (m *cliModel) commitPanelEdit() {
	if !m.panelEdit || m.panelCursor >= len(m.panelSchema) {
		return
	}
	def := m.panelSchema[m.panelCursor]
	if !def.ReadOnly {
		m.panelValues[def.Key] = strings.TrimSpace(m.panelEditTA.Value())
	}
}

// clickPanelItem clicks a settings panel item by index.
// Single click: activate item (same as Enter) after moving cursor.
// If currently in edit/combo mode, close the overlay first.
func (m *cliModel) clickPanelItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode == "settings" && idx < len(m.panelSchema) {
		// Close any active overlay before switching to a new item
		if m.panelEdit || m.panelCombo {
			m.commitPanelEdit()
			m.panelEdit = false
			m.panelCombo = false
		}
		m.panelCursor = idx
		return m.activatePanelItem()
	}
	// For other panel modes that use panelItem zones
	m.panelCursor = idx
	return true, m, nil
}

// clickPanelToggle handles clicking a toggle setting.
func (m *cliModel) clickPanelToggle(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "settings" || idx >= len(m.panelSchema) {
		return false, m, nil
	}
	// Close any active overlay first
	if m.panelEdit || m.panelCombo {
		m.commitPanelEdit()
		m.panelEdit = false
		m.panelCombo = false
	}
	def := m.panelSchema[idx]
	if def.ReadOnly || def.Type != SettingTypeToggle {
		return false, m, nil
	}
	cur := m.panelValues[def.Key]
	m.panelValues[def.Key] = toggleVal(cur)
	return true, m, nil
}

// clickPanelOpenURL handles clicking the "获取密钥" button.
// Opens the provider's API key management page in the default browser.
func (m *cliModel) clickPanelOpenURL() (bool, tea.Model, tea.Cmd) {
	provider := m.panelValues["llm_provider"]
	guide, ok := ProviderSetupGuides[provider]
	if !ok || guide.URL == "" {
		return true, m, nil
	}
	_ = openBrowser(guide.URL)
	return true, m, nil
}

// clickPanelSave handles clicking the "保存设置" button.
func (m *cliModel) clickPanelSave() (bool, tea.Model, tea.Cmd) {
	onSubmit := m.panelOnSubmit
	panelVals := m.panelValues
	m.closePanel()
	if onSubmit != nil && panelVals != nil {
		m.settingsSaving = true
		return true, m, m.doSaveSettings(onSubmit, panelVals)
	}
	return true, m, nil
}

// clickPanelCancel handles clicking the "取消" button.
func (m *cliModel) clickPanelCancel() (bool, tea.Model, tea.Cmd) {
	m.closePanel()
	return true, m, nil
}

// clickPanelCombo handles clicking a combo/select setting.
func (m *cliModel) clickPanelCombo(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "settings" || idx >= len(m.panelSchema) {
		return false, m, nil
	}
	// Close any active overlay first (unless clicking same combo to toggle off)
	def := m.panelSchema[idx]
	if def.ReadOnly || (def.Type != SettingTypeCombo && def.Type != SettingTypeSelect) {
		return false, m, nil
	}
	m.panelCursor = idx
	if m.panelCombo && m.panelCursor == idx {
		// Click same combo again to close
		m.panelCombo = false
		return true, m, nil
	}
	// Close previous edit if any
	if m.panelEdit {
		m.commitPanelEdit()
		m.panelEdit = false
	}
	m.panelCombo = true
	m.panelComboIdx = 0
	// Pre-select current value
	cur := m.panelValues[def.Key]
	for i, opt := range def.Options {
		if opt.Value == cur {
			m.panelComboIdx = i
			break
		}
	}
	extraLines := 2 + min(len(def.Options), 8)
	m.ensureSettingsCursorVisible(extraLines)
	return true, m, nil
}

// clickPanelComboItem handles clicking an item in an open combo dropdown.
func (m *cliModel) clickPanelComboItem(optIdx int) (bool, tea.Model, tea.Cmd) {
	if !m.panelCombo || m.panelCursor >= len(m.panelSchema) {
		return false, m, nil
	}
	def := m.panelSchema[m.panelCursor]
	if optIdx < len(def.Options) {
		m.panelValues[def.Key] = def.Options[optIdx].Value
		m.panelCombo = false
	}
	return true, m, nil
}

// activatePanelItem simulates pressing Enter on the current panel cursor item.
// Handles both regular setting types and special entries (runner, danger, subscription).
func (m *cliModel) activatePanelItem() (bool, tea.Model, tea.Cmd) {
	if m.panelCursor >= len(m.panelSchema) {
		return false, m, nil
	}
	def := m.panelSchema[m.panelCursor]
	if def.ReadOnly {
		return true, m, nil
	}

	// Special entries — same logic as keyboard Enter in updateSettingsPanel
	switch def.Key {
	case "runner_panel":
		m.pushPanel()
		m.openRunnerPanel()
		return true, m, nil
	case "danger_zone":
		m.pushPanel()
		m.openDangerPanelFromSettings()
		return true, m, nil
	case "subscription_manage":
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
		cur := m.panelValues[def.Key]
		m.panelValues[def.Key] = toggleVal(cur)
		return true, m, nil
	case SettingTypeSelect:
		opts := def.Options
		if len(opts) == 0 {
			return true, m, nil
		}
		cur := m.panelValues[def.Key]
		for i, opt := range opts {
			if opt.Value == cur {
				next := (i + 1) % len(opts)
				m.panelValues[def.Key] = opts[next].Value
				break
			}
		}
		return true, m, nil
	case SettingTypeCombo:
		if m.panelCombo {
			m.panelCombo = false
		} else if len(def.Options) > 0 {
			m.panelCombo = true
			m.panelComboIdx = 0
			extraLines := 2 + min(len(def.Options), 8)
			m.ensureSettingsCursorVisible(extraLines)
		}
		return true, m, nil
	default:
		// text/number/password/textarea: enter edit mode
		m.panelEdit = true
		m.panelEditTA = m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
		m.panelEditTA.Focus()
		m.ensureSettingsCursorVisible(3)
		return true, m, nil
	}
}

// --- AskUser click handlers ---

func (m *cliModel) clickAskUserOption(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "askuser" || m.panelTab >= len(m.panelItems) {
		return false, m, nil
	}
	item := m.panelItems[m.panelTab]
	if idx >= len(item.Options) {
		return false, m, nil
	}
	// Toggle selection
	if m.panelOptSel[m.panelTab] == nil {
		m.panelOptSel[m.panelTab] = make(map[int]bool)
	}
	m.panelOptSel[m.panelTab][idx] = !m.panelOptSel[m.panelTab][idx]
	return true, m, nil
}

func (m *cliModel) clickAskUserTab(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "askuser" || idx >= len(m.panelItems) {
		return false, m, nil
	}
	m.panelTab = idx
	return true, m, nil
}

func (m *cliModel) clickAskUserSubmit() (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "askuser" || m.panelOnAnswer == nil {
		return false, m, nil
	}
	// Reuse the same submission logic as keyboard Enter (collectAskAnswers
	// uses "q%d" keys matching what panelOnAnswer callbacks expect).
	return m.submitAskAnswers()
}

// --- Palette click handlers ---

func (m *cliModel) clickPaletteItem(idx int) (bool, tea.Model, tea.Cmd) {
	if !m.paletteOpen || idx >= len(m.paletteFiltered) {
		return false, m, nil
	}
	m.paletteCursor = idx + m.paletteScrollY
	if m.paletteCursor >= len(m.paletteFiltered) {
		m.paletteCursor = len(m.paletteFiltered) - 1
	}
	// Execute the command (same as Enter)
	m.applyPaletteCommand()
	return true, m, nil
}

func (m *cliModel) clickPaletteTab(idx int) (bool, tea.Model, tea.Cmd) {
	if !m.paletteOpen {
		return false, m, nil
	}
	// Tab line is a single zone; clicking it cycles to the next category
	// (same behavior as pressing Tab key).
	m.cyclePaletteCategory(1)
	return true, m, nil
}

// --- QuickSwitch click handler ---

func (m *cliModel) clickQuickSwitchItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.quickSwitchMode == "" || idx >= len(m.quickSwitchList) {
		return false, m, nil
	}
	m.quickSwitchCursor = idx
	// Use the same full apply logic as keyboard Enter (handles
	// subscription switch, model switch, async LLM creation, status bar update).
	m.applyQuickSwitch()
	if len(m.pendingCmds) > 0 {
		pending := m.pendingCmds
		m.pendingCmds = nil
		return true, m, tea.Batch(pending...)
	}
	return true, m, nil
}

// --- Rewind click handler ---

func (m *cliModel) clickRewindItem(idx int) (bool, tea.Model, tea.Cmd) {
	if !m.rewindMode || idx >= len(m.rewindItems) {
		return false, m, nil
	}
	m.rewindCursor = idx
	// Execute rewind (same as Enter)
	return m.executeRewind()
}

func (m *cliModel) executeRewind() (bool, tea.Model, tea.Cmd) {
	if m.rewindCursor >= len(m.rewindItems) {
		return true, m, nil
	}
	item := m.rewindItems[m.rewindCursor]
	if m.trimHistoryFn != nil {
		if err := m.trimHistoryFn(item.Time); err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Rewind failed: %v", err), feedbackError)
		} else {
			if m.resetTokenStateFn != nil {
				m.resetTokenStateFn()
			}
			m.showSystemMsg("✅ Rewound to selected point", feedbackInfo)
		}
	}
	m.rewindMode = false
	m.rewindResult = nil
	m.updateViewportContent()
	return true, m, nil
}

// --- Approval click handler ---

func (m *cliModel) clickApprovalBtn(idx int) (bool, tea.Model, tea.Cmd) {
	if m.approvalRequest == nil {
		return false, m, nil
	}
	if idx == 0 {
		// Approve
		m.approvalResultCh <- protocol.ApprovalResult{Approved: true}
		m.approvalRequest = nil
		m.panelMode = ""
		return true, m, nil
	}
	if idx == 1 {
		// Deny
		if m.approvalEnteringDeny {
			// Submit deny with reason
			reason := m.approvalDenyInput.Value()
			m.approvalResultCh <- protocol.ApprovalResult{Approved: false, DenyReason: reason}
			m.approvalRequest = nil
			m.panelMode = ""
			return true, m, nil
		}
		m.approvalEnteringDeny = true
		m.approvalDenyInput.Focus()
		m.approvalCursor = 1
		return true, m, nil
	}
	return false, m, nil
}

// --- Textarea click handler ---

func (m *cliModel) clickTextarea(x, y int) (bool, tea.Model, tea.Cmd) {
	// x is terminal column (from msg.X). Convert to textarea-relative column.
	// xShift is the actual rendered sidebar width (measured dynamically to account
	// for character width variations like RUNEWIDTH_EASTASIAN).
	// InputBox content offset = visual width of border-left char + padding-left.
	// Border char width varies: │ is width=1 normally, width=2 with EASTASIAN.
	inputBox := m.styles.InputBox
	borderLeftVisW := lipgloss.Width(lipgloss.RoundedBorder().Left)
	contentOffset := borderLeftVisW + inputBox.GetPaddingLeft()
	contentX := x - m.xShift - contentOffset
	if contentX < 0 {
		contentX = 0
	}
	// y is relative to the InputBox zone top (visual line 0 of the viewport).
	// ClickAt expects an absolute visual line index in the textarea content,
	// so add the viewport's scroll offset. Without this, clicking when the
	// textarea is scrolled down targets the wrong line.
	y = y + m.textarea.ScrollYOffset()
	m.textarea.ClickAt(contentX, y)
	return true, m, nil
}

func (m *cliModel) clickPanelTextarea(x, y int) (bool, tea.Model, tea.Cmd) {
	// Click on panel edit textarea
	if m.panelEdit {
		contentX := x - 1
		if contentX < 0 {
			contentX = 0
		}
		y = y + m.panelEditTA.ScrollYOffset()
		m.panelEditTA.ClickAt(contentX, y)
	}
	return true, m, nil
}

// --- Completions click handler ---

func (m *cliModel) clickCompletionsItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.fileCompActive {
		if idx < len(m.fileCompletions) {
			m.fileCompIdx = idx
			input := m.textarea.Value()
			selected := m.fileCompletions[m.fileCompIdx]
			if isDir(selected) {
				selected += "/"
			}
			_, prefix := detectAtPrefix(input)
			atStart := len(input) - len(prefix) - 1
			newInput := input[:atStart] + "@" + selected
			m.textarea.SetValue(newInput)
		}
	} else {
		if idx < len(m.completions) {
			m.compIdx = idx
			m.textarea.SetValue(m.completions[m.compIdx] + " ")
		}
	}
	return true, m, nil
}

// --- Sessions click handler ---

func (m *cliModel) clickSessionsItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "sessions" || idx >= len(m.panelSessionItems) {
		return false, m, nil
	}
	m.panelSessionCursor = idx
	return true, m, nil
}

// --- BgTasks click handler ---

func (m *cliModel) clickBgTasksItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "bgtasks" {
		return false, m, nil
	}
	m.panelBgCursor = idx
	return true, m, nil
}

// --- Danger click handler ---

func (m *cliModel) clickDangerItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "danger" || idx >= len(m.panelDangerItems) {
		return false, m, nil
	}
	m.panelDangerCursor = idx
	return true, m, nil
}

// --- Channel click handler ---

func (m *cliModel) clickChannelItem(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "channel" || idx >= len(m.panelChannelItems) {
		return false, m, nil
	}
	m.panelChannelCursor = idx
	return true, m, nil
}

// --- Runner field click handler ---

func (m *cliModel) clickRunnerField(idx int) (bool, tea.Model, tea.Cmd) {
	if m.panelMode != "runner" {
		return false, m, nil
	}
	// Focus the clicked textinput field
	m.panelRunnerEditField = idx
	// Blur all fields, focus selected one
	m.panelRunnerServerTI.Blur()
	m.panelRunnerTokenTI.Blur()
	m.panelRunnerWorkspace.Blur()
	switch idx {
	case 0:
		m.panelRunnerServerTI.Focus()
	case 1:
		m.panelRunnerTokenTI.Focus()
	case 2:
		m.panelRunnerWorkspace.Focus()
	}
	return true, m, nil
}

// --- View zone tracking helpers ---
// These are called from View() to record interactive regions.

// trackMainLayoutZones records zones for the main chat layout.
// y is the current Y position in the output.
// Returns the total height consumed.
func (m *cliModel) trackMainLayoutZones(zb *mouseZoneBuilder) {
	// titleBar: 1 line (not interactive)
	zb.skip(1)

	// viewport: layoutViewportHeight() lines (wheel handled by viewport automatically)
	viewportH := m.layoutViewportHeight()

	// If sidebar is visible, register session item zones and new-session button.
	showSidebar := m.sidebarShown()
	// xShift: when sidebar is on the left, all middleBlock content is shifted right
	// by the actual rendered sidebar width (depends on char widths, e.g. EASTASIAN).
	xShift := 0
	if showSidebar {
		// sidebarContentOffset: border left + padding left (in visual columns).
		// Border char width varies with EASTASIAN: │ is width=1 normally, width=2 with EASTASIAN.
		sbStyle := m.styles.SidebarBg
		sbBorderLeftVisW := lipgloss.Width(lipgloss.RoundedBorder().Left)
		sidebarContentOffset := sbBorderLeftVisW + sbStyle.GetPaddingLeft()
		sbVisW := m.sidebarRenderedWidth() // actual visual width (accounts for EASTASIAN etc.)

		// Helper to register section header zones
		registerSectionHeaders := func(xStart, xEnd int, borderOffset int) {
			for section, relY := range sidebarSectionHeaders {
				zoneID := "sidebarSectionHeader:" + section
				zb.addX(relY+borderOffset, xStart, xEnd, zoneID, 0)
			}
		}

		if m.sidebarPosition == "right" {
			// sidebar on right: middleBlock starts at 0, sidebar starts at chatWidth
			sbXStart := m.chatWidth()
			sbXEnd := m.width
			borderOffset := 1 // RoundedBorder top edge
			registerSectionHeaders(sbXStart, sbXEnd, borderOffset)
			for relY, sessionIdx := range sidebarSessionLines {
				if sessionIdx >= 0 {
					zb.addX(relY+borderOffset, sbXStart, sbXEnd, "sidebarSession", sessionIdx)
				}
				if relY < len(sidebarDeleteXStart) && sidebarDeleteXStart[relY] >= 0 {
					zb.addX(relY+borderOffset, sbXStart+sidebarContentOffset+sidebarDeleteXStart[relY], sbXStart+sidebarContentOffset+sidebarDeleteXEnd[relY], "sidebarDeleteSession", sessionIdx)
				}
			}
			if sidebarNewSessionY >= 0 {
				zb.addX(sidebarNewSessionY+borderOffset, sbXStart, sbXEnd, "sidebarNewSession", 0)
			}
			// Bg task zones in sidebar's Active section
			if sidebarActiveSectionOffset >= 0 {
				for relY, taskIdx := range sidebarBgTaskLines {
					if taskIdx >= 0 {
						absY := sidebarActiveSectionOffset + 1 + relY // +1 for header line
						zb.addX(absY+borderOffset, sbXStart, sbXEnd, "sidebarBgTask", taskIdx)
					}
				}
			}
		} else {
			// sidebar on left: middleBlock starts at sbVisW
			borderOffset := 1 // RoundedBorder top edge
			registerSectionHeaders(0, sbVisW, borderOffset)
			for relY, sessionIdx := range sidebarSessionLines {
				if sessionIdx >= 0 {
					zb.addX(relY+borderOffset, 0, sbVisW, "sidebarSession", sessionIdx)
				}
				if relY < len(sidebarDeleteXStart) && sidebarDeleteXStart[relY] >= 0 {
					zb.addX(relY+borderOffset, sidebarContentOffset+sidebarDeleteXStart[relY], sidebarContentOffset+sidebarDeleteXEnd[relY], "sidebarDeleteSession", sessionIdx)
				}
			}
			if sidebarNewSessionY >= 0 {
				zb.addX(sidebarNewSessionY+borderOffset, 0, sbVisW, "sidebarNewSession", 0)
			}
			// Bg task zones in sidebar's Active section
			if sidebarActiveSectionOffset >= 0 {
				for relY, taskIdx := range sidebarBgTaskLines {
					if taskIdx >= 0 {
						absY := sidebarActiveSectionOffset + 1 + relY // +1 for header line
						zb.addX(absY+borderOffset, 0, sbVisW, "sidebarBgTask", taskIdx)
					}
				}
			}
			xShift = sbVisW
		}
	}
	m.xShift = xShift

	zb.skip(viewportH)

	// status bar: 1 line — track clickable model name and "new content" hint
	// Model name zone is tracked in both ready and progress status bars.
	if m.modelNameZoneXStart >= 0 && m.modelNameZoneXEnd > m.modelNameZoneXStart {
		zb.addX(0, m.modelNameZoneXStart+xShift, m.modelNameZoneXEnd+xShift, "modelName", 0)
	}
	if m.newContentHintRendered != "" {
		// The new content hint is rendered inline in the status bar.
		// Use the pre-calculated X position from layoutMain.
		hintW := lipgloss.Width(m.newContentHintRendered)
		hintStartX := xShift + m.newContentHintXStart
		hintEndX := hintStartX + hintW
		zb.addX(0, hintStartX, hintEndX, "scrollToBottom", 0)
		zb.y++
	} else {
		zb.skip(1)
	}

	// todo bar: variable (only when sidebar is NOT visible — todo moves to sidebar)
	if !showSidebar {
		todoBar := m.renderTodoBar()
		if todoBar != "" {
			zb.skip(strings.Count(todoBar, "\n") + 1)
		}
	}

	// footer: 0 or 1 line
	footer := m.renderFooter()
	footer = m.augmentFooter(footer)
	if footer != "" {
		// Register footer hint zones (inline clickable regions)
		for i, h := range footerHints {
			if h.xStart >= 0 && h.xEnd > h.xStart {
				zb.addX(0, h.xStart+xShift, h.xEnd+xShift, "footerHint", i)
			}
		}
		zb.y++ // advance past footer line
	}

	// Input box: border top + textarea height + border bottom
	zb.skip(1) // top border (or context bar replacement)

	// Textarea content lines — interactive (click to position cursor).
	// Full-row zone (XStart=-1): matches any X at textarea Y level.
	// Coordinate offset is computed via m.xShift + InputBox border in clickTextarea.
	taH := m.textarea.Height()
	if taH < 1 {
		taH = 1
	}
	zb.add(taH, "textarea", 0)

	zb.skip(1) // bottom border

	// Completions popup (if visible)
	if len(m.completions) > 0 || len(m.fileCompletions) > 0 {
		items := m.completions
		if m.fileCompActive {
			items = m.fileCompletions
		}
		compH := min(len(items), 8)
		for i := 0; i < compH; i++ {
			zb.add(1, "completionsItem", i)
		}
	}

	// info bar: 0 or 1 line
	infoBar := m.renderInfoBar()
	infoBar = m.augmentInfoBar(infoBar)
	if infoBar != "" {
		zb.skip(1)
	}
}

// trackPanelZones records zones for the generic panel layout.
func (m *cliModel) trackPanelZones(zb *mouseZoneBuilder) {
	// titleBar: 1 line
	zb.skip(1)

	// PanelBox top border: 1 line
	zb.skip(1)

	// Panel content — record zones based on panel type
	visibleH := m.panelVisibleHeight()
	contentStartY := zb.y

	switch m.panelMode {
	case "settings":
		m.trackSettingsZones(zb, visibleH, contentStartY)
	case "sessions":
		m.trackSessionsZones(zb, visibleH)
	case "bgtasks":
		m.trackBgTasksZones(zb, visibleH)
	case "danger":
		m.trackDangerZones(zb, visibleH)
	case "channel":
		m.trackChannelZones(zb, visibleH)
	case "runner":
		m.trackRunnerZones(zb, visibleH)
	case "approval":
		m.trackApprovalZones(zb, visibleH)
	default:
		// Generic: skip the content area
		zb.skip(visibleH)
	}

	// Ensure we consumed at least visibleH lines
	consumed := zb.y - contentStartY
	if consumed < visibleH {
		zb.skip(visibleH - consumed)
	}

	// PanelBox bottom border: 1 line
	zb.skip(1)

	// Panel footer: 0 or 1 line
	footer := m.renderFooter()
	if footer != "" {
		zb.skip(1)
	}

	_ = contentStartY // suppress unused warning
}

// trackSettingsZones records zones for settings panel items.
// The rendering order is: header(1 line) + divider(1 line) + [category(2 lines) + items(1 line each)]...
// Zones must account for scroll offset (panelScrollY).
func (m *cliModel) trackSettingsZones(zb *mouseZoneBuilder, visibleH, contentStartY int) {
	scrollY := m.panelScrollY

	// Build the complete line map (same logic as viewSettingsPanel)
	type lineInfo struct {
		isItem       bool
		itemIndex    int    // >= 0: schema item, < 0: combo option (-(optIdx+1))
		isButton     bool   // true for clickable button lines
		buttonAction string // "openURL", "save"
		buttonURL    string // URL to open (for openURL action)
	}

	var lines []lineInfo

	// Header line
	lines = append(lines, lineInfo{})
	// Divider line
	lines = append(lines, lineInfo{})

	lastCat := ""
	for i := range m.panelSchema {
		def := m.panelSchema[i]
		if def.Category != lastCat {
			lastCat = def.Category
			lines = append(lines, lineInfo{}) // blank line
			lines = append(lines, lineInfo{}) // category header
		}
		lines = append(lines, lineInfo{isItem: true, itemIndex: i})

		// Description lines shown when cursor is on this field.
		if i == m.panelCursor && def.Description != "" {
			descLines := strings.Count(def.Description, "\n") + 1
			for k := 0; k < descLines; k++ {
				lines = append(lines, lineInfo{})
			}
		}

		// API Key field: always show "获取密钥" button line.
		if def.Key == "llm_api_key" {
			provider := m.panelValues["llm_provider"]
			if provider != "" {
				guide, hasGuide := ProviderSetupGuides[provider]
				if hasGuide && guide.URL != "" {
					lines = append(lines, lineInfo{isButton: true, buttonAction: "openURL", buttonURL: guide.URL})
				} else if hasGuide && guide.URL == "" {
					lines = append(lines, lineInfo{}) // info text (Ollama)
				}
			}
		}

		// Inline overlay: combo/edit rendered right after cursor item (Crush-style)
		if i == m.panelCursor {
			if m.panelEdit {
				lines = append(lines, lineInfo{}) // input line
				lines = append(lines, lineInfo{}) // hint line
				lines = append(lines, lineInfo{}) // trailing newline
			} else if m.panelCombo && len(def.Options) > 0 {
				maxShow := 8
				start := 0
				if m.panelComboIdx >= maxShow {
					start = m.panelComboIdx - maxShow + 1
				}
				end := min(start+maxShow, len(def.Options))
				for j := start; j < end; j++ {
					lines = append(lines, lineInfo{isItem: true, itemIndex: -(j + 1)})
					// Selected combo option may show a description line.
					if j == m.panelComboIdx && def.Options[j].Description != "" {
						lines = append(lines, lineInfo{})
					}
				}
				lines = append(lines, lineInfo{}) // hint line
			}
		}
	}

	// Bottom buttons (when no overlay active)
	if !m.panelEdit && !m.panelCombo {
		lines = append(lines, lineInfo{})                                     // blank line
		lines = append(lines, lineInfo{isButton: true, buttonAction: "save"}) // save+cancel buttons row
		lines = append(lines, lineInfo{})                                     // keyboard hint line
	}

	// Now apply scroll offset and track zones
	for ln := scrollY; ln < len(lines) && zb.y < contentStartY+visibleH; ln++ {
		info := lines[ln]
		if info.isItem {
			if info.itemIndex >= 0 {
				def := m.panelSchema[info.itemIndex]
				zoneID := "panelItem"
				switch def.Type {
				case SettingTypeToggle:
					zoneID = "panelToggle"
				case SettingTypeCombo:
					zoneID = "panelCombo"
				}
				zb.add(1, zoneID, info.itemIndex)
			} else {
				// Combo dropdown item (negative index)
				zb.add(1, "panelComboItem", -(info.itemIndex + 1))
			}
		} else if info.isButton {
			switch info.buttonAction {
			case "openURL":
				zb.add(1, "panelOpenURL", 0) // URL stored in lineInfo, looked up at click time
			case "save":
				// Save button: left portion of the line
				// Cancel button: right portion
				// Approximate positions — save is at x=2..20, cancel at x=24..36
				zb.addX(0, 2, 22, "panelSave", 0)
				zb.addX(0, 24, 38, "panelCancel", 0)
				zb.skip(1)
			default:
				zb.skip(1)
			}
		} else {
			zb.skip(1)
		}
	}
}

// trackSessionsZones records zones for sessions panel.
// Rendering order: header+help(1 line) + [deleteConfirm(1 line)] + items(1 line each).
// Zones account for scroll offset (panelScrollY).
func (m *cliModel) trackSessionsZones(zb *mouseZoneBuilder, visibleH int) {
	scrollY := m.panelScrollY
	lineIdx := 0

	// Header + help line — always advance zb.y, only add zone if visible
	if lineIdx >= scrollY {
		zb.skip(1)
	} else {
		zb.skip(1) // scrolled out — must still advance zb.y
	}
	lineIdx++

	// Delete confirmation (if shown)
	if m.panelSessionConfirmDelete {
		if lineIdx >= scrollY {
			zb.skip(1)
		} else {
			zb.skip(1)
		}
		lineIdx++
	}

	for i := range m.panelSessionItems {
		if lineIdx >= scrollY {
			zb.add(1, "sessionsItem", i)
		} else {
			zb.skip(1) // scrolled out — must still advance zb.y
		}
		lineIdx++
	}
}

// trackBgTasksZones records zones for background tasks panel.
// Rendering: header+help(1 line) + items(1 line each).
func (m *cliModel) trackBgTasksZones(zb *mouseZoneBuilder, visibleH int) {
	// Header + help line
	zb.skip(1)
	if m.panelBgViewing {
		// Log view: header only, log lines are not clickable
		return
	}
	for i := range m.panelBgTasks {
		zb.add(1, "bgtaskItem", i)
	}
}

// trackDangerZones records zones for danger zone panel.
// Selection mode: header(1 line) + items(1 line each).
// Confirm mode: header(1 line) + 4 info lines + input zone.
func (m *cliModel) trackDangerZones(zb *mouseZoneBuilder, visibleH int) {
	// Header line
	zb.skip(1)
	if m.panelDangerConfirm {
		// Confirm sub-mode: 4 info lines + input line
		zb.skip(4) // confirm text, desc, blank, type prompt
		zb.add(1, "dangerInput", 0)
	} else {
		// Selection mode
		for i := range m.panelDangerItems {
			zb.add(1, "dangerItem", i)
		}
	}
}

// trackChannelZones records zones for channel config panel.
// Rendering: header+help+"\n\n"(3 lines consumed by header block) + items(1 line each).
func (m *cliModel) trackChannelZones(zb *mouseZoneBuilder, visibleH int) {
	// header+help line + empty line from "\n\n"
	zb.skip(2)
	for i := range m.panelChannelItems {
		zb.add(1, "channelItem", i)
	}
}

// trackRunnerZones records zones for runner panel fields.
// Disconnected mode: header(1 line) + blank(1 line) + 3 fields × 2 lines (label + input).
// Connected/Connecting mode: header(1 line) only, no clickable zones.
func (m *cliModel) trackRunnerZones(zb *mouseZoneBuilder, visibleH int) {
	// Header line
	zb.skip(1)

	var status RunnerStatus
	if m.runnerBridge != nil {
		status = m.runnerBridge.Status()
	}
	if status != RunnerDisconnected {
		// Connected/connecting: no clickable fields
		return
	}

	// Blank line after header
	zb.skip(1)

	// 3 fields: label(1 line) + input(1 line) each
	for i := 0; i < 3; i++ {
		zb.skip(1)                  // label line
		zb.add(1, "runnerField", i) // input line
	}
}

// trackApprovalZones records zones for approval dialog.
// Rendering varies: header + question lines + approve/deny buttons.
func (m *cliModel) trackApprovalZones(zb *mouseZoneBuilder, visibleH int) {
	// Approval dialog has a custom layout; skip all for now
	// (approval is typically short-lived, not worth precise tracking)
}

// trackOverlayZones computes zones for overlay content (palette/quickSwitch/rewind).
// Overlays replace the main content, so we rebuild zones from scratch.
func (m *cliModel) trackOverlayZones(zb *mouseZoneBuilder) {
	if m.paletteOpen {
		m.trackPaletteZones(zb)
		return
	}
	if m.quickSwitchMode != "" {
		m.trackQuickSwitchZones(zb)
		return
	}
	if m.rewindMode {
		m.trackRewindZones(zb)
		return
	}
}

// trackPaletteZones records zones for the command palette overlay.
func (m *cliModel) trackPaletteZones(zb *mouseZoneBuilder) {
	// Count blank lines for centering
	totalLines := 0
	// Header + tabs + search + separator + items + footer
	nonEmpty := make(map[PaletteCategory]bool)
	for _, cmd := range m.paletteItems {
		nonEmpty[cmd.Category] = true
	}
	tabCount := 0
	for _, cat := range paletteCategories {
		if nonEmpty[cat] {
			tabCount++
		}
	}
	totalLines = 1 + // header
		1 + // tabs (if >1 category)
		1 + // search input
		1 + // separator
		min(len(m.paletteFiltered), paletteMaxVisible) + // items
		1 + // scroll indicator or empty message
		1 // footer
	if tabCount <= 1 {
		totalLines-- // no tabs line
	}
	totalH := totalLines + 2 // +2 for box border
	blankLines := max(0, (m.height-totalH)/2)

	zb.skip(blankLines) // blank lines for centering
	zb.skip(1)          // PanelBox top border
	zb.skip(1)          // header
	if tabCount > 1 {
		// The entire tab line is one line with multiple clickable segments.
		// Track it as a single zone (coarse: whole line = tab 0 for simplicity).
		zb.add(1, "paletteTab", 0)
	}
	zb.skip(1) // search input line
	zb.skip(1) // separator

	// Command items
	for i := 0; i < min(len(m.paletteFiltered), paletteMaxVisible); i++ {
		zb.add(1, "paletteItem", i)
	}

	// Scroll indicator / empty message / footer
	remainingLines := totalLines - (4 + min(len(m.paletteFiltered), paletteMaxVisible))
	if tabCount <= 1 {
		remainingLines++
	}
	if remainingLines > 0 {
		zb.skip(remainingLines)
	}
	zb.skip(1) // PanelBox bottom border
}

// trackQuickSwitchZones records zones for the quick switch overlay.
func (m *cliModel) trackQuickSwitchZones(zb *mouseZoneBuilder) {
	// Count separator line (present when __add__ entry exists)
	sepLines := 0
	for _, s := range m.quickSwitchList {
		if s.ID == "__add__" {
			sepLines = 1
			break
		}
	}
	totalLines := 2 + len(m.quickSwitchList) + sepLines // header + spacer + items + separator
	// Match viewQuickSwitch's centering formula exactly (listH = N+3+sepLines).
	totalH := totalLines + 1 // (2+N+sepLines)+1 = N+3+sepLines = view's listH
	blankLines := max(0, (m.height-totalH)/2)

	zb.skip(blankLines)
	zb.skip(1) // PanelBox top border
	zb.skip(1) // header
	zb.skip(1) // spacer

	for i, s := range m.quickSwitchList {
		if s.ID == "__add__" && i > 0 {
			zb.skip(1) // separator line before __add__
		}
		zb.add(1, "quickSwitchItem", i)
	}

	zb.skip(1) // PanelBox bottom border
	zb.skip(1) // hint line
}

// trackRewindZones records zones for the rewind overlay.
func (m *cliModel) trackRewindZones(zb *mouseZoneBuilder) {
	total := len(m.rewindItems)
	maxVisible := m.height - 10
	if maxVisible < 3 {
		maxVisible = 3
	}
	visibleItems := min(total, maxVisible)

	totalLines := 3 + visibleItems // header + hint + spacer + items
	scrollLines := 0
	if total > maxVisible {
		scrollLines = 1
	}
	totalH := totalLines + scrollLines + 2 + 1 // +2 border + 1 hint
	blankLines := max(0, (m.height-totalH)/2)

	zb.skip(blankLines)
	zb.skip(1) // PanelBox top border
	zb.skip(1) // header
	zb.skip(1) // hint
	zb.skip(1) // spacer

	scrollStart := 0
	if total > maxVisible {
		scrollStart = m.rewindCursor - maxVisible/2
		if scrollStart < 0 {
			scrollStart = 0
		}
		scrollEnd := scrollStart + maxVisible
		if scrollEnd > total {
			scrollEnd = total
			scrollStart = scrollEnd - maxVisible
		}
	}
	for i := 0; i < visibleItems; i++ {
		zb.add(1, "rewindItem", scrollStart+i)
	}
	if scrollLines > 0 {
		zb.skip(scrollLines)
	}
	zb.skip(1) // PanelBox bottom border
	zb.skip(1) // hint line
}

// trackAskUserZones records zones for the askuser split layout.
func (m *cliModel) trackAskUserZones(zb *mouseZoneBuilder) {
	// titleBar: 1 line
	zb.skip(1)

	// viewport: full height minus panel — mark as askViewport for wheel routing
	viewportH := m.layoutViewportHeight()
	zb.add(viewportH, "askViewport", 0)

	// AskUser panel in PanelBox
	zb.skip(1) // PanelBox top border

	// Panel content zones
	m.trackAskUserContentZones(zb)

	zb.skip(1) // PanelBox bottom border

	// Scroll hint (optional, no zone needed)
}

func (m *cliModel) trackAskUserContentZones(zb *mouseZoneBuilder) {
	if len(m.panelItems) == 0 {
		return
	}

	// Build complete line map matching viewAskUserPanel() output order.
	// Each line has an optional zone to register at that position.
	type askLine struct {
		zoneID    string
		index     int
		isTabLine bool // special: all tabs on one line, use addX
	}
	var lines []askLine

	// Tab bar (if multiple questions): all tabs rendered on ONE line by viewAskUserPanel().
	if len(m.panelItems) > 1 {
		lines = append(lines, askLine{isTabLine: true})
		lines = append(lines, askLine{}) // blank line ("\n\n" → 1 blank after tab line)
	}

	// Current tab content
	if m.panelTab >= 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		// Question text (may wrap to multiple lines — not tracked as zones)
		// viewAskUserPanel uses hardWrapRunes with qWrapWidth. We approximate.
		prefix := "❓ " + item.Question
		qWrapWidth := m.width - 6
		if qWrapWidth < 20 {
			qWrapWidth = 20
		}
		questionLines := max(1, strings.Count(hardWrapRunes(prefix, qWrapWidth), "\n")+1)
		for i := 0; i < questionLines; i++ {
			lines = append(lines, askLine{})
		}
		// ONE blank line after question (viewAskUserPanel writes "\n" to end the
		// question, then another "\n" for hasOpts/textarea = 1 blank line total).
		lines = append(lines, askLine{})

		if len(item.Options) > 0 {
			// Option items
			for i := range item.Options {
				lines = append(lines, askLine{zoneID: "askUserOption", index: i})
			}
			// "Other" input (not tracked as click zone — textinput handles its own input)
			lines = append(lines, askLine{})
			// Submit button (only on last tab)
			if m.panelTab == len(m.panelItems)-1 {
				lines = append(lines, askLine{zoneID: "askUserSubmit", index: 0})
			}
		}
		// Free-input mode (no options): textarea, not tracked
	}

	// Apply scroll offset: skip lines before askPanelScrollY, stop at visible height
	scrollY := m.askPanelScrollY
	visibleH := m.askUserPanelVisibleHeight()
	end := len(lines)
	if end > scrollY+visibleH {
		end = scrollY + visibleH
	}

	for ln := scrollY; ln < end; ln++ {
		l := lines[ln]
		if l.isTabLine {
			// All tabs on one line — register each with X bounds.
			// PanelBox border(1) + padding(1) = 2 chars before content.
			x := 2
			for i := range m.panelItems {
				label := fmt.Sprintf(" %d ", i+1)
				w := len(label)
				zb.addX(0, x, x+w, "askUserTab", i)
				x += w
				if i < len(m.panelItems)-1 {
					x += 1 // separator "│"
				}
			}
			zb.skip(1) // tab line
		} else if l.zoneID != "" {
			zb.add(1, l.zoneID, l.index)
		} else {
			zb.skip(1)
		}
	}
}

// toggleVal toggles a boolean string value.
func toggleVal(s string) string {
	if s == "true" {
		return "false"
	}
	return "true"
}

// clickSidebarSession handles clicking a session item in the sidebar.
func (m *cliModel) clickSidebarSession(index int) (bool, tea.Model, tea.Cmd) {
	entries := m.sidebarSessionEntries()
	if index < 0 || index >= len(entries) {
		return false, m, nil
	}
	handled, cmd := m.switchToSession(entries[index])
	return handled, m, cmd
}

// clickSidebarNewSession handles clicking the "+ New" button in the sidebar.
func (m *cliModel) clickSidebarNewSession() (bool, tea.Model, tea.Cmd) {
	cmd := m.showSessionCreateDialog()
	return true, m, cmd
}

// clickSidebarDeleteSession handles clicking the "×" delete button on a session item.
func (m *cliModel) clickSidebarDeleteSession(index int) (bool, tea.Model, tea.Cmd) {
	entries := m.sidebarSessionEntries()
	if index < 0 || index >= len(entries) {
		return false, m, nil
	}
	entry := entries[index]
	// Don't delete the currently active session
	if entry.ID == m.chatID {
		m.showTempStatus("Cannot delete the active session")
		return true, m, nil
	}
	cmd := m.deleteLocalSession(entry)
	return true, m, cmd
}

// clickSidebarSectionHeader handles clicking a sidebar section header to toggle collapse.
// The zoneID format is "sidebarSectionHeader:<section>" where section is "sessions", "todo", or "tasks".
func (m *cliModel) clickSidebarSectionHeader(zoneID string) (bool, tea.Model, tea.Cmd) {
	const prefix = "sidebarSectionHeader:"
	if !strings.HasPrefix(zoneID, prefix) {
		return false, m, nil
	}
	section := strings.TrimPrefix(zoneID, prefix)
	m.toggleSidebarSection(section)
	return true, m, nil
}

// clickSidebarBgTask handles clicking a background task item in the sidebar.
// Opens the bgtasks panel in log-viewing mode directly.
// Pushes a main-view (mode="") entry onto the navigator stack so ESC
// closes the panel entirely instead of showing the task list.
func (m *cliModel) clickSidebarBgTask(index int) (bool, tea.Model, tea.Cmd) {
	// Open the bg tasks panel first (sets up task list, panel mode, etc.)
	m.openBgTasksPanel()

	// Validate index against the panel's task list
	if index < 0 || index >= len(m.panelBgTasks) {
		return true, m, nil
	}

	// Select the clicked task and enter log view
	m.panelBgCursor = index
	task := m.panelBgTasks[index]
	m.panelBgLogLines = sanitizeOutputLines(task.Output)
	if len(m.panelBgLogLines) == 0 {
		m.panelBgLogLines = []string{"(no output)"}
	}
	m.panelBgViewing = true
	m.panelScrollY = 0
	m.panelBgLogFollow = true

	// Push main-view onto navigator stack so ESC from log view
	// closes the panel entirely (popPanel restores mode="" = main view).
	m.panelStack = append(m.panelStack, panelStackEntry{mode: ""})

	return true, m, nil
}
