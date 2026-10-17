package agent

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/plugin"
	"xbot/tools"
)

// RegistryManager manages skill installation and discovery.
type RegistryManager struct {
	store      *SkillStore
	agentStore *AgentStore
	workDir    string
	xbotHome   string // global xbot config dir (e.g. ~/.xbot), used for plugins dir
	sandbox    tools.Sandbox
}

// NewRegistryManager creates a new RegistryManager.
func NewRegistryManager(store *SkillStore, agentStore *AgentStore, workDir, xbotHome string, sandbox tools.Sandbox) *RegistryManager {
	return &RegistryManager{
		store:      store,
		agentStore: agentStore,
		workDir:    workDir,
		xbotHome:   xbotHome,
		sandbox:    sandbox,
	}
}

// pluginsDir returns the global plugins directory (~/.xbot/plugins/).
func (rm *RegistryManager) pluginsDir() string {
	return filepath.Join(rm.xbotHome, "plugins")
}

// useSandbox 判断是否应使用 Sandbox 访问用户文件。
func (rm *RegistryManager) useSandbox() bool {
	return rm.sandbox != nil && rm.sandbox.Name() != "none"
}

// sandboxCtx returns a context with a 30-second timeout for sandbox I/O operations.
func (rm *RegistryManager) sandboxCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func (rm *RegistryManager) userSkillsDir(senderID string) string {
	if rm.useSandbox() {
		return filepath.Join(rm.sandbox.Workspace(senderID), "skills")
	}
	return tools.UserSkillsRoot(rm.workDir, senderID)
}

func (rm *RegistryManager) userAgentsDir(senderID string) string {
	if rm.useSandbox() {
		return filepath.Join(rm.sandbox.Workspace(senderID), "agents")
	}
	return tools.UserAgentsRoot(rm.workDir, senderID)
}

// --- Find ---

func (rm *RegistryManager) findSkillDir(name string) string {
	for _, dir := range rm.store.globalDirs {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			return path
		}
	}
	return ""
}

func (rm *RegistryManager) findSkillDirForUser(name, senderID string) string {
	if dir := rm.findSkillDir(name); dir != "" {
		return dir
	}
	if senderID != "" {
		path := filepath.Join(rm.userSkillsDir(senderID), name)
		if rm.useSandbox() {
			ctx, cancel := rm.sandboxCtx()
			defer cancel()
			if _, err := rm.sandbox.Stat(ctx, filepath.Join(path, "SKILL.md"), senderID); err == nil {
				return path
			}
		} else {
			if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
				return path
			}
		}
	}
	return ""
}

