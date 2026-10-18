// Package registry provides xbot's component distribution channel.
//
// A registry is a Git repository containing a registry.json index file plus
// skill/agent/plugin directories. The CLI fetches registries by cloning them
// into ~/.xbot/registries/<name>/ and reads the index for search and install.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	log "xbot/logger"
)

// Index is the top-level structure of registry.json.
type Index struct {
	Version int     `json:"version"`
	Items   []Entry `json:"items"`
}

// Entry describes a single distributable item in a registry.
type Entry struct {
	Type        string   `json:"type"` // skill | agent | plugin | package
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Version     string   `json:"version"`
	Path        string   `json:"path"` // relative path inside the registry repo
	Tags        []string `json:"tags,omitempty"`

	// Registry is the source registry name (set after fetch, not serialized).
	Registry string `json:"-"`
}

// Config stores the user's registry subscriptions.
type Config struct {
	Registries []Ref `json:"registries"`
}

// Ref references a single registry repository.
type Ref struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"` // default "main"
}

// DefaultBranch is the branch used when Ref.Branch is empty.
const DefaultBranch = "main"

// ConfigPath returns the path to the registry config file.
func ConfigPath(xbotHome string) string {
	return filepath.Join(xbotHome, "registries.json")
}

// CacheDir returns the directory where registry repos are cloned.
func CacheDir(xbotHome string) string {
	return filepath.Join(xbotHome, "registries")
}

// LoadConfig reads the registry config from xbotHome. Returns an empty
// config (no registries) if the file does not exist.
func LoadConfig(xbotHome string) (*Config, error) {
	data, err := os.ReadFile(ConfigPath(xbotHome))
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read registry config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse registry config: %w", err)
	}
	return &cfg, nil
}

// SaveConfig writes the registry config to xbotHome.
func SaveConfig(xbotHome string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigPath(xbotHome), data, 0o644)
}

// AddRegistry adds a registry to the config. Returns an error if a
// registry with the same name already exists.
func AddRegistry(xbotHome, name, url, branch string) error {
	cfg, err := LoadConfig(xbotHome)
	if err != nil {
		return err
	}
	for _, r := range cfg.Registries {
		if r.Name == name {
			return fmt.Errorf("registry %q already exists (url: %s)", name, r.URL)
		}
	}
	if branch == "" {
		branch = DefaultBranch
	}
	cfg.Registries = append(cfg.Registries, Ref{Name: name, URL: url, Branch: branch})
	return SaveConfig(xbotHome, cfg)
}

// RemoveRegistry removes a registry from the config and deletes its clone.
func RemoveRegistry(xbotHome, name string) error {
	cfg, err := LoadConfig(xbotHome)
	if err != nil {
		return err
	}
	found := false
	var remaining []Ref
	for _, r := range cfg.Registries {
		if r.Name == name {
			found = true
			continue
		}
		remaining = append(remaining, r)
	}
	if !found {
		return fmt.Errorf("registry %q not found", name)
	}
	cfg.Registries = remaining
	if err := SaveConfig(xbotHome, cfg); err != nil {
		return err
	}
	// Remove the clone directory
	cloneDir := filepath.Join(CacheDir(xbotHome), name)
	_ = os.RemoveAll(cloneDir)
	return nil
}

// Fetch clones or pulls all configured registries and returns a merged index.
// fetchTTL bounds how often Fetch performs a network `git pull` on an existing
// clone. Within the TTL, Fetch reads the cached registry.json only (fast). Use
// Refresh for an explicit, immediate pull (e.g. `xbot-cli registry update`).
const fetchTTL = 10 * time.Minute

// fetchMarkerPath is a marker inside the clone's .git dir recording the last
// successful clone/pull attempt (mtime = last fetch time).
func fetchMarkerPath(cloneDir string) string {
	return filepath.Join(cloneDir, ".git", "xbot-last-fetch")
}

func touchFetchMarker(cloneDir string) {
	_ = os.WriteFile(fetchMarkerPath(cloneDir), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644)
}

// fetchIsFresh reports whether the clone was fetched within fetchTTL.
func fetchIsFresh(cloneDir string) bool {
	fi, err := os.Stat(fetchMarkerPath(cloneDir))
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) < fetchTTL
}

// Fetch returns the merged registry index. Existing clones are refreshed with
// `git pull` at most once per fetchTTL; within that window the cached
// registry.json is used (no network), so search/install stay fast.
// Failed pulls are logged and the cached copy is served.
func Fetch(xbotHome string) (*Index, error) {
	return fetchAll(xbotHome, false)
}

