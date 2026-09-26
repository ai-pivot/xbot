package selfupdate

import (
	"os"
	"testing"

	"xbot/version"
)

// GetSystemInfo must always carry the compile-time version fields and a
// managedBy verdict — the web About panel renders them unconditionally.
func TestGetSystemInfo_CarriesVersionAndSupervisor(t *testing.T) {
	info := GetSystemInfo()
	if info.Version != version.Version {
		t.Errorf("Version = %q, want %q", info.Version, version.Version)
	}
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("runtime fields must never be empty: %+v", info)
	}
	switch info.ManagedBy {
	case "systemd", "launchd", "supervisord", "docker", "none":
		// valid verdicts
	default:
		t.Errorf("ManagedBy = %q, want systemd|launchd|supervisord|docker|none", info.ManagedBy)
	}
	// DevBuild must be derived, not hardcoded: tests run without ldflags so
	// Version=="dev" and Commit=="unknown" — the dev verdict must hold here.
	if version.Version == "dev" && !info.DevBuild {
		t.Error("DevBuild must be true when version.Version == dev")
	}
}

// CheckForUpdate on a dev build must skip with a reason (never a bare error)
// — the web UI renders the reason instead of a dead end.
func TestCheckForUpdate_DevBuildSkipsWithReason(t *testing.T) {
	if version.Version != "dev" {
		t.Skip("test binary built with ldflags version; dev-skip path not reachable")
	}
	uc := CheckForUpdate()
	if !uc.Skipped {
		t.Fatal("dev build check must be skipped")
	}
	if uc.Reason == "" {
		t.Error("skipped check must carry a human-readable reason")
	}
	if uc.Tag != "" {
		t.Errorf("skipped check must not offer a download tag, got %q", uc.Tag)
	}
}

// systemdUnitName parses the unit from /proc/self/cgroup (cgroup v2 user
// services carry the unit path); outside systemd it falls back to the
// canonical install.sh unit name.
func TestSystemdUnitName_Fallback(t *testing.T) {
	if os.Getenv("INVOCATION_ID") != "" {
		t.Skip("running under systemd — cgroup parsing is live, fallback not reachable")
	}
	if got := systemdUnitName(); got != "xbot-server" {
		t.Errorf("systemdUnitName() = %q, want canonical fallback xbot-server", got)
	}
}

// DetectServiceManager must return a valid verdict in every environment.
func TestDetectServiceManager_ValidVerdict(t *testing.T) {
	switch got := DetectServiceManager(); got {
	case "systemd", "launchd", "supervisord", "docker", "none":
	default:
		t.Errorf("DetectServiceManager() = %q, want systemd|launchd|supervisord|docker|none", got)
	}
}
