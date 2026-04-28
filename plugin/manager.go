package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// PluginManager — central lifecycle coordinator for all plugins
// ---------------------------------------------------------------------------

// PluginEntry tracks a loaded plugin and its state.
type PluginEntry struct {
	Manifest    *PluginManifest
	Plugin      Plugin
	Context     *pluginContextImpl
	State       PluginState
	Dir         string // plugin directory on disk
	stateMu     sync.Mutex
	retryCount  int       // number of consecutive retry attempts
	lastError   error     // most recent error
	lastErrorAt time.Time // when the last error occurred
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
	bus            *PluginEventBus

	// Auto-retry configuration
	autoRetry     bool
	maxRetries    int
	retryCancel   context.CancelFunc // stops the retry goroutine
	retryMu       sync.Mutex         // protects autoRetry/maxRetries/retryCancel
	retryInterval time.Duration      // scan interval for retry loop (default 5s)
	auditLog      *AuditLogger

	// Rate limiting and quota (optional; nil = no enforcement)
	rateLimiter  *PluginRateLimiter
	quotaManager *PluginQuotaManager
	configStore  *PluginConfigStore
	notifier     *PluginEventNotifier

	activationOrder []string // topological activation order (computed after Discover)
}

// RuntimeFactory creates Plugin instances for different runtime types.
type RuntimeFactory interface {
	Create(manifest *PluginManifest, dir string) (Plugin, error)
}

// NewPluginManager creates a new PluginManager.
func NewPluginManager(xbotHome string) *PluginManager {
	auditPath := filepath.Join(xbotHome, "plugins", "audit.jsonl")
	al, err := NewAuditLogger(auditPath)
	if err != nil {
		log.WithField("path", auditPath).Warn("Failed to create audit logger: ", err)
	}

	return &PluginManager{
		entries:       make(map[string]*PluginEntry),
		disabled:      make(map[string]bool),
		xbotHome:      xbotHome,
		bus:           NewPluginEventBus(),
		retryInterval: 5 * time.Second,
		auditLog:      al,
		configStore:   NewPluginConfigStore(xbotHome),
		notifier:      NewPluginEventNotifier(),
	}
}

// SetRuntimeFactory sets the runtime factory for creating plugin instances.
func (pm *PluginManager) SetRuntimeFactory(factory RuntimeFactory) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.runtimeFactory = factory
}

// Bus returns the plugin event bus.
func (pm *PluginManager) Bus() *PluginEventBus {
	return pm.bus
}

// AuditLog returns the audit logger, or nil if initialization failed.
func (pm *PluginManager) AuditLog() *AuditLogger {
	return pm.auditLog
}

// OnPluginEvent registers a callback to receive plugin lifecycle events.
// Returns an error if callback is nil.
func (pm *PluginManager) OnPluginEvent(callback PluginEventCallback) error {
	return pm.notifier.Subscribe(callback)
}

// RemoveOnPluginEvent removes a previously registered lifecycle event callback.
func (pm *PluginManager) RemoveOnPluginEvent(callback PluginEventCallback) error {
	return pm.notifier.Unsubscribe(callback)
}

// notifyEvent sends a lifecycle event via the notifier.
func (pm *PluginManager) notifyEvent(typ PluginEventType, pluginID string, err error, data any) {
	if pm.notifier == nil {
		return
	}
	event := PluginEvent{
		Type:      typ,
		PluginID:  pluginID,
		Timestamp: time.Now(),
		Data:      data,
	}
	if err != nil {
		event.Error = err
	}
	pm.notifier.Notify(event)
}

// audit records an audit entry if the audit logger is available.
func (pm *PluginManager) audit(pluginID, action string, details map[string]any, err error) {
	if pm.auditLog == nil {
		return
	}
	entry := AuditEntry{
		PluginID: pluginID,
		Action:   action,
		Details:  details,
	}
	if err != nil {
		entry.Error = err.Error()
	}
	pm.auditLog.Log(entry)
}

// ---------------------------------------------------------------------------
// Auto-Retry
// ---------------------------------------------------------------------------