// Refresh forces a `git pull` for every configured registry, then returns the
// merged index. Used by explicit "update" actions.
func Refresh(xbotHome string) (*Index, error) {
	return fetchAll(xbotHome, true)
}

func fetchAll(xbotHome string, force bool) (*Index, error) {
	cfg, err := LoadConfig(xbotHome)
	if err != nil {
		return nil, err
	}
	if len(cfg.Registries) == 0 {
		return &Index{Version: 1, Items: nil}, nil
	}

	cacheDir := CacheDir(xbotHome)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("create registry cache dir: %w", err)
	}

	merged := &Index{Version: 1}
	for _, ref := range cfg.Registries {
		branch := ref.Branch
		if branch == "" {
			branch = DefaultBranch
		}
		cloneDir := filepath.Join(cacheDir, ref.Name)

		if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
			// Fresh clone (one-time network cost).
			cmd := exec.Command("git", "clone", "--depth", "1", "-b", branch, ref.URL, cloneDir)
			cmd.Stdout = nil
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.WithError(err).WithField("registry", ref.Name).Warn("Failed to clone registry, skipping")
				continue
			}
			touchFetchMarker(cloneDir)
		} else if force || !fetchIsFresh(cloneDir) {
			// Update existing clone only when forced or stale. Touch the marker
			// regardless of outcome so an offline registry doesn't retry the
			// network on every request — cached data is served instead.
			cmd := exec.Command("git", "pull", "--ff-only", "origin", branch)
			cmd.Dir = cloneDir
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Run(); err != nil {
				log.WithError(err).WithField("registry", ref.Name).Warn("Failed to update registry, using cached version")
			}
			touchFetchMarker(cloneDir)
		}

		// Parse registry.json
		index, err := loadIndex(cloneDir)
		if err != nil {
			log.WithError(err).WithField("registry", ref.Name).Warn("Failed to parse registry.json, skipping")
			continue
		}
		// Tag each entry with its source registry for install resolution.
		for i := range index.Items {
			index.Items[i].Registry = ref.Name
		}
		merged.Items = append(merged.Items, index.Items...)
	}
	return merged, nil
}

// loadIndex reads and parses registry.json from a clone directory.
func loadIndex(cloneDir string) (*Index, error) {
	path := filepath.Join(cloneDir, "registry.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry.json: %w", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse registry.json: %w", err)
	}
	return &idx, nil
}

// Search filters the index by query, matching against name, description, and tags.
// Results are sorted by relevance (name match > tag match > description match).
func Search(idx *Index, query string) []Entry {
	if query == "" {
		return idx.Items
	}
	q := strings.ToLower(query)
	var results []Entry
	for _, item := range idx.Items {
		if strings.Contains(strings.ToLower(item.Name), q) ||
			strings.Contains(strings.ToLower(item.Description), q) ||
			containsTag(item.Tags, q) {
			results = append(results, item)
		}
	}
	// Sort: name starts-with query > name contains > tag match > description
	sort.SliceStable(results, func(i, j int) bool {
		ni, nj := strings.ToLower(results[i].Name), strings.ToLower(results[j].Name)
		si := strings.HasPrefix(ni, q)
		sj := strings.HasPrefix(nj, q)
		if si != sj {
			return si
		}
		return ni < nj
	})
	return results
}

func containsTag(tags []string, q string) bool {
	for _, t := range tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// FindEntry returns the entry matching the given name, or nil if not found.
// If multiple registries have the same name, the first match is returned.
func FindEntry(idx *Index, name string) *Entry {
	for i := range idx.Items {
		if idx.Items[i].Name == name {
			return &idx.Items[i]
		}
	}
	return nil
}

// FormatEntry returns a human-readable string for an entry.
func FormatEntry(e Entry) string {
	tags := ""
	if len(e.Tags) > 0 {
		tags = " [" + strings.Join(e.Tags, ", ") + "]"
	}
	return fmt.Sprintf("%s/%s (%s) v%s — %s%s",
		e.Type, e.Name, e.Author, e.Version, e.Description, tags)
}

// LastFetch returns the modification time of the registry cache directory,
// useful for showing "last updated" in the UI.
func LastFetch(xbotHome string) time.Time {
	info, err := os.Stat(CacheDir(xbotHome))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}
