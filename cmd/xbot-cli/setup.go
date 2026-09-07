package main

// setup.go — one-command post-install setup for xbot-cli.
//
//	$ xbot-cli setup
//
// Completes the installation the install script started: downloads the web UI
// dist and the built-in plugins for THIS binary's release (version-pinned,
// checksum-verified), installs them under $XBOT_HOME, and fixes the channel
// activation config (channels.<name>.enabled=true for shipped channel plugins
// whose manifest defaults them on — the "installed but never activated" trap).
//
// The install scripts (scripts/install.sh, install.ps1) download only the
// binary; they delegate everything else to this command so Linux/macOS/Windows
// share ONE setup implementation instead of bash + PowerShell forks.
//
// Idempotent: sidecar version stamps ($XBOT_HOME/web/.dist-version,
// $XBOT_HOME/plugins/.builtin-version) record the installing binary's version;
// a matching stamp + intact target skips the download. Upgrades (new binary
// version) refresh automatically.
//
// Flags:
//
//	--check               diagnose only, exit 1 when pieces are missing
//	--config-only         skip downloads; only fix channel activation config
//	--offline-web F       install web dist from a local tarball (air-gapped)
//	--offline-plugins F   install plugins from a local tarball (air-gapped)
//	--tag TAG             release tag to download from (default: derived
//	                      from the binary's channel/version)
//	--mirror HOST         GitHub CDN mirror (same as GH_MIRROR env,
//	                    e.g. ghfast.top)
//	--force               re-download even when the version stamp matches

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"xbot/config"
	"xbot/version"
)

// setupExitWarning is returned when setup completed but some release assets
// were unavailable (HTTP 404 — e.g. an old release predating the plugins
// tarballs). Callers treat it as non-fatal: install.sh warns and continues.
var errSetupIncomplete = errors.New("setup completed with warnings")

const (
	setupRepoPrimary  = "ai-pivot/xbot"
	setupRepoFallback = "CjiW/xbot"
)

type setupOptions struct {
	check          bool
	configOnly     bool
	force          bool
	offlineWeb     string
	offlinePlugins string
	tag            string
	mirror         string
}

// setupWebDistMarker / setupPluginsMarker are the files whose presence
// validates an installed component (after the swap, not just the stamp).
const (
	setupWebDistMarker    = "index.html"
	setupPluginManifestFN = "plugin.json"
)

