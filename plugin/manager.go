package plugin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// PluginManager — central lifecycle coordinator for all plugins
// ---------------------------------------------------------------------------

// PluginEntry tracks a loaded plugin and its state.
type PluginEntry struct {
	Manifest *PluginManifest
	Plugin   Plugin
	Context  *pluginContextImpl
	State    PluginState
	Dir      string // plugin directory on disk
	stateMu  sync.Mutex
}

// PluginManager discovers, loads, activates, and manages plugins.
// Integration with xbot subsystems is done via plugin.WireAll() or
// individual Wire* functions in integration.go.
type PluginManager struct {
	mu        sync.RWMutex
	entries   map[string]*PluginEntry // pluginID → entry
	xbotHome  string
	extraDirs []string        // additional plugin search directories
	disabled  map[string]bool // plugin IDs to skip

	// Factory for creating plugin runtimes
	runtimeFactory RuntimeFactory
}

// RuntimeFactory creates Plugin instances for different runtime types.
type RuntimeFactory interface {
	Create(manifest *PluginManifest, dir string) (Plugin, error)
}

// NewPluginManager creates a new PluginManager.
func NewPluginManager(xbotHome string) *PluginManager {
	return &PluginManager{
		entries:  make(map[string]*PluginEntry),
		disabled: make(map[string]bool),
		xbotHome: xbotHome,
	}
}

// SetRuntimeFactory sets the runtime factory for creating plugin instances.
func (pm *PluginManager) SetRuntimeFactory(factory RuntimeFactory) {
	pm.runtimeFactory = factory
}

// AddSearchDirs adds additional directories to scan for plugins.
// Must be called before Discover().
func (pm *PluginManager) AddSearchDirs(dirs []string) {
	pm.extraDirs = append(pm.extraDirs, dirs...)
}

// DisablePlugins adds plugin IDs to the disabled list.
// Disabled plugins are skipped during activation.
func (pm *PluginManager) DisablePlugins(ids []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, id := range ids {
		pm.disabled[id] = true
	}
}

// ---------------------------------------------------------------------------
// Discovery & Loading
// ---------------------------------------------------------------------------

// Discover scans plugin directories and loads manifests.
// Returns the number of plugins discovered.
func (pm *PluginManager) Discover(ctx context.Context) (int, error) {
	dirs := DefaultPluginDirs(pm.xbotHome)
	dirs = append(dirs, pm.extraDirs...)
	manifests := DiscoverPlugins(dirs)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	loaded := 0
	for _, m := range manifests {
		if _, exists := pm.entries[m.ID]; exists {
			log.WithField("plugin", m.ID).Warn("Duplicate plugin ID, skipping")
			continue
		}
		if pm.disabled[m.ID] {
			log.WithField("plugin", m.ID).Debug("Plugin disabled by config, skipping")
			continue
		}

		// Find plugin directory
		pluginDir := pm.findPluginDir(dirs, m.ID)

		entry := &PluginEntry{
			Manifest: m,
			State:    StateDiscovered,
			Dir:      pluginDir,
		}

		// Create storage for this plugin
		storage, err := NewFileStorage(pluginDir)
		if err != nil {
			log.WithField("plugin", m.ID).Warn("Failed to create storage: ", err)
			storage = &noopStorage{}
		}

		// Create PluginContext
		entry.Context = newPluginContext(m, storage, newPluginLogger(m.ID))

		// Create runtime instance
		if pm.runtimeFactory != nil {
			plugin, err := pm.runtimeFactory.Create(m, pluginDir)
			if err != nil {
				log.WithField("plugin", m.ID).Warn("Failed to create runtime: ", err)
				entry.State = StateError
				pm.entries[m.ID] = entry
				continue
			}
			entry.Plugin = plugin
		}

		pm.entries[m.ID] = entry
		loaded++
		log.WithField("plugin", m.ID).Info("Plugin discovered")
	}

	return loaded, nil
}

