package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	log "xbot/logger"
	"xbot/tools"
)

// Install copies an item from the registry clone to the user's local directory.
// xbotHome is the global ~/.xbot directory; workDir is the project/workspace
// root; senderID is the user identity (e.g. "cli_user" for CLI).
func Install(xbotHome, workDir, senderID string, entry *Entry) error {
	cloneDir := filepath.Join(CacheDir(xbotHome), entry.Registry)
	srcPath := filepath.Join(cloneDir, entry.Path)

	if _, err := os.Stat(srcPath); err != nil {
		return fmt.Errorf("source path %q not found in registry %q: %w", entry.Path, entry.Registry, err)
	}

	switch entry.Type {
	case "skill":
		return installSkill(srcPath, entry.Name, workDir, senderID)
	case "agent":
		return installAgent(srcPath, entry.Name, workDir, senderID)
	case "plugin":
		return installPlugin(srcPath, entry.Name, xbotHome)
	case "package":
		return installPackage(cloneDir, entry, xbotHome, workDir, senderID)
	default:
		return fmt.Errorf("unsupported type %q (expected skill, agent, plugin, or package)", entry.Type)
	}
}

func installSkill(srcPath, name, workDir, senderID string) error {
	destDir := filepath.Join(tools.UserSkillsRoot(workDir, senderID), name)
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("skill %q already installed (remove it first or use --force)", name)
	}
	if err := copyDir(srcPath, destDir); err != nil {
		return fmt.Errorf("copy skill: %w", err)
	}
	log.WithFields(log.Fields{"skill": name, "from": srcPath, "to": destDir}).Info("Installed skill from registry")
	return nil
}

func installAgent(srcPath, name, workDir, senderID string) error {
	agentsDir := tools.UserAgentsRoot(workDir, senderID)
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("create agents dir: %w", err)
	}
	destFile := filepath.Join(agentsDir, name+".md")
	if _, err := os.Stat(destFile); err == nil {
		return fmt.Errorf("agent %q already installed (remove it first or use --force)", name)
	}
	if err := copyFile(srcPath, destFile); err != nil {
		return fmt.Errorf("copy agent: %w", err)
	}
	log.WithFields(log.Fields{"agent": name, "from": srcPath, "to": destFile}).Info("Installed agent from registry")
	return nil
}

