package serverapp

import (
	"testing"

	"xbot/channel"
)

// get_settings must never hand a configured credential to a client. The masked
// placeholder means "configured"; set_setting refuses to persist it.
func TestMaskSensitiveSettings(t *testing.T) {
	// DB row present → masked, non-sensitive keys untouched.
	s := map[string]string{"tavily_api_key": "tvly-secret", "theme": "dark"}
	maskSensitiveSettings(s, "tavily_api_key", "")
	if s["tavily_api_key"] != channel.MaskedSettingValue {
		t.Errorf("tavily_api_key = %q, want %q", s["tavily_api_key"], channel.MaskedSettingValue)
	}
	if s["theme"] != "dark" {
		t.Errorf("theme = %q, want dark", s["theme"])
	}

	// No DB row but config.json has a key → still surfaced as configured
	// (without exposing the file value).
	fallback := map[string]string{}
	maskSensitiveSettings(fallback, "tavily_api_key", "tvly-from-config")
	if fallback["tavily_api_key"] != channel.MaskedSettingValue {
		t.Errorf("fallback tavily_api_key = %q, want %q", fallback["tavily_api_key"], channel.MaskedSettingValue)
	}

	// Neither row nor fallback → key absent (UI shows "not configured").
	empty := map[string]string{}
	maskSensitiveSettings(empty, "tavily_api_key", "")
	if _, ok := empty["tavily_api_key"]; ok {
		t.Error("tavily_api_key must be absent when neither DB nor config.json has it")
	}
}
