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

func TestNormalizeConfigTypes_StringPort(t *testing.T) {
	// Simulates what install.sh (jq --arg) writes: port as string "8082"
	raw := `{
   "server": {"host": "0.0.0.0", "port": "8082"},
   "web": {"enable": "true", "host": "127.0.0.1", "port": "8082"},
   "oauth": {"enable": "false", "port": "8081"},
   "pprof": {"enable": "true", "port": "6060"},
   "feishu": {"enabled": "true"},
   "qq": {"enabled": "false"},
   "napcat": {"enabled": "1"},
   "agent": {
     "max_iterations": "2000",
     "max_concurrency": "3",
     "max_context_tokens": "200000",
     "enable_auto_compress": "true",
     "compression_threshold": "0.7",
     "purge_old_messages": "false",
     "max_sub_agent_depth": "6",
     "llm_retry_attempts": "5"
   },
   "embedding": {"max_tokens": "2048"},
   "sandbox": {"ws_port": "8080"},
   "event_webhook": {"enable": "true", "port": "9090", "max_body_size": "1048576", "rate_limit": "100"},
   "plugins": {"enabled": "true", "allow_unverified": "false"},
   "llm": {"max_output_tokens": "8192"},
   "subscriptions": [
     {
        "name": "default",
        "provider": "openai",
        "api_key": "sk-xxx",
        "model": "gpt-4o",
        "max_output_tokens": "4096",
        "max_context": "128000",
        "active": "true",
        "per_model_configs": {
          "gpt-4o": {"max_output_tokens": "8192", "max_context": "200000"}
        }
     }
   ],
   "cli_setup_completed": "true"
}`

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil — string types probably failed to unmarshal")
	}

	// Verify all coerced int fields
	if cfg.Server.Port != 8082 {
		t.Errorf("server.port: got %d, want 8082", cfg.Server.Port)
	}
	if cfg.Web.Port != 8082 {
		t.Errorf("web.port: got %d, want 8082", cfg.Web.Port)
	}
	if cfg.Web.Enable != true {
		t.Errorf("web.enable: got %v, want true", cfg.Web.Enable)
	}
	if cfg.OAuth.Enable != false {
		t.Errorf("oauth.enable: got %v, want false", cfg.OAuth.Enable)
	}
	if cfg.OAuth.Port != 8081 {
		t.Errorf("oauth.port: got %d, want 8081", cfg.OAuth.Port)
	}
	if cfg.PProf.Enable != true {
		t.Errorf("pprof.enable: got %v, want true", cfg.PProf.Enable)
	}
	if cfg.PProf.Port != 6060 {
		t.Errorf("pprof.port: got %d, want 6060", cfg.PProf.Port)
	}
	if cfg.Feishu.Enabled != true {
		t.Errorf("feishu.enabled: got %v, want true", cfg.Feishu.Enabled)
	}
	if cfg.QQ.Enabled != false {
		t.Errorf("qq.enabled: got %v, want false", cfg.QQ.Enabled)
	}
	if cfg.NapCat.Enabled != true {
		t.Errorf("napcat.enabled (='1'): got %v, want true", cfg.NapCat.Enabled)
	}

	// Agent fields
	if cfg.Agent.MaxIterations != 2000 {
		t.Errorf("agent.max_iterations: got %d, want 2000", cfg.Agent.MaxIterations)
	}
	if cfg.Agent.MaxConcurrency != 3 {
		t.Errorf("agent.max_concurrency: got %d, want 3", cfg.Agent.MaxConcurrency)
	}
	if cfg.Agent.MaxContextTokens != 200000 {
		t.Errorf("agent.max_context_tokens: got %d, want 200000", cfg.Agent.MaxContextTokens)
	}
	if cfg.Agent.EnableAutoCompress == nil || !*cfg.Agent.EnableAutoCompress {
		t.Errorf("agent.enable_auto_compress: got %v, want true", cfg.Agent.EnableAutoCompress)
	}
	if cfg.Agent.CompressionThreshold != 0.7 {
		t.Errorf("agent.compression_threshold: got %f, want 0.7", cfg.Agent.CompressionThreshold)
	}
	if cfg.Agent.PurgeOldMessages != false {
		t.Errorf("agent.purge_old_messages: got %v, want false", cfg.Agent.PurgeOldMessages)
	}
	if cfg.Agent.MaxSubAgentDepth != 6 {
		t.Errorf("agent.max_sub_agent_depth: got %d, want 6", cfg.Agent.MaxSubAgentDepth)
	}
	if cfg.Agent.LLMRetryAttempts != 5 {
		t.Errorf("agent.llm_retry_attempts: got %d, want 5", cfg.Agent.LLMRetryAttempts)
	}

	// Other sections
	if cfg.Embedding.MaxTokens != 2048 {
		t.Errorf("embedding.max_tokens: got %d, want 2048", cfg.Embedding.MaxTokens)
	}
	if cfg.Sandbox.WSPort != 8080 {
		t.Errorf("sandbox.ws_port: got %d, want 8080", cfg.Sandbox.WSPort)
	}
	if cfg.EventWebhook.Enable != true {
		t.Errorf("event_webhook.enable: got %v, want true", cfg.EventWebhook.Enable)
	}
	if cfg.EventWebhook.Port != 9090 {
		t.Errorf("event_webhook.port: got %d, want 9090", cfg.EventWebhook.Port)
	}
	if cfg.EventWebhook.MaxBodySize != 1048576 {
		t.Errorf("event_webhook.max_body_size: got %d, want 1048576", cfg.EventWebhook.MaxBodySize)
	}
	if cfg.EventWebhook.RateLimit != 100 {
		t.Errorf("event_webhook.rate_limit: got %d, want 100", cfg.EventWebhook.RateLimit)
	}
	if !cfg.Plugins.IsEnabled() {
		t.Errorf("plugins.enabled: got %v, want true", cfg.Plugins.IsEnabled())
	}
	if cfg.LLM.MaxOutputTokens != 8192 {
		t.Errorf("llm.max_output_tokens: got %d, want 8192", cfg.LLM.MaxOutputTokens)
	}

	// Subscription fields
	if len(cfg.Subscriptions) != 1 {
		t.Fatalf("subscriptions: got %d, want 1", len(cfg.Subscriptions))
	}
	sub := cfg.Subscriptions[0]
	if sub.MaxOutputTokens != 4096 {
		t.Errorf("subscription.max_output_tokens: got %d, want 4096", sub.MaxOutputTokens)
	}
	if sub.MaxContext != 128000 {
		t.Errorf("subscription.max_context: got %d, want 128000", sub.MaxContext)
	}
	if sub.Active != true {
		t.Errorf("subscription.active: got %v, want true", sub.Active)
	}

	// PerModelConfigs
	if len(sub.PerModelConfigs) != 1 {
		t.Fatalf("per_model_configs: got %d entries, want 1", len(sub.PerModelConfigs))
	}
	pmc, ok := sub.PerModelConfigs["gpt-4o"]
	if !ok {
		t.Fatal("per_model_configs['gpt-4o'] not found")
	}
	if pmc.MaxOutputTokens != 8192 {
		t.Errorf("per_model_configs.gpt-4o.max_output_tokens: got %d, want 8192", pmc.MaxOutputTokens)
	}
	if pmc.MaxContext != 200000 {
		t.Errorf("per_model_configs.gpt-4o.max_context: got %d, want 200000", pmc.MaxContext)
	}
}

