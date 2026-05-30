package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionConfigRoundtrip(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "sk-test",
			Model:    "gpt-4",
		},
		Subscriptions: []SubscriptionConfig{
			{
				ID:       "default",
				Name:     "openai",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "sk-test",
				Model:    "gpt-4",
				Active:   true,
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(cfg2.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(cfg2.Subscriptions))
	}

	sub := cfg2.Subscriptions[0]
	if sub.ID != "default" {
		t.Errorf("expected ID=default, got %s", sub.ID)
	}
	if sub.Provider != "openai" {
		t.Errorf("expected Provider=openai, got %s", sub.Provider)
	}
	if sub.Model != "gpt-4" {
		t.Errorf("expected Model=gpt-4, got %s", sub.Model)
	}
	if !sub.Active {
		t.Error("expected Active=true")
	}
}

func TestSubscriptionConfigOmitEmpty(t *testing.T) {
	// Config without subscriptions should serialize to empty or omit the field
	cfg := Config{
		LLM: LLMConfig{Provider: "openai", Model: "gpt-4"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(cfg2.Subscriptions) != 0 {
		t.Errorf("expected 0 subscriptions, got %d", len(cfg2.Subscriptions))
	}
}

func TestSubscriptionMigrationFromEmpty(t *testing.T) {
	// Simulate: user has no subscriptions, LLM config has provider/model
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.example.com/v1",
			APIKey:   "sk-key",
			Model:    "gpt-4",
		},
		Subscriptions: nil,
	}

	// Migration logic (mirrors main.go)
	if len(cfg.Subscriptions) == 0 {
		cfg.Subscriptions = []SubscriptionConfig{{
			ID:       "default",
			Name:     cfg.LLM.Provider,
			Provider: cfg.LLM.Provider,
			BaseURL:  cfg.LLM.BaseURL,
			APIKey:   cfg.LLM.APIKey,
			Model:    cfg.LLM.Model,
			Active:   true,
		}}
	}

	if len(cfg.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription after migration, got %d", len(cfg.Subscriptions))
	}

	sub := cfg.Subscriptions[0]
	if sub.ID != "default" {
		t.Errorf("expected ID=default, got %s", sub.ID)
	}
	if sub.Provider != "openai" {
		t.Errorf("expected Provider=openai, got %s", sub.Provider)
	}
	if sub.BaseURL != "https://api.example.com/v1" {
		t.Errorf("expected BaseURL from LLM config, got %s", sub.BaseURL)
	}
	if sub.APIKey != "sk-key" {
		t.Errorf("expected APIKey from LLM config, got %s", sub.APIKey)
	}
	if !sub.Active {
		t.Error("expected Active=true for migrated subscription")
	}
}

func TestSaveToFilePreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 1. Write initial config with a custom unknown field
	initial := `{
  "llm": {"provider": "openai", "model": "gpt-4"},
  "agent": {"work_dir": "/tmp/test", "prompt_file": "CLAUDE.md", "custom_future_field": "keep_me"},
  "my_custom_section": {"key": "value"}
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// 2. Load, modify a known field, save
	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil")
		return
	}
	cfg.Agent.MaxIterations = 500

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	// 3. Verify unknown fields are preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `"custom_future_field": "keep_me"`) {
		t.Errorf("custom_future_field not preserved in output:\n%s", content)
	}
	if !strings.Contains(content, `"my_custom_section"`) {
		t.Errorf("my_custom_section not preserved in output:\n%s", content)
	}

	// 4. Verify known fields are correctly updated
	if !strings.Contains(content, `"max_iterations": 500`) {
		t.Errorf("max_iterations not updated in output:\n%s", content)
	}
	if !strings.Contains(content, `"prompt_file": "CLAUDE.md"`) {
		t.Errorf("prompt_file not preserved in output:\n%s", content)
	}
	if !strings.Contains(content, `"work_dir": "/tmp/test"`) {
		t.Errorf("work_dir not preserved in output:\n%s", content)
	}
}

func TestSaveToFileCreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &Config{
		LLM:   LLMConfig{Provider: "openai", Model: "gpt-4"},
		Agent: AgentConfig{WorkDir: "/tmp", PromptFile: "prompt.md"},
	}

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.LLM.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", loaded.LLM.Model)
	}
	if loaded.Agent.PromptFile != "prompt.md" {
		t.Errorf("expected prompt.md, got %s", loaded.Agent.PromptFile)
	}
}

func TestMergeJSONPreserveUnknown(t *testing.T) {
	existing := `{"a": 1, "b": 2, "unknown_key": "keep"}`
	structData := `{"a": 10, "c": 3}`

	merged, err := mergeJSONPreserveUnknown([]byte(existing), []byte(structData))
	if err != nil {
		t.Fatalf("mergeJSONPreserveUnknown: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(merged, &m); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// struct key overrides existing
	if m["a"] != float64(10) {
		t.Errorf("expected a=10, got %v", m["a"])
	}
	// existing-only key preserved
	if m["b"] != float64(2) {
		t.Errorf("expected b=2, got %v", m["b"])
	}
	// unknown key preserved
	if m["unknown_key"] != "keep" {
		t.Errorf("expected unknown_key=keep, got %v", m["unknown_key"])
	}
	// struct-only key added
	if m["c"] != float64(3) {
		t.Errorf("expected c=3, got %v", m["c"])
	}
}

func TestSaveToFileLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write a full config with all known fields
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			BaseURL:  "https://open.bigmodel.cn/api/anthropic",
			APIKey:   "test-key",
			Model:    "glm-5.1",
		},
		Agent: AgentConfig{
			MaxIterations:    2000,
			MaxConcurrency:   100,
			MemoryProvider:   "flat",
			WorkDir:          "/ipfs_flash/test",
			PromptFile:       "CLAUDE.md",
			MaxContextTokens: 200000,
		},
		Feishu: FeishuConfig{
			Enabled: true,
			AppID:   "test-app",
		},
	}

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Load and save again (simulates the load → modify → save cycle)
	cfg2 := LoadFromFile(path)
	if cfg2 == nil {
		t.Fatal("LoadFromFile returned nil")
		return
	}
	cfg2.Agent.MaxIterations = 3000
	if err := SaveToFile(path, cfg2); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// Verify all fields preserved
	cfg3 := LoadFromFile(path)
	if cfg3 == nil {
		t.Fatal("final LoadFromFile returned nil")
		return
	}
	if cfg3.Agent.PromptFile != "CLAUDE.md" {
		t.Errorf("prompt_file lost: got %q", cfg3.Agent.PromptFile)
	}
	if cfg3.Agent.WorkDir != "/ipfs_flash/test" {
		t.Errorf("work_dir lost: got %q", cfg3.Agent.WorkDir)
	}
	if cfg3.Agent.MaxIterations != 3000 {
		t.Errorf("max_iterations not updated: got %d", cfg3.Agent.MaxIterations)
	}
	if cfg3.LLM.Provider != "anthropic" {
		t.Errorf("llm provider lost: got %q", cfg3.LLM.Provider)
	}
	if cfg3.Feishu.AppID != "test-app" {
		t.Errorf("feishu app_id lost: got %q", cfg3.Feishu.AppID)
	}
}

func TestDurationMarshalJSON(t *testing.T) {
	tests := []struct {
		d    Duration
		want string
	}{
		{1 * Second, `"1s"`},
		{30 * Minute, `"30m0s"`},
		{24 * Hour, `"24h0m0s"`},
		{1500 * Millisecond, `"1.5s"`},
		{0, `"0s"`},
	}
	for _, tt := range tests {
		data, err := tt.d.MarshalJSON()
		if err != nil {
			t.Errorf("MarshalJSON(%v): %v", tt.d, err)
			continue
		}
		if string(data) != tt.want {
			t.Errorf("MarshalJSON(%v) = %s, want %s", tt.d, string(data), tt.want)
		}
	}
}

func TestDurationUnmarshalJSON_String(t *testing.T) {
	tests := []struct {
		input string
		want  Duration
	}{
		{`"1s"`, 1 * Second},
		{`"30m0s"`, 30 * Minute},
		{`"30m"`, 30 * Minute},
		{`"2h"`, 2 * Hour},
		{`"0s"`, 0},
	}
	for _, tt := range tests {
		var d Duration
		if err := d.UnmarshalJSON([]byte(tt.input)); err != nil {
			t.Errorf("UnmarshalJSON(%s): %v", tt.input, err)
			continue
		}
		if d != tt.want {
			t.Errorf("UnmarshalJSON(%s) = %v, want %v", tt.input, time.Duration(d), time.Duration(tt.want))
		}
	}
}

func TestDurationUnmarshalJSON_Number(t *testing.T) {
	// Old config files store durations as nanoseconds (backward compat)
	tests := []struct {
		input string
		want  Duration
	}{
		{"0", 0},
		{`1000000000`, 1 * Second},
		{`1800000000000`, 30 * Minute},
	}
	for _, tt := range tests {
		var d Duration
		if err := d.UnmarshalJSON([]byte(tt.input)); err != nil {
			t.Errorf("UnmarshalJSON(%s): %v", tt.input, err)
			continue
		}
		if d != tt.want {
			t.Errorf("UnmarshalJSON(%s) = %v, want %v", tt.input, time.Duration(d), time.Duration(tt.want))
		}
	}
}

func TestDurationUnmarshalJSON_Invalid(t *testing.T) {
	var d Duration
	if err := d.UnmarshalJSON([]byte(`"xyz"`)); err == nil {
		t.Error("expected error for invalid duration string")
	}
	if err := d.UnmarshalJSON([]byte(`true`)); err == nil {
		t.Error("expected error for boolean")
	}
}

func TestConfigDurationRoundtrip(t *testing.T) {
	// Verify Duration fields serialize as strings and deserialize correctly
	cfg := &Config{
		Sandbox: SandboxConfig{
			Mode:        "docker",
			IdleTimeout: 30 * Minute,
		},
		Agent: AgentConfig{
			MCPInactivityTimeout: 30 * Minute,
			MCPCleanupInterval:   5 * Minute,
			LLMRetryDelay:        1 * Second,
		},
		Server: ServerConfig{
			ReadTimeout:  30 * Second,
			WriteTimeout: 120 * Second,
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	content := string(data)

	// Verify human-readable strings in JSON output
	for _, want := range []string{
		`"idle_timeout": "30m0s"`,
		`"mcp_inactivity_timeout": "30m0s"`,
		`"mcp_cleanup_interval": "5m0s"`,
		`"llm_retry_delay": "1s"`,
		`"read_timeout": "30s"`,
		`"write_timeout": "2m0s"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("JSON output missing %s:\n%s", want, content)
		}
	}

	// Verify round-trip deserialization
	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg2.Sandbox.IdleTimeout != 30*Minute {
		t.Errorf("IdleTimeout roundtrip: got %v, want 30m", time.Duration(cfg2.Sandbox.IdleTimeout))
	}
	if cfg2.Agent.LLMRetryDelay != 1*Second {
		t.Errorf("LLMRetryDelay roundtrip: got %v, want 1s", time.Duration(cfg2.Agent.LLMRetryDelay))
	}
	if cfg2.Server.ReadTimeout != 30*Second {
		t.Errorf("ReadTimeout roundtrip: got %v, want 30s", time.Duration(cfg2.Server.ReadTimeout))
	}
}

func TestConfigDurationBackwardCompat(t *testing.T) {
	// Old config files with nanosecond numbers must still parse
	oldJSON := `{
  "sandbox": {
    "idle_timeout": 1800000000000
  },
  "agent": {
    "mcp_inactivity_timeout": 1800000000000,
    "llm_retry_delay": 1000000000
  },
  "server": {
    "read_timeout": 30000000000,
    "write_timeout": 120000000000
  }
}`
	var cfg Config
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("Unmarshal old format: %v", err)
	}
	if cfg.Sandbox.IdleTimeout != 30*Minute {
		t.Errorf("IdleTimeout: got %v, want 30m", time.Duration(cfg.Sandbox.IdleTimeout))
	}
	if cfg.Agent.LLMRetryDelay != 1*Second {
		t.Errorf("LLMRetryDelay: got %v, want 1s", time.Duration(cfg.Agent.LLMRetryDelay))
	}
}
