package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
)

var channelLabels = map[string]string{
	"web":    "🌐 Web ch.Channel",
	"feishu": "🐦 Feishu (飞书)",
	"qq":     "💬 QQ",
	"napcat": "🐱 NapCat",
}

var builtinChannelNames = []string{"web", "feishu", "qq", "napcat"}

// openChannelPanel opens the channel configuration panel.
func (m *cliModel) openChannelPanel() {
	m.panelState.mode = "channel"
	m.relayoutViewport()
	m.panelState.misc.channelCursor = 0

	// Fetch current channel configs (includes plugin channels)
	if m.channel != nil && m.channel.config.ChannelConfigGetFn != nil {
		cfgs, err := m.channel.config.ChannelConfigGetFn()
		if err != nil {
			m.showSystemMsg("Failed to load channel configs: "+err.Error(), feedbackWarning)
			cfgs = nil
		}
		m.panelState.misc.channelCfg = cfgs
	}

	// Build channel list: built-in first (in fixed order), then plugin channels
	items := make([]string, 0, len(builtinChannelNames)+len(m.panelState.misc.channelCfg))
	seen := make(map[string]bool)
	for _, name := range builtinChannelNames {
		items = append(items, name)
		seen[name] = true
	}
	// Add plugin channels from config keys
	if m.panelState.misc.channelCfg != nil {
		for name := range m.panelState.misc.channelCfg {
			if !seen[name] {
				items = append(items, name)
				seen[name] = true
			}
		}
	}
	m.panelState.misc.channelItems = items
}

// updateChannelPanel handles key events in the channel config panel.
func (m *cliModel) updateChannelPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	switch {
	case msg.String() == "ctrl+c":
		return m.closePanelAndResume()
	case msg.Code == tea.KeyEsc:
		m.panelState.misc.channelItems = nil
		m.panelState.misc.channelCfg = nil
		if !m.popPanel() {
			m.panelState.mode = ""
			m.relayoutViewport()
		}
		return true, m, nil

	case msg.Code == tea.KeyUp:
		if m.panelState.misc.channelCursor > 0 {
			m.panelState.misc.channelCursor--
		}
		return true, m, nil

	case msg.Code == tea.KeyDown:
		if m.panelState.misc.channelCursor < len(m.panelState.misc.channelItems)-1 {
			m.panelState.misc.channelCursor++
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		if m.panelState.misc.channelCursor >= 0 && m.panelState.misc.channelCursor < len(m.panelState.misc.channelItems) {
			ch := m.panelState.misc.channelItems[m.panelState.misc.channelCursor]
			m.openChannelSettingsPanel(ch)
		}
		return true, m, nil
	}
	return true, m, nil
}

// viewChannelPanel renders the channel config panel.
func (m *cliModel) viewChannelPanel() string {
	s := &m.styles
	header := s.PanelHeader.Render("📡 ch.Channel Configuration")
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

	for i, ch := range m.panelState.misc.channelItems {
		prefix := "  "
		if i == m.panelState.misc.channelCursor {
			prefix = s.PanelCursor.Render("▸")
		}

		label := channelLabels[ch]
		if label == "" {
			label = ch
		}

		// Show enabled/disabled status
		status := "◦ disabled"
		statusStyle := s.TextMutedSt
		if m.panelState.misc.channelCfg != nil {
			if cfg, ok := m.panelState.misc.channelCfg[ch]; ok {
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
func (m *cliModel) pluginChannelSchema(channel string) []ch.SettingDefinition {
	if m.panelState.misc.channelCfg == nil {
		return nil
	}
	cfg, ok := m.panelState.misc.channelCfg[channel]
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

	schema := make([]ch.SettingDefinition, 0, len(rawSchema))
	for _, r := range rawSchema {
		sd := ch.SettingDefinition{
			Key:          r.Key,
			Label:        r.Label,
			Description:  r.Description,
			Type:         ch.SettingType(r.Type),
			DefaultValue: r.DefaultValue,
			Category:     r.Category,
		}
		schema = append(schema, sd)
	}
	return schema
}

// channelSettingsSchema returns the settings schema for a specific ch.
// Built-in channels come from the shared channel package (single source of
// truth, also used by the Web channels panel); plugin channels are handled by
// pluginChannelSchema.
func channelSettingsSchema(channel string) []ch.SettingDefinition {
	return ch.BuiltinChannelSchema(channel)
}

// openChannelSettingsPanel opens the settings panel for a specific ch.
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
	if m.panelState.misc.channelCfg != nil {
		if cfg, ok := m.panelState.misc.channelCfg[channel]; ok {
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
			m.showTempStatus("ch.Channel config save not available")
			return
		}
		if err := m.channel.config.ChannelConfigSetFn(channel, vals); err != nil {
			m.showTempStatus("Failed to save channel config: " + err.Error())
		} else {
			// Refresh cached configs
			if m.channel.config.ChannelConfigGetFn != nil {
				if cfgs, err := m.channel.config.ChannelConfigGetFn(); err == nil {
					m.panelState.misc.channelCfg = cfgs
				}
			}
			m.showTempStatus(fmt.Sprintf("✅ %s config saved", channel))
		}
	})
}
