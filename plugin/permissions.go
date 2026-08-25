package plugin

import (
	"strings"
)

// ---------------------------------------------------------------------------
// Permission Constants
// ---------------------------------------------------------------------------

const (
	// PermToolsRegister grants permission to register tools.
	PermToolsRegister = "tools.register"
	// PermToolsCall grants permission to invoke tools.
	PermToolsCall = "tools.call"
	// PermHooksSubscribe grants permission to subscribe to lifecycle hooks.
	PermHooksSubscribe = "hooks.subscribe"
	// PermContextEnrich grants permission to register context enrichers.
	PermContextEnrich = "context.enrich"
	// PermStoragePrivate grants access to the plugin's private key-value storage.
	PermStoragePrivate = "storage.private"
	// PermStorageShared grants access to the shared plugin storage.
	PermStorageShared = "storage.shared"
	// PermNetworkOutbound grants permission to make outbound network requests.
	PermNetworkOutbound = "network.outbound"
	// PermBusRead grants permission to read from the event bus.
	PermBusRead = "bus.read"
	// PermBusWrite grants permission to publish to the event bus.
	PermBusWrite = "bus.write"
	// PermBusPlugin grants permission to use the plugin-to-plugin event bus.
	PermBusPlugin = "bus.plugin"
	// PermUIContribute grants permission to contribute UI widgets.
	PermUIContribute = "ui.contribute"
	// PermChannelsRegister grants permission to register custom Channel providers.
	PermChannelsRegister = "channels.register"
	// PermCommandsRegister grants permission to register slash commands.
	PermCommandsRegister = "commands.register"
	// PermCronSchedule grants permission to schedule cron tasks.
	PermCronSchedule = "cron.schedule"
	// PermUIThemes grants permission to contribute themes.
	PermUIThemes = "ui.themes"
	// PermUIOverlay grants permission to register and control overlays.
	PermUIOverlay = "ui.overlay"
	// PermNotificationsSend grants permission to send notifications and play sounds.
	PermNotificationsSend = "notifications.send"
	// PermRPC grants permission for the frontend view to call the plugin's
	// backend process via ctx.rpc.call('pluginId.method') (web_plugin_rpc).
	// Matches the frontend Permission 'rpc' (web/src/plugin-api/manifest.ts).
	PermRPC = "rpc"
	// PermEvents grants access to the typed event bus (ctx.events).
	// Matches the frontend Permission 'events' (web/src/plugin-api/manifest.ts).
	PermEvents = "events"
	// PermCommands grants access to command registration/execution (ctx.commands).
	// Matches the frontend Permission 'commands' (web/src/plugin-api/manifest.ts).
	PermCommands = "commands"
	// PermState grants access to the key-value state store (ctx.state).
	// Matches the frontend Permission 'state' (web/src/plugin-api/manifest.ts).
	PermState = "state"
	// PermUI grants UI capabilities: toast, panel open/close, and editor view
	// tabs (ctx.ui.openViewTab/openFileTab). Matches the frontend Permission
	// 'ui' (web/src/plugin-api/manifest.ts).
	PermUI = "ui"
	// PermPlugins grants access to the inter-plugin registry (ctx.plugins).
	// Matches the frontend Permission 'plugins' (web/src/plugin-api/manifest.ts).
	PermPlugins = "plugins"
	// PermConfig grants access to read/write the plugin's own configuration
	// (ctx.config.get/set and ctx.config.onConfigChange). Matches the frontend
	// Permission 'config' (web/src/plugin-api/manifest.ts).
	PermConfig = "config"
)

// allPermissions is the set of all recognized permission strings.
var allPermissions = map[string]bool{
	PermToolsRegister:     true,
	PermToolsCall:         true,
	PermHooksSubscribe:    true,
	PermContextEnrich:     true,
	PermStoragePrivate:    true,
	PermStorageShared:     true,
	PermNetworkOutbound:   true,
	PermBusRead:           true,
	PermBusWrite:          true,
	PermBusPlugin:         true,
	PermUIContribute:      true,
	PermChannelsRegister:  true,
	PermCommandsRegister:  true,
	PermCronSchedule:      true,
	PermUIThemes:          true,
	PermUIOverlay:         true,
	PermNotificationsSend: true,
	PermRPC:               true,
	PermEvents:            true,
	PermCommands:          true,
	PermState:             true,
	PermUI:                true,
	PermPlugins:           true,
	PermConfig:            true,
}

// IsValidPermission returns true if the given string is a known permission.
func IsValidPermission(perm string) bool {
	return allPermissions[perm]
}

// AllPermissions returns a list of all valid permission strings.
func AllPermissions() []string {
	perms := make([]string, 0, len(allPermissions))
	for p := range allPermissions {
		perms = append(perms, p)
	}
	return perms
}

// ---------------------------------------------------------------------------
// PermissionChecker — validates permissions from manifest
// ---------------------------------------------------------------------------

// PermissionChecker determines whether a plugin has a specific permission.
type PermissionChecker struct {
	permissions map[string]bool
	wildcard    bool // true if "*" was in the permissions list
}

// NewPermissionChecker creates a checker from the plugin's declared permissions.
func NewPermissionChecker(permissions []string) *PermissionChecker {
	pc := &PermissionChecker{
		permissions: make(map[string]bool, len(permissions)),
	}
	for _, p := range permissions {
		p = strings.TrimSpace(p)
		if p == "*" {
			pc.wildcard = true
			continue
		}
		if IsValidPermission(p) {
			pc.permissions[p] = true
		}
	}
	return pc
}

// Has returns true if the plugin has the specified permission.
func (pc *PermissionChecker) Has(permission string) bool {
	if pc.wildcard {
		return true
	}
	return pc.permissions[permission]
}

// HasAll returns true if the plugin has all specified permissions.
func (pc *PermissionChecker) HasAll(permissions ...string) bool {
	for _, p := range permissions {
		if !pc.Has(p) {
			return false
		}
	}
	return true
}

// HasAny returns true if the plugin has at least one of the specified permissions.
func (pc *PermissionChecker) HasAny(permissions ...string) bool {
	for _, p := range permissions {
		if pc.Has(p) {
			return true
		}
	}
	return false
}
