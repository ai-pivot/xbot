package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"xbot/config"
	"xbot/version"
)

// ─── scanChannelActivation / fixChannelActivationConfig ────────────────────
// (deriveReleaseTag / parseChecksums / ghMirrorURL / extractTarGz /
// installFromTarGz / componentInstalled tests moved to
// internal/selfupdate/release_test.go with the extracted primitives.)

func writePluginManifest(t *testing.T, dir, id, channel string, enabledDefault string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"id":      id,
		"runtime": "stdio",
	}
	if channel != "" {
		manifest["contributes"] = map[string]any{
			"channelProvider": map[string]any{
				"name": channel,
				"config_schema": []map[string]any{
					{"key": "enabled", "default_value": enabledDefault},
					{"key": "other", "default_value": "x"},
				},
			},
		}
	}
	b, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanChannelActivation(t *testing.T) {
	home := t.TempDir()
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.git-fancy"), "xbot.git-fancy", "", "") // no channel provider
	// User-dir plugins are NEVER scanned (setup manages builtin/ only — the
	// user dir is entirely user-managed: examples, experiments, dev installs).
	// default=true in the user dir must NOT activate; default=false must not either.
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.ambience"), "xbot.ambience", "", "")
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.user-plugin"), "xbot.user-plugin", "userchan", "true")
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.custom"), "xbot.custom", "custom", "false")
	// Stray file (not a dir) must be skipped.
	if err := os.WriteFile(filepath.Join(home, "plugins", "stray.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := scanChannelActivation(home)
	if len(got) != 1 {
		t.Fatalf("scanChannelActivation = %v, want only genui (user-dir plugins MUST NOT be scanned: user-plugin/custom live in the user dir, not builtin/)", got)
	}
	if got["genui"] != "xbot.genui" {
		t.Errorf("genui → %q, want xbot.genui", got["genui"])
	}
}

func TestFixChannelActivationConfig_SetIfMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.custom"), "xbot.custom", "custom", "true")
	// User-dir plugin with default=true — must NEVER be auto-activated by
	// setup (the user dir is user-managed; setup manages builtin/ only).
	writePluginManifest(t, filepath.Join(home, "plugins", "xbot.user-plugin"), "xbot.user-plugin", "userchan", "true")

	// Pre-existing config: genui explicitly disabled by the user, custom absent.
	if err := os.WriteFile(config.ConfigFilePath(), []byte(`{
		"channels": {"genui": {"enabled": "false"}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (custom channel added)")
	}
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil {
		t.Fatal("config unparseable after fixup")
	}
	// set_if_missing: the user's explicit false is preserved.
	if v := cfg.Channels["genui"]["enabled"]; v != "false" {
		t.Errorf("genui enabled = %q, want \"false\" (user's explicit value must be preserved)", v)
	}
	// custom (missing) got enabled=true.
	if v := cfg.Channels["custom"]["enabled"]; v != "true" {
		t.Errorf("custom enabled = %q, want \"true\"", v)
	}
	// User-dir plugin MUST NOT be activated (builtin-only scope).
	if v, exists := cfg.Channels["userchan"]["enabled"]; exists {
		t.Errorf("userchan enabled = %q — user-dir plugins must never be auto-activated by setup", v)
	}

	// Idempotent: second run changes nothing.
	changed, err = fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second run must be a no-op (set_if_missing)")
	}
}

func TestFixChannelActivationConfig_CreatesConfigWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XBOT_HOME", home)
	writePluginManifest(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "xbot.genui", "genui", "true")
	// No config.json at all.

	changed, err := fixChannelActivationConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true on fresh config")
	}
	cfg := config.LoadFromFile(config.ConfigFilePath())
	if cfg == nil || cfg.Channels["genui"]["enabled"] != "true" {
		t.Fatalf("fresh config missing channels.genui.enabled=true: %+v", cfg)
	}
}

// ─── resolvePluginBinary is tested in plugin/runtime_entry_test.go ─────────

func TestSetupOptions_Parse(t *testing.T) {
	// version.Version is "dev" in tests; deriveReleaseTag must refuse
	// downloads for dev builds (guidance error), which is covered above.
	// This test guards the smoke path: errSetupIncomplete is a distinct
	// sentinel so main can exit 3 on warnings.
	if !strings.Contains(errSetupIncomplete.Error(), "warnings") {
		t.Error("errSetupIncomplete sentinel unexpectedly changed")
	}
	_ = version.Version // keep import
}

// ─── pluginHealthFindings (setup --check §4) ───────────────────────────────

// writePlugin 写一个插件 manifest（runtime/entry/web.entry 可指定）。
func writePlugin(t *testing.T, dir, runtimeKind, entry, webEntry string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := map[string]any{
		"id":      filepath.Base(dir),
		"name":    filepath.Base(dir),
		"version": "1.0.0",
		"runtime": runtimeKind,
		"entry":   entry,
	}
	if webEntry != "" {
		m["web"] = map[string]any{"entry": webEntry}
	}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPluginHealthFindings_BinaryAndWebArtifacts —— 新内置插件（stdio）必须被
// --check 的本地体检覆盖到**入口二进制**，否则"装了但从未工作"完全无声
// （与 xbot.iteration-stats 丢 web 产物同族的盲区）。
func TestPluginHealthFindings_BinaryAndWebArtifacts(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "plugins", "builtin", "xbot.ssh-runner")
	writePlugin(t, dir, "stdio", "./bin/ssh-runner-plugin", "index.js")
	bin := filepath.Join(dir, "bin", "ssh-runner-plugin")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web", "index.js"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if missing, disabled := pluginHealthFindings(home, nil); len(missing) != 0 || len(disabled) != 0 {
		t.Fatalf("完整安装不应有 finding，got missing=%v disabled=%v", missing, disabled)
	}

	// ① 入口二进制缺失（tarball 没打进 bin/）⇒ 必须报出来。
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	missing, _ := pluginHealthFindings(home, nil)
	if !strings.Contains(strings.Join(missing, "\n"), "plugin binary missing") {
		t.Fatalf("入口二进制缺失必须被报出，got %v", missing)
	}

	// ② unix 上丢了可执行位 ⇒ 同样起不来 ⇒ 必须报出。
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		missing, _ = pluginHealthFindings(home, nil)
		if !strings.Contains(strings.Join(missing, "\n"), "plugin binary not executable") {
			t.Fatalf("入口二进制不可执行必须被报出，got %v", missing)
		}
	}

	// ③ web.entry 产物缺失 ⇒ 报出（既有行为不回归）。
	if err := os.Remove(filepath.Join(dir, "web", "index.js")); err != nil {
		t.Fatal(err)
	}
	missing, _ = pluginHealthFindings(home, nil)
	if !strings.Contains(strings.Join(missing, "\n"), "web artifact missing") {
		t.Fatalf("web.entry 产物缺失必须被报出，got %v", missing)
	}
}

// TestShippedBinaryPath_Predicate —— "随包相对二进制"的判据必须 **GOOS 无关**：
// `filepath.IsAbs("/usr/bin/node")` 在 windows 上是 false（CI Test (Windows) 实测
// 报出假的 plugin binary missing），所以前导路径分隔符必须单独判定。
func TestShippedBinaryPath_Predicate(t *testing.T) {
	cases := []struct {
		entry string
		want  bool
	}{
		{"./bin/ssh-runner-plugin", true},
		{"bin/genui-plugin", true},
		{"/usr/bin/node", false},      // unix 绝对路径（windows 上 IsAbs 为 false ⇒ 必须仍跳过）
		{`\bin\x.exe`, false},         // windows 绝对路径（无盘符形式）
		{`C:\tools\x.exe`, false},     // windows 盘符绝对路径（非 windows 上 IsAbs 为 false）
		{"node server.js", false},     // 命令行（带参数）
		{"bash run.sh /tmp/x", false}, // script runtime 的命令行
		{"", false},
	}
	for _, c := range cases {
		if _, ok := shippedBinaryPath("/plugins/x", c.entry); ok != c.want {
			t.Errorf("shippedBinaryPath(entry=%q) ok = %v, want %v", c.entry, ok, c.want)
		}
	}
}

// TestPluginHealthFindings_OnlyJudgesShippedBinaries —— entry 不一定是"随包文件"：
// script runtime 的 entry 是命令行，stdio 的 entry 也可能是绝对路径（系统二进制）
// 或带参数的命令行。这些都无法判定为随包文件 ⇒ 一律不产生假警。
func TestPluginHealthFindings_OnlyJudgesShippedBinaries(t *testing.T) {
	home := t.TempDir()
	writePlugin(t, filepath.Join(home, "plugins", "builtin", "xbot.ambience"), "script", "bash run.sh /tmp/x", "")
	writePlugin(t, filepath.Join(home, "plugins", "builtin", "xbot.sys"), "stdio", "/usr/bin/node", "")
	writePlugin(t, filepath.Join(home, "plugins", "builtin", "xbot.cmd"), "stdio", "node server.js", "")

	if missing, _ := pluginHealthFindings(home, nil); len(missing) != 0 {
		t.Fatalf("非随包 entry 不得产生 finding，got %v", missing)
	}
}

// TestPluginHealthFindings_ReportsDisabled —— 被 config 禁用的插件单独列出
// （报告用，不影响 --check 退出码）。
func TestPluginHealthFindings_ReportsDisabled(t *testing.T) {
	home := t.TempDir()
	writePlugin(t, filepath.Join(home, "plugins", "builtin", "xbot.genui"), "script", "", "")
	cfg := &config.Config{}
	cfg.Plugins.DisabledPlugins = []string{"xbot.genui"}
	_, disabled := pluginHealthFindings(home, cfg)
	if len(disabled) != 1 || !strings.Contains(disabled[0], "xbot.genui") {
		t.Fatalf("被禁用插件必须列出，got %v", disabled)
	}
}