func (rm *RegistryManager) findAgentFile(name, senderID string) string {
	if rm.agentStore != nil && rm.agentStore.globalDir != "" {
		path := filepath.Join(rm.agentStore.globalDir, name+".md")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if senderID != "" {
		path := filepath.Join(rm.userAgentsDir(senderID), name+".md")
		if rm.useSandbox() {
			ctx, cancel := rm.sandboxCtx()
			defer cancel()
			if _, err := rm.sandbox.Stat(ctx, path, senderID); err == nil {
				return path
			}
		} else {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}

// findPluginDir locates a plugin directory by ID or name.
func (rm *RegistryManager) findPluginDir(name string) string {
	pluginsDir := rm.pluginsDir()
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return ""
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pluginDir := filepath.Join(pluginsDir, ent.Name())
		manifest, err := plugin.LoadManifest(pluginDir)
		if err != nil {
			continue
		}
		if manifest.ID == name || manifest.Name == name {
			return pluginDir
		}
		if ent.Name() == name {
			return pluginDir
		}
	}
	return ""
}

// --- Sandbox helpers ---

// copyDirToSandbox copies a local directory (server cache) to a sandbox directory (user workspace).
func (rm *RegistryManager) copyDirToSandbox(ctx context.Context, src, dst, userID string) error {
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

		if d.IsDir() {
			return rm.sandbox.MkdirAll(ctx, targetPath, fi.Mode(), userID)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return rm.sandbox.WriteFile(ctx, targetPath, data, fi.Mode(), userID)
	})
}

// --- File helpers ---

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

// unzipToDir extracts a zip archive to destDir.
func unzipToDir(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, f.Name)

		// Prevent zip slip
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("zip slip detected: %s", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(destPath, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return err
		}

		out, err := os.Create(destPath)
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			out.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// --- Install ---

// InstallSkillFromFile installs a skill from a plain zip (no xbot.json manifest).
// The zip must contain a top-level directory with a SKILL.md file, e.g.:
//
//	my-skill/
//	  SKILL.md
//	  references/...
//	  scripts/...
//
// The top-level directory name becomes the skill name. If SKILL.md is at the
// zip root (no wrapping directory), the zip filename (without extension) is used.
func (rm *RegistryManager) InstallSkillFromFile(zipPath, senderID string, force bool) (string, error) {
	tmpDir, err := os.MkdirTemp("", "xbot-skill-unpack-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := unzipToDir(zipPath, tmpDir); err != nil {
		return "", fmt.Errorf("unzip: %w", err)
	}

	// Find the skill directory: either a top-level dir containing SKILL.md,
	// or SKILL.md at the root (use zip filename as skill name).
	skillName, srcDir, err := findSkillDir(tmpDir, zipPath)
	if err != nil {
		return "", err
	}

	destDir := filepath.Join(rm.userSkillsDir(senderID), skillName)

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if _, err := rm.sandbox.Stat(ctx, destDir, senderID); err == nil {
			if !force {
				return "", fmt.Errorf("skill %q already exists (use force=true to overwrite)", skillName)
			}
			if err := rm.sandbox.RemoveAll(ctx, destDir, senderID); err != nil {
				return "", fmt.Errorf("remove old skill: %w", err)
			}
		}
		if err := rm.copyDirToSandbox(ctx, srcDir, destDir, senderID); err != nil {
			return "", fmt.Errorf("copy skill: %w", err)
		}
	} else {
		if _, err := os.Stat(destDir); err == nil {
			if !force {
				return "", fmt.Errorf("skill %q already exists (use force=true to overwrite)", skillName)
			}
			if err := os.RemoveAll(destDir); err != nil {
				return "", fmt.Errorf("remove old skill: %w", err)
			}
		}
		if err := copyDir(srcDir, destDir); err != nil {
			return "", fmt.Errorf("copy skill: %w", err)
		}
	}

	// Invalidate the skill cache so the newly installed skill shows up immediately.
	if rm.store != nil {
		rm.store.InvalidateCache()
	}

	log.WithFields(log.Fields{
		"skill":  skillName,
		"sender": senderID,
	}).Info("Installed skill from zip")
	return skillName, nil
}

// findSkillDir locates the skill directory and name from an unpacked zip.
// Returns (skillName, srcDir, error).
func findSkillDir(tmpDir, zipPath string) (string, string, error) {
	// Check if SKILL.md is at the root
	rootSkill := filepath.Join(tmpDir, "SKILL.md")
	if _, err := os.Stat(rootSkill); err == nil {
		// SKILL.md at root — use zip filename (without extension) as skill name
		base := filepath.Base(zipPath)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		return name, tmpDir, nil
	}

	// Look for a top-level directory containing SKILL.md
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return "", "", fmt.Errorf("read unpacked dir: %w", err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillMD := filepath.Join(tmpDir, e.Name(), "SKILL.md")
		if _, err := os.Stat(skillMD); err == nil {
			return e.Name(), filepath.Join(tmpDir, e.Name()), nil
		}
	}

	return "", "", fmt.Errorf("no SKILL.md found in zip (expected a directory containing SKILL.md)")
}

// --- Uninstall ---

// UninstallSkill removes a user-installed skill by name.
// Only removes from the user's skills directory (not global or embedded).
func (rm *RegistryManager) UninstallSkill(name, senderID string) error {
	dir := filepath.Join(rm.userSkillsDir(senderID), name)

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if _, err := rm.sandbox.Stat(ctx, dir, senderID); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("skill %q is not installed", name)
		}
		if err := rm.sandbox.RemoveAll(ctx, dir, senderID); err != nil {
			return fmt.Errorf("remove skill: %w", err)
		}
	} else {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return fmt.Errorf("skill %q is not installed", name)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove skill: %w", err)
		}
	}
	// Invalidate the skill cache so the uninstalled skill disappears immediately.
	if rm.store != nil {
		rm.store.InvalidateCache()
	}
	log.WithFields(log.Fields{"type": "skill", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}

// --- Agent install / uninstall ---

// InstallAgentFromFile copies an agent .md file to the user's agents directory.
// The agent name is derived from the provided name parameter (typically the
// original filename without .md extension). Returns the installed agent name.
func (rm *RegistryManager) InstallAgentFromFile(mdPath, name, senderID string, force bool) (string, error) {
	agentsDir := rm.userAgentsDir(senderID)
	destFile := filepath.Join(agentsDir, name+".md")

	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("read agent file: %w", err)
	}

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if err := rm.sandbox.MkdirAll(ctx, agentsDir, 0o755, senderID); err != nil {
			return "", fmt.Errorf("create agents dir: %w", err)
		}
		if _, err := rm.sandbox.Stat(ctx, destFile, senderID); err == nil && !force {
			return "", fmt.Errorf("agent %q already exists (use force=true to overwrite)", name)
		}
		if err := rm.sandbox.WriteFile(ctx, destFile, data, 0o644, senderID); err != nil {
			return "", fmt.Errorf("write agent file: %w", err)
		}
	} else {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return "", fmt.Errorf("create agents dir: %w", err)
		}
		if _, err := os.Stat(destFile); err == nil && !force {
			return "", fmt.Errorf("agent %q already exists (use force=true to overwrite)", name)
		}
		if err := os.WriteFile(destFile, data, 0o644); err != nil {
			return "", fmt.Errorf("write agent file: %w", err)
		}
	}

	log.WithFields(log.Fields{
		"agent":  name,
		"sender": senderID,
	}).Info("Installed agent from file")
	return name, nil
}

// UninstallAgent removes an agent .md file from the user's agents directory.
func (rm *RegistryManager) UninstallAgent(name, senderID string) error {
	agentsDir := rm.userAgentsDir(senderID)
	mdFile := filepath.Join(agentsDir, name+".md")

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if _, err := rm.sandbox.Stat(ctx, mdFile, senderID); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent %q is not installed", name)
		}
		if err := rm.sandbox.Remove(ctx, mdFile, senderID); err != nil {
			return fmt.Errorf("remove agent: %w", err)
		}
	} else {
		if _, err := os.Stat(mdFile); os.IsNotExist(err) {
			return fmt.Errorf("agent %q is not installed", name)
		}
		if err := os.Remove(mdFile); err != nil {
			return fmt.Errorf("remove agent: %w", err)
		}
	}
	log.WithFields(log.Fields{"type": "agent", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}
