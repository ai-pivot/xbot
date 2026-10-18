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

// Fetch returns the merged registry index from the LOCAL cache. A registry that
// has never been cloned is cloned once (initial population); existing clones are
// NOT pulled — registry data only changes on startup (async Refresh) or an
// explicit refresh (UI button / `xbot-cli registry update`). This keeps
// search/install fast and avoids periodic network pulls.
func Fetch(xbotHome string) (*Index, error) {
	return collect(xbotHome, false)
}

// Refresh clones any missing registries and forces a `git pull` for the existing
// ones, then returns the merged index. Used at startup (async) and by explicit
// refresh actions.
func Refresh(xbotHome string) (*Index, error) {
	return collect(xbotHome, true)
}

func collect(xbotHome string, refresh bool) (*Index, error) {
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
			// Not cloned yet — initial population (one-time). Even a read-only
			// Fetch must clone the first time so the index has something to read.
			cmd := exec.Command("git", "clone", "--depth", "1", "-b", branch, ref.URL, cloneDir)
			cmd.Stdout = nil
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				log.WithError(err).WithField("registry", ref.Name).Warn("Failed to clone registry, skipping")
				continue
			}
		} else if refresh {
			// Explicit/startup refresh only — never on ordinary reads.
			cmd := exec.Command("git", "pull", "--ff-only", "origin", branch)
			cmd.Dir = cloneDir
			cmd.Stdout = nil
			cmd.Stderr = nil
			if err := cmd.Run(); err != nil {
				log.WithError(err).WithField("registry", ref.Name).Warn("Failed to update registry, using cached version")
			}
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
