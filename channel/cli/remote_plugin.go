package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"xbot/plugin"

	log "xbot/logger"
)

// remotePluginCache caches plugin status and widget zone content for remote mode.
// It fetches data from the server via RPC and stores it locally for the TUI to use.
type remotePluginCache struct {
	mu sync.RWMutex

	// ChatID is the CLI window's session key (working directory path),
	// used to pass to plugin_widgets RPC for per-session widget content.
	chatID string

	// Plugin status
	plugins []remotePluginEntry
	active  int
	total   int

	// Widget zone content (pre-rendered strings from server)
	widgetZones map[string]string // zone name → rendered content
	widgetInfos []plugin.WidgetInfo
	widgetCount int

	// Health & metrics
	health  map[string]error
	metrics *plugin.PluginMetrics

	// RPC caller
	callRPC func(method string, params any) (json.RawMessage, error)

	// onUpdated is called after widget content refresh to trigger TUI redraw.
	onUpdated func()
}

type remotePluginEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	State   string `json:"state"`
	Runtime string `json:"runtime"`
}

// NewRemotePluginCache creates a new remote plugin cache that fetches plugin data
// from the server via the provided RPC call function.
// SetOnUpdated sets the callback invoked after widget content is refreshed.
func (c *remotePluginCache) SetOnUpdated(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onUpdated = fn
}

func NewRemotePluginCache(chatID string, callRPC func(method string, params any) (json.RawMessage, error)) *remotePluginCache {
	return &remotePluginCache{
		chatID:      chatID,
		widgetZones: make(map[string]string),
		callRPC:     callRPC,
	}
}

// Refresh fetches all plugin data from the server (on-demand, e.g. /plugin commands).
func (c *remotePluginCache) Refresh() {
	c.refreshStatus()
	c.refreshWidgets()
}

// UpdateZones replaces cached widget zone content from a server push.
// Called by the WebSocket push handler — no RPC needed.
// Triggers onUpdated callback for TUI redraw.
func (c *remotePluginCache) UpdateZones(zones map[string]string) {
	c.mu.Lock()
	c.widgetZones = zones
	onUpdated := c.onUpdated
	c.mu.Unlock()
	if onUpdated != nil {
		onUpdated()
	}
}

// UpdateChatID updates the cached chatID so subsequent refreshWidgets()
// RPCs fetch widgets for the correct session after Cd.
func (c *remotePluginCache) UpdateChatID(chatID string) {
	c.mu.Lock()
	c.chatID = chatID
	c.mu.Unlock()
}

// refreshStatus fetches plugin list/status from server.
func (c *remotePluginCache) refreshStatus() {
	if c.callRPC == nil {
		return
	}
	raw, err := c.callRPC("plugin_status", nil)
	if err != nil {
		log.WithError(err).Debug("RemotePlugin: plugin_status RPC failed")
		return
	}
	var result struct {
		Plugins []remotePluginEntry `json:"plugins"`
		Active  int                 `json:"active"`
		Total   int                 `json:"total"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Debug("RemotePlugin: unmarshal plugin_status failed")
		return
	}
	c.mu.Lock()
	c.plugins = result.Plugins
	c.active = result.Active
	c.total = result.Total
	c.mu.Unlock()
}

// refreshWidgets fetches widget zone content from server.
func (c *remotePluginCache) refreshWidgets() {
	if c.callRPC == nil {
		return
	}
	raw, err := c.callRPC("plugin_widgets", map[string]string{"chat_id": c.chatID})
	if err != nil {
		log.WithError(err).Warn("RemotePlugin: plugin_widgets RPC failed")
		return
	}
	var result struct {
		Zones map[string]string   `json:"zones"`
		Infos []plugin.WidgetInfo `json:"infos"`
		Count int                 `json:"count"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Warn("RemotePlugin: unmarshal plugin_widgets failed")
		return
	}
	c.mu.Lock()
	c.widgetZones = result.Zones
	c.widgetInfos = result.Infos
	c.widgetCount = result.Count
	onUpdated := c.onUpdated
	c.mu.Unlock()
	if onUpdated != nil {
		onUpdated()
	}
}

// RefreshHealth fetches plugin health from server.
func (c *remotePluginCache) RefreshHealth() map[string]error {
	if c.callRPC == nil {
		return nil
	}
	raw, err := c.callRPC("plugin_health", nil)
	if err != nil {
		return map[string]error{"error": err}
	}
	var statuses map[string]string
	if err := json.Unmarshal(raw, &statuses); err != nil {
		return map[string]error{"error": err}
	}
	results := make(map[string]error, len(statuses))
	for id, status := range statuses {
		if status == "ok" {
			results[id] = nil
		} else {
			results[id] = fmt.Errorf("%s", status)
		}
	}
	c.mu.Lock()
	c.health = results
	c.mu.Unlock()
	return results
}

// RefreshMetrics fetches plugin metrics from server.
func (c *remotePluginCache) RefreshMetrics() *plugin.PluginMetrics {
	if c.callRPC == nil {
		return nil
	}
	raw, err := c.callRPC("plugin_metrics", nil)
	if err != nil {
		return nil
	}
	var m plugin.PluginMetrics
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	c.mu.Lock()
	c.metrics = &m
	c.mu.Unlock()
	return &m
}

// PluginReload tells the server to reload a plugin.
func (c *remotePluginCache) PluginReload(id string) error {
	if c.callRPC == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.callRPC("plugin_reload", map[string]string{"id": id})
	return err
}

// PluginReloadAll tells the server to reload all plugins.
func (c *remotePluginCache) PluginReloadAll() error {
	if c.callRPC == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.callRPC("plugin_reload_all", nil)
	return err
}

