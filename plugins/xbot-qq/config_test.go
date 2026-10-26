package main

import (
	"strings"
	"testing"
)

// TestParseConfig 覆盖 host 推来的 map[string]string 的宽松解析。
func TestParseConfig(t *testing.T) {
	raw := map[string]string{
		KeyEnabled:     "TRUE",
		KeyWSURL:       " ws://127.0.0.1:3001 ",
		KeyAccessToken: " s3cret ",
		KeyAllow:       "10001, group:*\n10002",
		KeyAdmins:      "10001，10003",
		"unknown_key":  "ignored",
	}
	cfg := ParseConfig(raw)

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true for \"TRUE\"")
	}
	if cfg.WSURL != "ws://127.0.0.1:3001" {
		t.Fatalf("WSURL = %q, want trimmed value", cfg.WSURL)
	}
	if cfg.AccessToken != "s3cret" {
		t.Fatalf("AccessToken = %q, want trimmed value", cfg.AccessToken)
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
	if cfg.Policy().ShouldHandle("10001") {
		t.Fatal("zero config must reject everything")
	}
}

// TestConfigPolicyWiring 确认 Config → Policy 的接线（白名单与管理员名单各就各位）。
func TestConfigPolicyWiring(t *testing.T) {
	cfg := ParseConfig(map[string]string{
		KeyAllow:  "10001",
		KeyAdmins: "10001",
	})
	p := cfg.Policy()
	if !p.ShouldHandle("10001") {
		t.Fatal("10001 should be admitted")
	}
	if p.ShouldHandle("20002") {
		t.Fatal("20002 should be rejected")
	}
	if !p.IsAdmin("10001") || p.IsAdmin("20002") {
		t.Fatal("admin set mismatch")
	}
}

// TestConfigValidate 只有"启用时"才要求连接参数齐全、且 scheme 必须受支持。
func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"关闭+无URL", Config{Enabled: false}, false},
		{"启用+无URL", Config{Enabled: true}, true},
		{"启用+垃圾URL", Config{Enabled: true, WSURL: "not a url"}, true},
		{"启用+ws", Config{Enabled: true, WSURL: "ws://127.0.0.1:3001"}, false},
		{"启用+wss", Config{Enabled: true, WSURL: "wss://bot.example/ws"}, false},
		{"启用+http", Config{Enabled: true, WSURL: "http://127.0.0.1:3000"}, false},
		{"启用+不支持scheme", Config{Enabled: true, WSURL: "ftp://127.0.0.1:21"}, true},
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

// TestConfigRedacted token 不得以明文出现在日志快照里。
func TestConfigRedacted(t *testing.T) {
	cfg := ParseConfig(map[string]string{
		KeyAccessToken: "super-secret-token",
		KeyAllow:       "*",
		KeyAdmins:      "10001",
	})
	r := cfg.Redacted()
	if strings.Contains(r[KeyAccessToken], "super-secret-token") {
		t.Fatalf("Redacted leaked the token: %q", r[KeyAccessToken])
	}
	if !strings.Contains(r[KeyAccessToken], "18 chars") {
		t.Fatalf("Redacted token marker = %q, want length marker", r[KeyAccessToken])
	}
	if r[KeyAllow] != "[*]" {
		t.Fatalf("Redacted allow = %q, want [*]", r[KeyAllow])
	}
}
