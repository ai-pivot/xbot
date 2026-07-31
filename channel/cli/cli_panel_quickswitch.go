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
//	  + Add subscription
//	Models
//	  ... (all selectable models across subs, 订阅名 · 模型名)
//	  + Add model
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
	kind     qsRowKind
	sub      ch.Subscription     // qsSub
	model    protocol.ModelEntry // qsModel
	label    string              // qsSection / qsAddSub / qsAddModel
	subID    string              // qsModel: owning subscription ID (for expand tracking)
	expanded bool                // qsSub: whether this sub is expanded (showing models)
}

// openQuickSwitch opens the unified LLM panel. The mode argument is kept for
// backward compatibility with existing call sites (palette / mouse / settings)
// and is ignored — there is now only one panel.
func (m *cliModel) openQuickSwitch(_ string) {
	m.openLLMPanel()
}

// drainPendingCmds returns pending cmds and clears the list. Used by callers
// after openQuickSwitch to flush async cmds (e.g. refreshModelEntriesCmd).
func (m *cliModel) drainPendingCmds() []tea.Cmd {
	if len(m.pendingCmds) == 0 {
		return nil
	}
	cmds := m.pendingCmds
	m.pendingCmds = nil
	return cmds
}

// openLLMPanel builds the unified panel from cached data (or DB on first
// access), then kicks off a background /models API refresh.
//
// Execution order (no blocking):
//  1. rebuildLLMRows() → llmCache.Get() → sync DB read (~ms), panel renders
//  2. refreshModelEntriesCmd → goroutine → /models API (seconds), updates
//     panel via cliModelEntriesRefreshedMsg when done
//
// The two steps don't conflict: step 1 completes synchronously inside Update,
// step 2 runs in a BubbleTea goroutine that only starts AFTER Update returns.
// By the time the refresh RPC reaches the server, the sync RPCs from step 1
// have already completed.
func (m *cliModel) openLLMPanel() {
	if m.subscriptionMgr == nil {
		return
	}
	m.quickSwitchMode = "llm"
	m.quickSwitchFiltering = false
	m.quickSwitchShowAll = false
	m.quickSwitchRefreshing = true
	m.quickSwitchScrollY = 0
	ti := textinput.New()
	ti.Placeholder = "Filter subscriptions / models…"
	ti.Prompt = " > "
	ti.CharLimit = 80
	ti.SetWidth(40)
	m.quickSwitchFilterInput = ti

	// Step 1: expand active sub, then single rebuild (not twice).
	if m.activeSubID != "" {
		m.expandOnly(m.activeSubID)
	}
	m.rebuildLLMRows()
	m.cursorToActiveLLMRow()
	// Step 2: kick off background /models refresh — updates panel when done.
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

// rebuildLLMRows rebuilds quickSwitchRows from cached data + current filter.
// Reads from llmCache (sync DB read on first access, cache thereafter) —
// no blocking RPC on the main thread (issue #199).
func (m *cliModel) rebuildLLMRows() {
	d := m.llmCache.Get()
	m.quickSwitchRows = m.buildLLMRows(d)
}

// rebuildLLMRowsFresh reloads subscriptions + model entries from the sync source
// before rebuilding rows. Use after any subscription/model metadata mutation so
// the open panel does not keep a stale llmCache snapshot.
func (m *cliModel) rebuildLLMRowsFresh() {
	d := m.llmSource()
	m.llmCache.Apply(d)
	m.quickSwitchRows = m.buildLLMRows(d)
}

// buildLLMRows assembles the tree-structured row list for the unified panel.
// Subscriptions are top-level rows; expanded subscriptions show their models
// (and an "Add model" row) indented underneath. No separate "Models" section.
//
// Filter mode: matching subscriptions auto-expand and show only matching models.
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
	rows = append(rows, qsRow{kind: qsSection, label: "Subscriptions"})

	for _, s := range d.subs {
		name := s.Name
		if name == "" {
			name = s.ID
		}

		// In filter mode: include sub if its name matches OR any of its models match.
		subMatches := match(name)
		modelMatches := false
		var matchingModels []protocol.ModelEntry
		for _, e := range d.entries {
			if e.SubID != s.ID {
				continue
			}
			if !m.quickSwitchShowAll && isNoiseModel(e.Model) {
				continue
			}
			if filtering {
				if match(e.Model, e.Status, name) {
					modelMatches = true
					matchingModels = append(matchingModels, e)
				}
			} else {
				matchingModels = append(matchingModels, e)
			}
		}

		if filtering && !subMatches && !modelMatches {
			continue
		}

		// Auto-expand in filter mode if models match (even if sub name doesn't).
		exp := m.expandedSubs[s.ID]
		if filtering && modelMatches {
			exp = true
		}

		rows = append(rows, qsRow{kind: qsSub, sub: s, expanded: exp, subID: s.ID})

		if exp {
			for _, e := range matchingModels {
				rows = append(rows, qsRow{kind: qsModel, model: e, subID: s.ID})
			}
			if !filtering {
				rows = append(rows, qsRow{kind: qsAddModel, label: "+ Add custom model", subID: s.ID})
			}
		}
	}

	if !filtering {
		rows = append(rows, qsRow{kind: qsAddSub, label: "+ Add subscription"})
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
			// Fall back to cache lookup (cursor row may be a model
			// whose sub row is filtered out).
			d := m.llmCache.Get()
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
	providerVal := ch.ProviderToSelectValue(s.Provider, s.APIType)
	return fmt.Sprintf("/set-llm provider=%s base_url=%s api_key=%s", providerVal, s.BaseURL, key)
}

// cursorToActiveLLMRow parks the cursor on the active model's row (fall back to
// the active subscription row, else the first sub row, else 0).
// Caller must ensure quickSwitchRows is already built (via rebuildLLMRows).
func (m *cliModel) cursorToActiveLLMRow() {

	if m.cachedModelName != "" {
		for i, r := range m.quickSwitchRows {
			if r.kind == qsModel && r.model.Model == m.cachedModelName && r.model.SubID == m.activeSubID {
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
		// Enter on subscription row = toggle expand/collapse
		m.toggleExpand(row.sub.ID)
	case qsModel:
		m.switchToModelRow(row.model)
	case qsAddSub:
		m.quickSwitchMode = ""
		m.openAddSubscriptionPanel()
	case qsAddModel:
		m.quickSwitchMode = ""
		m.openAddModelPanel(row.subID)
	}
}

// toggleExpand flips the expansion state of a subscription in the panel.
// Accordion mode: expanding one subscription collapses all others.
func (m *cliModel) toggleExpand(subID string) {
	if m.expandedSubs[subID] {
		delete(m.expandedSubs, subID)
	} else {
		m.expandOnly(subID)
	}
	m.rebuildLLMRows()
}

// expandOnly expands exactly one subscription, collapsing all others.
func (m *cliModel) expandOnly(subID string) {
	m.expandedSubs = make(map[string]bool)
	m.expandedSubs[subID] = true
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
	m.rebuildLLMRowsFresh()
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
			m.showTempStatus("系统订阅只读，不可禁用")
			return
		}
		// D on subscription row = toggle enabled/disabled
		m.toggleSubscription(row.sub)
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
	m.rebuildLLMRowsFresh()
	m.cursorToActiveLLMRow()
}

// deleteModelRow permanently removes a model from subscription_models.
func (m *cliModel) deleteModelRow(subID, model string) {
	if m.subscriptionMgr == nil {
		return
	}
	if subID == "" || m.subIsSystem(subID) {
		m.showTempStatus("系统订阅模型不可删除")
		return
	}
	if err := m.subscriptionMgr.RemoveModel(subID, model); err != nil {
		m.showTempStatus(fmt.Sprintf("删除失败: %v", err))
		return
	}
	m.showTempStatus(fmt.Sprintf("已删除: %s", model))
	m.rebuildLLMRowsFresh()
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

// providerSelectOptions returns the three supported provider types for the
// subscription panel dropdown. The select value encodes both provider and
// API type: "openai" (Chat Completions), "openai_responses" (Responses API),
// "anthropic" (Anthropic Messages API).
// addSubscriptionSchema builds the settings schema for creating a subscription.
// A subscription is credentials-only (Name/Provider/BaseURL/APIKey). Model-related
// knobs (default model, max output, thinking mode) are NOT collected here —
// models come from the provider's /models list (auto-fetched) or manual model
// rows; max output is per-model (model row E panel); thinking mode is a global
// toggle (Ctrl+M). See "订阅是订阅，模型是模型".
func addSubscriptionSchema() []ch.SettingDefinition {
	return []ch.SettingDefinition{
		{Key: "sub_name", Label: "Name", Description: "Display name for this subscription", Type: ch.SettingTypeText, DefaultValue: ""},
		{Key: "sub_provider", Label: "Provider", Description: "API type: OpenAI Complete (Chat Completions), OpenAI Responses, or Anthropic", Type: ch.SettingTypeSelect, DefaultValue: "openai", Options: ch.ProviderSelectOptions()},
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
		provider, apiType := ch.SelectValueToProvider(values["sub_provider"])
		if name == "" {
			name = provider
		}
		if name == "" {
			name = "unnamed"
		}
		sub := &ch.Subscription{
			ID:       fmt.Sprintf("sub_%d", time.Now().UnixNano()),
			Name:     name,
			Provider: provider,
			APIType:  apiType,
			BaseURL:  values["sub_base_url"],
			APIKey:   values["sub_api_key"],
			Active:   false,
		}
		if err := m.subscriptionMgr.Add(sub); err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to add subscription: %v", err))
		} else {
			m.showTempStatus(fmt.Sprintf("Added subscription: %s", sub.Name))
			m.expandOnly(sub.ID)
			m.reopenLLMPanelOn(sub.ID, "")
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
		{Key: "sub_provider", Label: "Provider", Description: "API type: OpenAI Complete (Chat Completions), OpenAI Responses, or Anthropic", Type: ch.SettingTypeSelect, DefaultValue: ch.ProviderToSelectValue(target.Provider, target.APIType), Options: ch.ProviderSelectOptions()},
		{Key: "sub_base_url", Label: "Base URL", Description: "API base URL (leave empty for provider default)", Type: ch.SettingTypeText, DefaultValue: target.BaseURL},
		{Key: "sub_api_key", Label: "API Key", Description: "API key (leave empty to use global key)", Type: ch.SettingTypePassword, DefaultValue: target.APIKey},
	}
	values := map[string]string{
		"sub_name":     target.Name,
		"sub_provider": ch.ProviderToSelectValue(target.Provider, target.APIType),
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
		provider, apiType := ch.SelectValueToProvider(values["sub_provider"])
		updated := &ch.Subscription{
			ID:              curID,
			Name:            values["sub_name"],
			Provider:        provider,
			APIType:         apiType,
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
			m.reopenLLMPanelOn(curID, "")
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
	m.rebuildLLMRowsFresh()
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
		{Key: "pm_enabled", Label: "Enabled", Description: "Disabled models show greyed and are rejected on switch", Type: ch.SettingTypeSelect, DefaultValue: enabledDef, Options: []ch.SettingOption{
			{Label: "Enabled", Value: "enabled"},
			{Label: "Disabled", Value: "disabled"},
		}},
		{Key: "pm_max_output", Label: "Max Output Tokens", Description: "Max output tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: strconv.Itoa(maxOut)},
		{Key: "pm_max_context", Label: "Max Context", Description: "Max context tokens (0 = use subscription default)", Type: ch.SettingTypeNumber, DefaultValue: strconv.Itoa(maxCtx)},
		{Key: "pm_api_type", Label: "API Type", Description: "API endpoint override (blank = use subscription default)", Type: ch.SettingTypeSelect, DefaultValue: apiType, Options: []ch.SettingOption{
			{Label: "Default", Value: ""},
			{Label: "Chat Completions", Value: "chat_completions"},
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
		m.reopenLLMPanelOn(subID, model)
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
			{Label: "Chat Completions", Value: "chat_completions"},
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
		m.reopenLLMPanelOn(subID, model)
	})
}

// reopenLLMPanelOn reopens the panel from the DB snapshot (no async /models
// refresh) and parks the cursor on the given (subID, model) if present.
func (m *cliModel) reopenLLMPanelOn(subID, model string) {
	if m.subscriptionMgr == nil {
		return
	}
	m.llmCache.Invalidate() // clear stale cache so rebuildLLMRows reads fresh from DB
	m.quickSwitchMode = "llm"
	m.quickSwitchFiltering = false
	m.quickSwitchRefreshing = false
	m.quickSwitchScrollY = 0
	m.rebuildLLMRowsFresh()
	// Match both SubID AND model to avoid selecting a same-named model
	// from a different subscription.
	if subID != "" && model != "" {
		for i, r := range m.quickSwitchRows {
			if r.kind == qsModel && r.model.Model == model && r.model.SubID == subID {
				m.quickSwitchCursor = i
				return
			}
		}
	}
	// Fallback: try matching just subID (e.g. after sub credential edit)
	if subID != "" {
		for i, r := range m.quickSwitchRows {
			if r.kind == qsSub && r.sub.ID == subID {
				m.quickSwitchCursor = i
				return
			}
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

	// ── Compute available height for the list ──
	// Overhead: header(1) + blank(1) + search(1) + refresh/blank(1) + hint(1) + imcmd(0-1) + borders(2)
	// We reserve a fixed overhead and give the rest to scrollable rows.
	const overhead = 7 // header + blank + search + refresh/blank + hint + borders
	maxVisibleRows := height - overhead
	if maxVisibleRows < 3 {
		maxVisibleRows = 3
	}

	// ── Ensure cursor is visible ──
	m.ensureQuickSwitchCursorVisible(maxVisibleRows)

	// ── Build all lines ──
	var lines []string
	lines = append(lines, m.styles.PanelHeader.Render("Models & Subscriptions"))
	lines = append(lines, "")

	// Search input — always visible, shows hint when not filtering
	if m.quickSwitchFiltering {
		searchLine := "🔍 " + m.quickSwitchFilterInput.View()
		lines = append(lines, searchLine)
	} else {
		lines = append(lines, m.styles.TextMutedSt.Render("🔍 press / to search"))
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

	// ── Slice rows for scrolling ──
	totalRows := len(m.quickSwitchRows)
	start := m.quickSwitchScrollY
	if start < 0 {
		start = 0
	}
	if start > totalRows {
		start = totalRows
	}
	end := start + maxVisibleRows
	if end > totalRows {
		end = totalRows
	}

	for i := start; i < end; i++ {
		r := m.quickSwitchRows[i]
		cursor := " "
		style := m.styles.TextSecondarySt
		if i == m.quickSwitchCursor {
			cursor = "▸"
			style = m.styles.Accent
		}
		switch r.kind {
		case qsSection:
			lines = append(lines, m.styles.PanelDivider.Render(" "+r.label))
		case qsSub:
			name := r.sub.Name
			if name == "" {
				name = r.sub.ID
			}
			// Expand/collapse marker
			expMark := "▸"
			if r.expanded {
				expMark = "▾"
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
			lines = append(lines, style.Render(fmt.Sprintf(" %s %s %s%s%s%s", cursor, expMark, name, disabledTag, active, sysTag)))
		case qsModel:
			label := r.model.Model
			statusTag := ""
			if r.model.Status == "disabled" {
				statusTag = " (disabled)"
			}
			mark := ""
			if r.model.Model == m.cachedModelName && r.model.SubID == m.activeSubID {
				mark = " ✓"
			}
			modelStyle := style
			if r.model.Status == "disabled" {
				modelStyle = m.styles.TextMutedSt
			}
			line := modelStyle.Render(fmt.Sprintf(" %s     %s%s%s", cursor, label, statusTag, mark))
			lines = append(lines, line)
		case qsAddSub:
			lines = append(lines, style.Render(fmt.Sprintf(" %s %s", cursor, r.label)))
		case qsAddModel:
			lines = append(lines, m.styles.TextMutedSt.Render(fmt.Sprintf(" %s     %s", cursor, r.label)))
		}
	}

	// Scroll indicator
	if totalRows > maxVisibleRows {
		lines = append(lines, m.styles.TextMutedSt.Render(fmt.Sprintf(" ── %d-%d / %d ──", start+1, end, totalRows)))
	}

	panelContent := strings.Join(lines, "\n")
	box := m.styles.PanelBox.Render(panelContent)

	// Hint — contextual based on cursor row type
	var hint string
	if m.quickSwitchFiltering {
		hint = m.styles.PanelHint.Render(" Type to filter  ↑↓ Nav  Enter Select  Esc Exit filter")
	} else {
		hint = m.buildQuickSwitchHint()
	}

	var b strings.Builder
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

// ensureQuickSwitchCursorVisible adjusts quickSwitchScrollY so that the cursor
// row is within the visible window. Called before rendering.
func (m *cliModel) ensureQuickSwitchCursorVisible(maxVisible int) {
	if maxVisible <= 0 || len(m.quickSwitchRows) == 0 {
		return
	}
	cur := m.quickSwitchCursor
	if cur < m.quickSwitchScrollY {
		m.quickSwitchScrollY = cur
	}
	if cur >= m.quickSwitchScrollY+maxVisible {
		m.quickSwitchScrollY = cur - maxVisible + 1
	}
	// Clamp
	if m.quickSwitchScrollY < 0 {
		m.quickSwitchScrollY = 0
	}
	maxScroll := len(m.quickSwitchRows) - maxVisible
	if m.quickSwitchScrollY > maxScroll {
		m.quickSwitchScrollY = max(0, maxScroll)
	}
}

// buildQuickSwitchHint returns a contextual hint string based on the current
// cursor row type. Row-specific actions are rendered with Accent style for
// visibility; common keys (Search, Esc, etc.) use PanelHint style.
func (m *cliModel) buildQuickSwitchHint() string {
	common := "  / Search  S Show all  R Refresh  Esc Close"
	if m.quickSwitchShowAll {
		common = "  / Search  S [show all]  R Refresh  Esc Close"
	}

	var rowHint string
	if m.quickSwitchCursor < 0 || m.quickSwitchCursor >= len(m.quickSwitchRows) {
		rowHint = "↑↓ Nav"
	} else {
		r := m.quickSwitchRows[m.quickSwitchCursor]
		switch r.kind {
		case qsSection:
			rowHint = "↑↓ Nav"
		case qsSub:
			if r.sub.IsSystem {
				if r.expanded {
					rowHint = "🔒 ↑↓ Nav  Enter/← Collapse  E View params  N Add model"
				} else {
					rowHint = "🔒 ↑↓ Nav  Enter/→ Expand  N Add model"
				}
			} else {
				if r.expanded {
					rowHint = "↑↓ Nav  Enter/← Collapse  E Edit  D Toggle enabled  X Delete  N Add model"
				} else {
					rowHint = "↑↓ Nav  Enter/→ Expand  E Edit  D Toggle enabled  X Delete  N Add model"
				}
			}
		case qsModel:
			if r.model.Status == "disabled" {
				rowHint = "↑↓ Nav  Enter Select  E Enable  X Delete"
			} else {
				rowHint = "↑↓ Nav  Enter Switch  E Edit params  X Delete"
			}
		case qsAddSub:
			rowHint = "↑↓ Nav  Enter Add subscription"
		case qsAddModel:
			rowHint = "↑↓ Nav  Enter Add model"
		}
	}

	return m.styles.PanelHint.Render(rowHint) + m.styles.TextMutedSt.Render(common)
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
		m.quickSwitchMode = ""
		m.openSettingsFromQuickSwitch()
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
	case tea.KeyRight:
		// → on subscription row = expand (accordion: collapses others)
		if m.quickSwitchCursor < len(m.quickSwitchRows) && m.quickSwitchRows[m.quickSwitchCursor].kind == qsSub {
			subID := m.quickSwitchRows[m.quickSwitchCursor].sub.ID
			if !m.expandedSubs[subID] {
				m.expandOnly(subID)
				m.rebuildLLMRows()
			}
		}
		return true, nil
	case tea.KeyLeft:
		// ← on subscription row = collapse (stay on sub row)
		// ← on model row = collapse parent subscription (jump to sub row)
		if m.quickSwitchCursor < len(m.quickSwitchRows) {
			r := m.quickSwitchRows[m.quickSwitchCursor]
			switch r.kind {
			case qsSub:
				if m.expandedSubs[r.sub.ID] {
					delete(m.expandedSubs, r.sub.ID)
					m.rebuildLLMRows()
				}
			case qsModel:
				if m.expandedSubs[r.subID] {
					delete(m.expandedSubs, r.subID)
					m.rebuildLLMRows()
					// Position cursor on the parent sub row.
					for i, row := range m.quickSwitchRows {
						if row.kind == qsSub && row.sub.ID == r.subID {
							m.quickSwitchCursor = i
							break
						}
					}
				}
			}
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
	case "x":
		// X on subscription row = delete subscription; on model row = delete model
		if m.quickSwitchCursor < len(m.quickSwitchRows) {
			r := m.quickSwitchRows[m.quickSwitchCursor]
			switch r.kind {
			case qsSub:
				m.deleteSubscription(r.sub.ID)
			case qsModel:
				m.deleteModelRow(r.model.SubID, r.model.Model)
			}
		}
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
		cmd := m.quickSwitchFilterInput.Focus() // returns cursor-blink cmd
		m.rebuildLLMRows()
		m.quickSwitchCursor = 0
		return true, cmd
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

// refreshModelEntriesCmd issues the backend refresh (/models API, slow) in a
// goroutine and emits cliModelEntriesRefreshedMsg with the fresh entries.
// RefreshModelEntries() internally persists results to DB (subscription_models
// via UpsertModel in OnModelsLoaded) — this is the "DB write" half of the
// double-write. The handler does the "cache write" half via llmCache.Apply().
func (m *cliModel) refreshModelEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		var entries []protocol.ModelEntry
		if m.channel != nil && m.channel.modelLister != nil {
			entries = m.channel.modelLister.RefreshModelEntries()
		}
		return cliModelEntriesRefreshedMsg{entries: entries}
	}
}

// handleModelEntriesRefreshed applies the fresh entry list to the cache and
// rebuilds rows. RefreshModelEntries already wrote to DB — here we update the
// cache (the second half of double-write) and rebuild the panel.
func (m *cliModel) handleModelEntriesRefreshed(msg cliModelEntriesRefreshedMsg) {
	m.quickSwitchRefreshing = false
	if m.quickSwitchMode != "llm" {
		return
	}
	// RefreshModelEntries has already persisted provider results. Re-read the
	// full sync snapshot so subscriptions edited while the refresh was in flight
	// are not overwritten by a stale cache entry.
	d := m.llmSource()
	d.entries = msg.entries
	m.llmCache.Apply(d)
	m.quickSwitchRows = m.buildLLMRows(d)
}