// runSetup is the entry point for `xbot-cli setup`. It never calls os.Exit
// directly (except via the returned error → main dispatch exit code).
func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	fs.Usage = func() {
		fmt.Println("Usage: xbot-cli setup [flags]")
		fmt.Println("")
		fmt.Println("Completes the xbot installation: web UI dist + built-in plugins")
		fmt.Println("from THIS binary's GitHub release, then activates channel plugins")
		fmt.Println("(channels.<name>.enabled=true) in config.json.")
		fmt.Println("")
		fmt.Println("  --check               diagnose only; exit 1 if pieces are missing")
		fmt.Println("  --config-only         only fix channel activation config, no downloads")
		fmt.Println("  --offline-web F       install web dist from local tarball F")
		fmt.Println("  --offline-plugins F   install plugins from local tarball F")
		fmt.Println("  --tag TAG             release tag (default: derived from binary version)")
		fmt.Println("  --mirror HOST         GitHub CDN mirror (or GH_MIRROR env)")
		fmt.Println("  --force               re-download even when version stamp matches")
	}
	var o setupOptions
	fs.BoolVar(&o.check, "check", false, "diagnose only")
	fs.BoolVar(&o.configOnly, "config-only", false, "only fix channel activation config")
	fs.BoolVar(&o.force, "force", false, "force re-download")
	fs.StringVar(&o.offlineWeb, "offline-web", "", "local web dist tarball")
	fs.StringVar(&o.offlinePlugins, "offline-plugins", "", "local plugins tarball")
	fs.StringVar(&o.tag, "tag", "", "release tag")
	fs.StringVar(&o.mirror, "mirror", "", "GitHub CDN mirror host (e.g. ghfast.top)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if o.mirror == "" {
		o.mirror = os.Getenv("GH_MIRROR")
	}

	xbotHome := config.XbotHome()
	fmt.Printf("xbot-cli setup (home: %s)\n", xbotHome)

	// --check: diagnose and report; no downloads, no writes.
	if o.check {
		return checkSetup(&o)
	}

	// Component installation: web dist + plugins (skipped for --config-only).
	// Each component independently soft-fails on 404 (old releases predate
	// the plugins tarball); real failures (checksum mismatch, extraction)
	// abort. Offline tarballs bypass downloads entirely.
	var warnings []string

	if !o.configOnly {
		if o.offlineWeb != "" {
			if err := installWebDistFromFile(xbotHome, o.offlineWeb, version.Version); err != nil {
				return fmt.Errorf("install web dist from %s: %w", o.offlineWeb, err)
			}
		} else if tag, err := deriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err != nil {
			warnings = append(warnings, "web dist: "+err.Error())
		} else {
			ok, err := installComponentFromRelease(xbotHome, tag, "xbot-web-dist.tar.gz", "web", setupWebDistMarker, o)
			if err != nil {
				return err
			}
			if !ok {
				warnings = append(warnings, "web dist not found in release "+tag+" (the server will run in API-only mode)")
			}
		}

		if o.offlinePlugins != "" {
			if err := installPluginsFromFile(xbotHome, o.offlinePlugins, version.Version); err != nil {
				return fmt.Errorf("install plugins from %s: %w", o.offlinePlugins, err)
			}
		} else if tag, err := deriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err != nil {
			warnings = append(warnings, "plugins: "+err.Error())
		} else {
			artifact := fmt.Sprintf("xbot-plugins-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			ok, err := installComponentFromRelease(xbotHome, tag, artifact, "plugins", setupPluginManifestFN, o)
			if err != nil {
				return err
			}
			if !ok {
				warnings = append(warnings, "built-in plugins not found in release "+tag+" (xbot.genui / xbot.git-fancy unavailable)")
			}
		}
	}

	// Channel activation config fixup (always runs — also for --config-only and
	// after offline installs). set_if_missing semantics: a user's explicit
	// channels.<name>.enabled=false is never overwritten.
	if changed, err := fixChannelActivationConfig(xbotHome); err != nil {
		warnings = append(warnings, "channel activation config: "+err.Error())
	} else if changed {
		fmt.Println("[OK] Channel activation config updated (config.json)")
	}

	if len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Printf("[WARN] %s\n", w)
		}
		fmt.Println("[WARN] setup completed with warnings — re-run after upgrading, or use --offline-* flags")
		return errSetupIncomplete
	}
	fmt.Println("[OK] setup complete")
	return nil
}

// deriveReleaseTag maps the binary's compile-time version/channel to the
// GitHub release tag the artifacts live under. Nightly builds publish under
// the fixed "nightly" tag (overwritten each merge); stable/beta releases
// tag == version (release.yml prepare job). Explicit flag/env wins.
func deriveReleaseTag(flagTag, envTag, ver, channel string) (string, error) {
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

// componentInstalled reports whether a component's install target looks
// complete (marker file present). web → web/dist/index.html; plugins →
// builtin/ has at least one plugin dir with a plugin.json.
func componentInstalled(xbotHome, component string) bool {
	switch component {
	case "web":
		_, err := os.Stat(filepath.Join(componentDir(xbotHome, component), setupWebDistMarker))
		return err == nil
	case "plugins":
		entries, err := os.ReadDir(componentDir(xbotHome, component))
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(componentDir(xbotHome, component), e.Name(), setupPluginManifestFN)); err == nil {
					return true
				}
			}
		}
		return false
	}
	return false
}

