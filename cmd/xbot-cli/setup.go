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
//	--check               diagnose only; exit 1 only when the release-pinned
//	                      pieces are missing (local plugin health is reported,
//	                      not enforced — see scanLocalPluginHealth)
//	--config-only         skip downloads; only fix channel activation config
//	--offline-web F       install web dist from a local tarball (air-gapped)
//	--offline-plugins F   install plugins from a local tarball (air-gapped)
//	--tag TAG             release tag to download from (default: derived
//	                      from the binary's channel/version)
//	--mirror HOST         GitHub CDN mirror (same as GH_MIRROR env,
//	                    e.g. ghfast.top)
//	--force               re-download even when the version stamp matches

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"xbot/config"
	"xbot/internal/selfupdate"
	"xbot/plugin"
	"xbot/version"
)

// setupExitWarning is returned when setup completed but some release assets
// were unavailable (HTTP 404 — e.g. an old release predating the plugins
// tarballs). Callers treat it as non-fatal: install.sh warns and continues.
var errSetupIncomplete = errors.New("setup completed with warnings")

type setupOptions struct {
	check          bool
	configOnly     bool
	force          bool
	offlineWeb     string
	offlinePlugins string
	tag            string
	mirror         string
}

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
		fmt.Println("  --check               diagnose only; exit 1 only if release-pinned pieces are missing")
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
		} else if tag, err := selfupdate.DeriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err != nil {
			warnings = append(warnings, "web dist: "+err.Error())
		} else {
			ok, err := installComponentFromRelease(xbotHome, tag, "xbot-web-dist.tar.gz", "web", selfupdate.WebDistMarker, o)
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
		} else if tag, err := selfupdate.DeriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err != nil {
			warnings = append(warnings, "plugins: "+err.Error())
		} else {
			artifact := fmt.Sprintf("xbot-plugins-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			ok, err := installComponentFromRelease(xbotHome, tag, artifact, "plugins", selfupdate.PluginManifestFN, o)
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

// installComponentFromRelease downloads one release artifact (web dist or
// plugins tarball), verifies its checksum, and installs it via the component
// installer. Returns ok=false when the artifact is missing from the release
// (HTTP 404 on both repos) — a soft failure for old releases.
// Download/verify/install primitives live in internal/selfupdate (shared with
// the web update RPC); this wrapper keeps the CLI printing + stamp semantics.
func installComponentFromRelease(xbotHome, tag, artifact, component, marker string, o setupOptions) (bool, error) {
	_ = marker // markers checked via selfupdate.ComponentInstalled (per-component layout)
	// Stamp check: skip download when this binary version already installed
	// this component and the marker check still passes.
	stampPath := selfupdate.ComponentStampPath(xbotHome, component)
	if !o.force {
		if stamp, err := os.ReadFile(stampPath); err == nil && strings.TrimSpace(string(stamp)) == version.Version {
			if selfupdate.ComponentInstalled(xbotHome, component) {
				fmt.Printf("[OK] %s: already at version %s (use --force to reinstall)\n", component, version.Version)
				return true, nil
			}
		}
	}

	fmt.Printf("Downloading %s from release %s...\n", artifact, tag)
	data, err := selfupdate.FetchArtifact(tag, artifact, o.mirror)
	if err != nil {
		if errors.Is(err, selfupdate.ErrAssetNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("download %s: %w", artifact, err)
	}
	if warn, err := selfupdate.VerifyChecksum(tag, artifact, data, o.mirror); err != nil {
		return false, fmt.Errorf("checksum %s: %w", artifact, err)
	} else if warn != "" {
		fmt.Printf("[WARN] %s\n", warn)
	}

	switch component {
	case "web":
		err = selfupdate.InstallWebDist(xbotHome, data, version.Version)
	case "plugins":
		err = selfupdate.InstallPlugins(xbotHome, data, version.Version)
	default:
		err = fmt.Errorf("unknown component %q", component)
	}
	if err != nil {
		return false, err
	}
	fmt.Printf("[OK] %s installed (version %s)\n", component, version.Version)
	return true, nil
}

// installWebDistFromFile / installPluginsFromFile install from a local
// (air-gapped) tarball; they delegate to the shared selfupdate installers.
func installWebDistFromFile(xbotHome, path, ver string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return selfupdate.InstallWebDist(xbotHome, data, ver)
}

func installPluginsFromFile(xbotHome, path, ver string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return selfupdate.InstallPlugins(xbotHome, data, ver)
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

// scanChannelActivation walks the BUILTIN plugin dir (release-shipped via
// `xbot-cli setup` / install.sh) and returns channel names whose manifests
// default to enabled=true.
// Scope: plugins/builtin ONLY. The user dir (~/.xbot/plugins) is entirely
// user-managed (make plugins-install / manual installs) — setup never
// activates, deactivates, or reports plugins the user installed themselves.
// Manifest-driven: any builtin channel plugin (genui today, more tomorrow)
// activates without hardcoding names here.
func scanChannelActivation(xbotHome string) map[string]string {
	dirs := []string{
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
			manifestPath := filepath.Join(dir, e.Name(), selfupdate.PluginManifestFN)
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
// BUILTIN channel plugin (release-shipped via setup/install.sh) that defaults
// to enabled — set_if_missing semantics: an existing value (including the
// user's deliberate "false") is preserved. Plugins in the USER dir
// (~/.xbot/plugins) are entirely user-managed: setup never touches their
// activation (they may be examples, experiments, or deliberately disabled).
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
	if tag, err := selfupdate.DeriveReleaseTag(o.tag, os.Getenv("XBOT_RELEASE_TAG"), version.Version, version.Channel); err == nil {
		fmt.Printf("Release: %s\n", tag)
	} else {
		fmt.Printf("Release: n/a (%v)\n", err)
	}

	// 1. Web dist
	webDir := selfupdate.ComponentDir(xbotHome, "web")
	if _, err := os.Stat(filepath.Join(webDir, selfupdate.WebDistMarker)); err == nil {
		stamp, _ := os.ReadFile(selfupdate.ComponentStampPath(xbotHome, "web"))
		by := strings.TrimSpace(string(stamp))
		if by == "" {
			by = "unknown (installed outside setup — no version stamp)"
		}
		fmt.Printf("Web UI:  OK %s (installed by %s)\n", webDir, by)
	} else {
		fmt.Printf("Web UI:  MISSING %s — run: xbot-cli setup\n", webDir)
		ok = false
	}

	// 2. Plugins — TWO layers with DIFFERENT semantics:
	//    - builtin/ (release-shipped via setup/install.sh): completeness basis
	//      for this check — missing = actionable by running setup.
	//    - user dir (~/.xbot/plugins): entirely user-managed (make
	//      plugins-install / manual installs). NEVER judged here — listed as
	//      information only, so dev machines see their own installs instead of
	//      a misleading "MISSING" (git-fancy etc. live there on dev machines).
	listPluginIDs := func(dir string) []string {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var ids []string
		for _, e := range entries {
			if e.IsDir() {
				if _, err := os.Stat(filepath.Join(dir, e.Name(), selfupdate.PluginManifestFN)); err == nil {
					ids = append(ids, e.Name())
				}
			}
		}
		return ids
	}
	builtinIDs := listPluginIDs(selfupdate.ComponentDir(xbotHome, "plugins")) // .../plugins/builtin
	userIDs := listPluginIDs(filepath.Join(xbotHome, "plugins"))              // .../plugins (user dir)
	if len(builtinIDs) > 0 {
		stamp, _ := os.ReadFile(selfupdate.ComponentStampPath(xbotHome, "plugins"))
		by := strings.TrimSpace(string(stamp))
		if by == "" {
			by = "unknown"
		}
		fmt.Printf("Plugins: OK (builtin) %s (%s) (installed by %s)\n", selfupdate.ComponentDir(xbotHome, "plugins"), strings.Join(builtinIDs, ", "), by)
	} else {
		fmt.Printf("Plugins: MISSING (builtin) %s — run: xbot-cli setup\n", selfupdate.ComponentDir(xbotHome, "plugins"))
		ok = false
	}
	if len(userIDs) > 0 {
		fmt.Printf("         user-dir %s: %s — user-managed (dev/manual install, not judged by setup)\n",
			filepath.Join(xbotHome, "plugins"), strings.Join(userIDs, ", "))
	}

	// 3. Channel activation — BUILTIN plugins only (setup manages what it
	//    shipped). Channel plugins in the user dir are the user's own
	//    activation decision (config.json), never reported here.
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

	// 4. Local plugin health scan — report-only, never touches the exit code.
	//    Checks EVERY plugin under both discovery dirs (user + builtin):
	//    manifest readable/valid, and for plugins declaring web.entry the web
	//    artifact on disk (runtime serves a missing artifact as a silent 404 —
	//    exactly how xbot.iteration-stats was lost without anyone noticing).
	//    Disabled plugins (config plugins.disabled_plugins) are listed
	//    separately.
	//
	//    Exit-code rationale: the sections above keep their existing semantics
	//    (exit 1 only when the RELEASE-PINNED pieces are missing). The local
	//    dirs are user-managed — a plugin the user installed once and later
	//    deleted must NOT turn into "installation failed". Issues found here
	//    are printed as diagnostics instead.
	scanLocalPluginHealth(xbotHome, cfg)

	if !ok {
		fmt.Println("setup --check: incomplete (see above)")
		return errors.New("setup incomplete")
	}
	fmt.Println("setup --check: all good")
	return nil
}

// scanLocalPluginHealth walks every plugin directory under $XBOT_HOME/plugins
// (user layer) and $XBOT_HOME/plugins/builtin (release layer) and prints, one
// per line, the two failure modes that are otherwise invisible:
//
//   - missing/invalid: manifest unreadable or failing validation; a declared
//     web.entry whose artifact file is absent from <dir>/web/
//   - disabled: the plugin ID appears in config plugins.disabled_plugins
//
// Report-only by design (see the call-site comment): it never changes the
// --check exit code.
func scanLocalPluginHealth(xbotHome string, cfg *config.Config) {
	missing, disabledLines := pluginHealthFindings(xbotHome, cfg)
	if len(missing) == 0 && len(disabledLines) == 0 {
		fmt.Println("Plugin health: OK (local manifests readable; declared web entries + plugin binaries present)")
		return
	}
	if len(missing) > 0 {
		fmt.Println("Plugin health: issues (report-only — does not affect exit code):")
		for _, line := range missing {
			fmt.Printf("  %s\n", line)
		}
	}
	if len(disabledLines) > 0 {
		fmt.Println("Plugin health: disabled (config plugins.disabled_plugins):")
		for _, line := range disabledLines {
			fmt.Printf("  %s\n", line)
		}
	}
}

// pluginHealthFindings 扫描两层插件目录，返回（问题行, 被禁用行）。
// 与打印解耦，便于单测（打印函数只负责呈现）。
//
// 判定三类"装了但从未工作"的形态：
//   - manifest 读不出/校验失败
//   - 声明了 web.entry 但 <dir>/web/<entry> 不在（web 层只会 404）
//   - stdio/grpc 插件的入口二进制不在（或 unix 上丢了可执行位）——
//     插件进程起不来，运行期没有任何地方会报出来（xbot.iteration-stats 同族形态）
func pluginHealthFindings(xbotHome string, cfg *config.Config) (missing, disabledLines []string) {
	type layer struct{ label, dir string }
	layers := []layer{
		{"user", filepath.Join(xbotHome, "plugins")},
		{"builtin", filepath.Join(xbotHome, "plugins", "builtin")},
	}
	disabled := map[string]bool{}
	if cfg != nil {
		for _, id := range cfg.Plugins.DisabledPlugins {
			disabled[id] = true
		}
	}

	for _, l := range layers {
		entries, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(l.dir, e.Name())
			if _, err := os.Stat(filepath.Join(dir, selfupdate.PluginManifestFN)); err != nil {
				continue
			}
			m, err := plugin.LoadManifest(dir)
			if err != nil {
				missing = append(missing, fmt.Sprintf("manifest invalid: dir=%s (%s): %v", dir, l.label, err))
				continue
			}
			if m.Web != nil && m.Web.Entry != "" {
				artifact := filepath.Join(dir, "web", filepath.FromSlash(m.Web.Entry))
				if _, err := os.Stat(artifact); err != nil {
					missing = append(missing, fmt.Sprintf("web artifact missing: plugin=%s web.entry=%q dir=%s (%s)", m.ID, m.Web.Entry, dir, l.label))
				}
			}
			if m.Runtime == plugin.RuntimeStdio || m.Runtime == plugin.RuntimeGRPC {
				if entry := platformEntry(m); entry != "" {
					if binPath, ok := shippedBinaryPath(dir, entry); ok {
						if issue := shippedBinaryIssue(binPath); issue != "" {
							missing = append(missing, fmt.Sprintf("%s: plugin=%s entry=%q dir=%s (%s)", issue, m.ID, entry, dir, l.label))
						}
					}
				}
			}
			if disabled[m.ID] {
				disabledLines = append(disabledLines, fmt.Sprintf("%s (%s)", m.ID, l.label))
			}
		}
	}
	return missing, disabledLines
}

// platformEntry 返回当前平台的入口（平台专属字段优先），与 plugin 包
// scriptPlugin.resolvedEntry() 同一规则。
func platformEntry(m *plugin.PluginManifest) string {
	firstNonEmpty := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch runtime.GOOS {
	case "windows":
		return firstNonEmpty(m.EntryWindows, m.Entry)
	case "darwin":
		return firstNonEmpty(m.EntryDarwin, m.Entry)
	case "linux":
		return firstNonEmpty(m.EntryLinux, m.Entry)
	}
	return m.Entry
}

// shippedBinaryPath 判断入口是否是"随插件分发的相对二进制"（单个 token 的相对
// 路径），是则给出磁盘路径。带空格的命令行（entry 也可以是启动命令）与绝对路径
// （系统二进制）无法判定为随包文件 ⇒ 跳过（不产生假警）。
//
// ⚠️ "绝对路径"的判定必须 **GOOS 无关**：`filepath.IsAbs("/usr/bin/node")` 在
// windows 上是 **false**（windows 要求盘符），异平台写法会被误判成"随包相对
// 二进制"⇒ `setup --check` 报出假的 `plugin binary missing`（CI Test (Windows)
// 实测）。因此额外按前导路径分隔符判定。
func shippedBinaryPath(dir, entry string) (string, bool) {
	e := strings.TrimSpace(entry)
	if e == "" || strings.ContainsAny(e, " \t") || filepath.IsAbs(e) ||
		hasLeadingPathSeparator(e) || isWindowsDrivePath(e) {
		return "", false
	}
	return filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(e, "./"))), true
}

// hasLeadingPathSeparator 报告路径是否以 `/` 或 `\` 开头（绝对/系统路径的标志），
// 与运行平台无关。
func hasLeadingPathSeparator(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}

// isWindowsDrivePath 报告路径是否是 windows 盘符绝对路径（`C:\x` / `C:/x`）。
// 非 windows 平台上 `filepath.IsAbs` 不认它 ⇒ 不单独跳过就会把跨平台 manifest 的
// 系统二进制误判成"随包相对文件"，报出假的 `plugin binary missing`。
func isWindowsDrivePath(p string) bool {
	if len(p) < 3 || p[1] != ':' {
		return false
	}
	c := p[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	if !isLetter {
		return false
	}
	return p[2] == '\\' || p[2] == '/'
}

// shippedBinaryIssue 返回入口二进制的问题描述（"" = 正常）。
// windows 上按 plugin/runtime.go 的 resolvePluginBinary 同一规则允许
// <entry>.exe sibling（tarball 里的 windows 产物就是带 .exe 的）。
func shippedBinaryIssue(path string) string {
	fi, err := os.Stat(path)
	if err != nil && runtime.GOOS == "windows" {
		if exeFi, exeErr := os.Stat(path + ".exe"); exeErr == nil {
			fi, err = exeFi, nil
		}
	}
	switch {
	case err != nil:
		return "plugin binary missing"
	case runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0:
		return "plugin binary not executable"
	}
	return ""
}
