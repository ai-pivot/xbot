package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	ch "xbot/channel"
	"xbot/protocol"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// ─── Unified LLM panel ──────────────────────────────────────────────────────
//
// One panel (opened by Ctrl+N / click status-bar model / palette "Models &
// Subscriptions") consolidates everything model/subscription:
//
//	Subscriptions
//	  ▸ gpt            enabled     E edit  D delete  Enter toggle
//	    · gpt-5.2      normal      e edit  d disable Enter switch
//	    · gpt-image-1  (noise)
//	  ▸ deepseek       disabled
//	    · deepseek-v4  offline
//	  ➕ Add subscription
//	Models
//	  ... (all selectable models across subs, 订阅名 · 模型名)
//	  ➕ Add model
//
// Key scheme (command mode): letters are commands; `/` toggles filter mode so
// letters can be typed without colliding with e/d/n/s. Model params are edited
// on the model row via `e`; subscriptions only manage credentials + enabled.

type qsRowKind int

const (
	qsSection qsRowKind = iota
	qsSub
	qsModel
	qsAddSub
	qsAddModel
)

// qsRow is one line of the unified LLM panel.
type qsRow struct {
	kind  qsRowKind
	sub   ch.Subscription     // qsSub
	model protocol.ModelEntry // qsModel
	label string              // qsSection / qsAddSub / qsAddModel
}

// openQuickSwitch opens the unified LLM panel. The mode argument is kept for
// backward compatibility with existing call sites (palette / mouse / settings)
// and is ignored — there is now only one panel.
func (m *cliModel) openQuickSwitch(_ string) {
	m.openLLMPanel()
}

// openLLMPanel builds the unified panel from the DB snapshot and kicks off a
// background /models refresh.
func (m *cliModel) openLLMPanel() {
	if m.subscriptionMgr == nil {
		return
	}
	m.quickSwitchMode = "llm"
	m.quickSwitchFiltering = false
	m.quickSwitchShowAll = false
	m.quickSwitchRefreshing = true
	ti := textinput.New()
	ti.Placeholder = "Filter subscriptions / models…"
	ti.Prompt = " > "
	ti.CharLimit = 80
	ti.SetWidth(40)
	m.quickSwitchFilterInput = ti
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
	m.pendingCmds = append(m.pendingCmds, m.refreshModelEntriesCmd())
}

// llmData holds the source lists the rows are built from.
type llmData struct {
	subs    []ch.Subscription
	entries []protocol.ModelEntry
}

// llmSource fetches the current subscription + model-entry snapshot.
func (m *cliModel) llmSource() llmData {
	var d llmData
	if m.subscriptionMgr != nil {
		if subs, err := m.subscriptionMgr.List(""); err == nil {
			d.subs = subs
		}
	}
	if m.channel != nil && m.channel.modelLister != nil {
		d.entries = m.channel.modelLister.ListAllModelEntries()
	}
	return d
}

// rebuildLLMRows rebuilds quickSwitchRows from the current source + filter.
func (m *cliModel) rebuildLLMRows() {
	d := m.llmSource()
	m.quickSwitchRows = m.buildLLMRows(d)
}

// buildLLMRows assembles the flat row list (sections + subs + models + actions),
// applying the noise filter and the text filter.
func (m *cliModel) buildLLMRows(d llmData) []qsRow {
	q := strings.ToLower(strings.TrimSpace(m.quickSwitchFilterInput.Value()))
	filtering := m.quickSwitchFiltering && q != ""
	match := func(s ...string) bool {
		if !filtering {
			return true
		}
		for _, x := range s {
			if strings.Contains(strings.ToLower(x), q) {
				return true
			}
		}
		return false
	}

	var rows []qsRow

	// --- Subscriptions section ---
	subRows := make([]qsRow, 0, len(d.subs))
	for _, s := range d.subs {
		name := s.Name
		if name == "" {
			name = s.ID
		}
		if match(name) {
			subRows = append(subRows, qsRow{kind: qsSub, sub: s})
		}
	}
	if len(subRows) > 0 || !filtering {
		rows = append(rows, qsRow{kind: qsSection, label: "Subscriptions"})
	}
	rows = append(rows, subRows...)
	// "Add subscription" sits at the end of the subscription list, not at the
	// bottom of the whole panel — it groups with the section it acts on.
	if !filtering {
		rows = append(rows, qsRow{kind: qsAddSub, label: "➕ Add subscription"})
	}

	// --- Models section ---
	modelRows := make([]qsRow, 0, len(d.entries))
	for _, e := range d.entries {
		if !m.quickSwitchShowAll && isNoiseModel(e.Model) {
			continue
		}
		if match(e.SubName, e.Model, e.Status) {
			modelRows = append(modelRows, qsRow{kind: qsModel, model: e})
		}
	}
	if len(modelRows) > 0 || (!filtering && m.quickSwitchShowAll) {
		// In command mode keep the section header; when filtering, only show it
		// if there are matching model rows.
		if !filtering || len(modelRows) > 0 {
			rows = append(rows, qsRow{kind: qsSection, label: "Models"})
		}
	}
	rows = append(rows, modelRows...)
	// "Add model" sits at the end of the models list.
	if !filtering {
		rows = append(rows, qsRow{kind: qsAddModel, label: "➕ Add model"})
	}

	if m.quickSwitchCursor >= len(rows) {
		m.quickSwitchCursor = max(0, len(rows)-1)
	}
	return rows
}