// SetAutoRetry enables or disables automatic retry for plugins in error state.
// When enabled, a background goroutine periodically scans error-state plugins
// and attempts to reactivate them with exponential backoff.
// maxRetries: maximum retry attempts per plugin (0 = unlimited).
func (pm *PluginManager) SetAutoRetry(enabled bool, maxRetries int) {
	pm.retryMu.Lock()
	defer pm.retryMu.Unlock()

	// Stop existing goroutine if running
	if pm.retryCancel != nil {
		pm.retryCancel()
		pm.retryCancel = nil
	}

	pm.autoRetry = enabled
	if maxRetries < 0 {
		maxRetries = 0
	}
	pm.maxRetries = maxRetries

	if enabled {
		ctx, cancel := context.WithCancel(context.Background())
		pm.retryCancel = cancel
		go pm.retryLoop(ctx)
		log.Info("Plugin auto-retry enabled")
	}
}

// SetRetryInterval sets the scan interval for the auto-retry loop.
// Intended for testing; production code should use the default (5s).
func (pm *PluginManager) SetRetryInterval(d time.Duration) {
	pm.retryMu.Lock()
	defer pm.retryMu.Unlock()
	if d > 0 {
		pm.retryInterval = d
	}
}

const (
	retryInitialDelay = 1 * time.Second
	retryMaxDelay     = 30 * time.Second
)

// retryLoop periodically scans for error-state plugins and attempts reactivation.
func (pm *PluginManager) retryLoop(ctx context.Context) {
	pm.retryMu.Lock()
	interval := pm.retryInterval
	pm.retryMu.Unlock()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.retryErrorPlugins(ctx)
		}
	}
}

// retryErrorPlugins scans all entries and retries activation for error-state plugins.
func (pm *PluginManager) retryErrorPlugins(ctx context.Context) {
	pm.retryMu.Lock()
	maxRetries := pm.maxRetries
	pm.retryMu.Unlock()

	entries := pm.ListPlugins()
	for _, entry := range entries {
		entry.stateMu.Lock()
		if entry.State != StateError {
			entry.stateMu.Unlock()
			continue
		}
		if maxRetries > 0 && entry.retryCount >= maxRetries {
			entry.stateMu.Unlock()
			continue
		}
		entry.retryCount++
		retryNum := entry.retryCount
		entry.stateMu.Unlock()

		// Exponential backoff: 1s * 2^(retryNum-1), capped at 30s
		shift := retryNum - 1
		if shift > 5 {
			shift = 5 // 2^5 = 32s, capped at retryMaxDelay (30s)
		}
		delay := retryInitialDelay * time.Duration(1<<uint(shift))
		if delay > retryMaxDelay {
			delay = retryMaxDelay
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		log.WithField("plugin", entry.Manifest.ID).
			WithField("attempt", retryNum).
			Info("Retrying plugin activation")

		// Reset to discovered and attempt activation
		entry.stateMu.Lock()
		entry.State = StateDiscovered
		entry.stateMu.Unlock()

		if err := pm.activate(ctx, entry); err != nil {
			entry.stateMu.Lock()
			entry.lastError = err
			entry.lastErrorAt = time.Now()
			entry.stateMu.Unlock()

			pm.notifyPluginError(entry, "activate", err)

			log.WithField("plugin", entry.Manifest.ID).
				WithField("attempt", retryNum).
				Warn("Plugin retry failed: ", err)
		} else {
			// Success — reset retry counter
			entry.stateMu.Lock()
			entry.retryCount = 0
			entry.lastError = nil
			entry.stateMu.Unlock()

			pm.notifyEvent(PluginEventActivated, entry.Manifest.ID, nil, map[string]any{"recovered": true, "attempt": retryNum})
			log.WithField("plugin", entry.Manifest.ID).
				WithField("attempt", retryNum).
				Info("Plugin recovered after retry")
		}
	}
}

// notifyPluginError invokes the plugin's error callback if registered.
func (pm *PluginManager) notifyPluginError(entry *PluginEntry, phase string, err error) {
	if entry.Context == nil {
		return
	}
	callback := entry.Context.GetErrorCallback()
	if callback == nil {
		return
	}

	// Run callback with panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("plugin", entry.Manifest.ID).
					Warn("Plugin error callback panicked: ", r)
			}
		}()
		callback(context.Background(), fmt.Errorf("plugin %s [%s]: %w", entry.Manifest.ID, phase, err))
	}()
}

// AddSearchDirs adds additional directories to scan for plugins.
// Must be called before Discover().
func (pm *PluginManager) AddSearchDirs(dirs []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.extraDirs = append(pm.extraDirs, dirs...)
}

// DisablePlugins adds plugin IDs to the disabled list.
// Disabled plugins are skipped during activation.
func (pm *PluginManager) DisablePlugins(ids []string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, id := range ids {
		pm.disabled[id] = true
		pm.audit(id, AuditDisable, nil, nil)
	}
}