// installComponentFromRelease downloads one release artifact (web dist or
// plugins tarball), verifies its checksum, and installs it via the component
// installer. Returns ok=false when the artifact is missing from the release
// (HTTP 404 on both repos) — a soft failure for old releases.
func installComponentFromRelease(xbotHome, tag, artifact, component, marker string, o setupOptions) (bool, error) {
	_ = marker // markers checked via componentInstalled (per-component layout)
	// Stamp check: skip download when this binary version already installed
	// this component and the marker check still passes.
	stampPath := componentStampPath(xbotHome, component)
	if !o.force {
		if stamp, err := os.ReadFile(stampPath); err == nil && strings.TrimSpace(string(stamp)) == version.Version {
			if componentInstalled(xbotHome, component) {
				fmt.Printf("[OK] %s: already at version %s (use --force to reinstall)\n", component, version.Version)
				return true, nil
			}
		}
	}

	fmt.Printf("Downloading %s from release %s...\n", artifact, tag)
	data, err := fetchReleaseArtifact(tag, artifact, o.mirror)
	if err != nil {
		if errors.Is(err, errReleaseAssetNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("download %s: %w", artifact, err)
	}
	if err := verifyChecksum(tag, artifact, data, o.mirror); err != nil {
		return false, fmt.Errorf("checksum %s: %w", artifact, err)
	}

	switch component {
	case "web":
		err = installWebDistFromBytes(xbotHome, data, version.Version)
	case "plugins":
		err = installPluginsFromBytes(xbotHome, data, version.Version)
	default:
		err = fmt.Errorf("unknown component %q", component)
	}
	if err != nil {
		return false, err
	}
	fmt.Printf("[OK] %s installed (version %s)\n", component, version.Version)
	return true, nil
}

// componentDir returns the install target for a component:
// web → $XBOT_HOME/web/dist, plugins → $XBOT_HOME/plugins/builtin.
func componentDir(xbotHome, component string) string {
	switch component {
	case "web":
		return filepath.Join(xbotHome, "web", "dist")
	case "plugins":
		return filepath.Join(xbotHome, "plugins", "builtin")
	}
	return filepath.Join(xbotHome, component)
}

// componentStampPath returns the sidecar version stamp path for a component.
// Stamps live OUTSIDE the installed dirs so they never interfere with static
// serving (web) or plugin discovery (builtin scans dirs with plugin.json only,
// but keeping stamps out is cleaner).
func componentStampPath(xbotHome, component string) string {
	switch component {
	case "web":
		return filepath.Join(xbotHome, "web", ".dist-version")
	case "plugins":
		return filepath.Join(xbotHome, "plugins", ".builtin-version")
	}
	return filepath.Join(xbotHome, "."+component+"-version")
}

// installWebDistFromBytes / installPluginsFromBytes share the swap logic:
// extract into a fresh temp dir, swap into place, write the version stamp.
func installWebDistFromBytes(xbotHome string, data []byte, ver string) error {
	return installFromTarGz(filepath.Join(xbotHome, "web", "dist"), data, componentStampPath(xbotHome, "web"), ver)
}

func installWebDistFromFile(xbotHome, path, ver string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return installWebDistFromBytes(xbotHome, data, ver)
}

func installPluginsFromBytes(xbotHome string, data []byte, ver string) error {
	return installFromTarGz(filepath.Join(xbotHome, "plugins", "builtin"), data, componentStampPath(xbotHome, "plugins"), ver)
}

func installPluginsFromFile(xbotHome, path, ver string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return installPluginsFromBytes(xbotHome, data, ver)
}

// installFromTarGz extracts a tar.gz into dest (via staging + atomic swap)
// and writes the version stamp. Old directory is moved aside and removed
// after the swap — a crash mid-way leaves at most the .old directory, never
// a half-extracted target.
func installFromTarGz(dest string, data []byte, stampPath, ver string) error {
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
		return err
	}
	_ = os.RemoveAll(old)
	if err := os.MkdirAll(filepath.Dir(stampPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(stampPath, []byte(ver), 0o644)
}

// extractTarGz securely extracts a gzipped tarball into dest:
// no absolute paths, no ".." traversal, no symlinks/hardlinks, modes
// preserved (exec bits on plugin binaries).
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

// ---------------------------------------------------------------------------
// Release downloads
// ---------------------------------------------------------------------------

var errReleaseAssetNotFound = errors.New("release asset not found")

// fetchReleaseArtifact downloads {artifact} from the release {tag}, trying
// the primary repo then the fallback repo, applying the CDN mirror if set.
// HTTP 404 on both → errReleaseAssetNotFound (soft failure for old releases).
func fetchReleaseArtifact(tag, artifact, mirror string) ([]byte, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	var lastErr error
	for _, repo := range []string{setupRepoPrimary, setupRepoFallback} {
		raw := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, artifact)
		data, err := httpGetRelease(client, ghMirrorURL(mirror, raw))
		if err == nil {
			return data, nil
		}
		if errors.Is(err, errReleaseAssetNotFound) {
			lastErr = err
			continue // try fallback repo
		}
		return nil, err
	}
	return nil, lastErr // 404 on both repos
}

// httpGetRelease fetches a release URL; 404 maps to errReleaseAssetNotFound.
func httpGetRelease(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xbot-cli-setup/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	case http.StatusNotFound:
		return nil, errReleaseAssetNotFound
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

// verifyChecksum downloads checksums.txt from the same release and compares
// the artifact's sha256. Missing checksums.txt (404 on both repos) is a
// warning (soft-fail, mirrors install.sh); a mismatch is a hard error.
func verifyChecksum(tag, artifact string, data []byte, mirror string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	var sums []byte
	for _, repo := range []string{setupRepoPrimary, setupRepoFallback} {
		raw := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", repo, tag)
		b, err := httpGetRelease(client, ghMirrorURL(mirror, raw))
		if err == nil {
			sums = b
			break
		}
		if !errors.Is(err, errReleaseAssetNotFound) {
			return fmt.Errorf("fetch checksums.txt: %w", err)
		}
	}
	if sums == nil {
		fmt.Println("[WARN] checksums.txt not found in release, skipping verification")
		return nil
	}
	expected, ok := parseChecksums(string(sums))[artifact]
	if !ok {
		fmt.Printf("[WARN] %s not listed in checksums.txt, skipping verification\n", artifact)
		return nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("sha256 mismatch: expected %s, got %s", expected, actual)
	}
	fmt.Println("[OK] checksum verified:", artifact)
	return nil
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

// ---------------------------------------------------------------------------
// Channel activation config fixup
// ---------------------------------------------------------------------------

// setupPluginManifest is the minimal plugin.json shape needed to detect
// channel providers and their enabled-by-default config.
type setupPluginManifest struct {
	ID          string `json:"id"`
	Contributes struct {
		ChannelProvider *struct {
			Name         string `json:"name"`
			ConfigSchema []struct {
				Key          string `json:"key"`
				DefaultValue string `json:"default_value"`
			} `json:"config_schema"`
		} `json:"channelProvider"`
	} `json:"contributes"`
}

// scanChannelActivation walks the plugin dirs (user dir first, then builtin)
// and returns channel names whose manifests default to enabled=true.
// Manifest-driven: any shipped channel plugin (genui today, more tomorrow)
// activates without hardcoding names here.
func scanChannelActivation(xbotHome string) map[string]string {
	dirs := []string{
		filepath.Join(xbotHome, "plugins"),            // user-installed (highest precedence)
		filepath.Join(xbotHome, "plugins", "builtin"), // release-shipped
	}
	activated := map[string]string{} // channel name → plugin id
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			manifestPath := filepath.Join(dir, e.Name(), setupPluginManifestFN)
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var m setupPluginManifest
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			cp := m.Contributes.ChannelProvider
			if cp == nil || cp.Name == "" {
				continue
			}
			for _, sc := range cp.ConfigSchema {
				if sc.Key == "enabled" && isEnabledDefault(sc.DefaultValue) {
					activated[cp.Name] = m.ID
					break
				}
			}
		}
	}
	return activated
}

func isEnabledDefault(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// fixChannelActivationConfig writes channels.<name>.enabled="true" for every
// shipped channel plugin that defaults to enabled — set_if_missing semantics:
// an existing value (including the user's deliberate "false") is preserved.
// Uses config.LoadFromFile + SaveToFile (deep merge preserves unknown fields).
func fixChannelActivationConfig(xbotHome string) (bool, error) {
	activated := scanChannelActivation(xbotHome)
	if len(activated) == 0 {
		return false, nil
	}
	path := config.ConfigFilePath()
	cfg := config.LoadFromFile(path)
	if cfg == nil {
		if _, err := os.Stat(path); err == nil {
			return false, fmt.Errorf("config.json exists but cannot be parsed: %s", path)
		}
		cfg = &config.Config{}
	}
	if cfg.Channels == nil {
		cfg.Channels = map[string]map[string]string{}
	}
	changed := false
	for name := range activated {
		if cfg.Channels[name] == nil {
			cfg.Channels[name] = map[string]string{}
		}
		if _, exists := cfg.Channels[name]["enabled"]; !exists {
			cfg.Channels[name]["enabled"] = "true"
			changed = true
			fmt.Printf("[OK] activated channel plugin %q (channels.%s.enabled=true)\n", activated[name], name)
		}
	}
	if !changed {
		return false, nil
	}
	if err := config.SaveToFile(path, cfg); err != nil {
		return false, fmt.Errorf("save config: %w", err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// --check: diagnose the installation
// ---------------------------------------------------------------------------

func checkSetup(o *setupOptions) error {
	xbotHome := config.XbotHome()
	ok := true

	fmt.Printf("Binary:  %s (channel %q)\n", version.Version, version.Channel)
	if tag, err := deriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err == nil {
		fmt.Printf("Release: %s\n", tag)
	} else {
		fmt.Printf("Release: n/a (%v)\n", err)
	}

	// 1. Web dist
	webDir := componentDir(xbotHome, "web")
	if _, err := os.Stat(filepath.Join(webDir, setupWebDistMarker)); err == nil {
		stamp, _ := os.ReadFile(componentStampPath(xbotHome, "web"))
		fmt.Printf("Web UI:  OK %s (installed by %s)\n", webDir, strings.TrimSpace(string(stamp)))
	} else {
		fmt.Printf("Web UI:  MISSING %s — run: xbot-cli setup\n", webDir)
		ok = false
	}

	// 2. Built-in plugins
	pluginsDir := componentDir(xbotHome, "plugins")
	entries, _ := os.ReadDir(pluginsDir)
	var pluginIDs []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(pluginsDir, e.Name(), setupPluginManifestFN)); err == nil {
				pluginIDs = append(pluginIDs, e.Name())
			}
		}
	}
	if len(pluginIDs) > 0 {
		stamp, _ := os.ReadFile(componentStampPath(xbotHome, "plugins"))
		fmt.Printf("Plugins: OK %s (%s) (installed by %s)\n", pluginsDir, strings.Join(pluginIDs, ", "), strings.TrimSpace(string(stamp)))
	} else {
		fmt.Printf("Plugins: MISSING %s — run: xbot-cli setup\n", pluginsDir)
		ok = false
	}

	// 3. Channel activation
	activated := scanChannelActivation(xbotHome)
	cfg := config.LoadFromFile(config.ConfigFilePath())
	for name, pluginID := range activated {
		state := "MISSING"
		if cfg != nil && cfg.Channels != nil {
			if v, exists := cfg.Channels[name]["enabled"]; exists {
				state = "enabled=" + v
				if v != "true" {
					ok = false
				}
			}
		}
		fmt.Printf("Channel: %-10s plugin=%s %s\n", name, pluginID, state)
		if state == "MISSING" {
			ok = false
		}
	}

	if !ok {
		fmt.Println("setup --check: incomplete (see above)")
		return errors.New("setup incomplete")
	}
	fmt.Println("setup --check: all good")
	return nil
}
