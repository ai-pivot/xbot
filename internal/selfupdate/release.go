// Package selfupdate provides release-artifact primitives shared by
// `xbot-cli setup` (CLI install) and the web update RPC (serverapp).
//
// Everything here was extracted verbatim from cmd/xbot-cli/setup.go so the
// two callers share ONE download/verify/install implementation. The CLI keeps
// its printing wrappers; this package is library-only (returns errors and
// warnings, never prints).
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrAssetNotFound is returned when a release artifact is missing (HTTP 404 on
// both repos) — a soft failure for old releases that predate the asset.
var ErrAssetNotFound = errors.New("release asset not found")

// Repos tried in order: primary first, fallback for the migration window.
const (
	RepoPrimary  = "ai-pivot/xbot"
	RepoFallback = "CjiW/xbot"
)

// Component marker files: presence validates an installed component after the
// swap (not just the version stamp).
const (
	WebDistMarker    = "index.html"
	PluginManifestFN = "plugin.json"
)

// FetchArtifact downloads {artifact} from the release {tag}, trying the
// primary repo then the fallback repo, applying the CDN mirror if set.
// HTTP 404 on both → ErrAssetNotFound (soft failure for old releases).
func FetchArtifact(tag, artifact, mirror string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	var lastErr error
	for _, repo := range []string{RepoPrimary, RepoFallback} {
		raw := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, artifact)
		data, err := httpGetRelease(client, ghMirrorURL(mirror, raw))
		if err == nil {
			return data, nil
		}
		if errors.Is(err, ErrAssetNotFound) {
			lastErr = err
			continue // try fallback repo
		}
		return nil, err
	}
	return nil, lastErr // 404 on both repos
}

// httpGetRelease fetches a release URL; 404 maps to ErrAssetNotFound.
func httpGetRelease(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xbot-selfupdate")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	case http.StatusNotFound:
		return nil, ErrAssetNotFound
	default:
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
}

// ghMirrorURL proxies a GitHub URL through a CDN mirror (install.sh protocol:
// https://{mirror}/https://github.com/...). Mirror never applies to API URLs.
func ghMirrorURL(mirror, rawURL string) string {
	if mirror != "" && !strings.Contains(rawURL, "api.github.com") {
		return "https://" + mirror + "/" + rawURL
	}
	return rawURL
}

// VerifyChecksum downloads checksums.txt from the same release and compares
// the artifact's sha256. Missing checksums.txt (404 on both repos) is a
// warning (soft-fail, mirrors install.sh); a mismatch is a hard error.
// The returned warning is non-empty when verification was skipped.
func VerifyChecksum(tag, artifact string, data []byte, mirror string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var sums []byte
	for _, repo := range []string{RepoPrimary, RepoFallback} {
		raw := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", repo, tag)
		b, err := httpGetRelease(client, ghMirrorURL(mirror, raw))
		if err == nil {
			sums = b
			break
		}
		if !errors.Is(err, ErrAssetNotFound) {
			return "", fmt.Errorf("fetch checksums.txt: %w", err)
		}
	}
	if sums == nil {
		return "checksums.txt not found in release, skipped verification", nil
	}
	expected, ok := parseChecksums(string(sums))[artifact]
	if !ok {
		return artifact + " not listed in checksums.txt, skipped verification", nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return "", fmt.Errorf("sha256 mismatch: expected %s, got %s", expected, actual)
	}
	return "", nil
}

// parseChecksums parses `sha256sum` output lines ("<hash>  <filename>").
func parseChecksums(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			out[fields[1]] = fields[0]
		}
	}
	return out
}

// DeriveReleaseTag maps the binary's compile-time version/channel to the
// GitHub release tag the artifacts live under. Nightly builds publish under
// the fixed "nightly" tag (overwritten each merge); stable/beta releases
// tag == version (release.yml prepare job). Explicit flag/env wins.
func DeriveReleaseTag(flagTag, envTag, ver, channel string) (string, error) {
	if flagTag != "" {
		return flagTag, nil
	}
	if envTag != "" {
		return envTag, nil
	}
	switch channel {
	case "nightly":
		return "nightly", nil
	case "stable", "beta":
		if strings.HasPrefix(ver, "v") {
			return ver, nil
		}
	}
	// Fallbacks for ldflags variants (channel empty but tagged version, or
	// nightly-versioned dev builds).
	if strings.HasPrefix(ver, "nightly") {
		return "nightly", nil
	}
	if strings.HasPrefix(ver, "v") && len(ver) > 1 {
		return ver, nil
	}
	return "", fmt.Errorf("dev build (version %q, channel %q) cannot download release artifacts — use --tag, --offline-web/--offline-plugins, or install via scripts/install.sh", ver, channel)
}

// ComponentDir returns the install target for a component:
// web → $XBOT_HOME/web/dist, plugins → $XBOT_HOME/plugins/builtin.
func ComponentDir(xbotHome, component string) string {
	switch component {
	case "web":
		return filepath.Join(xbotHome, "web", "dist")
	case "plugins":
		return filepath.Join(xbotHome, "plugins", "builtin")
	}
	return filepath.Join(xbotHome, component)
}

