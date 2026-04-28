package plugin

import (
	"strings"
)

// ---------------------------------------------------------------------------
// Permission Constants
// ---------------------------------------------------------------------------

const (
	PermToolsRegister   = "tools.register"
	PermToolsCall       = "tools.call"
	PermHooksSubscribe  = "hooks.subscribe"
	PermContextEnrich   = "context.enrich"
	PermStoragePrivate  = "storage.private"
	PermStorageShared   = "storage.shared"
	PermNetworkOutbound = "network.outbound"
	PermBusRead         = "bus.read"
	PermBusWrite        = "bus.write"
)

// allPermissions is the set of all recognized permission strings.
var allPermissions = map[string]bool{
	PermToolsRegister:   true,
	PermToolsCall:       true,
	PermHooksSubscribe:  true,
	PermContextEnrich:   true,
	PermStoragePrivate:  true,
	PermStorageShared:   true,
	PermNetworkOutbound: true,
	PermBusRead:         true,
	PermBusWrite:        true,
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