// findPluginDir locates the directory containing the plugin.
func (pm *PluginManager) findPluginDir(dirs []string, pluginID string) string {
	for _, dir := range dirs {
		candidate := filepath.Join(dir, pluginID)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(dirs[0], pluginID)
}

// ---------------------------------------------------------------------------
// Activation
// ---------------------------------------------------------------------------

// ActivateAll activates all plugins that have "onStart" in their activation events.
func (pm *PluginManager) ActivateAll(ctx context.Context) error {
	pm.mu.RLock()
	entries := make([]*PluginEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		entries = append(entries, e)
	}
	pm.mu.RUnlock()

	var errs []error
	for _, entry := range entries {
		if entry.State != StateDiscovered {
			continue
		}
		if !hasActivationEvent(entry.Manifest, "onStart") {
			continue
		}
		if err := pm.activate(ctx, entry); err != nil {
			errs = append(errs, fmt.Errorf("activate %s: %w", entry.Manifest.ID, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d plugin(s) failed to activate: %v", len(errs), errs)
	}
	return nil
}

// ActivateForEvent activates plugins that match the given activation event.
// Called by the integration layer when events fire (onTool:xxx, onHook:xxx, etc.)
func (pm *PluginManager) ActivateForEvent(ctx context.Context, event string) error {
	pm.mu.RLock()
	var toActivate []*PluginEntry
	for _, e := range pm.entries {
		if e.State == StateDiscovered && hasActivationEvent(e.Manifest, event) {
			toActivate = append(toActivate, e)
		}
	}
	pm.mu.RUnlock()

	for _, entry := range toActivate {
		if err := pm.activate(ctx, entry); err != nil {
			log.WithField("plugin", entry.Manifest.ID).Error("Activation failed: ", err)
		}
	}
	return nil
}

func (pm *PluginManager) activate(ctx context.Context, entry *PluginEntry) error {
	if entry.Plugin == nil {
		entry.stateMu.Lock()
		entry.State = StateError
		entry.stateMu.Unlock()
		return fmt.Errorf("no runtime instance")
	}

	// CAS: StateDiscovered → StateActivating
	entry.stateMu.Lock()
	if entry.State != StateDiscovered {
		entry.stateMu.Unlock()
		return nil // already activating/active, skip
	}
	entry.State = StateActivating
	entry.stateMu.Unlock()

	// Call plugin's Activate method with panic recovery
	var activateErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				activateErr = fmt.Errorf("plugin panic during Activate: %v", r)
			}
		}()
		activateErr = entry.Plugin.Activate(entry.Context)
	}()

	if activateErr != nil {
		entry.stateMu.Lock()
		entry.State = StateError
		entry.stateMu.Unlock()
		return activateErr
	}

	// Note: Capability registration is done by integration.WireAll() after
	// activation. The plugin's tools/hooks/enrichers are collected in
	// entry.Context during Activate() and wired to xbot subsystems separately.

	entry.stateMu.Lock()
	entry.State = StateActive
	entry.stateMu.Unlock()
	log.WithField("plugin", entry.Manifest.ID).Info("Plugin activated")
	return nil
}

// ---------------------------------------------------------------------------
// Deactivation
// ---------------------------------------------------------------------------

// DeactivateAll deactivates all active plugins. Called on shutdown.
func (pm *PluginManager) DeactivateAll(ctx context.Context) {
	pm.mu.RLock()
	entries := make([]*PluginEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		if e.State == StateActive {
			entries = append(entries, e)
		}
	}
	pm.mu.RUnlock()

	for _, entry := range entries {
		entry.State = StateDeactivating
		if err := entry.Plugin.Deactivate(entry.Context); err != nil {
			log.WithField("plugin", entry.Manifest.ID).Warn("Deactivation error: ", err)
		}
		entry.State = StateInactive
		log.WithField("plugin", entry.Manifest.ID).Info("Plugin deactivated")
	}
}

// ---------------------------------------------------------------------------
// Query
// ---------------------------------------------------------------------------

// GetPlugin returns a plugin entry by ID.
func (pm *PluginManager) GetPlugin(id string) (*PluginEntry, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	e, ok := pm.entries[id]
	return e, ok
}

// ListPlugins returns all loaded plugin entries.
func (pm *PluginManager) ListPlugins() []*PluginEntry {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make([]*PluginEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		result = append(result, e)
	}
	return result
}

// ActiveCount returns the number of currently active plugins.
func (pm *PluginManager) ActiveCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	count := 0
	for _, e := range pm.entries {
		if e.State == StateActive {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Manual Registration (for Go native plugins compiled into the binary)
// ---------------------------------------------------------------------------

// Register directly registers a native Go Plugin instance.
// This is for plugins that are compiled into the xbot binary (built-in plugins).
// The plugin must already have its manifest populated.
func (pm *PluginManager) Register(p Plugin) error {
	m := p.Manifest()
	if m.ID == "" {
		return fmt.Errorf("plugin manifest ID is empty")
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if _, exists := pm.entries[m.ID]; exists {
		return fmt.Errorf("plugin %s already registered", m.ID)
	}

	pluginDir := filepath.Join(pm.xbotHome, "plugins", m.ID)
	storage, err := NewFileStorage(pluginDir)
	if err != nil {
		storage = &noopStorage{}
	}

	entry := &PluginEntry{
		Manifest: &m,
		Plugin:   p,
		Context:  newPluginContext(&m, storage, newPluginLogger(m.ID)),
		State:    StateDiscovered,
		Dir:      pluginDir,
	}

	pm.entries[m.ID] = entry
	log.WithField("plugin", m.ID).Info("Native plugin registered")
	return nil
}

// RegisterAndActivate registers a plugin and immediately activates it.
// This is a convenience method that combines Register() and activate().
func (pm *PluginManager) RegisterAndActivate(ctx context.Context, p Plugin) error {
	if err := pm.Register(p); err != nil {
		return err
	}

	m := p.Manifest()
	pm.mu.RLock()
	entry, ok := pm.entries[m.ID]
	pm.mu.RUnlock()

	if !ok {
		return fmt.Errorf("plugin %s not found after registration", m.ID)
	}

	return pm.activate(ctx, entry)
}

// IsPluginActive returns true if the plugin with the given ID is currently active.
func (pm *PluginManager) IsPluginActive(id string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	entry, ok := pm.entries[id]
	if !ok {
		return false
	}
	entry.stateMu.Lock()
	defer entry.stateMu.Unlock()
	return entry.State == StateActive
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// hasActivationEvent checks if a manifest includes the given activation event.
func hasActivationEvent(m *PluginManifest, event string) bool {
	for _, e := range m.ActivationEvents {
		if e == event {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Health Check
// ---------------------------------------------------------------------------

// HealthChecker is an optional interface that plugins can implement to report
// their health status. Plugins that don't implement this are assumed healthy.
type HealthChecker interface {
	HealthCheck(ctx context.Context) error
}

// HealthCheck performs a health check on all active plugins.
// Returns a map of plugin ID → error (nil means healthy).
// Plugins that don't implement HealthChecker are reported as healthy (nil error).
func (pm *PluginManager) HealthCheck(ctx context.Context) map[string]error {
	pm.mu.RLock()
	entries := make([]*PluginEntry, 0, len(pm.entries))
	for _, e := range pm.entries {
		if e.State == StateActive {
			entries = append(entries, e)
		}
	}
	pm.mu.RUnlock()

	results := make(map[string]error)
	for _, entry := range entries {
		if hc, ok := entry.Plugin.(HealthChecker); ok {
			results[entry.Manifest.ID] = hc.HealthCheck(ctx)
		} else {
			results[entry.Manifest.ID] = nil
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// PluginMetrics holds aggregate metrics about the plugin system.
type PluginMetrics struct {
	TotalPlugins   int `json:"totalPlugins"`
	ActivePlugins  int `json:"activePlugins"`
	TotalTools     int `json:"totalTools"`
	TotalHooks     int `json:"totalHooks"`
	TotalEnrichers int `json:"totalEnrichers"`
}

// Metrics returns aggregate metrics about the plugin system.
func (pm *PluginManager) Metrics() PluginMetrics {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	m := PluginMetrics{
		TotalPlugins: len(pm.entries),
	}

	for _, entry := range pm.entries {
		if entry.State == StateActive {
			m.ActivePlugins++
			if entry.Context != nil {
				m.TotalTools += len(entry.Context.GetTools())
				m.TotalHooks += len(entry.Context.GetHooks())
				m.TotalEnrichers += len(entry.Context.GetEnrichers())
			}
		}
	}
	return m
}

// noopStorage is a no-op storage used when storage creation fails.
type noopStorage struct{}

func (n *noopStorage) Get(key string) (string, bool) { return "", false }
func (n *noopStorage) Set(key, value string) error   { return nil }
func (n *noopStorage) Delete(key string) error       { return nil }
func (n *noopStorage) Keys() []string                { return nil }
func (n *noopStorage) Clear() error                  { return nil }

// ---------------------------------------------------------------------------
// String — human-readable status summary
// ---------------------------------------------------------------------------

// String returns a compact status summary of the plugin manager.
// Format: PluginManager{total=5, active=3, error=1, disabled=1}
func (pm *PluginManager) String() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var total, active, errCount, disabled int
	for _, e := range pm.entries {
		total++
		switch e.State {
		case StateActive:
			active++
		case StateError:
			errCount++
		}
	}
	for id := range pm.disabled {
		if _, exists := pm.entries[id]; !exists {
			disabled++
		}
	}

	return fmt.Sprintf("PluginManager{total=%d, active=%d, error=%d, disabled=%d}",
		total, active, errCount, disabled)
}