// ---------------------------------------------------------------------------
// Discovery & Loading
// ---------------------------------------------------------------------------

// Discover scans plugin directories and loads manifests.
// Returns the number of plugins discovered.
func (pm *PluginManager) Discover(ctx context.Context) (int, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	dirs := DefaultPluginDirs(pm.xbotHome)
	dirs = append(dirs, pm.extraDirs...)
	manifests := DiscoverPlugins(dirs)

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
		entry.Context = newPluginContext(m, storage, newPluginLogger(m.ID), pm.bus, pm.configStore)

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

	// Resolve dependency activation order
	if err := pm.resolveActivationOrder(); err != nil {
		log.Error("Plugin dependency resolution failed: ", err)
		// Don't fail discovery — plugins are still loaded, just without guaranteed order
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
	if len(dirs) == 0 {
		return pluginID
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
	if len(pm.activationOrder) > 0 {
		for _, id := range pm.activationOrder {
			if e, ok := pm.entries[id]; ok {
				entries = append(entries, e)
			}
		}
	} else {
		for _, e := range pm.entries {
			entries = append(entries, e)
		}
	}
	pm.mu.RUnlock()

	var errs []error
	for _, entry := range entries {
		entry.stateMu.Lock()
		if entry.State != StateDiscovered {
			entry.stateMu.Unlock()
			continue
		}
		entry.stateMu.Unlock()
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
		e.stateMu.Lock()
		isDiscovered := e.State == StateDiscovered
		e.stateMu.Unlock()
		if isDiscovered && hasActivationEvent(e.Manifest, event) {
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
		entry.lastError = fmt.Errorf("no runtime instance")
		entry.lastErrorAt = time.Now()
		entry.stateMu.Unlock()
		return entry.lastError
	}

	// CAS: StateDiscovered → StateActivating
	entry.stateMu.Lock()
	if entry.State != StateDiscovered {
		entry.stateMu.Unlock()
		return nil // already activating/active, skip
	}
	entry.State = StateActivating
	entry.stateMu.Unlock()

	// Apply activation timeout from manifest
	timeout := entry.Manifest.Timeout
	if timeout <= 0 {
		timeout = DefaultPluginTimeout
	}

	// Run activation in a goroutine with timeout
	type activateResult struct {
		err error
	}
	done := make(chan activateResult, 1)
	go func() {
		var activateErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					activateErr = &ErrPluginActivationFailed{PluginID: entry.Manifest.ID, Err: fmt.Errorf("panic: %v", r)}
				}
			}()
			activateErr = entry.Plugin.Activate(entry.Context)
		}()
		done <- activateResult{err: activateErr}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-done:
		if result.err != nil {
			// Wrap in ErrPluginActivationFailed (avoid double-wrapping)
			if _, ok := result.err.(*ErrPluginActivationFailed); !ok {
				result.err = &ErrPluginActivationFailed{PluginID: entry.Manifest.ID, Err: result.err}
			}
			entry.stateMu.Lock()
			entry.State = StateError
			entry.lastError = result.err
			entry.lastErrorAt = time.Now()
			entry.stateMu.Unlock()
			pm.notifyEvent(PluginEventError, entry.Manifest.ID, result.err, map[string]any{"phase": "activate"})
			pm.notifyPluginError(entry, "activate", result.err)
			pm.audit(entry.Manifest.ID, AuditActivate, map[string]any{"state": "error"}, result.err)
			return result.err
		}

		// CAS: only set Active if still in Activating state
		entry.stateMu.Lock()
		if entry.State == StateActivating {
			entry.State = StateActive
			entry.retryCount = 0
			entry.lastError = nil
		}
		entry.stateMu.Unlock()
		pm.notifyEvent(PluginEventActivated, entry.Manifest.ID, nil, nil)
		log.WithField("plugin", entry.Manifest.ID).Info("Plugin activated")
		pm.audit(entry.Manifest.ID, AuditActivate, map[string]any{"state": "active"}, nil)
		return nil

	case <-timer.C:
		// Timeout — set error state; the goroutine may still be running
		// but will find state != StateActivating and not overwrite.
		timeoutErr := &ErrPluginActivationFailed{PluginID: entry.Manifest.ID, Err: fmt.Errorf("timed out after %v", timeout)}
		entry.stateMu.Lock()
		if entry.State == StateActivating {
			entry.State = StateError
			entry.lastError = timeoutErr
			entry.lastErrorAt = time.Now()
		}
		entry.stateMu.Unlock()
		pm.notifyEvent(PluginEventError, entry.Manifest.ID, timeoutErr, map[string]any{"phase": "activate", "timeout": true})
		pm.notifyPluginError(entry, "activate", timeoutErr)
		pm.audit(entry.Manifest.ID, AuditActivate, map[string]any{"state": "timeout"}, timeoutErr)
		return timeoutErr
	}
}

