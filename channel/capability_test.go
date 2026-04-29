package channel

import (
	"testing"
)

func TestBuildTextSettingsUI(t *testing.T) {
	schema := []SettingDefinition{
		{
			Key:          "reply_style",
			Label:        "回复风格",
			Description:  "控制机器人的回复风格",
			Type:         SettingTypeSelect,
			Category:     "对话",
			DefaultValue: "detailed",
			Options: []SettingOption{
				{Label: "简洁", Value: "concise"},
				{Label: "详细", Value: "detailed"},
			},
		},
		{
			Key:      "language",
			Label:    "语言",
			Type:     SettingTypeSelect,
			Category: "对话",
			Options:  []SettingOption{{Label: "中文", Value: "zh"}},
		},
		{
			Key:          "notify",
			Label:        "通知",
			Type:         SettingTypeToggle,
			Category:     "通知",
			DefaultValue: "true",
		},
	}

	// With no current values
	ui := BuildTextSettingsUI(schema, nil)
	if ui == "" {
		t.Fatal("expected non-empty UI output")
	}
	if !contains(ui, "回复风格") {
		t.Error("expected label in output")
	}
	if !contains(ui, "选项") {
		t.Error("expected options prefix in output")
	}

	// With current values overriding defaults
	currentValues := map[string]string{
		"reply_style": "concise",
	}
	ui = BuildTextSettingsUI(schema, currentValues)
	if !contains(ui, "`concise`") {
		t.Error("expected current value 'concise' in output")
	}
}

func TestBuildTextSettingsUIEmpty(t *testing.T) {
	ui := BuildTextSettingsUI(nil, nil)
	if ui != "没有可配置的设置项。" {
		t.Errorf("expected empty message, got %q", ui)
	}
}

func TestProviderDefaultURLs(t *testing.T) {
	// All expected providers should have URLs
	expected := []string{"openai", "anthropic", "openrouter", "ollama", "google"}
	for _, p := range expected {
		if _, ok := ProviderDefaultURLs[p]; !ok {
			t.Errorf("ProviderDefaultURLs missing %q", p)
		}
	}
	// Azure and custom should NOT be in the map
	for _, p := range []string{"azure", "custom"} {
		if _, ok := ProviderDefaultURLs[p]; ok {
			t.Errorf("ProviderDefaultURLs should not contain %q", p)
		}
	}
}

func TestIsProviderDefaultURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://api.openai.com/v1", true},
		{"https://api.anthropic.com", true},
		{"https://openrouter.ai/api/v1", true},
		{"http://localhost:11434/v1", true},
		{"https://generativelanguage.googleapis.com/v1beta/openai", true},
		{"https://custom.api.example.com/v1", false},
		{"", false},
		{"https://api.openai.com/v1/chat", false}, // not an exact match
	}
	for _, tt := range tests {
		if got := IsProviderDefaultURL(tt.url); got != tt.want {
			t.Errorf("IsProviderDefaultURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && search(s, substr)
}

func search(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
