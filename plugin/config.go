package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// PluginConfigStore — per-plugin user configuration storage
// ---------------------------------------------------------------------------

// PluginConfigStore manages user-level configuration for plugins.
// Config files are stored at ~/.xbot/plugins/<id>/config.json (user-level,
// independent of plugin installation directory).
type PluginConfigStore struct {
	mu       sync.RWMutex
	xbotHome string
	cache    map[string]map[string]any // pluginID → config (in-memory cache)
	// changeSubscribers maps pluginID → set of callbacks notified on config
	// change. Subscribers are identified by a monotonically increasing id.
	changeSubscribers map[string]map[int64]func(map[string]any)
	nextSubID         int64
}

// NewPluginConfigStore creates a new PluginConfigStore rooted at xbotHome.
func NewPluginConfigStore(xbotHome string) *PluginConfigStore {
	return &PluginConfigStore{
		xbotHome:          xbotHome,
		cache:             make(map[string]map[string]any),
		changeSubscribers: make(map[string]map[int64]func(map[string]any)),
	}
}

// Subscribe registers a callback invoked whenever the given plugin's config
// changes (via Save or Update). The callback receives the full merged config
// (defaults overlaid with user values). It returns an unsubscribe function.
//
// Subscribe is safe for concurrent use and does not block the writer.
func (s *PluginConfigStore) Subscribe(pluginID string, cb func(map[string]any)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.changeSubscribers[pluginID] == nil {
		s.changeSubscribers[pluginID] = make(map[int64]func(map[string]any))
	}
	s.nextSubID++
	id := s.nextSubID
	s.changeSubscribers[pluginID][id] = cb
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if subs, ok := s.changeSubscribers[pluginID]; ok {
			delete(subs, id)
			if len(subs) == 0 {
				delete(s.changeSubscribers, pluginID)
			}
		}
	}
}

// notifyChange synchronously invokes all subscribers for the given plugin with
// a copy of the merged config. Panics are recovered so a misbehaving callback
// cannot crash the writer.
func (s *PluginConfigStore) notifyChange(pluginID string, config map[string]any) {
	s.mu.RLock()
	subs := s.changeSubscribers[pluginID]
	callbacks := make([]func(map[string]any), 0, len(subs))
	for _, cb := range subs {
		callbacks = append(callbacks, cb)
	}
	s.mu.RUnlock()
	for _, cb := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.WithField("plugin", pluginID).Warn("plugin config subscriber panicked: ", r)
				}
			}()
			cb(cloneMap(config))
		}()
	}
}

// configPath returns the path to the config file for a given plugin.
func (s *PluginConfigStore) configPath(pluginID string) string {
	return filepath.Join(s.xbotHome, "plugins", pluginID, "config.json")
}

// Load loads the user configuration for the given plugin.
// Returns an empty map if no config file exists.
func (s *PluginConfigStore) Load(pluginID string) (map[string]any, error) {
	s.mu.RLock()
	if cached, ok := s.cache[pluginID]; ok {
		s.mu.RUnlock()
		return cloneMap(cached), nil
	}
	s.mu.RUnlock()

	path := s.configPath(pluginID)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]any), nil
		}
		return nil, fmt.Errorf("plugin config: failed to read %s: %w", path, err)
	}

	config := make(map[string]any)
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("plugin config: failed to parse %s: %w", path, err)
	}

	s.mu.Lock()
	s.cache[pluginID] = config
	s.mu.Unlock()

	return cloneMap(config), nil
}

// Save persists the user configuration for the given plugin.
// Creates parent directories if they don't exist.
func (s *PluginConfigStore) Save(pluginID string, config map[string]any) error {
	path := s.configPath(pluginID)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("plugin config: failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("plugin config: failed to marshal: %w", err)
	}

	// Atomic write via temp file + rename
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("plugin config: failed to write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("plugin config: failed to rename %s: %w", tmp, err)
	}

	// Update cache
	s.mu.Lock()
	s.cache[pluginID] = cloneMap(config)
	s.mu.Unlock()

	s.notifyChange(pluginID, config)
	return nil
}