// ---------------------------------------------------------------------------
// Deactivation
// ---------------------------------------------------------------------------

// DeactivateAll deactivates all active plugins. Called on shutdown.
func (pm *PluginManager) DeactivateAll(ctx context.Context) {
	// Stop auto-retry goroutine
	pm.retryMu.Lock()
	if pm.retryCancel != nil {
		pm.retryCancel()
		pm.retryCancel = nil
	}
	pm.autoRetry = false
	pm.retryMu.Unlock()

	pm.mu.RLock()
	entries := make([]*PluginEntry, 0, len(pm.entries))
	if len(pm.activationOrder) > 0 {
		// Reverse order: dependents before dependencies
		for i := len(pm.activationOrder) - 1; i >= 0; i-- {
			id := pm.activationOrder[i]
			if e, ok := pm.entries[id]; ok {
				e.stateMu.Lock()
				if e.State == StateActive {
					e.stateMu.Unlock()
					entries = append(entries, e)
				} else {
					e.stateMu.Unlock()
				}
			}
		}
	} else {
		for _, e := range pm.entries {
			e.stateMu.Lock()
			if e.State == StateActive {
				e.stateMu.Unlock()
				entries = append(entries, e)
			} else {
				e.stateMu.Unlock()
			}
		}
	}
	pm.mu.RUnlock()

	for _, entry := range entries {
		entry.stateMu.Lock()
		entry.State = StateDeactivating
		entry.stateMu.Unlock()
		if err := entry.Plugin.Deactivate(entry.Context); err != nil {
			pm.notifyEvent(PluginEventError, entry.Manifest.ID, err, map[string]any{"phase": "deactivate"})
			log.WithField("plugin", entry.Manifest.ID).Warn("Deactivation error: ", err)
			pm.audit(entry.Manifest.ID, AuditDeactivate, nil, err)
		}
		entry.stateMu.Lock()
		entry.State = StateInactive
		entry.stateMu.Unlock()
		pm.notifyEvent(PluginEventDeactivated, entry.Manifest.ID, nil, nil)
		log.WithField("plugin", entry.Manifest.ID).Info("Plugin deactivated")
		pm.audit(entry.Manifest.ID, AuditDeactivate, nil, nil)
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
		return fmt.Errorf("%w: %s", ErrPluginAlreadyRegistered, m.ID)
	}

	pluginDir := filepath.Join(pm.xbotHome, "plugins", m.ID)
	storage, err := NewFileStorage(pluginDir)
	if err != nil {
		storage = &noopStorage{}
	}

	entry := &PluginEntry{
		Manifest: &m,
		Plugin:   p,
		Context:  newPluginContext(&m, storage, newPluginLogger(m.ID), pm.bus, pm.configStore),
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
		return fmt.Errorf("%w: %s (after registration)", ErrPluginNotFound, m.ID)
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
// Hot Reload
// ---------------------------------------------------------------------------

// Reload unloads and reloads a single plugin by ID.
// The plugin's directory is re-scanned (not a full Discover).
// The manager's write lock is held for the entire operation.
func (pm *PluginManager) Reload(ctx context.Context, pluginID string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	entry, ok := pm.entries[pluginID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrPluginNotFound, pluginID)
	}

	// Deactivate if active
	entry.stateMu.Lock()
	isActive := entry.State == StateActive
	if isActive {
		entry.State = StateDeactivating
	}
	entry.stateMu.Unlock()
	if isActive {
		if entry.Plugin != nil {
			if err := entry.Plugin.Deactivate(entry.Context); err != nil {
				log.WithField("plugin", pluginID).Warn("Deactivation error during reload: ", err)
			}
		}
		entry.stateMu.Lock()
		entry.State = StateInactive
		entry.stateMu.Unlock()
	}

	// Delete old entry
	delete(pm.entries, pluginID)

	// Re-scan only this plugin's directory
	dirs := DefaultPluginDirs(pm.xbotHome)
	dirs = append(dirs, pm.extraDirs...)
	pluginDir := pm.findPluginDir(dirs, pluginID)

	m, err := LoadManifest(pluginDir)
	if err != nil {
		pm.notifyEvent(PluginEventError, pluginID, err, map[string]any{"phase": "reload", "step": "manifest"})
		return fmt.Errorf("reload %s: failed to load manifest: %w", pluginID, err)
	}

	storage, err2 := NewFileStorage(pluginDir)
	if err2 != nil {
		log.WithField("plugin", m.ID).Warn("Failed to create storage on reload: ", err2)
		storage = &noopStorage{}
	}

	pm.configStore.InvalidateCache(pluginID)

	newEntry := &PluginEntry{
		Manifest: m,
		State:    StateDiscovered,
		Dir:      pluginDir,
		Context:  newPluginContext(m, storage, newPluginLogger(m.ID), pm.bus, pm.configStore),
	}

	if pm.runtimeFactory != nil {
		plugin, err3 := pm.runtimeFactory.Create(m, pluginDir)
		if err3 != nil {
			log.WithField("plugin", m.ID).Warn("Failed to create runtime on reload: ", err3)
			newEntry.State = StateError
			pm.entries[m.ID] = newEntry
			pm.notifyEvent(PluginEventError, m.ID, err3, map[string]any{"phase": "reload", "step": "runtime"})
			return fmt.Errorf("reload %s: failed to create runtime: %w", m.ID, err3)
		}
		newEntry.Plugin = plugin
	}

	pm.entries[m.ID] = newEntry

	// Activate if has onStart event (activate only touches entry.stateMu, not pm.mu)
	if hasActivationEvent(m, "onStart") {
		if err4 := pm.activate(ctx, newEntry); err4 != nil {
			pm.notifyEvent(PluginEventError, m.ID, err4, map[string]any{"phase": "reload", "step": "activate"})
			return fmt.Errorf("reload %s: activation failed: %w", m.ID, err4)
		}
	}

	pm.notifyEvent(PluginEventReloaded, m.ID, nil, nil)
	log.WithField("plugin", m.ID).Info("Plugin reloaded")
	pm.audit(m.ID, AuditReload, nil, nil)
	return nil
}

// ReloadAll deactivates all plugins, clears entries, re-discovers, and re-activates.
func (pm *PluginManager) ReloadAll(ctx context.Context) error {
	pm.DeactivateAll(ctx)

	pm.mu.Lock()
	pm.entries = make(map[string]*PluginEntry)
	pm.mu.Unlock()

	if _, err := pm.Discover(ctx); err != nil {
		return fmt.Errorf("reload all: discover failed: %w", err)
	}

	return pm.ActivateAll(ctx)
}

// ---------------------------------------------------------------------------
// Install & Uninstall
// ---------------------------------------------------------------------------

// InstallPlugin copies a plugin directory from source to the plugin install directory
// (~/.xbot/plugins/<id>/) and loads it into the manager.
// If a plugin with the same ID already exists, it returns an error (use Uninstall first).
//
// Note: The manager's write lock is held for the entire operation, including file copy.
// This prevents concurrent install of the same plugin ID. Installation is a low-frequency
// operation, so blocking other plugin operations during copy is acceptable.
func (pm *PluginManager) InstallPlugin(ctx context.Context, sourceDir string) (*PluginEntry, error) {
	// Step 1: Validate source directory — must contain a valid manifest
	manifest, err := LoadManifest(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("install: invalid plugin at %s: %w", sourceDir, err)
	}

	pluginID := manifest.ID

	// Hold write lock for the entire install to prevent TOCTOU races
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Step 2: Check for existing plugin with same ID
	if _, exists := pm.entries[pluginID]; exists {
		return nil, fmt.Errorf("install: %w: %s (uninstall it first)", ErrPluginAlreadyRegistered, pluginID)
	}

	// Step 3: Determine target directory
	targetDir := filepath.Join(pm.xbotHome, "plugins", pluginID)

	// Step 4: Clean target and copy source
	if err := os.RemoveAll(targetDir); err != nil {
		log.WithField("dir", targetDir).Debug("Clean target dir (may not exist): ", err)
	}
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return nil, fmt.Errorf("install: failed to create plugin directory: %w", err)
	}
	if err := copyDir(sourceDir, targetDir); err != nil {
		os.RemoveAll(targetDir)
		return nil, fmt.Errorf("install: failed to copy plugin files: %w", err)
	}

	// Step 5: Load manifest from the installed location (re-validate after copy)
	installedManifest, err := LoadManifest(targetDir)
	if err != nil {
		os.RemoveAll(targetDir)
		return nil, fmt.Errorf("install: failed to load manifest after copy: %w", err)
	}

	// Step 6: Create entry
	storage, storageErr := NewFileStorage(targetDir)
	if storageErr != nil {
		log.WithField("plugin", pluginID).Warn("Failed to create storage: ", storageErr)
		storage = &noopStorage{}
	}

	entry := &PluginEntry{
		Manifest: installedManifest,
		State:    StateDiscovered,
		Dir:      targetDir,
		Context:  newPluginContext(installedManifest, storage, newPluginLogger(pluginID), pm.bus, pm.configStore),
	}

	if pm.runtimeFactory != nil {
		p, err3 := pm.runtimeFactory.Create(installedManifest, targetDir)
		if err3 != nil {
			log.WithField("plugin", pluginID).Warn("Failed to create runtime: ", err3)
			entry.State = StateError
			pm.entries[pluginID] = entry
			pm.notifyEvent(PluginEventError, pluginID, err3, map[string]any{"phase": "install", "step": "runtime"})
			pm.audit(pluginID, AuditInstall, nil, err3)
			return entry, fmt.Errorf("install: runtime creation failed: %w", err3)
		}
		entry.Plugin = p
	}

	pm.entries[pluginID] = entry

	// Step 7: Auto-activate if has onStart event
	if hasActivationEvent(installedManifest, "onStart") {
		if err4 := pm.activate(ctx, entry); err4 != nil {
			return entry, fmt.Errorf("install: activation failed: %w", err4)
		}
	}

	pm.notifyEvent(PluginEventInstalled, pluginID, nil, map[string]any{"dir": targetDir})
	log.WithField("plugin", pluginID).WithField("dir", targetDir).Info("Plugin installed")
	pm.audit(pluginID, AuditInstall, map[string]any{"dir": targetDir}, nil)
	return entry, nil
}

