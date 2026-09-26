package selfupdate

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"xbot/version"
)

// SystemInfo describes the running backend binary: version, build metadata,
// runtime environment, and how the process is supervised (systemd / launchd /
// supervisord / docker / none). Served by the get_system_info RPC and rendered
// in the web About panel.
type SystemInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
	Channel   string `json:"channel"` // "stable" | "beta" | "nightly" | "" (dev)
	GoVersion string `json:"goVersion"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	ExePath   string `json:"exePath"`
	// ManagedBy reports the detected supervisor: "systemd", "launchd",
	// "supervisord", "docker", or "none" (unknown — could be a manual start OR
	// an unrecognized manager; the UI must NOT assume the process will or
	// will not come back after a restart).
	ManagedBy string `json:"managedBy"`
	// DevBuild is true when the binary was built without release ldflags
	// (Version=="dev" or Commit=="unknown"). The web UI uses it to explain
	// why update checks are unavailable.
	DevBuild bool `json:"devBuild"`
}

// GetSystemInfo collects version + runtime info. For dev builds whose commit
// was not injected at build time, it best-effort resolves the git commit at
// runtime (git rev-parse in the executable's directory) so the About panel can
// still show "which dev build is this" — the user explicitly asked for DEV
// builds to show as much version detail as possible.
func GetSystemInfo() SystemInfo {
	info := SystemInfo{
		Version:   version.Version,
		Commit:    version.Commit,
		BuildTime: version.BuildTime,
		Channel:   version.Channel,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		ManagedBy: DetectServiceManager(),
		DevBuild:  version.Version == "dev" || version.Commit == "unknown",
	}
	if exe, err := os.Executable(); err == nil {
		info.ExePath = exe
	}
	if info.Commit == "unknown" {
		if c := resolveGitCommit(); c != "" {
			info.Commit = c + " (runtime)"
		}
	}
	return info
}

// resolveGitCommit best-effort resolves the current git commit by running
// git rev-parse in the executable's directory (dev builds are typically run
// straight from the repo). Returns "" when git is unavailable or the dir is
// not a repo — callers treat that as "unknown".
func resolveGitCommit() string {
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = exeDir(exe)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		// Fall back to the working directory (go run / go build . from repo root).
		out, err = exec.Command("git", "rev-parse", "--short", "HEAD").Output()
		if err != nil {
			return ""
		}
	}
	c := strings.TrimSpace(string(out))
	if len(c) > 12 {
		c = c[:12]
	}
	return c
}

func exeDir(exe string) string {
	if i := strings.LastIndexByte(exe, '/'); i > 0 {
		return exe[:i]
	}
	if i := strings.LastIndexByte(exe, '\\'); i > 0 {
		return exe[:i]
	}
	return "."
}

// UpdateCheck is the result of checking GitHub for a newer release on the
// binary's channel. Mirrors version.UpdateInfo but adds the release tag (the
// download target for apply_update) and a human-readable reason when skipped.
type UpdateCheck struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Tag       string `json:"tag"` // release tag to download from ("" when skipped)
	HasUpdate bool   `json:"hasUpdate"`
	Channel   string `json:"channel"`
	URL       string `json:"url"` // release page
	Skipped   bool   `json:"skipped"`
	Reason    string `json:"reason"`
}

// CheckForUpdate queries GitHub Releases for the latest version on the
// binary's channel (stable → /releases/latest; beta/nightly → tag scan) and
// compares with the local build. Unlike the CLI's CheckUpdate (stable-only,
// silent skip for dev), this always reports a reason so the web UI can show
// why a check was skipped (e.g. dev build).
func CheckForUpdate() *UpdateCheck {
	uc := &UpdateCheck{Current: version.Version, Channel: version.Channel}

	if version.Version == "" || version.Version == "dev" {
		uc.Skipped = true
		uc.Reason = "dev build — update checks need a release build (install via scripts/install.sh)"
		return uc
	}

	ch := version.Channel
	if ch == "" {
		ch = "stable"
	}
	uc.Channel = ch

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	release := version.FetchLatestRelease(ctx, version.ReleaseChannel(ch))
	if release == nil || release.Tag == "" {
		uc.Skipped = true
		uc.Reason = "could not reach GitHub Releases (network offline or rate-limited)"
		return uc
	}

	uc.Latest = release.Tag
	uc.Tag = release.Tag
	uc.URL = release.URL
	uc.HasUpdate = hasNewerBuild(release)
	return uc
}

// hasNewerBuild decides whether the remote release is a different build than
// the local one, per channel:
//   - stable/beta: the tag IS the version at build time (v1.2.3) → semver
//     comparison via version.IsNewer.
//   - nightly: the tag is the fixed "nightly" (redeployed each merge) while
//     the local version embeds date+sha, so tags can't be compared. Compare
//     the release's publish time against the local build time instead; when
//     the build time is unknown (dev), conservatively report an update so the
//     user can inspect the latest nightly version string and decide.
func hasNewerBuild(release *version.ReleaseInfo) bool {
	switch version.Channel {
	case "stable", "beta", "":
		return version.IsNewer(version.Version, release.Tag)
	default: // nightly
		if version.BuildTime == "unknown" {
			return true
		}
		local, err := time.Parse(time.RFC3339, version.BuildTime)
		if err != nil {
			return true
		}
		return release.PublishedAt.After(local)
	}
}
