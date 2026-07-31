package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/plugin"
	"xbot/tools"
)

// RegistryManager manages skill/agent/plugin packaging, installation, and uninstall.
type RegistryManager struct {
	store             *SkillStore
	agentStore        *AgentStore
	workDir           string
	xbotHome          string // global xbot config dir (e.g. ~/.xbot), used for plugins dir
	sandbox           tools.Sandbox
	pluginActivator   func(pluginID string) error // activate a plugin after install (nil if plugin system unavailable)
	pluginDeactivator func(pluginID string) error // deactivate+remove a plugin after uninstall (nil if plugin system unavailable)
}

// SetPluginActivator sets a callback to activate a plugin immediately after installation.
// Called by Agent after the plugin system is initialized.
func (rm *RegistryManager) SetPluginActivator(fn func(pluginID string) error) {
	rm.pluginActivator = fn
}

// SetPluginDeactivator sets a callback to deactivate a plugin before uninstall.
// Called by Agent after the plugin system is initialized.
func (rm *RegistryManager) SetPluginDeactivator(fn func(pluginID string) error) {
	rm.pluginDeactivator = fn
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

// appsDir returns the directory for storing installed app manifests.
func (rm *RegistryManager) appsDir() string {
	return filepath.Join(rm.xbotHome, "apps")
}

// useSandbox 判断是否应使用 Sandbox 访问用户文件。
func (rm *RegistryManager) useSandbox() bool {
	return rm.sandbox != nil && rm.sandbox.Name() != "none"
}

// isDockerSandbox returns true if the sandbox is Docker (syncs to .skills/.agents).
func (rm *RegistryManager) isDockerSandbox() bool {
	return rm.sandbox != nil && rm.sandbox.Name() == "docker"
}

// globalSyncedSkillsDir returns the directory where global skills are synced inside the sandbox.
func (rm *RegistryManager) globalSyncedSkillsDir(senderID string) string {
	if !rm.useSandbox() {
		return ""
	}
	ws := rm.sandbox.Workspace(senderID)
	if ws == "" {
		return ""
	}
	if rm.isDockerSandbox() {
		return filepath.Join(ws, ".skills")
	}
	return filepath.Join(ws, "skills")
}

// globalSyncedAgentsDir returns the directory where global agents are synced inside the sandbox.
func (rm *RegistryManager) globalSyncedAgentsDir(senderID string) string {
	if !rm.useSandbox() {
		return ""
	}
	ws := rm.sandbox.Workspace(senderID)
	if ws == "" {
		return ""
	}
	if rm.isDockerSandbox() {
		return filepath.Join(ws, ".agents")
	}
	return filepath.Join(ws, "agents")
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

// --- Uninstall ---

// Uninstall removes a user-installed skill/agent/plugin/app.
func (rm *RegistryManager) Uninstall(entryType, name, senderID string) error {
	switch entryType {
	case "skill":
		return rm.uninstallSkill(name, senderID)
	case "agent":
		return rm.uninstallAgent(name, senderID)
	case "plugin":
		return rm.uninstallPlugin(name, senderID)
	case "app":
		return rm.uninstallApp(name, senderID)
	default:
		return fmt.Errorf("unknown type %q", entryType)
	}
}

func (rm *RegistryManager) uninstallSkill(name, senderID string) error {
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
	log.WithFields(log.Fields{"type": "skill", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}

// uninstallAgent removes the agent's .md file from user's agents dir.
func (rm *RegistryManager) uninstallAgent(name, senderID string) error {
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

// uninstallPlugin removes a plugin directory from the global plugins directory.
func (rm *RegistryManager) uninstallPlugin(name, senderID string) error {
	pluginDir := rm.findPluginDir(name)
	if pluginDir == "" {
		return fmt.Errorf("plugin %q is not installed", name)
	}

	// Remove files FIRST, then ReloadAll.
	// If we ReloadAll before removing, the plugin is still on disk →
	// ReloadAll re-discovers and re-activates it → zombie plugin running
	// on deleted files.
	if err := os.RemoveAll(pluginDir); err != nil {
		return fmt.Errorf("remove plugin: %w", err)
	}

	// ReloadAll re-discovers from disk (deleted plugin won't be found),
	// re-activates remaining plugins, and fires OnReload callbacks which
	// re-wire hooks/tools/widgets/commands.
	if rm.pluginDeactivator != nil {
		if err := rm.pluginDeactivator(name); err != nil {
			log.WithFields(log.Fields{"plugin": name, "error": err}).Warn("Failed to reload plugins after uninstall")
		}
	}

	log.WithFields(log.Fields{"type": "plugin", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}

// uninstallApp reads the saved manifest for an installed app and removes all its items.
func (rm *RegistryManager) uninstallApp(name, senderID string) error {
	manifestPath := filepath.Join(rm.appsDir(), name+".json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("app %q is not installed (no manifest found)", name)
	}

	var manifest AppManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("read app manifest: %w", err)
	}

	var errs []string
	for _, c := range manifest.Contents {
		switch c.Type {
		case "skill":
			if err := rm.uninstallSkill(c.Name, senderID); err != nil {
				errs = append(errs, fmt.Sprintf("skill %s: %v", c.Name, err))
			}
		case "agent":
			if err := rm.uninstallAgent(c.Name, senderID); err != nil {
				errs = append(errs, fmt.Sprintf("agent %s: %v", c.Name, err))
			}
		case "plugin":
			if err := rm.uninstallPlugin(c.Name, senderID); err != nil {
				errs = append(errs, fmt.Sprintf("plugin %s: %v", c.Name, err))
			}
		}
	}

	// Only remove the manifest if all items uninstalled successfully
	if len(errs) > 0 {
		return fmt.Errorf("partial uninstall, %d errors: %s", len(errs), strings.Join(errs, "; "))
	}
	_ = os.Remove(manifestPath)
	log.WithFields(log.Fields{"type": "app", "name": name, "sender": senderID}).Info("Uninstalled")
	return nil
}

// --- List installed ---

// ListInstalledSkills returns the names of skills installed for the given user.
func (rm *RegistryManager) ListInstalledSkills(senderID string) []string {
	dir := rm.userSkillsDir(senderID)
	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		entries, err := rm.sandbox.ReadDir(ctx, dir, senderID)
		if err != nil {
			return nil
		}
		var names []string
		for _, e := range entries {
			if e.IsDir {
				names = append(names, e.Name)
			}
		}
		return names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// ListInstalledAgents returns the names of agents installed for the given user.
func (rm *RegistryManager) ListInstalledAgents(senderID string) []string {
	dir := rm.userAgentsDir(senderID)
	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		entries, err := rm.sandbox.ReadDir(ctx, dir, senderID)
		if err != nil {
			return nil
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir && strings.HasSuffix(e.Name, ".md") {
				names = append(names, strings.TrimSuffix(e.Name, ".md"))
			}
		}
		return names
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, strings.TrimSuffix(e.Name(), ".md"))
		}
	}
	return names
}

// ListInstalledPlugins returns the names (IDs) of installed plugins.
func (rm *RegistryManager) ListInstalledPlugins(senderID string) []string {
	pluginsDir := rm.pluginsDir()
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pluginDir := filepath.Join(pluginsDir, ent.Name())
		manifest, err := plugin.LoadManifest(pluginDir)
		if err != nil {
			continue
		}
		names = append(names, manifest.ID)
	}
	return names
}

// ListInstalledApps returns the names of installed apps.
func (rm *RegistryManager) ListInstalledApps() []string {
	entries, err := os.ReadDir(rm.appsDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
			names = append(names, strings.TrimSuffix(ent.Name(), ".json"))
		}
	}
	return names
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
		if rm.isDockerSandbox() {
			if syncedDir := rm.globalSyncedSkillsDir(senderID); syncedDir != "" {
				path := filepath.Join(syncedDir, name)
				ctx, cancel := rm.sandboxCtx()
				defer cancel()
				if _, err := rm.sandbox.Stat(ctx, filepath.Join(path, "SKILL.md"), senderID); err == nil {
					return path
				}
			}
		}
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
		if rm.isDockerSandbox() {
			if syncedDir := rm.globalSyncedAgentsDir(senderID); syncedDir != "" {
				path := filepath.Join(syncedDir, name+".md")
				ctx, cancel := rm.sandboxCtx()
				defer cancel()
				if _, err := rm.sandbox.Stat(ctx, path, senderID); err == nil {
					return path
				}
			}
		}
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

// --- Pack / Install ---

// PackApp packs local skill/agent/plugin items into a .xbot.zip file.
func (rm *RegistryManager) PackApp(items []AppItem, outputPath, author string) error {
	bp := NewAppPackager(rm.workDir)
	return bp.Pack(rm, items, outputPath, author)
}

// InstallAppFromFile installs a .xbot.zip app from a local file path.
func (rm *RegistryManager) InstallAppFromFile(zipPath, senderID string, force bool) (*AppInstallResult, error) {
	bp := NewAppPackager(rm.workDir)
	manifest, tmpDir, err := bp.Unpack(zipPath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	if err := bp.Validate(manifest, tmpDir); err != nil {
		return nil, err
	}

	result := &AppInstallResult{
		Manifest:  *manifest,
		Installed: []string{},
		Skipped:   []string{},
	}

	for _, c := range manifest.Contents {
		switch c.Type {
		case "skill":
			skipped, err := rm.installAppSkill(c, tmpDir, senderID, force)
			if err != nil {
				return nil, fmt.Errorf("install skill %q: %w", c.Name, err)
			}
			if skipped {
				result.Skipped = append(result.Skipped, fmt.Sprintf("skill: %s", c.Name))
			} else {
				result.Installed = append(result.Installed, fmt.Sprintf("skill: %s", c.Name))
			}
		case "agent":
			skipped, err := rm.installAppAgent(c, tmpDir, senderID, force)
			if err != nil {
				return nil, fmt.Errorf("install agent %q: %w", c.Name, err)
			}
			if skipped {
				result.Skipped = append(result.Skipped, fmt.Sprintf("agent: %s", c.Name))
			} else {
				result.Installed = append(result.Installed, fmt.Sprintf("agent: %s", c.Name))
			}
		case "plugin":
			skipped, err := rm.installAppPlugin(c, tmpDir, senderID, force)
			if err != nil {
				return nil, fmt.Errorf("install plugin %q: %w", c.Name, err)
			}
			if skipped {
				result.Skipped = append(result.Skipped, fmt.Sprintf("plugin: %s", c.Name))
			} else {
				result.Installed = append(result.Installed, fmt.Sprintf("plugin: %s", c.Name))
			}
		default:
			return nil, fmt.Errorf("unsupported content type %q (use skill, agent, or plugin)", c.Type)
		}
	}

	// Save manifest for future uninstall
	if err := rm.saveAppManifest(manifest); err != nil {
		log.WithError(err).Warn("Failed to save app manifest for uninstall tracking")
	}

	log.WithFields(log.Fields{
		"app":    manifest.Name,
		"items":  len(result.Installed),
		"sender": senderID,
	}).Info("Installed app from file")
	return result, nil
}

// InstallAppFromURL downloads a .xbot.zip from a URL and installs it.
func (rm *RegistryManager) InstallAppFromURL(url, senderID string, force bool) (*AppInstallResult, error) {
	// Download to temp file
	tmpFile, err := os.CreateTemp("", "xbot-install-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("download from %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Limit download size to 100MB
	if resp.ContentLength > 100*1024*1024 {
		tmpFile.Close()
		return nil, fmt.Errorf("file too large: %d bytes (max 100MB)", resp.ContentLength)
	}

	limited := io.LimitReader(resp.Body, 100*1024*1024+1)
	written, err := io.Copy(tmpFile, limited)
	if err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("download write: %w", err)
	}
	if written > 100*1024*1024 {
		tmpFile.Close()
		return nil, fmt.Errorf("file too large: exceeds 100MB limit")
	}
	tmpFile.Close()

	return rm.InstallAppFromFile(tmpPath, senderID, force)
}

// saveAppManifest writes the app manifest to ~/.xbot/apps/<name>.json.
func (rm *RegistryManager) saveAppManifest(manifest *AppManifest) error {
	dir := rm.appsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create apps dir: %w", err)
	}
	path := filepath.Join(dir, manifest.Name+".json")
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// installAppSkill copies a skill from the unpacked app to the user's skill directory.
// installAppSkill copies a skill directory from the unpacked app to the user's skills directory.
// Returns skipped=true if the skill already exists and force=false.
func (rm *RegistryManager) installAppSkill(c AppContent, srcDir, senderID string, force bool) (bool, error) {
	srcPath := filepath.Join(srcDir, strings.TrimRight(c.Source, "/"))
	destDir := filepath.Join(rm.userSkillsDir(senderID), c.Name)

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if _, err := rm.sandbox.Stat(ctx, destDir, senderID); err == nil {
			if !force {
				return true, nil
			}
			if err := rm.sandbox.RemoveAll(ctx, destDir, senderID); err != nil {
				return false, fmt.Errorf("remove old skill: %w", err)
			}
		}
		if err := rm.copyDirToSandbox(ctx, srcPath, destDir, senderID); err != nil {
			return false, fmt.Errorf("copy skill: %w", err)
		}
	} else {
		if _, err := os.Stat(destDir); err == nil {
			if !force {
				return true, nil
			}
			if err := os.RemoveAll(destDir); err != nil {
				return false, fmt.Errorf("remove old skill: %w", err)
			}
		}
		if err := copyDir(srcPath, destDir); err != nil {
			return false, fmt.Errorf("copy skill: %w", err)
		}
	}
	return false, nil
}

// installAppAgent copies an agent .md file from the unpacked app to the user's agents directory.
// Returns skipped=true if the agent already exists and force=false.
func (rm *RegistryManager) installAppAgent(c AppContent, srcDir, senderID string, force bool) (bool, error) {
	srcPath := filepath.Join(srcDir, strings.TrimRight(c.Source, "/"))
	agentsDir := rm.userAgentsDir(senderID)

	if rm.useSandbox() {
		ctx, cancel := rm.sandboxCtx()
		defer cancel()
		if err := rm.sandbox.MkdirAll(ctx, agentsDir, 0o755, senderID); err != nil {
			return false, fmt.Errorf("create agents dir: %w", err)
		}
		destFile := filepath.Join(agentsDir, c.Name+".md")
		if _, err := rm.sandbox.Stat(ctx, destFile, senderID); err == nil {
			if !force {
				return true, nil
			}
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return false, fmt.Errorf("read agent file: %w", err)
		}
		if err := rm.sandbox.WriteFile(ctx, destFile, data, 0o644, senderID); err != nil {
			return false, fmt.Errorf("write agent file: %w", err)
		}
	} else {
		if err := os.MkdirAll(agentsDir, 0o755); err != nil {
			return false, fmt.Errorf("create agents dir: %w", err)
		}
		destFile := filepath.Join(agentsDir, c.Name+".md")
		if _, err := os.Stat(destFile); err == nil {
			if !force {
				return true, nil
			}
		}
		if err := copyFile(srcPath, destFile); err != nil {
			return false, fmt.Errorf("copy agent file: %w", err)
		}
	}
	return false, nil
}

// installAppPlugin copies a plugin directory from the unpacked app to the global plugins directory.
// Returns skipped=true if the plugin already exists and force=false.
func (rm *RegistryManager) installAppPlugin(c AppContent, srcDir, senderID string, force bool) (bool, error) {
	srcPath := filepath.Join(srcDir, strings.TrimRight(c.Source, "/"))
	pluginsDir := rm.pluginsDir()

	manifest, err := plugin.LoadManifest(srcPath)
	if err != nil {
		return false, fmt.Errorf("read plugin manifest: %w", err)
	}

	destDir := filepath.Join(pluginsDir, manifest.ID)

	if _, err := os.Stat(destDir); err == nil {
		if !force {
			return true, nil
		}
		if err := os.RemoveAll(destDir); err != nil {
			return false, fmt.Errorf("remove old plugin: %w", err)
		}
	}

	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return false, fmt.Errorf("create plugins dir: %w", err)
	}

	if err := copyDir(srcPath, destDir); err != nil {
		return false, fmt.Errorf("copy plugin: %w", err)
	}

	log.WithFields(log.Fields{
		"type": "plugin", "name": manifest.ID, "sender": senderID,
		"from": srcPath, "to": destDir,
	}).Info("Installed plugin from app")

	// Activate the plugin immediately if a plugin manager is available
	if rm.pluginActivator != nil {
		if err := rm.pluginActivator(manifest.ID); err != nil {
			log.WithFields(log.Fields{"plugin": manifest.ID, "error": err}).Warn("Failed to activate plugin after install, use /plugin reload-all")
		}
	}

	return false, nil
}
