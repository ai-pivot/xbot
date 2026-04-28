package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Plugin Registry — plugin marketplace and discovery
// ---------------------------------------------------------------------------

// RegistrySourceType constants define the supported source types for plugin registries.
const (
	RegistrySourceGitHub = "github"
	RegistrySourceURL    = "url"
	RegistrySourceLocal  = "local"
)

// RegistrySource defines a source for plugin discovery (e.g., GitHub, URL, local directory).
type RegistrySource struct {
	Type string // one of RegistrySourceGitHub, RegistrySourceURL, RegistrySourceLocal
	URL  string
}

// RegistryEntry describes a plugin available in a registry.
type RegistryEntry struct {
	ID          string
	Name        string
	Version     string
	Description string
	Author      string
	Source      RegistrySource
	DownloadURL string
	Checksum    string // SHA256 of the plugin archive
}

// PluginRegistry manages available plugins from multiple sources.
// It wraps a PluginManager and adds source-based discovery, search,
// and marketplace capabilities.
//
// In the current MVP, only local sources are supported for installation,
// and search operates on locally installed plugins only.
type PluginRegistry struct {
	mu      sync.RWMutex
	mgr     *PluginManager
	sources []RegistrySource
	entries map[string]RegistryEntry // cached registry entries (keyed by plugin ID)
}

// NewPluginRegistry creates a new PluginRegistry backed by the given PluginManager.
// Sources define where plugins can be fetched from; only "local" sources are
// currently supported for installation.
func NewPluginRegistry(mgr *PluginManager, sources ...RegistrySource) *PluginRegistry {
	return &PluginRegistry{
		mgr:     mgr,
		sources: sources,
		entries: make(map[string]RegistryEntry),
	}
}

// Search returns registry entries matching the query string.
// The query is matched case-insensitively against ID, Name, and Description fields.
// Currently only searches locally installed plugins via PluginManager.
func (r *PluginRegistry) Search(ctx context.Context, query string) ([]RegistryEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("registry search: %w", err)
	}

	lower := strings.ToLower(query)
	plugins := r.mgr.ListPlugins()

	var results []RegistryEntry
	for _, p := range plugins {
		m := p.Manifest
		if m == nil {
			continue
		}
		if matchPluginEntry(lower, m.ID, m.Name, m.Description) {
			entry := manifestToRegistryEntry(*m)
			results = append(results, entry)
		}
	}

	return results, nil
}

// Install installs a plugin from a registry source.
// Currently only "local" sources are supported, where URL is the path to
// the plugin directory on disk.
//
// MVP behavior: the first local source URL is used as the installation source
// directory. Future versions will support per-plugin source resolution.
func (r *PluginRegistry) Install(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("registry install %s: %w", id, err)
	}

	// Find a local source to install from.
	// MVP: use the first local source URL.
	var sourceDir string
	for _, src := range r.sources {
		if src.Type == RegistrySourceLocal && src.URL != "" {
			sourceDir = src.URL
			break
		}
	}
	if sourceDir == "" {
		return fmt.Errorf("registry install %s: no local source available", id)
	}

	if _, err := r.mgr.InstallPlugin(ctx, sourceDir); err != nil {
		return fmt.Errorf("registry install %s: %w", id, err)
	}

	// Cache the installed entry.
	if entry, ok := r.mgr.GetPlugin(id); ok && entry.Manifest != nil {
		r.mu.Lock()
		r.entries[id] = manifestToRegistryEntry(*entry.Manifest)
		r.mu.Unlock()
	}

	return nil
}

// Update reloads a plugin by ID, effectively re-reading its manifest and
// re-creating its runtime instance. In the current implementation, Update
// is equivalent to PluginManager.Reload.
func (r *PluginRegistry) Update(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("registry update %s: %w", id, err)
	}

	if err := r.mgr.Reload(ctx, id); err != nil {
		return fmt.Errorf("registry update %s: %w", id, err)
	}

	// Refresh cached entry after reload.
	if entry, ok := r.mgr.GetPlugin(id); ok && entry.Manifest != nil {
		r.mu.Lock()
		r.entries[id] = manifestToRegistryEntry(*entry.Manifest)
		r.mu.Unlock()
	}

	return nil
}

// List returns all cached registry entries.
func (r *PluginRegistry) List() []RegistryEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]RegistryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		result = append(result, e)
	}
	return result
}

// matchPluginEntry checks if the lowercased query matches any of the given fields
// using case-insensitive substring matching. An empty query matches everything.
func matchPluginEntry(query, id, name, description string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(id), query) ||
		strings.Contains(strings.ToLower(name), query) ||
		strings.Contains(strings.ToLower(description), query)
}

// manifestToRegistryEntry converts a PluginManifest to a RegistryEntry.
func manifestToRegistryEntry(m PluginManifest) RegistryEntry {
	return RegistryEntry{
		ID:          m.ID,
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
		Author:      m.Author,
	}
}