func installPlugin(srcPath, name, xbotHome string) error {
	// Read plugin.json to get the canonical plugin ID and runtime. The repo
	// directory name (e.g. "xbot-ssh-runner") is not the plugin ID
	// (e.g. "xbot.ssh-runner"); plugins must be installed under their ID dir
	// so the runtime's data/logs dirs and the plugin loader agree.
	manifestPath := filepath.Join(srcPath, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read plugin.json: %w", err)
	}
	var manifest struct {
		ID         string `json:"id"`
		Runtime    string `json:"runtime"`
		Entry      string `json:"entry"`
		Executable string `json:"executable"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse plugin.json: %w", err)
	}
	id := manifest.ID
	if id == "" {
		id = name
	}

	destDir := filepath.Join(xbotHome, "plugins", id)
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("plugin %q already installed (remove it first or use --force)", id)
	}
	pluginsDir := filepath.Join(xbotHome, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return fmt.Errorf("create plugins dir: %w", err)
	}

	// Binary-runtime plugins (stdio/grpc) need a compiled entry binary. The
	// registry ships per-platform binaries under bin/<os>-<arch>/; pick the one
	// matching this host and place it at the manifest's entry path. Source
	// files are copied too (useful for auditing; they do not affect execution).
	if manifest.Runtime == "stdio" || manifest.Runtime == "grpc" {
		entryBin := manifest.Executable
		if entryBin == "" {
			entryBin = manifest.Entry
		}
		// Only path-like entries (./x, ../x) point at a bundled binary; a bare
		// command name is resolved via PATH and needs no bundled file.
		if strings.HasPrefix(entryBin, "./") || strings.HasPrefix(entryBin, "../") {
			rel := filepath.Clean(strings.TrimPrefix(entryBin, "./"))
			binarySrc, err := selectPlatformBinary(srcPath, rel)
			if err != nil {
				return fmt.Errorf("plugin %q: %w", id, err)
			}
			if err := copyPluginSource(srcPath, destDir); err != nil {
				return fmt.Errorf("copy plugin: %w", err)
			}
			if err := copyFileMode(binarySrc, filepath.Join(destDir, rel), 0o755); err != nil {
				return fmt.Errorf("install binary: %w", err)
			}
			log.WithFields(log.Fields{"plugin": id, "from": srcPath, "binary": binarySrc, "to": destDir}).Info("Installed plugin from registry")
			return nil
		}
	}

	// Script-runtime (or PATH-resolved) plugin: copy the source directory as-is.
	if err := copyDir(srcPath, destDir); err != nil {
		return fmt.Errorf("copy plugin: %w", err)
	}
	log.WithFields(log.Fields{"plugin": id, "from": srcPath, "to": destDir}).Info("Installed plugin from registry")
	return nil
}

// selectPlatformBinary finds the per-platform binary for entryRel under
// srcPath/bin/<goos>-<goarch>/, or a prebuilt binary already at srcPath/entryRel,
// or a single-platform srcPath/bin/<base>. Returns an error naming the
// available platforms when nothing matches this host.
func selectPlatformBinary(srcPath, entryRel string) (string, error) {
	base := filepath.Base(entryRel)
	exe := base
	if runtime.GOOS == "windows" && !strings.HasSuffix(exe, ".exe") {
		exe += ".exe"
	}

	// 1. Prebuilt binary already at the entry path (single-platform layout).
	if p := filepath.Join(srcPath, entryRel); isRegularFile(p) {
		return p, nil
	}
	// 2. Per-platform: bin/<goos>-<goarch>/<exe>.
	if p := filepath.Join(srcPath, "bin", runtime.GOOS+"-"+runtime.GOARCH, exe); isRegularFile(p) {
		return p, nil
	}
	// 3. Single-platform under bin/.
	if p := filepath.Join(srcPath, "bin", exe); isRegularFile(p) {
		return p, nil
	}

	// Nothing matched — report which platforms the registry does provide.
	available := "none"
	if entries, err := os.ReadDir(filepath.Join(srcPath, "bin")); err == nil {
		var plats []string
		for _, e := range entries {
			if e.IsDir() {
				plats = append(plats, e.Name())
			}
		}
		if len(plats) > 0 {
			available = strings.Join(plats, ", ")
		}
	}
	return "", fmt.Errorf("no prebuilt binary for %s-%s (entry %q); registry provides: %s", runtime.GOOS, runtime.GOARCH, entryRel, available)
}

func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// copyPluginSource copies a plugin directory to dst, skipping the top-level
// bin/ build-output subtree (platform binaries for other arches are large and
// unnecessary once the matching binary is placed explicitly).
func copyPluginSource(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return os.MkdirAll(dst, 0o755)
		}
		// Skip the whole top-level bin/ subtree.
		if relPath == "bin" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(dst, relPath)
		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, targetPath)
		}
		if d.IsDir() {
			return os.MkdirAll(targetPath, fi.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, fi.Mode())
	})
}

// copyFileMode copies a single file and applies an explicit mode (e.g. 0755
// for plugin binaries).
func copyFileMode(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, mode); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// installPackage installs a package's items: a flat subscription list of base
// components (skill/agent/plugin) resolved from this registry's catalog.
// Already-installed components are skipped, so several packages can reference
// the same shared component (e.g. the github skill) without duplicating it.
func installPackage(cloneDir string, entry *Entry, xbotHome, workDir, senderID string) error {
	manifest, err := loadPackageManifest(filepath.Join(cloneDir, entry.Path))
	if err != nil {
		return err
	}
	catalog, err := loadIndex(cloneDir)
	if err != nil {
		return fmt.Errorf("package %q: registry.json unreadable: %w", entry.Name, err)
	}
	for _, it := range manifest.Items {
		item := findCatalogItem(catalog, it.Type, it.Name)
		if item == nil {
			return fmt.Errorf("package %q: item %s %q is not a catalog component in this registry", entry.Name, it.Type, it.Name)
		}
		if item.Type == "package" {
			return fmt.Errorf("package %q: item %q is a package; packages may only list base components (skill/agent/plugin)", entry.Name, it.Name)
		}
		if err := installResolved(item.Type, item.Name, filepath.Join(cloneDir, item.Path), xbotHome, workDir, senderID, true); err != nil {
			return fmt.Errorf("package %q: %w", entry.Name, err)
		}
	}
	log.WithFields(log.Fields{"package": entry.Name, "items": len(manifest.Items)}).Info("Installed package from registry")
	return nil
}

// installResolved installs one skill/agent/plugin from an absolute source path.
// When skipIfPresent is true (package content/dependency install), an
// already-installed component is skipped instead of failing — several packages
// may reference the same shared component.
func installResolved(itemType, name, srcPath, xbotHome, workDir, senderID string, skipIfPresent bool) error {
	if skipIfPresent && isItemInstalled(itemType, name, xbotHome, workDir, senderID) {
		log.WithFields(log.Fields{"type": itemType, "name": name}).Debug("Component already installed, skipping")
		return nil
	}
	switch itemType {
	case "skill":
		return installSkill(srcPath, name, workDir, senderID)
	case "agent":
		return installAgent(srcPath, name, workDir, senderID)
	case "plugin":
		return installPlugin(srcPath, name, xbotHome)
	default:
		return fmt.Errorf("unsupported content type %q (allowed: skill, agent, plugin)", itemType)
	}
}

// isItemInstalled reports whether a skill/agent/plugin is already installed for
// this user. Best-effort (plugins are keyed by manifest id / dir name).
func isItemInstalled(itemType, name, xbotHome, workDir, senderID string) bool {
	switch itemType {
	case "skill":
		_, err := os.Stat(filepath.Join(tools.UserSkillsRoot(workDir, senderID), name))
		return err == nil
	case "agent":
		_, err := os.Stat(filepath.Join(tools.UserAgentsRoot(workDir, senderID), name+".md"))
		return err == nil
	case "plugin":
		info, err := os.Stat(filepath.Join(xbotHome, "plugins", name))
		return err == nil && info.IsDir()
	}
	return false
}

// findCatalogItem finds a catalog entry by name (+ optional type).
func findCatalogItem(idx *Index, itemType, name string) *Entry {
	if idx == nil {
		return nil
	}
	for i := range idx.Items {
		it := &idx.Items[i]
		if it.Name == name && (itemType == "" || it.Type == itemType) {
			return it
		}
	}
	return nil
}

// Local package.json structs (kept here to avoid importing agent, which would
// introduce a cycle via tools).
type pkgItem struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type pkgManifest struct {
	Schema  int       `json:"schema"`
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Version string    `json:"version"`
	Items   []pkgItem `json:"items"`
}

func loadPackageManifest(pkgDir string) (*pkgManifest, error) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("read package.json: %w", err)
	}
	var m pkgManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse package.json: %w", err)
	}
	return &m, nil
}

// copyDir copies a directory tree from src to dst, preserving file modes.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)

		fi, err := os.Lstat(path)
		if err != nil {
			return err
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, targetPath)
		}

		if d.IsDir() {
			return os.MkdirAll(targetPath, fi.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, fi.Mode())
	})
}

// copyFile copies a single file from src to dst, creating parent dirs.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
