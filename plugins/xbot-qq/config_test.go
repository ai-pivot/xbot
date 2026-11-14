package main

import (
	"strings"
	"testing"
)

// TestParseConfig 覆盖 host 推来的 map[string]string 的宽松解析。
func TestParseConfig(t *testing.T) {
	raw := map[string]string{
		KeyEnabled:    "TRUE",
		KeyAppID:      " 102030405 ",
		KeyAppSecret:  " s3cret ",
		KeyAllow:      "openid-a, group:*\nopenid-b",
		KeyAdmins:     "openid-a，openid-c",
		"unknown_key": "ignored",
	}
	cfg := ParseConfig(raw)

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true for \"TRUE\"")
	}
	if cfg.AppID != "102030405" {
		t.Fatalf("AppID = %q, want trimmed value", cfg.AppID)
	}
	if cfg.AppSecret != "s3cret" {
		t.Fatalf("AppSecret = %q, want trimmed value", cfg.AppSecret)
	}
	if got, want := cfg.Allow.Len(), 3; got != want {
		t.Fatalf("Allow.Len() = %d, want %d (%v)", got, want, cfg.Allow.List())
	}
	if got, want := cfg.Admins.Len(), 2; got != want {
		t.Fatalf("Admins.Len() = %d, want %d (%v)", got, want, cfg.Admins.List())
	}
}

// TestParseConfigBoolForms toggle 组件可能写出多种真值写法。
func TestParseConfigBoolForms(t *testing.T) {
	for _, s := range []string{"true", "TRUE", "1", "yes", "on", " True "} {
		if !ParseConfig(map[string]string{KeyEnabled: s}).Enabled {
			t.Errorf("enabled=%q parsed as false, want true", s)
		}
	}
	for _, s := range []string{"", "false", "0", "no", "off", "nah"} {
		if ParseConfig(map[string]string{KeyEnabled: s}).Enabled {
			t.Errorf("enabled=%q parsed as true, want false", s)
		}
	}
}

// TestParseConfigDefaults 空配置 → 关闭 + 空白名单（安全默认），且不报错。
func TestParseConfigDefaults(t *testing.T) {
	cfg := ParseConfig(nil)
	if cfg.Enabled {
		t.Fatal("zero config must default to disabled")
	}
	if !cfg.Allow.Empty() {
		t.Fatalf("zero config must have an empty allow list, got %v", cfg.Allow.List())
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled zero config must validate: %v", err)
	}
	if cfg.Policy().ShouldHandle("openid-a") {
		t.Fatal("zero config must reject everything")
	}
	if cfg.CredentialsReady() {
		t.Fatal("zero config must not be credentials-ready")
	}
}

// TestConfigPolicyWiring 确认 Config → Policy 的接线（白名单与管理员名单各就各位）。
func TestConfigPolicyWiring(t *testing.T) {
	cfg := ParseConfig(map[string]string{
		KeyAllow:  "openid-a",
		KeyAdmins: "openid-a",
	})
	p := cfg.Policy()
	if !p.ShouldHandle("openid-a") {
		t.Fatal("openid-a should be admitted")
	}
	if p.ShouldHandle("openid-b") {
		t.Fatal("openid-b should be rejected")
	}
	if !p.IsAdmin("openid-a") || p.IsAdmin("openid-b") {
		t.Fatal("admin set mismatch")
	}
}

// TestConfigValidate 只有"启用时"才要求 app_id/app_secret 齐全。
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"关闭+无凭据", Config{Enabled: false}, false},
		{"启用+无app_id", Config{Enabled: true}, true},
		{"启用+app_id非数字", Config{Enabled: true, AppID: "abc", AppSecret: "s"}, true},
		{"启用+无secret", Config{Enabled: true, AppID: "102030405"}, true},
		{"启用+齐全", Config{Enabled: true, AppID: "102030405", AppSecret: "s"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("Validate = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
		})
	}
}

// TestConfigRedacted secret 不得以明文出现在日志快照里。
func TestConfigRedacted(t *testing.T) {
	cfg := ParseConfig(map[string]string{
		KeyAppID:     "102030405",
		KeyAppSecret: "super-secret-token",
		KeyAllow:     "*",
		KeyAdmins:    "openid-a",
	})
	r := cfg.Redacted()
	if strings.Contains(r[KeyAppSecret], "super-secret-token") {
		t.Fatalf("Redacted leaked the secret: %q", r[KeyAppSecret])
	}
	if !strings.Contains(r[KeyAppSecret], "18 chars") {
		t.Fatalf("Redacted secret marker = %q, want length marker", r[KeyAppSecret])
	}
	if r[KeyAllow] != "[*]" {
		t.Fatalf("Redacted allow = %q, want [*]", r[KeyAllow])
	}
	if strings.Contains(cfg.Summary(), "super-secret-token") {
		t.Fatalf("Summary leaked the secret: %q", cfg.Summary())
	}
}

// TestConfigKeysExcludeOneBot 配置键集合必须只有官方那几项（+ 环境开关）。
//
// 这是对"方向纠正"的硬断言：一旦有人把 OneBot 的键加回来，这里变红。
func TestConfigKeysExcludeOneBot(t *testing.T) {
	keys := map[string]bool{
		KeyEnabled: false, KeyAppID: false, KeyAppSecret: false,
		KeySandbox: false, KeyAllow: false, KeyAdmins: false, KeyIntents: false, KeyMarkdown: false,
	}
	for _, def := range qqChannelDeclaration().ConfigSchema {
		if _, ok := keys[def.Key]; !ok {
			t.Fatalf("declaration exposes unexpected config key %q", def.Key)
		}
		keys[def.Key] = true
	}
	for k, seen := range keys {
		if !seen {
			t.Fatalf("declaration missing config key %q", k)
		}
	}
	if len(keys) != 8 {
		t.Fatalf("expected exactly 8 config keys, got %d", len(keys))
	}
}
