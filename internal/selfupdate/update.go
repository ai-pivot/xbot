package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"xbot/version"
)

// ApplyUpdate downloads and installs a release update in-place:
//
//  1. binary asset (xbot-cli-<os>-<arch>) — verified, then atomically swapped
//     over the running executable (safe on Unix: the running process keeps
//     the old inode; the next start picks up the new binary);
//  2. web dist tarball → $XBOT_HOME/web/dist;
//  3. built-in plugins tarball → $XBOT_HOME/plugins/builtin.
//
// It does NOT restart the process — the caller (web UI) prompts the user to
// press the separate restart button, which routes through Restart().
//
// tag is the GitHub release tag to download from (from CheckForUpdate). For
// dev builds the caller must pass an explicit tag (DeriveReleaseTag refuses
// to guess for dev versions).
type ApplyResult struct {
	NewVersion string   `json:"newVersion"` // version string stamped into components
	Tag        string   `json:"tag"`        // release tag downloaded from
	Components []string `json:"components"` // updated components: "binary", "web", "plugins"
	Warnings   []string `json:"warnings"`   // non-fatal issues (e.g. checksum skipped)
}

// ApplyUpdate performs the full in-place update. xbotHome is the data dir
// (config.XbotHome()); mirror is the optional GitHub CDN mirror (GH_MIRROR).
func ApplyUpdate(ctx context.Context, tag, xbotHome, mirror string) (*ApplyResult, error) {
	if tag == "" {
		return nil, fmt.Errorf("update target tag is required (run a check first)")
	}
	res := &ApplyResult{Tag: tag, NewVersion: tag}

	// 1. Binary — download, verify, atomic swap over the running executable.
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate running executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return nil, fmt.Errorf("resolve executable path: %w", err)
	}
	binAsset := BinaryAssetName()
	binData, err := FetchArtifact(tag, binAsset, mirror)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", binAsset, err)
	}
	if warn, err := VerifyChecksum(tag, binAsset, binData, mirror); err != nil {
		return nil, fmt.Errorf("verify %s: %w", binAsset, err)
	} else if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	if err := replaceBinary(exe, binData); err != nil {
		return nil, fmt.Errorf("replace binary %s: %w", exe, err)
	}
	res.Components = append(res.Components, "binary")

	// 2. Web dist + 3. built-in plugins — same artifacts setup downloads,
	// stamped with the NEW version so the next `setup` run sees them current.
	// Each soft-fails on 404 (old releases predate the plugins tarball).
	for _, comp := range []struct{ artifact, name string }{
		{"xbot-web-dist.tar.gz", "web"},
		{"xbot-plugins-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz", "plugins"},
	} {
		data, err := FetchArtifact(tag, comp.artifact, mirror)
		if err != nil {
			if err == ErrAssetNotFound {
				res.Warnings = append(res.Warnings, comp.name+" not found in release "+tag+" (skipped)")
				continue
			}
			return nil, fmt.Errorf("download %s: %w", comp.artifact, err)
		}
		if warn, err := VerifyChecksum(tag, comp.artifact, data, mirror); err != nil {
			return nil, fmt.Errorf("verify %s: %w", comp.artifact, err)
		} else if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
		switch comp.name {
		case "web":
			err = InstallWebDist(xbotHome, data, res.NewVersion)
		case "plugins":
			err = InstallPlugins(xbotHome, data, res.NewVersion)
		}
		if err != nil {
			return nil, fmt.Errorf("install %s: %w", comp.name, err)
		}
		res.Components = append(res.Components, comp.name)
	}

	return res, nil
}

// replaceBinary atomically swaps the running executable. On Unix, renaming
// over a running binary is safe (the process keeps the old inode). On
// Windows the running file is locked, so the old binary is renamed aside
// first and removed best-effort after the swap.
func replaceBinary(exe string, data []byte) error {
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("move old binary aside: %w", err)
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe) // rollback
			return fmt.Errorf("swap in new binary: %w", err)
		}
		_ = os.Remove(old) // best effort (may be locked until exit)
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("swap in new binary: %w", err)
	}
	return nil
}

// DeriveUpdateTag resolves the release tag to update TO from the running
// binary's version/channel (same rules as setup). Dev builds without an
// explicit tag return an error — the web UI passes the tag from CheckForUpdate
// instead, so this is only a fallback for direct callers.
func DeriveUpdateTag() (string, error) {
	return DeriveReleaseTag("", os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel)
}

// applyTimeout bounds the whole download+install flow (large binaries on slow
// links); individual HTTP fetches already have their own 10-minute cap.
const applyTimeout = 15 * time.Minute

// ApplyUpdateWithTimeout is ApplyUpdate with a bounded context for RPC callers.
func ApplyUpdateWithTimeout(tag, xbotHome, mirror string) (*ApplyResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	return ApplyUpdate(ctx, tag, xbotHome, mirror)
}