// currentLLMPanelSub returns the subscription under the panel cursor: the sub
// row itself when cursor is on qsSub, or the owning subscription of the model
// row when cursor is on qsModel. Returns ok=false otherwise.
func (m *cliModel) currentLLMPanelSub() (ch.Subscription, bool) {
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		return ch.Subscription{}, false
	}
	r := m.quickSwitchRows[m.quickSwitchCursor]
	switch r.kind {
	case qsSub:
		return r.sub, true
	case qsModel:
		if r.model.SubID != "" {
			for _, s := range m.quickSwitchRows {
				if s.kind == qsSub && s.sub.ID == r.model.SubID {
					return s.sub, true
				}
			}
			// Fall back to a fresh source lookup (cursor row may be a model
			// whose sub row is filtered out).
			d := m.llmSource()
			for _, s := range d.subs {
				if s.ID == r.model.SubID {
					return s, true
				}
			}
		}
	}
	return ch.Subscription{}, false
}

// setLLMCmdForSub builds the equivalent /set-llm command string for a
// subscription, with the API key masked. Shown at the panel bottom so users
// can copy it for IM channels that have no TUI panel. A subscription is
// credentials-only (provider/base_url/api_key); model-related params are not
// part of the subscription anymore (models come from /models + model rows,
// thinking mode is a global toggle).
func setLLMCmdForSub(s ch.Subscription) string {
	key := s.APIKey
	if len(key) > 4 {
		key = key[:4] + "****"
	} else if key != "" {
		key = "****"
	}
	return fmt.Sprintf("/set-llm provider=%s base_url=%s api_key=%s", s.Provider, s.BaseURL, key)
}

// cursorToActiveLLMRow parks the cursor on the active model's row (fall back to
// the active subscription row, else the first sub row, else 0).
func (m *cliModel) cursorToActiveLLMRow() {
	if m.cachedModelName != "" {
		for i, r := range m.quickSwitchRows {
			if r.kind == qsModel && r.model.Model == m.cachedModelName {
				m.quickSwitchCursor = i
				return
			}
		}
	}
	if m.activeSubID != "" {
		for i, r := range m.quickSwitchRows {
			if r.kind == qsSub && r.sub.ID == m.activeSubID {
				m.quickSwitchCursor = i
				return
			}
		}
	}
	for i, r := range m.quickSwitchRows {
		if r.kind == qsSub {
			m.quickSwitchCursor = i
			return
		}
	}
	m.quickSwitchCursor = 0
}

// applyQuickSwitch is the Enter action on the current row.
func (m *cliModel) applyQuickSwitch() {
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		m.quickSwitchMode = ""
		return
	}
	row := m.quickSwitchRows[m.quickSwitchCursor]
	switch row.kind {
	case qsSection:
		// no-op
	case qsSub:
		m.toggleSubscription(row.sub)
	case qsModel:
		m.switchToModelRow(row.model)
	case qsAddSub:
		m.quickSwitchMode = ""
		m.openAddSubscriptionPanel()
	case qsAddModel:
		m.quickSwitchMode = ""
		m.openAddModelPanel("")
	}
}

// subIsSystem reports whether the subscription with the given ID is the shared
// read-only system subscription, according to the panel's current row cache.
func (m *cliModel) subIsSystem(subID string) bool {
	for _, r := range m.quickSwitchRows {
		if r.kind == qsSub && r.sub.ID == subID {
			return r.sub.IsSystem
		}
	}
	return false
}