func TestNormalizeConfigTypes_AlreadyCorrect(t *testing.T) {
	// When types are already correct, normalization should be a no-op
	raw := `{
	  "server": {"host": "0.0.0.0", "port": 8082},
	  "web": {"enable": true, "port": 8082},
	  "agent": {"max_iterations": 2000}
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil")
	}
	if cfg.Server.Port != 8082 {
		t.Errorf("server.port: got %d, want 8082", cfg.Server.Port)
	}
	if cfg.Web.Port != 8082 {
		t.Errorf("web.port: got %d, want 8082", cfg.Web.Port)
	}
	if cfg.Web.Enable != true {
		t.Errorf("web.enable: got %v, want true", cfg.Web.Enable)
	}
	if cfg.Agent.MaxIterations != 2000 {
		t.Errorf("agent.max_iterations: got %d, want 2000", cfg.Agent.MaxIterations)
	}
}

func TestNormalizeConfigTypes_PreservesUnknownFields(t *testing.T) {
	// Verify that dirty data + unknown fields are preserved through save/load cycle
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	dirtyConfig := `{
	"server": {"host": "0.0.0.0", "port": "8082"},
	"web": {"enable": "true", "port": "8082", "custom_web_key": "preserved"},
	"agent": {"max_iterations": "2000"},
	"my_custom_section": {"key": "value"},
	"llm": {"provider": "openai", "model": "gpt-4o"}
	}`
	if err := os.WriteFile(path, []byte(dirtyConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Load should succeed despite string-typed port/enable/max_iterations
	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil")
	}
	if cfg.Server.Port != 8082 {
		t.Errorf("server.port: got %d, want 8082", cfg.Server.Port)
	}
	if !cfg.Web.Enable {
		t.Error("web.enable: got false, want true")
	}
	if cfg.Web.Port != 8082 {
		t.Errorf("web.port: got %d, want 8082", cfg.Web.Port)
	}
	if cfg.Agent.MaxIterations != 2000 {
		t.Errorf("agent.max_iterations: got %d, want 2000", cfg.Agent.MaxIterations)
	}

	// Modify a known field and save
	cfg.Agent.MaxConcurrency = 50
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	// Verify: unknown fields preserved, dirty types fixed, new field present
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `"custom_web_key": "preserved"`) {
		t.Errorf("custom_web_key not preserved in:\n%s", content)
	}
	if !strings.Contains(content, `"my_custom_section"`) {
		t.Errorf("my_custom_section not preserved in:\n%s", content)
	}
	if !strings.Contains(content, `"max_concurrency": 50`) {
		t.Errorf("max_concurrency=50 not written in:\n%s", content)
	}
	// Dirty string values should now be proper types after merge
	if !strings.Contains(content, `"port": 8082`) {
		t.Errorf("server.port should be integer 8082, got:\n%s", content)
	}
	if !strings.Contains(content, `"enable": true`) {
		t.Errorf("web.enable should be boolean true, got:\n%s", content)
	}
}

func TestNormalizeConfigTypes_FastPath(t *testing.T) {
	// When types are already correct, normalizeConfigTypes should return original bytes
	input := `{"server":{"port":8082},"web":{"enable":true}}`
	result := normalizeConfigTypes([]byte(input))
	if string(result) != input {
		t.Errorf("fast path should return original data unchanged.\ninput:  %s\nresult: %s", input, string(result))
	}
}

func TestSaveToFile_DirtyDataPreserved(t *testing.T) {
	// Simulate: user has dirty config.json on disk, code saves back, unknown fields preserved
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	dirty := `{
	"server": {"host": "0.0.0.0", "port": "8082"},
	"web": {"enable": "true", "port": "8082"},
	"agent": {"max_iterations": "100"},
	"llm": {"provider": "openai", "model": "gpt-4o"},
	"custom_field": "keep_me"
	}`
	if err := os.WriteFile(path, []byte(dirty), 0o600); err != nil {
		t.Fatal(err)
	}

	// Load (normalizes dirty types)
	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil")
	}

	// Save back
	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	// Reload and verify
	cfg2 := LoadFromFile(path)
	if cfg2 == nil {
		t.Fatal("second LoadFromFile returned nil")
	}
	if cfg2.Server.Port != 8082 {
		t.Errorf("server.port not preserved: got %d", cfg2.Server.Port)
	}
	if !cfg2.Web.Enable {
		t.Error("web.enable not preserved")
	}
	if cfg2.Agent.MaxIterations != 100 {
		t.Errorf("agent.max_iterations not preserved: got %d", cfg2.Agent.MaxIterations)
	}

	// Unknown field must survive the save/load cycle
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"custom_field": "keep_me"`) {
		t.Errorf("custom_field lost after save/load cycle:\n%s", string(data))
	}
}