// PluginInstall tells the server to install a plugin from a directory.
func (c *remotePluginCache) PluginInstall(sourceDir string) (pluginID, pluginDir string, err error) {
	if c.callRPC == nil {
		return "", "", fmt.Errorf("not connected")
	}
	raw, err := c.callRPC("plugin_install", map[string]string{"source_dir": sourceDir})
	if err != nil {
		return "", "", err
	}
	var result struct {
		ID  string `json:"id"`
		Dir string `json:"dir"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", err
	}
	return result.ID, result.Dir, nil
}

// PluginUninstall tells the server to uninstall a plugin.
func (c *remotePluginCache) PluginUninstall(id string) error {
	if c.callRPC == nil {
		return fmt.Errorf("not connected")
	}
	_, err := c.callRPC("plugin_uninstall", map[string]string{"id": id})
	return err
}

// ── Read methods (thread-safe) ──

func (c *remotePluginCache) Plugins() []remotePluginEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make([]remotePluginEntry, len(c.plugins))
	copy(cp, c.plugins)
	return cp
}

func (c *remotePluginCache) Active() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.active
}

func (c *remotePluginCache) Total() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.total
}

func (c *remotePluginCache) WidgetZone(zone string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.widgetZones[zone]
}

func (c *remotePluginCache) WidgetInfos() []plugin.WidgetInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := make([]plugin.WidgetInfo, len(c.widgetInfos))
	copy(cp, c.widgetInfos)
	return cp
}

func (c *remotePluginCache) WidgetCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.widgetCount
}

func (c *remotePluginCache) Health() map[string]error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.health == nil {
		return nil
	}
	cp := make(map[string]error, len(c.health))
	for k, v := range c.health {
		cp[k] = v
	}
	return cp
}

func (c *remotePluginCache) Metrics() *plugin.PluginMetrics {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.metrics == nil {
		return nil
	}
	cp := *c.metrics
	return &cp
}

// HasPlugins returns true if the remote server has any plugins loaded.
func (c *remotePluginCache) HasPlugins() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.total > 0
}

// ── Background refresh goroutine ──

// ── Remote /plugin command formatters ──

// FormatStatus returns a formatted plugin status string.
func (c *remotePluginCache) FormatStatus() string {
	entries := c.Plugins()
	if len(entries) == 0 {
		return "🔌 No plugins loaded."
	}
	return fmt.Sprintf("🔌 Plugins: %d loaded, %d active\nUse /plugin list for details, /plugin health for status.",
		c.Total(), c.Active())
}

// FormatList returns a formatted plugin list string.
func (c *remotePluginCache) FormatList() string {
	entries := c.Plugins()
	if len(entries) == 0 {
		return "No plugins loaded."
	}
	var sb strings.Builder
	sb.WriteString("🔌 Plugins\n\n")
	fmt.Fprintf(&sb, "  %-20s %-16s %-10s %-14s %s\n",
		"ID", "Name", "Version", "State", "Runtime")
	sb.WriteString("  ─────────────────────────────────────────────────────────\n")
	for _, e := range entries {
		fmt.Fprintf(&sb, "  %-20s %-16s %-10s %-14s %-8s\n",
			e.ID, e.Name, e.Version, e.State, e.Runtime)
	}
	return sb.String()
}

// FormatHealth returns a formatted health check string.
func (c *remotePluginCache) FormatHealth(results map[string]error) string {
	if len(results) == 0 {
		return "No plugins to check."
	}
	var sb strings.Builder
	sb.WriteString("🔍 Plugin Health\n\n")
	for id, err := range results {
		icon := "🟢"
		status := "healthy"
		if err != nil {
			icon = "🔴"
			status = err.Error()
		}
		fmt.Fprintf(&sb, "  %s %s: %s\n", icon, id, status)
	}
	return sb.String()
}

// FormatMetrics returns a formatted metrics string.
func (c *remotePluginCache) FormatMetrics() string {
	m := c.Metrics()
	if m == nil {
		return "Plugin metrics not available."
	}
	var sb strings.Builder
	sb.WriteString("# Plugin Metrics\n\n")
	sb.WriteString("| | |\n|---|---|\n")
	fmt.Fprintf(&sb, "| **Total plugins** | **%d** |\n", m.TotalPlugins)
	fmt.Fprintf(&sb, "| Active plugins | %d |\n", m.ActivePlugins)
	fmt.Fprintf(&sb, "| Registered tools | %d |\n", m.TotalTools)
	fmt.Fprintf(&sb, "| Registered hooks | %d |\n", m.TotalHooks)
	fmt.Fprintf(&sb, "| Registered enrichers | %d |\n", m.TotalEnrichers)
	if m.TotalPlugins > 0 {
		activeRate := float64(m.ActivePlugins) / float64(m.TotalPlugins) * 100
		fmt.Fprintf(&sb, "| **Active rate** | **%.0f%%** |\n", activeRate)
	}
	return sb.String()
}

// FormatWidgets returns a formatted widget list string.
func (c *remotePluginCache) FormatWidgets() string {
	infos := c.WidgetInfos()
	if len(infos) == 0 {
		return fmt.Sprintf("🖼️  No UI widgets registered.\n   Plugin system: %d active plugins, %d total widgets in registry.",
			c.Active(), c.WidgetCount())
	}
	var sb strings.Builder
	sb.WriteString("🖼️  UI Widgets:\n")
	for _, info := range infos {
		fmt.Fprintf(&sb, "  [%s/%s] zone=%s priority=%d\n",
			info.PluginID, info.WidgetID, info.Zone, info.Priority)
	}
	return sb.String()
}
