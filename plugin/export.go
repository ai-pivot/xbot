package plugin

import (
	"encoding/json"
	"fmt"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Config Export / Import — backup and restore plugin system state
// ---------------------------------------------------------------------------

// ConfigExportVersion is the current export format version.
const ConfigExportVersion = 1

// ConfigExport represents the top-level export format for plugin system state.
type ConfigExport struct {
	Version    int                 `json:"version"`
	ExportedAt string              `json:"exportedAt"`
	Disabled   []string            `json:"disabled"`
	Plugins    []PluginConfigEntry `json:"plugins"`
}

// PluginConfigEntry captures serializable state for a single plugin.
type PluginConfigEntry struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	State    PluginState     `json:"state"`
	Manifest *PluginManifest `json:"manifest,omitempty"`
	Config   map[string]any  `json:"config"`
}

// ExportConfig exports the current plugin system state as JSON.
// The export includes all discovered plugins with their manifest, state,
// and user configuration, plus the list of disabled plugin IDs.
func (pm *PluginManager) ExportConfig() ([]byte, error) {
	pm.mu.RLock()

	plugins := make([]PluginConfigEntry, 0, len(pm.entries))
	for _, entry := range pm.entries {
		config, err := pm.configStore.Load(entry.Manifest.ID)
		if err != nil {
			config = make(map[string]any)
		}

		plugins = append(plugins, PluginConfigEntry{
			ID:       entry.Manifest.ID,
			Name:     entry.Manifest.Name,
			Version:  entry.Manifest.Version,
			State:    entry.State,
			Manifest: entry.Manifest,
			Config:   config,
		})
	}

	disabled := make([]string, 0, len(pm.disabled))
	for id := range pm.disabled {
		if pm.disabled[id] {
			disabled = append(disabled, id)
		}
	}

	pm.mu.RUnlock()

	export := ConfigExport{
		Version:    ConfigExportVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Disabled:   disabled,
		Plugins:    plugins,
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("export config: marshal failed: %w", err)
	}
	return data, nil
}

// ImportConfig imports a previously exported plugin system state.
// It restores user configurations for plugins that exist in the current manager,
// and merges disabled plugin IDs into the current disabled set.
// Plugins in the export that don't exist locally are skipped with a warning log.
func (pm *PluginManager) ImportConfig(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("import config: empty data")
	}

	var export ConfigExport
	if err := json.Unmarshal(data, &export); err != nil {
		return fmt.Errorf("import config: invalid JSON: %w", err)
	}

	if export.Version > ConfigExportVersion {
		return fmt.Errorf("import config: unsupported version %d (max %d)", export.Version, ConfigExportVersion)
	}

	// Snapshot of existing plugin IDs
	pm.mu.RLock()
	existing := make(map[string]bool, len(pm.entries))
	for id := range pm.entries {
		existing[id] = true
	}
	pm.mu.RUnlock()

	// Restore config for existing plugins (best-effort)
	for _, pce := range export.Plugins {
		if !existing[pce.ID] {
			log.WithField("plugin", pce.ID).Warn("Import config: plugin not found locally, skipping")
			continue
		}
		if pce.Config != nil {
			if err := pm.configStore.Save(pce.ID, pce.Config); err != nil {
				log.WithField("plugin", pce.ID).Warn("Import config: failed to save config: ", err)
			}
		}
	}

	// Merge disabled list (union, not replace)
	if len(export.Disabled) > 0 {
		pm.mu.Lock()
		for _, id := range export.Disabled {
			pm.disabled[id] = true
		}
		pm.mu.Unlock()
	}

	log.WithField("plugins", len(export.Plugins)).
		WithField("disabled", len(export.Disabled)).
		Info("Plugin config imported")

	return nil
}