// toggleSubscription flips the subscription-level enabled flag and keeps the
// panel open + refreshed.
func (m *cliModel) toggleSubscription(sub ch.Subscription) {
	if sub.IsSystem {
		m.showTempStatus("系统订阅只读，不可禁用")
		return
	}
	if m.subscriptionMgr == nil {
		return
	}
	want := !sub.Enabled
	if err := m.subscriptionMgr.SetSubscriptionEnabled(sub.ID, want); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to toggle: %v", err))
		return
	}
	verb := "Disabled"
	if want {
		verb = "Enabled"
	}
	warning := ""
	if !want && (sub.ID == m.activeSubID || (m.activeSubID == "" && sub.Active)) {
		warning = " — active session's models hidden; switch model to continue"
	}
	m.showTempStatus(fmt.Sprintf("%s: %s%s", verb, sub.Name, warning))
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
}

// switchToModelRow switches the session to the selected model (rejects disabled).
func (m *cliModel) switchToModelRow(e protocol.ModelEntry) {
	if e.Status == "disabled" {
		m.showTempStatus(fmt.Sprintf("%s is disabled — press E to enable", e.Model))
		return
	}
	m.quickSwitchMode = ""
	m.applyModelSwitch(e.Model, e.SubID)
}

// editCurrentRow handles the `e` command on the current row.
func (m *cliModel) editCurrentRow() {
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		return
	}
	row := m.quickSwitchRows[m.quickSwitchCursor]
	switch row.kind {
	case qsSub:
		if row.sub.IsSystem {
			m.showTempStatus("系统订阅只读，不可编辑")
			return
		}
		m.openEditSubscriptionPanel(row.sub.ID)
	case qsModel:
		if row.model.SubID == "" || m.subIsSystem(row.model.SubID) {
			m.showTempStatus("系统订阅模型只读，不可编辑")
			return
		}
		m.openEditModelPanel(row.model.SubID, row.model.Model)
	}
}

// disableCurrentRow handles the `d` command: model → toggle enabled, sub → delete.
func (m *cliModel) disableCurrentRow() {
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		return
	}
	row := m.quickSwitchRows[m.quickSwitchCursor]
	switch row.kind {
	case qsSub:
		if row.sub.IsSystem {
			m.showTempStatus("系统订阅只读，不可删除")
			return
		}
		m.deleteSubscription(row.sub.ID)
	case qsModel:
		if row.model.SubID == "" || m.subIsSystem(row.model.SubID) {
			return
		}
		m.toggleModelEnabled(row.model.SubID, row.model.Model, row.model.Status)
	}
}

// currentSubForAdd returns the subscription ID to prefill "add model" with,
// based on the current cursor position (sub row → that sub; model row → its
// owner; else "").
func (m *cliModel) currentSubForAdd() string {
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		return ""
	}
	row := m.quickSwitchRows[m.quickSwitchCursor]
	switch row.kind {
	case qsSub:
		return row.sub.ID
	case qsModel:
		return row.model.SubID
	}
	return ""
}

// toggleModelEnabled flips a model's enabled flag via SetModelEnabled.
func (m *cliModel) toggleModelEnabled(subID, model, status string) {
	if m.subscriptionMgr == nil {
		return
	}
	enable := status == "disabled"
	if err := m.subscriptionMgr.SetModelEnabled(subID, model, enable); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed: %v", err))
		return
	}
	verb := "Disabled"
	if enable {
		verb = "Enabled"
	}
	m.showTempStatus(fmt.Sprintf("%s: %s", verb, model))
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
}

