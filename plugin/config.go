package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
}

// NewPluginConfigStore creates a new PluginConfigStore rooted at xbotHome.
func NewPluginConfigStore(xbotHome string) *PluginConfigStore {
	return &PluginConfigStore{
		xbotHome: xbotHome,
		cache:    make(map[string]map[string]any),
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

	return nil
}

// Update atomically sets a single configuration key for a plugin.
// The entire load-modify-save operation is protected by a write lock
// to prevent concurrent updates from overwriting each other.
func (s *PluginConfigStore) Update(pluginID, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
		return fmt.Errorf("plugin config: failed to create directory: %w", err)
	}

	out, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("plugin config: failed to marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("plugin config: failed to write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("plugin config: failed to rename %s: %w", tmp, err)
	}

	// Update cache
	s.cache[pluginID] = config
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

// cloneMap creates a shallow copy of a map to prevent mutation of cached data.
func cloneMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
