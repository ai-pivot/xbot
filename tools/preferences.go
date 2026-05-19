package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// UserPreferences holds per-user UI preferences stored as a JSON file.
// New fields should be added here — the file uses a JSON merge strategy
// so unknown fields are preserved across read-modify-write cycles.
type UserPreferences struct {
	// Sidebar section collapse state. Key = section name ("sessions", "todo", "tasks"),
	// Value = true means collapsed. Sections not in the map are expanded by default.
	SidebarCollapsed map[string]bool `json:"sidebar_collapsed,omitempty"`
}

// preferencesMu guards concurrent file access per user path.
var preferencesMu sync.Mutex

// LoadPreferences reads per-user preferences from the JSON file.
// Returns a zero-value UserPreferences if the file does not exist or cannot be parsed.
func LoadPreferences(workDir, senderID string) UserPreferences {
	path := UserPreferencesPath(workDir, senderID)

	preferencesMu.Lock()
	defer preferencesMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return UserPreferences{}
	}

	var p UserPreferences
	if err := json.Unmarshal(data, &p); err != nil {
		return UserPreferences{}
	}
	return p
}

// SavePreferences writes per-user preferences to the JSON file.
// Uses deep JSON merge to preserve unknown fields from newer versions.
func SavePreferences(workDir, senderID string, p UserPreferences) error {
	path := UserPreferencesPath(workDir, senderID)

	preferencesMu.Lock()
	defer preferencesMu.Unlock()

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Deep merge: read existing file first to preserve unknown fields
	var existing map[string]any
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	var incoming map[string]any
	raw, _ := json.Marshal(p)
	_ = json.Unmarshal(raw, &incoming)

	merged := deepMerge(existing, incoming)

	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// deepMerge recursively merges src into dst. Keys in src override dst.
func deepMerge(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = make(map[string]any)
	}
	for k, sv := range src {
		if dv, ok := dst[k]; ok {
			if srcMap, ok := sv.(map[string]any); ok {
				if dstMap, ok := dv.(map[string]any); ok {
					dst[k] = deepMerge(dstMap, srcMap)
					continue
				}
			}
		}
		dst[k] = sv
	}
	return dst
}
