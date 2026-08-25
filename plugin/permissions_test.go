package plugin

import "testing"

// TestConfigPermissionValid ensures the 'config' permission (added to the
// frontend Permission type) is recognized by the backend permission registry.
// Regression: reloading a plugin declaring permissions:["config"] failed with
// "unknown permission \"config\"" — the backend allPermissions whitelist was
// not synced with web/src/plugin-api/manifest.ts.
func TestConfigPermissionValid(t *testing.T) {
	if !IsValidPermission("config") {
		t.Fatal("IsValidPermission('config') should be true — 'config' is a frontend v2 Permission")
	}
	perms := AllPermissions()
	found := false
	for _, p := range perms {
		if p == "config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("AllPermissions() must include 'config'")
	}
	// NewPermissionChecker should honor the config permission.
	pc := NewPermissionChecker([]string{"config"})
	if !pc.Has("config") {
		t.Fatal("PermissionChecker with ['config'] should have config permission")
	}
}

// TestAllFrontendPermissionsRegistered ensures every frontend v2 Permission
// string (web/src/plugin-api/manifest.ts) is present in the backend whitelist —
// preventing the "unknown permission" drift that rejects a plugin's manifest on
// reload.
func TestAllFrontendPermissionsRegistered(t *testing.T) {
	frontend := []string{"events", "commands", "rpc", "state", "ui", "plugins", "config"}
	for _, p := range frontend {
		if !IsValidPermission(p) {
			t.Errorf("frontend Permission %q is missing from backend allPermissions — reload will reject it", p)
		}
	}
}