// ComponentStampPath returns the sidecar version stamp path for a component.
// Stamps live OUTSIDE the installed dirs so they never interfere with static
// serving (web) or plugin discovery (builtin scans dirs with plugin.json only,
// but keeping stamps out is cleaner).
func ComponentStampPath(xbotHome, component string) string {
	switch component {
	case "web":
		return filepath.Join(xbotHome, "web", ".dist-version")
	case "plugins":
		return filepath.Join(xbotHome, "plugins", ".builtin-version")
	}
	return filepath.Join(xbotHome, "."+component+"-version")
}

// ComponentInstalled reports whether a component's install target looks
// complete (marker file present). web → web/dist/index.html; plugins →
// builtin/ has at least one plugin dir with a plugin.json.
func ComponentInstalled(xbotHome, component string) bool {
	switch component {
	case "web":
		_, err := os.Stat(filepath.Join(ComponentDir(xbotHome, component), WebDistMarker))
		return err == nil
	case "plugins":
		entries, err := os.ReadDir(ComponentDir(xbotHome, component))
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(ComponentDir(xbotHome, component), e.Name(), PluginManifestFN)); err == nil {
					return true
				}
			}
		}
		return false
	}
	return false
}

// InstallWebDist installs a web dist tarball (already downloaded bytes) into
// $XBOT_HOME/web/dist with the given version stamp.
func InstallWebDist(xbotHome string, data []byte, ver string) error {
	return InstallTarGz(filepath.Join(xbotHome, "web", "dist"), data, ComponentStampPath(xbotHome, "web"), ver)
}

// InstallPlugins installs a built-in plugins tarball into
// $XBOT_HOME/plugins/builtin with the given version stamp.
func InstallPlugins(xbotHome string, data []byte, ver string) error {
	return InstallTarGz(filepath.Join(xbotHome, "plugins", "builtin"), data, ComponentStampPath(xbotHome, "plugins"), ver)
}

// InstallTarGz extracts a tar.gz into dest (via staging + atomic swap)
// and writes the version stamp. Old directory is moved aside and removed
// after the swap — a crash mid-way leaves at most the .old directory, never
// a half-extracted target.
func InstallTarGz(dest string, data []byte, stampPath, ver string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".new-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractTarGz(data, staging); err != nil {
		return fmt.Errorf("extract tarball: %w", err)
	}
	// Sanity: refuse an empty extraction (corrupt tarball).
	if entries, err := os.ReadDir(staging); err != nil || len(entries) == 0 {
		return fmt.Errorf("tarball extracted to nothing (corrupt?)")
	}
	// Swap: dest → .old, staging → dest, rm .old.
	old := dest + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return fmt.Errorf("move old %s away: %w", dest, err)
		}
	}
	if err := os.Rename(staging, dest); err != nil {
		// Try to restore the old dir on failure.
		if _, statErr := os.Stat(old); statErr == nil {
			_ = os.Rename(old, dest)
		}
		return fmt.Errorf("swap %s into place: %w", dest, err)
	}
	_ = os.RemoveAll(old)
	// Stamp AFTER a successful swap so a crashed install never records a
	// version it didn't reach.
	return os.WriteFile(stampPath, []byte(ver), 0o644)
}

// extractTarGz extracts tar.gz bytes into dest, refusing entries that escape
// the target dir (absolute paths or .. traversal).
func extractTarGz(data []byte, dest string) error {
	gzr, err := gzip.NewReader(newByteReader(data))
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(hdr.Name)
		if name == "." || name == "" {
			continue
		}
		// Absolute-path rejection must use the RAW tar header: on Windows
		// filepath.Clean("/abs/x") → "\\abs\\x" and filepath.IsAbs returns false
		// (no drive letter), so an absolute tar entry would slip through the
		// platform check. Tar paths are Unix-style — a leading "/" (or "\") in
		// hdr.Name is absolute regardless of host OS.
		if strings.HasPrefix(hdr.Name, "/") || strings.HasPrefix(hdr.Name, "\\") ||
			filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
			return fmt.Errorf("tarball entry escapes target dir: %q", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := hdr.FileInfo().Mode().Perm()
			if err := writeFileMode(target, tr, mode); err != nil {
				return err
			}
		default:
			// Skip symlinks, hardlinks, devices — release tarballs never
			// contain them; refusing keeps extraction side-effect free.
			continue
		}
	}
}

type byteReader struct {
	b []byte
	i int
}

func newByteReader(b []byte) *byteReader { return &byteReader{b: b} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}

func writeFileMode(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// BinaryAssetName returns the release asset name for the current platform
// (e.g. "xbot-cli-linux-amd64", "xbot-cli-windows-amd64.exe").
func BinaryAssetName() string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	return fmt.Sprintf("xbot-cli-%s-%s%s", runtime.GOOS, runtime.GOARCH, ext)
}