// UninstallPlugin deactivates (if active), removes the plugin entry from the manager,
// and deletes the plugin directory from disk.
// Only plugin directories under xbotHome are deleted; registered native plugins
// are only removed from the manager.
func (pm *PluginManager) UninstallPlugin(ctx context.Context, pluginID string) error {
	pm.mu.Lock()
	entry, exists := pm.entries[pluginID]
	if !exists {
		pm.mu.Unlock()
		return fmt.Errorf("uninstall: %w: %s", ErrPluginNotFound, pluginID)
	}
	pluginDir := entry.Dir

	// Deactivate if active (under write lock)
	if entry.State == StateActive && entry.Plugin != nil {
		entry.stateMu.Lock()
		entry.State = StateDeactivating
		entry.stateMu.Unlock()
		if err := entry.Plugin.Deactivate(entry.Context); err != nil {
			log.WithField("plugin", pluginID).Warn("Deactivation error during uninstall: ", err)
		}
		entry.stateMu.Lock()
		entry.State = StateInactive
		entry.stateMu.Unlock()
	}

	// Remove from entries map
	delete(pm.entries, pluginID)
	delete(pm.disabled, pluginID)
	pm.notifyEvent(PluginEventUninstalled, pluginID, nil, map[string]any{"dir": pluginDir})
	pm.audit(pluginID, AuditUninstall, map[string]any{"dir": pluginDir}, nil)
	pm.mu.Unlock()

	// Delete directory from disk (outside lock — pure I/O)
	// Safety: only delete if directory is under xbotHome
	if pluginDir != "" {
		realDir, err1 := filepath.EvalSymlinks(pluginDir)
		if err1 != nil {
			realDir = pluginDir
		}
		realHome, err2 := filepath.EvalSymlinks(pm.xbotHome)
		if err2 != nil {
			realHome = pm.xbotHome
		}
		if strings.HasPrefix(realDir, realHome) {
			if err := os.RemoveAll(pluginDir); err != nil {
				log.WithField("plugin", pluginID).WithField("dir", pluginDir).
					Warn("Failed to remove plugin directory: ", err)
			}
		} else {
			log.WithField("plugin", pluginID).WithField("dir", pluginDir).
				Warn("Skipping directory removal: plugin directory is outside xbot home")
		}
	}

	log.WithField("plugin", pluginID).Info("Plugin uninstalled")
	return nil
}