// Update atomically sets a single configuration key for a plugin.
// The entire load-modify-save operation is protected by a write lock
// to prevent concurrent updates from overwriting each other.
// Subscribers are notified AFTER the write lock is released — notifyChange
// takes an RLock, and Go's sync.RWMutex deadlocks if a writer re-enters RLock.
func (s *PluginConfigStore) Update(pluginID, key string, value any) error {
	s.mu.Lock()

	// Load current config from cache or disk
	config := make(map[string]any)
	path := s.configPath(pluginID)
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &config)
	} else if cached, ok := s.cache[pluginID]; ok {
		for k, v := range cached {
			config[k] = v
		}
	}

	// Update the key
	config[key] = value

	// Persist
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("plugin config: failed to create directory: %w", err)
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("plugin config: failed to marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("plugin config: failed to write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("plugin config: failed to rename %s: %w", tmp, err)
	}

	// Update cache
	s.cache[pluginID] = config
	s.mu.Unlock()

	// Notify subscribers outside the write lock.
	s.notifyChange(pluginID, config)
	return nil
}

// InvalidateCache removes the cached config for a plugin, forcing a reload
// from disk on the next Load() call.
func (s *PluginConfigStore) InvalidateCache(pluginID string) {
	s.mu.Lock()
	delete(s.cache, pluginID)
	s.mu.Unlock()
}

// GetDefaultConfig extracts default values from a manifest's configuration schema.
// Returns an empty map if no configuration is declared.
func GetDefaultConfig(manifest *PluginManifest) map[string]any {
	if manifest == nil || manifest.Contributes == nil || manifest.Contributes.Configuration == nil {
		return make(map[string]any)
	}
	defaults := make(map[string]any)
	for key, prop := range manifest.Contributes.Configuration.Properties {
		if prop.Default != nil {
			defaults[key] = prop.Default
		}
	}
	return defaults
}

// ConfigSchema returns the unified configuration schema for a plugin.
//
// It prefers the Go manifest's contributes.configuration (the authoritative,
// VSCode-style declaration). When absent — a Web-only frontend plugin that
// declares its settings as 'setting' contributions in web.contributes — it
// converts those into an equivalent ConfigurationContribution so the Web UI
// can render a single, uniform settings form for all plugin types.
func ConfigSchema(m *PluginManifest) *ConfigurationContribution {
	if m == nil {
		return nil
	}
	if m.Contributes != nil && m.Contributes.Configuration != nil {
		return m.Contributes.Configuration
	}
	return configSchemaFromWebContribs(m)
}

// configSchemaFromWebContribs converts frontend 'setting' contributions
// (web.contributes array) into a ConfigurationContribution. Returns nil when
// the web.contributes blob is not a JSON array or contains no 'setting' entries.
func configSchemaFromWebContribs(m *PluginManifest) *ConfigurationContribution {
	if m.Web == nil || len(m.Web.Contributes) == 0 {
		return nil
	}
	var raw []struct {
		Kind        string         `json:"kind"`
		Key         string         `json:"key"`
		Type        string         `json:"type"`
		Label       string         `json:"label"`
		Description string         `json:"description"`
		Default     any            `json:"default"`
		Options     []ConfigOption `json:"options"`
		Section     string         `json:"section"`
	}
	if err := json.Unmarshal(m.Web.Contributes, &raw); err != nil {
		log.WithField("plugin", m.ID).Warn("ConfigSchema: web.contributes is not a JSON array: ", err)
		return nil
	}
	props := make(map[string]ConfigProperty)
	for _, c := range raw {
		if c.Kind != "setting" || c.Key == "" {
			continue
		}
		ptype := c.Type
		switch ptype {
		case "boolean", "string", "number", "select", "multiselect":
		default:
			ptype = "string"
		}
		props[c.Key] = ConfigProperty{
			Type:        ptype,
			Label:       c.Label,
			Description: c.Description,
			Default:     c.Default,
			Options:     c.Options,
			Section:     c.Section,
		}
	}
	if len(props) == 0 {
		return nil
	}
	return &ConfigurationContribution{
		Title:      "Plugin Settings",
		Properties: props,
	}
}

// cloneMap creates a shallow copy of a map to prevent mutation of cached data.
func cloneMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
