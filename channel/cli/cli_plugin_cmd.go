package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"path/filepath"
	ch "xbot/channel"

	tea "charm.land/bubbletea/v2"
)

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
			m.rc.valid = false
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
			m.rc.valid = false
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
		if m.remotePluginCache != nil || m.remoteMode {
			if m.pluginReloading {
				m.showSystemMsg("Plugin reload already in progress, please wait...", feedbackWarning)
				return nil
			}
			m.pluginReloading = true
			m.showSystemMsg("🔄 Reloading all plugins...", feedbackInfo)
			m.updateViewportContent()
			// Send as regular message — agent's builtin command handler
			// calls ReloadAll directly, avoiding RPC deadlock.
			if m.sendInboundFn != nil {
				m.sendInboundFn(ch.InboundMsg{
					Channel: m.channelName,
					ChatID:  m.chatID,
					Content: "/plugin reload-all",
				})
			}
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
// handlePluginRefresh forces a full refresh of plugin status and widget content.
// Useful when widget content changes on the server but the periodic refresh hasn't fired yet.
func (m *cliModel) handlePluginRefresh() tea.Cmd {
	if m.pluginMgrFn == nil {
		if m.remotePluginCache != nil {
			m.remotePluginCache.Refresh()
			m.showSystemMsg("🔄 Plugin data refreshed.", feedbackInfo)
			m.rc.valid = false
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
			m.rc.valid = false
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