// copyDir recursively copies a directory from src to dst.
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, info.Mode())
	})
}

// ---------------------------------------------------------------------------
// Config Watch — periodic polling for plugin config changes
// ---------------------------------------------------------------------------

// configWatcher periodically checks the plugin config file for changes.
type configWatcher struct {
	mu          sync.Mutex
	pm          *PluginManager
	configPath  string
	lastModTime time.Time
	lastConfig  *configChangeState
	interval    time.Duration
}

// configChangeState captures the relevant config fields to detect changes.
type configChangeState struct {
	DisabledPlugins map[string]bool
}

// WatchConfig starts a background goroutine that periodically polls the config file
// for changes to plugins.disabled_plugins. When a change is detected, it applies the
// delta: newly disabled plugins are deactivated, newly enabled plugins are discovered
// and activated.
//
// Returns a stop channel that can be closed to stop watching.
// The caller should close the returned channel when shutting down.
func (pm *PluginManager) WatchConfig(configPath string, interval time.Duration) chan struct{} {
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}

	stop := make(chan struct{})

	w := &configWatcher{
		pm:         pm,
		configPath: configPath,
		interval:   interval,
	}

	// Initialize baseline
	if info, err := os.Stat(configPath); err == nil {
		w.lastModTime = info.ModTime()
	}
	w.lastConfig = w.readConfig()

	go w.loop(stop)

	return stop
}

