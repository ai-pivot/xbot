package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Manifest Loading & Validation
// ---------------------------------------------------------------------------

// LoadManifest reads and validates a plugin.json file from the given directory.
func LoadManifest(dir string) (*PluginManifest, error) {
	manifestPath := filepath.Join(dir, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}

	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", manifestPath, err)
	}

	if err := validateManifest(&manifest, dir); err != nil {
		return nil, fmt.Errorf("validate manifest %s: %w", manifestPath, err)
	}

	return &manifest, nil
}

// validateManifest checks that a manifest has all required fields and valid values.
func validateManifest(m *PluginManifest, dir string) error {
	// ID is required and must be a valid identifier
	if m.ID == "" {
		return fmt.Errorf("plugin id is required")
	}
	if !isValidPluginID(m.ID) {
		return fmt.Errorf("plugin id must match ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$, got %q", m.ID)
	}

	// Name is required
	if m.Name == "" {
		return fmt.Errorf("plugin name is required")
	}

	// Version is required and should be semver
	if m.Version == "" {
		return fmt.Errorf("plugin version is required")
	}

	// Runtime must be valid
	switch m.Runtime {
	case RuntimeNative, RuntimeGRPC, RuntimeWASM, "":
		// valid
	default:
		return fmt.Errorf("invalid runtime %q (must be native, grpc, or wasm)", m.Runtime)
	}

	// Default runtime to native
	if m.Runtime == "" {
		m.Runtime = RuntimeNative
	}

	// Entry or Executable must be non-empty for grpc runtime
	if m.Runtime == RuntimeGRPC && m.Entry == "" && m.Executable == "" {
		return fmt.Errorf("entry or executable is required for grpc runtime plugins")
	}

	// Validate activation events
	for _, event := range m.ActivationEvents {
		if err := validateActivationEvent(event); err != nil {
			return fmt.Errorf("invalid activation event %q: %w", event, err)
		}
	}

	// Default activation to onStart
	if len(m.ActivationEvents) == 0 {
		m.ActivationEvents = []string{"onStart"}
	}

	// Validate permissions
	for _, perm := range m.Permissions {
		if perm == "*" {
			continue // wildcard allowed in manifest
		}
		if !IsValidPermission(perm) {
			return fmt.Errorf("unknown permission %q", perm)
		}
	}

	// Validate tool contributions
	if m.Contributes != nil {
		for i, tool := range m.Contributes.Tools {
			if tool.Name == "" {
				return fmt.Errorf("contributes.tools[%d].name is required", i)
			}
			if tool.Description == "" {
				return fmt.Errorf("contributes.tools[%d].description is required", i)
			}
		}
		for i, hook := range m.Contributes.Hooks {
			if hook.Event == "" {
				return fmt.Errorf("contributes.hooks[%d].event is required", i)
			}
			if !IsValidHookEvent(hook.Event) {
				return fmt.Errorf("contributes.hooks[%d].event: unknown event %q", i, hook.Event)
			}
		}
	}

	return nil
}

// isValidPluginID checks that a plugin ID matches the pattern ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$.
// This prevents path traversal, null bytes, and other injection attacks.
func isValidPluginID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	// First char must be alphanumeric
	if !isAlphanumeric(id[0]) {
		return false
	}
	// Remaining chars: alphanumeric, dot, underscore, hyphen
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !isAlphanumeric(c) && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isAlphanumeric(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// validateActivationEvent checks if an activation event string is well-formed.
func validateActivationEvent(event string) error {
	switch {
	case event == "onStart":
		return nil
	case strings.HasPrefix(event, "onTool:"):
		toolName := strings.TrimPrefix(event, "onTool:")
		if toolName == "" {
			return fmt.Errorf("tool name after 'onTool:' must not be empty")
		}
		return nil
	case strings.HasPrefix(event, "onHook:"):
		hookName := strings.TrimPrefix(event, "onHook:")
		if !IsValidHookEvent(hookName) {
			return fmt.Errorf("unknown hook event %q", hookName)
		}
		return nil
	case strings.HasPrefix(event, "onCommand:"):
		cmd := strings.TrimPrefix(event, "onCommand:")
		if cmd == "" {
			return fmt.Errorf("command after 'onCommand:' must not be empty")
		}
		return nil
	default:
		return fmt.Errorf("unknown activation event format (expected onStart, onTool:<name>, onHook:<event>, or onCommand:<cmd>)")
	}
}

// IsValidHookEvent checks if a hook event name is recognized.
func IsValidHookEvent(name string) bool {
	switch HookEvent(name) {
	case HookPreToolUse, HookPostToolUse, HookPostToolUseError,
		HookUserPromptSubmit, HookAgentStop,
		HookSessionStart, HookSessionEnd,
		HookSubAgentStart, HookSubAgentStop,
		HookPreCompact, HookPostCompact,
		HookCronFired, HookWebhookReceived:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Manifest Discovery — scan plugin directories
// ---------------------------------------------------------------------------

// DiscoverPlugins scans the given directories for plugin.json manifests.
// Returns a list of PluginManifest for each valid plugin found.
// Invalid plugins are logged as warnings and skipped.
func DiscoverPlugins(dirs []string) []*PluginManifest {
	var manifests []*PluginManifest

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				log.WithField("dir", dir).Warn("Failed to scan plugin directory")
			}
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pluginDir := filepath.Join(dir, entry.Name())
			manifest, err := LoadManifest(pluginDir)
			if err != nil {
				log.WithField("dir", pluginDir).Warn("Skipping invalid plugin: ", err)
				continue
			}
			manifests = append(manifests, manifest)
		}
	}

	return manifests
}

// DefaultPluginDirs returns the standard plugin search paths.
func DefaultPluginDirs(xbotHome string) []string {
	return []string{
		filepath.Join(xbotHome, "plugins"),            // user-installed plugins
		filepath.Join(xbotHome, "plugins", "builtin"), // built-in plugin packages
	}
}
