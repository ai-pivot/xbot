package channel

import "testing"

// tavily_api_key is ONE per-operator value shared by every channel. It must be
// stored/read under the canonical channel — otherwise a key saved from the Web
// settings tab is invisible to CLI/Feishu sessions and "runtime setting" silently
// does nothing outside the channel that wrote it.
func TestTavilyKeyUsesCanonicalChannel(t *testing.T) {
	if TavilyKeyChannel != "cli" {
		t.Fatalf("TavilyKeyChannel = %q, want %q", TavilyKeyChannel, "cli")
	}
	if got := CanonicalChannelForSetting("tavily_api_key"); got != TavilyKeyChannel {
		t.Errorf("CanonicalChannelForSetting(tavily_api_key) = %q, want %q", got, TavilyKeyChannel)
	}
	// Per-channel settings keep their historical scope.
	if got := CanonicalChannelForSetting("thinking_mode"); got != "" {
		t.Errorf("CanonicalChannelForSetting(thinking_mode) = %q, want empty", got)
	}
}

// Sensitive settings must be masked in API payloads, and the mask must be
// recognisable by write paths so it is never persisted over the real secret.
func TestSensitiveSettingMaskingHelpers(t *testing.T) {
	if !IsSensitiveSettingKey("tavily_api_key") {
		t.Error("tavily_api_key must be marked sensitive")
	}
	if IsSensitiveSettingKey("thinking_mode") {
		t.Error("thinking_mode must not be marked sensitive")
	}
	for _, v := range []string{MaskedSettingValue, "sk-abc****", "tvly-1234****"} {
		if !IsMaskedSettingValue(v) {
			t.Errorf("IsMaskedSettingValue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "tvly-real-key", "sk-real"} {
		if IsMaskedSettingValue(v) {
			t.Errorf("IsMaskedSettingValue(%q) = true, want false", v)
		}
	}

	// AI-facing config listings also hide sensitive values.
	if got := MaskConfigItemValue(true, "tvly-secret"); got != MaskedSettingValue {
		t.Errorf("MaskConfigItemValue(sensitive) = %q, want %q", got, MaskedSettingValue)
	}
	if got := MaskConfigItemValue(false, "dark"); got != "dark" {
		t.Errorf("MaskConfigItemValue(non-sensitive) = %q, want dark", got)
	}
	if got := MaskConfigItemValue(true, ""); got != "" {
		t.Errorf("MaskConfigItemValue(sensitive, empty) = %q, want empty", got)
	}
}