func (w *configWatcher) loop(stop chan struct{}) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			w.check()
		}
	}
}

func (w *configWatcher) check() {
	w.mu.Lock()
	defer w.mu.Unlock()

	info, err := os.Stat(w.configPath)
	if err != nil {
		return
	}

	if info.ModTime().Equal(w.lastModTime) {
		return
	}
	w.lastModTime = info.ModTime()

	newState := w.readConfig()
	if newState == nil {
		return
	}

	w.applyDiff(w.lastConfig, newState)
	w.lastConfig = newState
}

func (w *configWatcher) readConfig() *configChangeState {
	data, err := os.ReadFile(w.configPath)
	if err != nil {
		return nil
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	state := &configChangeState{DisabledPlugins: make(map[string]bool)}

	pluginsVal := raw["plugins"]
	if pluginsMap, ok := pluginsVal.(map[string]any); ok {
		if disabledVal, ok := pluginsMap["disabled_plugins"]; ok {
			if disabledList, ok := disabledVal.([]any); ok {
				for _, id := range disabledList {
					if idStr, ok := id.(string); ok {
						state.DisabledPlugins[idStr] = true
					}
				}
			}
		}
	}

	return state
}

func (w *configWatcher) applyDiff(old, new_ *configChangeState) {
	if old == nil {
		old = &configChangeState{DisabledPlugins: make(map[string]bool)}
	}
	if new_ == nil {
		return
	}

	pm := w.pm
	ctx := context.Background()

	// Find newly disabled plugins → deactivate them
	for id := range new_.DisabledPlugins {
		if !old.DisabledPlugins[id] {
			if entry, ok := pm.GetPlugin(id); ok {
				entry.stateMu.Lock()
				isActive := entry.State == StateActive
				entry.stateMu.Unlock()
				if isActive {
					if entry.Plugin != nil {
						entry.stateMu.Lock()
						entry.State = StateDeactivating
						entry.stateMu.Unlock()
						_ = entry.Plugin.Deactivate(entry.Context)
						entry.stateMu.Lock()
						entry.State = StateInactive
						entry.stateMu.Unlock()
						pm.notifyEvent(PluginEventDeactivated, id, nil, map[string]any{"reason": "config_change"})
						log.WithField("plugin", id).Info("Plugin deactivated by config change (newly disabled)")
					}
				}
			}
			// Also add to disabled set
			pm.mu.Lock()
			pm.disabled[id] = true
			pm.mu.Unlock()
		}
	}

	// Find newly enabled plugins (removed from disabled list) → discover & activate
	for id := range old.DisabledPlugins {
		if !new_.DisabledPlugins[id] {
			// Remove from disabled set
			pm.mu.Lock()
			delete(pm.disabled, id)
			pm.mu.Unlock()

			if entry, ok := pm.GetPlugin(id); ok {
				// Entry exists but inactive → just reactivate
				entry.stateMu.Lock()
				isInactive := entry.State == StateInactive
				entry.stateMu.Unlock()
				if isInactive && hasActivationEvent(entry.Manifest, "onStart") {
					if err := pm.activate(ctx, entry); err != nil {
						log.WithField("plugin", id).Warn("Failed to re-activate after config enable: ", err)
					}
				}
			} else {
				// Entry doesn't exist → discover from disk and activate
				pm.Discover(ctx)
				if entry2, ok := pm.GetPlugin(id); ok && hasActivationEvent(entry2.Manifest, "onStart") {
					if err := pm.activate(ctx, entry2); err != nil {
						log.WithField("plugin", id).Warn("Failed to activate after config enable: ", err)
					}
				}
			}
			log.WithField("plugin", id).Info("Plugin enabled by config change")
		}
	}
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

// resolveActivationOrder computes the topological activation order from current entries.
func (pm *PluginManager) resolveActivationOrder() error {
	dr := NewDependencyResolver()
	for _, e := range pm.entries {
		dr.AddManifest(e.Manifest)
	}

	// Validate first — missing deps mark plugins as error
	if err := dr.Validate(); err != nil {
		if missing, ok := err.(*ErrMissingDependency); ok {
			if entry, exists := pm.entries[missing.PluginID]; exists {
				entry.stateMu.Lock()
				entry.State = StateError
				entry.lastError = err
				entry.lastErrorAt = time.Now()
				entry.stateMu.Unlock()
				log.WithField("plugin", missing.PluginID).
					WithField("missing", missing.Missing).
					Error("Plugin has missing dependency")
				pm.notifyEvent(PluginEventError, missing.PluginID, err, map[string]any{"phase": "dependency"})
			}
			// Don't fail discovery entirely — just mark the offending plugin as error
			// and re-resolve without it (it won't be in the order)
		}
	}

	order, err := dr.Resolve()
	if err != nil {
		return err
	}

	pm.activationOrder = order
	return nil
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
		e.stateMu.Lock()
		if e.State == StateActive {
			e.stateMu.Unlock()
			entries = append(entries, e)
		} else {
			e.stateMu.Unlock()
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
	TotalPlugins   int   `json:"totalPlugins"`
	ActivePlugins  int   `json:"activePlugins"`
	TotalTools     int   `json:"totalTools"`
	TotalHooks     int   `json:"totalHooks"`
	TotalEnrichers int   `json:"totalEnrichers"`
	ToolCallCount  int64 `json:"toolCallCount"` // runtime cumulative tool executions
	HookCallCount  int64 `json:"hookCallCount"` // runtime cumulative hook dispatches
}

// Metrics returns aggregate metrics about the plugin system.
func (pm *PluginManager) Metrics() PluginMetrics {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	m := PluginMetrics{
		TotalPlugins: len(pm.entries),
	}

	for _, entry := range pm.entries {
		entry.stateMu.Lock()
		isActive := entry.State == StateActive
		entry.stateMu.Unlock()
		if isActive {
			m.ActivePlugins++
			if entry.Context != nil {
				m.TotalTools += len(entry.Context.GetTools())
				m.TotalHooks += len(entry.Context.GetHooks())
				m.TotalEnrichers += len(entry.Context.GetEnrichers())
				m.ToolCallCount += entry.Context.ToolCallCount()
				m.HookCallCount += entry.Context.HookCallCount()
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
		e.stateMu.Lock()
		st := e.State
		e.stateMu.Unlock()
		switch st {
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

// SetRateLimiter sets the rate limiter for the plugin manager.
func (pm *PluginManager) SetRateLimiter(rl *PluginRateLimiter) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.rateLimiter = rl
}

// SetQuotaManager sets the quota manager for the plugin manager.
func (pm *PluginManager) SetQuotaManager(qm *PluginQuotaManager) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.quotaManager = qm
}

// RateLimiter returns the current rate limiter (may be nil).
func (pm *PluginManager) RateLimiter() *PluginRateLimiter {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.rateLimiter
}

// QuotaManager returns the current quota manager (may be nil).
func (pm *PluginManager) QuotaManager() *PluginQuotaManager {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.quotaManager
}

// SetRateLimit is a convenience method to set rate limit for a specific plugin.
func (pm *PluginManager) SetRateLimit(pluginID string, limit RateLimit) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.rateLimiter == nil {
		pm.rateLimiter = NewPluginRateLimiter(nil)
	}
	pm.rateLimiter.SetRateLimit(pluginID, limit)
}

// SetQuota is a convenience method to set quota for a specific plugin.
func (pm *PluginManager) SetQuota(pluginID string, quota PluginQuota) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pm.quotaManager == nil {
		pm.quotaManager = NewPluginQuotaManager(nil)
	}
	pm.quotaManager.SetQuota(pluginID, quota)
}

// ConfigStore returns the plugin configuration store.
func (pm *PluginManager) ConfigStore() *PluginConfigStore {
	return pm.configStore
}