// dateSnapRegex matches dated snapshot model ids, e.g. gpt-5.2-2025-12-11.
var dateSnapRegex = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}`)

// isNoiseModel reports whether a model id is chat-unusable noise that providers
// dump into /models (image generation, realtime/audio, speech, transcription,
// embedding, moderation, dated snapshots). Filtered out of the panel by default;
// toggle with 's' (quickSwitchShowAll).
func isNoiseModel(model string) bool {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "image"),
		strings.Contains(m, "realtime"),
		strings.Contains(m, "whisper"),
		strings.Contains(m, "tts"),
		strings.Contains(m, "transcrib"),
		strings.Contains(m, "audio"),
		strings.Contains(m, "moderation"),
		strings.Contains(m, "embed"):
		return true
	case dateSnapRegex.MatchString(model):
		return true
	}
	return false
}

// ─── Subscription edit / add / delete ───────────────────────────────────────

// addSubscriptionSchema builds the settings schema for creating a subscription.
// A subscription is credentials-only (Name/Provider/BaseURL/APIKey). Model-related
// knobs (default model, max output, thinking mode) are NOT collected here —
// models come from the provider's /models list (auto-fetched) or manual model
// rows; max output is per-model (model row E panel); thinking mode is a global
// toggle (Ctrl+M). See "订阅是订阅，模型是模型".
func addSubscriptionSchema() []ch.SettingDefinition {
	return []ch.SettingDefinition{
		{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: ch.SettingTypeText, DefaultValue: "openai"},
		{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: ch.SettingTypePassword, DefaultValue: ""},
	}
}

// openAddSubscriptionPanel opens the mini panel to create a subscription.
func (m *cliModel) openAddSubscriptionPanel() {
	if m.subscriptionMgr == nil {
		return
	}
	schema := addSubscriptionSchema()
	m.openSettingsPanel(schema, map[string]string{}, func(values map[string]string) {
		name := values["sub_name"]
		if name == "" {
			name = values["sub_provider"]
		}
		if name == "" {
			name = "unnamed"
		}
		sub := &ch.Subscription{
			ID:       fmt.Sprintf("sub_%d", time.Now().UnixNano()),
			Name:     name,
			Provider: values["sub_provider"],
			BaseURL:  values["sub_base_url"],
			APIKey:   values["sub_api_key"],
			Active:   false,
		}
		if err := m.subscriptionMgr.Add(sub); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to add subscription: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Added subscription: %s", sub.Name))
		}
	})
}

// openEditSubscriptionPanel opens the mini panel to edit a subscription's
// credentials. Model-related knobs are NOT edited here — default model/max
// output/thinking mode are not subscription-level anymore (models come from
// /models fetch + manual model rows; max output is per-model on the model row
// E panel; thinking mode is a global Ctrl+M toggle). The existing internal
// Model/MaxOutputTokens/ThinkingMode values are preserved as fallbacks.
func (m *cliModel) openEditSubscriptionPanel(subID string) {
	if m.subscriptionMgr == nil {
		return
	}
	var target *ch.Subscription
	if subs, err := m.subscriptionMgr.List(""); err == nil {
		for i := range subs {
			if subs[i].ID == subID {
				target = &subs[i]
				break
			}
		}
	}
	if target == nil {
		m.showTempStatus("Subscription not found")
		return
	}
	schema := []ch.SettingDefinition{
		{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: ch.SettingTypeText, DefaultValue: target.Name},
		{Key: "sub_provider", Label: "Provider", Description: "LLM provider (openai, anthropic, deepseek, etc.)", Type: ch.SettingTypeText, DefaultValue: target.Provider},
		{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: ch.SettingTypeText, DefaultValue: target.BaseURL},
		{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: ch.SettingTypePassword, DefaultValue: target.APIKey},
	}
	values := map[string]string{
		"sub_name":     target.Name,
		"sub_provider": target.Provider,
		"sub_base_url": target.BaseURL,
		"sub_api_key":  target.APIKey,
	}
	curID := target.ID
	m.quickSwitchMode = "" // close overlay while editing
	m.openSettingsPanel(schema, values, func(values map[string]string) {
		if m.subscriptionMgr == nil {
			return
		}
		apiKey := values["sub_api_key"]
		if isMaskedAPIKey(apiKey) { // never write back a masked key
			apiKey = target.APIKey
		}
		updated := &ch.Subscription{
			ID:              curID,
			Name:            values["sub_name"],
			Provider:        values["sub_provider"],
			Model:           target.Model, // preserved internal fallback
			BaseURL:         values["sub_base_url"],
			APIKey:          apiKey,
			MaxOutputTokens: target.MaxOutputTokens, // preserved internal fallback
			ThinkingMode:    target.ThinkingMode,    // preserved internal fallback
			PerModelConfigs: target.PerModelConfigs, // preserved — per-model params edited via model row
			Active:          target.Active,
		}
		if err := m.subscriptionMgr.Update(curID, updated); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to update: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Updated: %s", updated.Name))
		}
	})
}

// deleteSubscription removes a subscription (with guards against deleting the
// active/last one).
func (m *cliModel) deleteSubscription(subID string) {
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil {
		return
	}
	// Refuse to delete the read-only system subscription.
	for _, s := range subs {
		if s.ID == subID && s.IsSystem {
			m.showTempStatus("系统订阅只读，不可删除")
			return
		}
	}
	// Count user-owned (non-system) subscriptions; must keep at least one.
	userOwned := 0
	for _, s := range subs {
		if !s.IsSystem {
			userOwned++
		}
	}
	if userOwned <= 0 {
		m.showTempStatus("Cannot delete the last subscription")
		return
	}
	activeID := m.activeSubID
	if activeID == "" {
		for _, s := range subs {
			if s.Active {
				activeID = s.ID
				break
			}
		}
	}
	if subID == activeID {
		m.showTempStatus("Cannot delete the active subscription — switch model first")
		return
	}
	var name string
	for _, s := range subs {
		if s.ID == subID {
			name = s.Name
			break
		}
	}
	if err := m.subscriptionMgr.Remove(subID); err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to delete: %v", err))
		return
	}
	m.showTempStatus(fmt.Sprintf("Deleted: %s", name))
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
}

// ─── Model edit / add ───────────────────────────────────────────────────────

// openEditModelPanel edits a model's parameters (max_context / max_output /
// api_type / enabled) and upserts them into subscription_models keyed by
// (SubID, Model). The table is append-only — disabling sets enabled=0, nothing
// is ever deleted. After save the panel reopens on the same model.
func (m *cliModel) openEditModelPanel(subID, model string) {
	if m.subscriptionMgr == nil || subID == "" || model == "" {
		return
	}
	maxCtx, maxOut, apiType := 0, 0, ""
	enabled := true
	if subs, err := m.subscriptionMgr.List(""); err == nil {
		for i := range subs {
			if subs[i].ID == subID {
				if cfg, ok := subs[i].PerModelConfigs[model]; ok {
					maxCtx = cfg.MaxContext
					maxOut = cfg.MaxOutputTokens
					apiType = cfg.APIType
					enabled = cfg.Enabled
				}
				break
			}
		}
	}
	enabledDef := "enabled"
	if !enabled {
		enabledDef = "disabled"
	}
	schema := []ch.SettingDefinition{
		{Key: "__header__", Label: "Edit Model: " + model, Description: "Parameters are stored per (subscription, model). 0 = use subscription default.", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "pm_enabled", Label: "Enabled", Description: "Disabled models show greyed and are rejected on switch", Type: ch.SettingTypeSelect, DefaultValue: enabledDef, Options: []ch.SettingOption{
			{Label: "Enabled", Value: "enabled"},
			{Label: "Disabled", Value: "disabled"},
		}},
		{Key: "pm_max_output", Label: "Max Output Tokens", Description: "Max output tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: strconv.Itoa(maxOut)},
		{Key: "pm_max_context", Label: "Max Context", Description: "Max context tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: strconv.Itoa(maxCtx)},
		{Key: "pm_api_type", Label: "API Type", Description: "API endpoint override (blank = use subscription default)", Type: ch.SettingTypeSelect, DefaultValue: apiType, Options: []ch.SettingOption{
			{Label: "Default", Value: ""},
			{Label: "Responses", Value: "responses"},
		}},
	}
	values := map[string]string{
		"pm_enabled":     enabledDef,
		"pm_max_output":  strconv.Itoa(maxOut),
		"pm_max_context": strconv.Itoa(maxCtx),
		"pm_api_type":    apiType,
	}
	origEnabled := enabled
	m.quickSwitchMode = "" // close overlay while editing
	m.openSettingsPanel(schema, values, func(values map[string]string) {
		if m.subscriptionMgr == nil {
			return
		}
		mo, _ := strconv.Atoi(values["pm_max_output"])
		mc, _ := strconv.Atoi(values["pm_max_context"])
		wantEnabled := values["pm_enabled"] != "disabled"
		pmc := ch.PerModelConfig{MaxOutputTokens: mo, MaxContext: mc, APIType: values["pm_api_type"], Enabled: wantEnabled}
		if err := m.subscriptionMgr.UpdatePerModelConfig(subID, model, pmc); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to save %s: %v", model, err))
			return
		}
		if wantEnabled != origEnabled {
			if err := m.subscriptionMgr.SetModelEnabled(subID, model, wantEnabled); err != nil {
				m.showTempStatus(fmt.Sprintf("Failed to %s %s: %v", map[bool]string{true: "enable", false: "disable"}[wantEnabled], model, err))
				return
			}
		}
		m.showTempStatus(fmt.Sprintf("Saved: %s", model))
		m.reopenLLMPanelOn(model)
	})
}

// openAddModelPanel opens a mini panel to manually add a model not listed by
// /models. defaultSubID pre-selects the owner (from the current cursor row).
func (m *cliModel) openAddModelPanel(defaultSubID string) {
	if m.subscriptionMgr == nil {
		return
	}
	subs, err := m.subscriptionMgr.List("")
	if err != nil || len(subs) == 0 {
		m.showTempStatus("Add a subscription first")
		return
	}
	opts := make([]ch.SettingOption, 0, len(subs))
	firstID := defaultSubID
	for _, s := range subs {
		if !s.Enabled {
			continue
		}
		name := s.Name
		if name == "" {
			name = s.ID
		}
		opts = append(opts, ch.SettingOption{Label: name, Value: s.ID})
		if firstID == "" {
			firstID = s.ID
		}
	}
	if len(opts) == 0 {
		m.showTempStatus("No enabled subscription to add to")
		return
	}
	schema := []ch.SettingDefinition{
		{Key: "__header__", Label: "Add Model", Description: "Manually register a model not listed by /models. Stored per (subscription, model); appears as offline until fetched.", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "add_sub", Label: "Subscription", Description: "Owning subscription (provides credentials)", Type: ch.SettingTypeSelect, DefaultValue: firstID, Options: opts},
		{Key: "add_model", Label: "Model", Description: "Model name (must match what the provider accepts)", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "add_max_output", Label: "Max Output Tokens", Description: "Max output tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: "0"},
		{Key: "add_max_context", Label: "Max Context", Description: "Max context tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: "0"},
		{Key: "add_api_type", Label: "API Type", Description: "API endpoint override (blank = use subscription default)", Type: ch.SettingTypeSelect, DefaultValue: "", Options: []ch.SettingOption{
			{Label: "Default", Value: ""},
			{Label: "Responses", Value: "responses"},
		}},
	}
	values := map[string]string{
		"add_sub":         firstID,
		"add_model":       "",
		"add_max_output":  "0",
		"add_max_context": "0",
		"add_api_type":    "",
	}
	m.quickSwitchMode = "" // close overlay while editing
	m.openSettingsPanel(schema, values, func(values map[string]string) {
		if m.subscriptionMgr == nil {
			return
		}
		subID := values["add_sub"]
		model := strings.TrimSpace(values["add_model"])
		if subID == "" || model == "" {
			m.showTempStatus("Subscription and model name are required")
			return
		}
		mo, _ := strconv.Atoi(values["add_max_output"])
		mc, _ := strconv.Atoi(values["add_max_context"])
		if err := m.subscriptionMgr.UpdatePerModelConfig(subID, model, ch.PerModelConfig{
			MaxOutputTokens: mo, MaxContext: mc, APIType: values["add_api_type"], Enabled: true,
		}); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to add %s: %v", model, err))
			return
		}
		m.showTempStatus(fmt.Sprintf("Added: %s", model))
		m.reopenLLMPanelOn(model)
	})
}

// reopenLLMPanelOn reopens the panel from the DB snapshot (no async /models
// refresh) and parks the cursor on the given model if present.
func (m *cliModel) reopenLLMPanelOn(focusModel string) {
	if m.subscriptionMgr == nil {
		return
	}
	m.quickSwitchMode = "llm"
	m.quickSwitchFiltering = false
	m.quickSwitchRefreshing = false
	m.rebuildLLMRows()
	for i, r := range m.quickSwitchRows {
		if r.kind == qsModel && r.model.Model == focusModel {
			m.quickSwitchCursor = i
			return
		}
	}
	m.cursorToActiveLLMRow()
}

// updateQuickSwitchModels is called after a model switch to keep the open
// panel's active-model marker in sync.
func (m *cliModel) updateQuickSwitchModels(newModel string) {
	if m.quickSwitchMode != "llm" {
		return
	}
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
}

// ─── Rendering ──────────────────────────────────────────────────────────────

func (m *cliModel) viewQuickSwitch(width, height int) string {
	if m.quickSwitchMode != "llm" {
		return ""
	}
	if len(m.quickSwitchRows) == 0 && !m.quickSwitchFiltering {
		return ""
	}

	var lines []string
	lines = append(lines, m.styles.PanelHeader.Render("Models & Subscriptions"))
	lines = append(lines, "")

	// Filter input (shown in filter mode, or as a hint line otherwise).
	if m.quickSwitchFiltering {
		lines = append(lines, m.quickSwitchFilterInput.View())
	} else {
		lines = append(lines, m.styles.TextMutedSt.Render("  press / to filter"))
	}
	if m.quickSwitchRefreshing {
		lines = append(lines, m.styles.TextMutedSt.Render("  ↻ 刷新模型列表…"))
	} else {
		lines = append(lines, "")
	}

	activeID := m.activeSubID
	if activeID == "" {
		for _, r := range m.quickSwitchRows {
			if r.kind == qsSub && r.sub.Active {
				activeID = r.sub.ID
				break
			}
		}
	}

	if m.quickSwitchFiltering && len(m.quickSwitchRows) == 0 {
		lines = append(lines, m.styles.TextMutedSt.Render("  无匹配项 — 按 Esc 退出过滤，或继续输入"))
		lines = append(lines, "")
	}

	for i, r := range m.quickSwitchRows {
		cursor := " "
		style := m.styles.TextMutedSt
		if i == m.quickSwitchCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		switch r.kind {
		case qsSection:
			lines = append(lines, m.styles.TextMutedSt.Render(" "+r.label))
		case qsSub:
			name := r.sub.Name
			if name == "" {
				name = r.sub.ID
			}
			disabledTag := ""
			if !r.sub.Enabled {
				disabledTag = " (disabled)"
			}
			active := ""
			if r.sub.ID == activeID {
				active = " ✓"
			}
			sysTag := ""
			if r.sub.IsSystem {
				sysTag = " 🔒"
			}
			lines = append(lines, style.Render(fmt.Sprintf(" %s %-28s%s%s%s", cursor, name, disabledTag, active, sysTag)))
		case qsModel:
			label := r.model.Model
			if r.model.SubName != "" {
				label = r.model.SubName + " · " + r.model.Model
			}
			statusTag := ""
			switch r.model.Status {
			case "offline":
				statusTag = " (offline)"
			case "disabled":
				statusTag = " (disabled)"
			}
			mark := ""
			if r.model.Model == m.cachedModelName {
				mark = " ✓"
			}
			line := style.Render(fmt.Sprintf(" %s   %s%s%s", cursor, label, statusTag, mark))
			if r.model.Status == "disabled" || r.model.Status == "offline" {
				line = m.styles.TextMutedSt.Render(line)
			}
			lines = append(lines, line)
		case qsAddSub, qsAddModel:
			lines = append(lines, style.Render(fmt.Sprintf(" %s %s", cursor, r.label)))
		}
	}

	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint
	var hint string
	if m.quickSwitchFiltering {
		hint = m.styles.PanelHint.Render(" Type to filter  ↑↓ Nav  Enter Select  Esc Exit filter")
	} else {
		showAllTag := ""
		if m.quickSwitchShowAll {
			showAllTag = "  [show all]"
		}
		hint = m.styles.PanelHint.Render(" ↑↓ Nav  Enter Select  R Refresh  E Edit  D Disable/Del  N Add model  S Show all" + showAllTag + "  / Filter  Esc Close")
	}

	// Vertical centering (rough).
	listH := len(m.quickSwitchRows) + 5 // header + spacer + filter + refresh/spacer + items + borders
	blankLines := max(0, (height-listH)/2)
	var b strings.Builder
	for i := 0; i < blankLines; i++ {
		b.WriteString("\n")
	}
	b.WriteString(box)
	b.WriteString("\n")
	b.WriteString(hint)
	// Show the equivalent /set-llm command for the cursor's subscription, so
	// users can copy it to IM channels (Feishu/QQ/Web) that have no TUI panel.
	if !m.quickSwitchFiltering {
		if sub, ok := m.currentLLMPanelSub(); ok && sub.Provider != "" {
			b.WriteString("\n")
			b.WriteString(m.styles.TextMutedSt.Render(" IM 命令: " + setLLMCmdForSub(sub)))
		}
	}
	return b.String()
}

// ─── Key handling ───────────────────────────────────────────────────────────

// handleQuickSwitchKey handles key events for the unified LLM panel.
// Returns (handled, cmd). Called from Update() BEFORE panelMode check so the
// panel has higher priority than other panels.
func (m *cliModel) handleQuickSwitchKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	if m.quickSwitchMode != "llm" {
		return false, nil
	}

	// Filter mode: letters feed the filter; Esc exits filter; Up/Down/Enter navigate.
	if m.quickSwitchFiltering {
		switch msg.Code {
		case tea.KeyEsc:
			m.quickSwitchFiltering = false
			m.quickSwitchFilterInput.SetValue("")
			m.rebuildLLMRows()
			m.cursorToActiveLLMRow()
			return true, nil
		case tea.KeyUp:
			m.moveLLMCursor(-1)
			return true, nil
		case tea.KeyDown:
			m.moveLLMCursor(1)
			return true, nil
		case tea.KeyEnter:
			// In filter mode Enter selects a model (switch) or falls back to the
			// command-mode action for non-model rows.
			if m.quickSwitchCursor < len(m.quickSwitchRows) && m.quickSwitchRows[m.quickSwitchCursor].kind == qsModel {
				m.applyQuickSwitch()
			} else {
				m.quickSwitchFiltering = false
				m.quickSwitchFilterInput.SetValue("")
				m.rebuildLLMRows()
				m.cursorToActiveLLMRow()
			}
			if len(m.pendingCmds) > 0 {
				pending := m.pendingCmds
				m.pendingCmds = nil
				return true, tea.Batch(pending...)
			}
			return true, nil
		}
		var cmd tea.Cmd
		m.quickSwitchFilterInput, cmd = m.quickSwitchFilterInput.Update(msg)
		m.rebuildLLMRows()
		if strings.TrimSpace(m.quickSwitchFilterInput.Value()) != "" {
			m.quickSwitchCursor = 0
		} else {
			m.cursorToActiveLLMRow()
		}
		return true, cmd
	}

	// Command mode.
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
		m.moveLLMCursor(-1)
		return true, nil
	case tea.KeyDown:
		m.moveLLMCursor(1)
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
	switch msg.String() {
	case "e":
		m.editCurrentRow()
		return true, nil
	case "d":
		m.disableCurrentRow()
		return true, nil
	case "n":
		m.quickSwitchMode = ""
		m.openAddModelPanel(m.currentSubForAdd())
		return true, nil
	case "r":
		if !m.quickSwitchRefreshing {
			m.quickSwitchRefreshing = true
			m.pendingCmds = append(m.pendingCmds, m.refreshModelEntriesCmd())
		}
		return true, nil
	case "s":
		m.quickSwitchShowAll = !m.quickSwitchShowAll
		m.rebuildLLMRows()
		m.cursorToActiveLLMRow()
		return true, nil
	case "/":
		m.quickSwitchFiltering = true
		m.quickSwitchFilterInput.SetValue("")
		m.quickSwitchFilterInput.Focus()
		m.rebuildLLMRows()
		m.quickSwitchCursor = 0
		return true, nil
	}
	return true, nil // block other keys
}

// moveLLMCursor moves the cursor skipping non-actionable section rows.
func (m *cliModel) moveLLMCursor(dir int) {
	n := len(m.quickSwitchRows)
	if n == 0 {
		return
	}
	cur := m.quickSwitchCursor
	for step := 0; step < n; step++ {
		cur += dir
		if cur < 0 {
			cur = n - 1
		} else if cur >= n {
			cur = 0
		}
		if m.quickSwitchRows[cur].kind != qsSection {
			m.quickSwitchCursor = cur
			return
		}
	}
}

// ─── Background /models refresh ─────────────────────────────────────────────

// cliModelEntriesRefreshedMsg carries the fresh model entry list after a
// background /models refresh of every enabled subscription.
type cliModelEntriesRefreshedMsg struct {
	entries []protocol.ModelEntry
}

// refreshModelEntriesCmd issues the backend refresh (blocking RPC) in a
// goroutine and emits cliModelEntriesRefreshedMsg with the fresh entries.
func (m *cliModel) refreshModelEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		var entries []protocol.ModelEntry
		if m.channel != nil && m.channel.modelLister != nil {
			entries = m.channel.modelLister.RefreshModelEntries()
		}
		return cliModelEntriesRefreshedMsg{entries: entries}
	}
}

// handleModelEntriesRefreshed applies the fresh entry list to the panel if it
// is still open. Preserves the cursor (clamped) and filter state.
func (m *cliModel) handleModelEntriesRefreshed(msg cliModelEntriesRefreshedMsg) {
	m.quickSwitchRefreshing = false
	if m.quickSwitchMode != "llm" || len(msg.entries) == 0 {
		return
	}
	m.rebuildLLMRows()
}
