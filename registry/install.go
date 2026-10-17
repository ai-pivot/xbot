package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

	// Binary-runtime plugins (stdio/grpc) require a compiled entry binary.
	// A registry checkout carries source only — its go.mod replaces xbot's
	// plugin/protocol via a repo-relative path that does not exist in the
	// registry clone, so it cannot be built here. Refuse rather than install a
	// plugin that will fail at activation ("no such file or directory").
	if manifest.Runtime == "stdio" || manifest.Runtime == "grpc" {
		entryBin := manifest.Executable
		if entryBin == "" {
			entryBin = manifest.Entry
		}
		if strings.HasPrefix(entryBin, "./") || strings.HasPrefix(entryBin, "../") {
			if _, err := os.Stat(filepath.Join(srcPath, entryBin)); err != nil {
				return fmt.Errorf("plugin %q needs a prebuilt binary at %q, but the registry only ships source; build it from the xbot repo (make plugins-install) instead", id, entryBin)
			}
		}
	}

	destDir := filepath.Join(xbotHome, "plugins", id)
	if _, err := os.Stat(destDir); err == nil {
		return fmt.Errorf("plugin %q already installed (remove it first or use --force)", id)
	}
	pluginsDir := filepath.Join(xbotHome, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return fmt.Errorf("create plugins dir: %w", err)
	}
	if err := copyDir(srcPath, destDir); err != nil {
		return fmt.Errorf("copy plugin: %w", err)
	}
	log.WithFields(log.Fields{"plugin": id, "from": srcPath, "to": destDir}).Info("Installed plugin from registry")
	return nil
}

// installPackage reads a package.json manifest from the registry clone and
// installs each content item (skill/agent/plugin) individually.
func installPackage(cloneDir string, entry *Entry, xbotHome, workDir, senderID string) error {
	manifestPath := filepath.Join(cloneDir, entry.Path, "package.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read package.json: %w", err)
	}

	// Use the agent package's PackageManifest type via a local struct
	// to avoid a circular dependency (agent → tools, registry → tools).
	type pkgContent struct {
		Type   string `json:"type"`
		Name   string `json:"name"`
		Source string `json:"source"`
	}
	type pkgManifest struct {
		Schema   int          `json:"schema"`
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Version  string       `json:"version"`
		Contents []pkgContent `json:"contents"`
	}

	var manifest pkgManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse package.json: %w", err)
	}

	pkgDir := filepath.Join(cloneDir, entry.Path)
	for _, c := range manifest.Contents {
		srcPath := filepath.Join(pkgDir, c.Source)
		switch c.Type {
		case "skill":
			if err := installSkill(srcPath, c.Name, workDir, senderID); err != nil {
				return fmt.Errorf("package %q: %w", entry.Name, err)
			}
		case "agent":
			if err := installAgent(srcPath, c.Name, workDir, senderID); err != nil {
				return fmt.Errorf("package %q: %w", entry.Name, err)
			}
		case "plugin":
			if err := installPlugin(srcPath, c.Name, xbotHome); err != nil {
				return fmt.Errorf("package %q: %w", entry.Name, err)
			}
		default:
			return fmt.Errorf("package %q: unsupported content type %q", entry.Name, c.Type)
		}
	}
	log.WithFields(log.Fields{"package": entry.Name, "items": len(manifest.Contents)}).Info("Installed package from registry")
	return nil
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
